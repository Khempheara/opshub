package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
)

// RunFilter narrows the run list.
type RunFilter struct {
	Status  string
	Trigger string
	Ref     string
}

var (
	runStatuses = []store.RunStatus{store.RunStatusQueued, store.RunStatusRunning, store.RunStatusWaiting,
		store.RunStatusSucceeded, store.RunStatusFailed, store.RunStatusCanceled}
	runTriggers = []store.RunTrigger{store.RunTriggerPush, store.RunTriggerPullRequest, store.RunTriggerTag,
		store.RunTriggerManual, store.RunTriggerSchedule}
)

type runCursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

// ListRuns lists a project's runs, newest first (run.view).
func (s *Service) ListRuns(ctx context.Context, projectID uuid.UUID, f RunFilter, page pagination.Params) (pagination.Page[Run], error) {
	q := store.New(s.pool)
	if _, err := project.Authorize(ctx, q, projectID, authz.RunView); err != nil {
		return pagination.Page[Run]{}, err
	}
	params := store.ListRunsParams{ProjectID: projectID, PageSize: page.FetchSize()}
	var fields []apperr.FieldError
	if f.Status != "" {
		st := store.RunStatus(f.Status)
		if !slices.Contains(runStatuses, st) {
			fields = append(fields, apperr.FieldError{Field: "status", Rule: "oneof"})
		}
		params.Status = &st
	}
	if f.Trigger != "" {
		tr := store.RunTrigger(f.Trigger)
		if !slices.Contains(runTriggers, tr) {
			fields = append(fields, apperr.FieldError{Field: "trigger", Rule: "oneof"})
		}
		params.Trigger = &tr
	}
	if len(fields) > 0 {
		return pagination.Page[Run]{}, apperr.Validation(fields)
	}
	if f.Ref != "" {
		ref := f.Ref
		if !strings.HasPrefix(ref, "refs/") {
			ref = "refs/heads/" + ref
		}
		params.Ref = &ref
	}
	var cur runCursor
	if has, err := page.Decode(&cur); err != nil {
		return pagination.Page[Run]{}, err
	} else if has {
		params.CursorAt, params.CursorID = &cur.At, &cur.ID
	}
	rows, err := q.ListRuns(ctx, params)
	if err != nil {
		return pagination.Page[Run]{}, err
	}
	return pagination.Build(rows, page.Limit, func(r store.ListRunsRow) Run {
		return toRun(store.PipelineRun{
			ID: r.ID, OrganizationID: r.OrganizationID, ProjectID: r.ProjectID, Number: r.Number, Status: r.Status,
			Trigger: r.Trigger, Ref: r.Ref, CommitSha: r.CommitSha, Title: r.Title, ActorName: r.ActorName,
			CreatedBy: r.CreatedBy, RerunOf: r.RerunOf, Problems: r.Problems, Variables: r.Variables,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, CreatedAt: r.CreatedAt,
		}, r.CreatedByName)
	}, func(r store.ListRunsRow) any { return runCursor{At: r.CreatedAt, ID: r.ID} }), nil
}

// GetRun returns a run with its job graph (run.view).
func (s *Service) GetRun(ctx context.Context, runID uuid.UUID) (RunDetail, error) {
	q := store.New(s.pool)
	r, _, err := s.loadRun(ctx, q, runID, authz.RunView)
	if err != nil {
		return RunDetail{}, err
	}
	return s.runDetail(ctx, q, r)
}

func (s *Service) runDetail(ctx context.Context, q *store.Queries, r store.PipelineRun) (RunDetail, error) {
	name := ""
	if r.CreatedBy != nil {
		if u, err := q.GetUserByID(ctx, *r.CreatedBy); err == nil {
			name = u.DisplayName
		}
	}
	out := RunDetail{Run: toRun(r, name), Stages: []string{}, Jobs: []Job{}}
	if len(r.Definition) > 0 {
		var def spec.Definition
		if err := json.Unmarshal(r.Definition, &def); err == nil {
			out.Stages = def.Stages
		}
	}
	jobs, err := q.CurrentJobs(ctx, r.ID)
	if err != nil {
		return out, err
	}
	ids := make([]uuid.UUID, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	steps, err := q.ListStepsForJobs(ctx, ids)
	if err != nil {
		return out, err
	}
	byJob := map[uuid.UUID][]store.JobStep{}
	for _, st := range steps {
		byJob[st.JobID] = append(byJob[st.JobID], st)
	}
	for _, j := range jobs {
		out.Jobs = append(out.Jobs, toJob(j, byJob[j.ID]))
	}
	// Stage order, then as written in the definition (CurrentJobs orders by name).
	slices.SortStableFunc(out.Jobs, func(a, b Job) int { return int(a.StageIndex - b.StageIndex) })
	return out, nil
}

// ManualRunInput is POST /projects/{id}/runs.
type ManualRunInput struct {
	Ref       string            `json:"ref" validate:"max=255"`
	Variables map[string]string `json:"variables"`
}

var (
	refInputPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
	runVarPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// TriggerManual starts a run for a branch, tag or commit (pipeline.trigger). It resolves the
// ref and reads `.opshub.yml` from the Git host; an invalid file is reported as
// PIPELINE_INVALID with the problems, and no run is created.
func (s *Service) TriggerManual(ctx context.Context, projectID uuid.UUID, in ManualRunInput) (RunDetail, error) {
	q := store.New(s.pool)
	acc, err := project.Authorize(ctx, q, projectID, authz.PipelineTrigger)
	if err != nil {
		return RunDetail{}, err
	}
	ref := strings.TrimSpace(in.Ref)
	if ref == "" {
		ref = acc.Project.DefaultBranch
	}
	var fields []apperr.FieldError
	if !refInputPattern.MatchString(ref) || strings.Contains(ref, "..") {
		fields = append(fields, apperr.FieldError{Field: "ref", Rule: "branch"})
	}
	if len(in.Variables) > 50 {
		fields = append(fields, apperr.FieldError{Field: "variables", Rule: "max", Param: "50"})
	}
	for k, v := range in.Variables {
		if !runVarPattern.MatchString(k) || strings.HasPrefix(strings.ToUpper(k), "OPSHUB_") {
			fields = append(fields, apperr.FieldError{Field: "variables." + k, Rule: "variable_name"})
		}
		if len(v) > spec.MaxVariableLen {
			fields = append(fields, apperr.FieldError{Field: "variables." + k, Rule: "max", Param: "4096"})
		}
	}
	if len(fields) > 0 {
		return RunDetail{}, apperr.Validation(fields)
	}

	client, _, err := s.projects.GitClient(ctx, q, projectID)
	if err != nil {
		return RunDetail{}, err
	}
	commit, err := client.Commit(ctx, ref)
	if errors.Is(err, gitprovider.ErrNotFound) || errors.Is(err, gitprovider.ErrInvalidInput) {
		return RunDetail{}, apperr.New(apperr.CodeRefNotFound, http.StatusUnprocessableEntity, "branch, tag or commit not found").
			WithDetails(map[string]any{"ref": ref})
	}
	if err != nil {
		return RunDetail{}, project.GitError(err)
	}
	def, problems, err := s.fetchDefinition(ctx, client, commit.SHA)
	if err != nil {
		return RunDetail{}, err
	}
	if len(problems) > 0 {
		return RunDetail{}, errInvalid(problems)
	}
	fullRef := ref
	if !strings.HasPrefix(ref, "refs/") {
		fullRef = "refs/heads/" + ref
	}
	var detail RunDetail
	err = s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		run, err := s.createRun(ctx, tx, q, newRun{
			project: acc.Project, trigger: store.RunTriggerManual, ref: fullRef, sha: commit.SHA,
			title: firstLine(commit.Message), createdBy: &acc.UserID, def: def, variables: in.Variables,
			committedAt: commitTime(commit),
		})
		if err != nil {
			return err
		}
		if ref == acc.Project.DefaultBranch {
			if err := s.syncSchedules(ctx, q, projectID, def); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, q, audit.Entry{
			OrganizationID: &acc.Project.OrganizationID, ProjectID: &projectID, Action: string(authz.PipelineTrigger),
			ResourceType: "run", ResourceID: run.ID.String(),
			After: map[string]any{"number": run.Number, "ref": fullRef, "commit_sha": commit.SHA, "variables": sortedKeys(in.Variables)},
		}); err != nil {
			return err
		}
		detail, err = s.runDetail(ctx, q, run)
		return err
	})
	return detail, err
}

func errInvalid(problems spec.Problems) *apperr.Error {
	return apperr.New(apperr.CodePipelineInvalid, http.StatusUnprocessableEntity, "the pipeline file is invalid").
		WithDetails(map[string]any{"problems": []spec.Problem(problems)})
}

// fetchDefinition reads and parses `.opshub.yml` at a commit. Problems are returned
// separately from errors (a missing file is PIPELINE_FILE_NOT_FOUND).
func (s *Service) fetchDefinition(ctx context.Context, c gitprovider.Client, sha string) (*spec.Definition, spec.Problems, error) {
	src, err := c.File(ctx, DefinitionPath, sha)
	switch {
	case errors.Is(err, gitprovider.ErrNotFound):
		return nil, nil, apperr.New(apperr.CodePipelineFileNotFound, http.StatusUnprocessableEntity,
			"there is no .opshub.yml at this commit").WithDetails(map[string]any{"path": DefinitionPath})
	case errors.Is(err, gitprovider.ErrFileTooLarge):
		return nil, spec.Problems{{Line: 1, Column: 1, Rule: "file_too_large", Param: "1048576"}}, nil
	case err != nil:
		return nil, nil, project.GitError(err)
	}
	def, err := spec.Parse(src)
	var problems spec.Problems
	if errors.As(err, &problems) {
		return nil, problems, nil
	}
	return def, nil, err
}

// ValidateResult is POST /projects/{id}/pipeline/validate.
type ValidateResult struct {
	Valid      bool             `json:"valid"`
	Problems   []spec.Problem   `json:"problems"`
	Definition *spec.Definition `json:"definition"`
}

// Validate lints a pipeline file without running it (any project member).
func (s *Service) Validate(ctx context.Context, projectID uuid.UUID, content string) (ValidateResult, error) {
	if _, err := project.Authorize(ctx, store.New(s.pool), projectID, authz.ProjectView); err != nil {
		return ValidateResult{}, err
	}
	def, err := spec.Parse([]byte(content))
	var problems spec.Problems
	if errors.As(err, &problems) {
		return ValidateResult{Problems: problems}, nil
	}
	if err != nil {
		return ValidateResult{}, err
	}
	return ValidateResult{Valid: true, Problems: []spec.Problem{}, Definition: def}, nil
}

// CancelRun cancels every unfinished job of a run (run.cancel). Running jobs are told to
// stop by their runner (Module 5) on its next report.
func (s *Service) CancelRun(ctx context.Context, runID uuid.UUID) (RunDetail, error) {
	var detail RunDetail
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		r, acc, err := s.loadRun(ctx, q, runID, authz.RunCancel)
		if err != nil {
			return err
		}
		if _, err := q.LockRun(ctx, runID); err != nil {
			return err
		}
		jobs, err := q.CurrentJobs(ctx, runID)
		if err != nil {
			return err
		}
		n := 0
		for _, j := range jobs {
			if terminal(j.Status) {
				continue
			}
			n++
			if err := s.setStatus(ctx, q, j.ID, store.JobStatusCanceled, "", nil); err != nil {
				return err
			}
			if err := q.FinishOpenSteps(ctx, store.FinishOpenStepsParams{JobID: j.ID, Status: store.StepStatusCanceled}); err != nil {
				return err
			}
		}
		if n == 0 {
			return apperr.New(apperr.CodeRunNotCancelable, http.StatusConflict, "this run has already finished")
		}
		if err := s.advance(ctx, tx, q, runID); err != nil {
			return err
		}
		if err := audit.Record(ctx, q, audit.Entry{
			OrganizationID: &acc.Project.OrganizationID, ProjectID: &r.ProjectID, Action: string(authz.RunCancel),
			ResourceType: "run", ResourceID: runID.String(), After: map[string]any{"number": r.Number, "canceled_jobs": n},
		}); err != nil {
			return err
		}
		fresh, err := q.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		detail, err = s.runDetail(ctx, q, fresh)
		return err
	})
	return detail, err
}

// Rerun starts the run again (pipeline.trigger). failedOnly retries the run's failed and
// canceled jobs in place (with the jobs after them); otherwise a new run is created from the
// same definition snapshot, commit and variables.
func (s *Service) Rerun(ctx context.Context, runID uuid.UUID, failedOnly bool) (RunDetail, error) {
	var detail RunDetail
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		r, acc, err := s.loadRun(ctx, q, runID, authz.PipelineTrigger)
		if err != nil {
			return err
		}
		if len(r.Definition) == 0 {
			var problems spec.Problems
			_ = json.Unmarshal(r.Problems, &problems)
			return errInvalid(problems)
		}
		target := r
		if failedOnly {
			if _, err := q.LockRun(ctx, runID); err != nil {
				return err
			}
			jobs, err := q.CurrentJobs(ctx, runID)
			if err != nil {
				return err
			}
			var names []string
			for _, j := range jobs {
				if j.Status == store.JobStatusFailed || j.Status == store.JobStatusCanceled {
					names = append(names, j.Name)
				}
			}
			if len(names) == 0 || !allTerminal(jobs) {
				return apperr.New(apperr.CodeJobNotRetryable, http.StatusConflict, "nothing to re-run: the run is still going or nothing failed")
			}
			if err := s.retry(ctx, tx, q, r, jobs, names); err != nil {
				return err
			}
		} else {
			var def spec.Definition
			if err := json.Unmarshal(r.Definition, &def); err != nil {
				return err
			}
			var vars map[string]string
			_ = json.Unmarshal(r.Variables, &vars)
			if target, err = s.createRun(ctx, tx, q, newRun{
				project: acc.Project, trigger: r.Trigger, ref: r.Ref, sha: r.CommitSha, title: r.Title,
				actor: r.ActorName, createdBy: &acc.UserID, rerunOf: &r.ID, def: &def, variables: vars,
				committedAt: r.CommittedAt,
			}); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, q, audit.Entry{
			OrganizationID: &acc.Project.OrganizationID, ProjectID: &r.ProjectID, Action: "run.rerun",
			ResourceType: "run", ResourceID: target.ID.String(),
			After: map[string]any{"from_number": r.Number, "number": target.Number, "failed_only": failedOnly},
		}); err != nil {
			return err
		}
		fresh, err := q.GetRun(ctx, target.ID)
		if err != nil {
			return err
		}
		detail, err = s.runDetail(ctx, q, fresh)
		return err
	})
	return detail, err
}

// RetryJob runs a failed or canceled job again as a new attempt, with the jobs that depend
// on it (pipeline.trigger).
func (s *Service) RetryJob(ctx context.Context, jobID uuid.UUID) (Job, error) {
	var out Job
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		j, acc, err := s.loadJob(ctx, q, jobID, authz.PipelineTrigger)
		if err != nil {
			return err
		}
		r, err := q.LockRun(ctx, j.RunID)
		if err != nil {
			return err
		}
		jobs, err := q.CurrentJobs(ctx, j.RunID)
		if err != nil {
			return err
		}
		cur := findJob(jobs, j.Name)
		if cur.ID != j.ID || (j.Status != store.JobStatusFailed && j.Status != store.JobStatusCanceled) {
			return apperr.New(apperr.CodeJobNotRetryable, http.StatusConflict, "only the latest attempt of a failed or canceled job can be retried")
		}
		if err := s.retry(ctx, tx, q, r, jobs, []string{j.Name}); err != nil {
			return err
		}
		if err := audit.Record(ctx, q, audit.Entry{
			OrganizationID: &acc.Project.OrganizationID, ProjectID: &j.ProjectID, Action: "job.retry",
			ResourceType: "job", ResourceID: j.ID.String(), After: map[string]any{"name": j.Name, "attempt": j.Attempt + 1},
		}); err != nil {
			return err
		}
		latest, err := q.ListJobAttempts(ctx, store.ListJobAttemptsParams{RunID: j.RunID, Name: j.Name})
		if err != nil {
			return err
		}
		steps, err := q.ListSteps(ctx, latest[0].ID)
		out = toJob(latest[0], steps)
		return err
	})
	return out, err
}

// retry creates new attempts for names and every finished job after them, then advances.
func (s *Service) retry(ctx context.Context, tx pgx.Tx, q *store.Queries, r store.PipelineRun, jobs []store.PipelineJob, names []string) error {
	targets := append(slices.Clone(names), dependents(toNodes(jobs), names)...)
	for _, name := range targets {
		old := findJob(jobs, name)
		if !terminal(old.Status) {
			continue // a dependent still waiting on something else keeps its attempt
		}
		var js spec.Job
		if err := json.Unmarshal(old.Spec, &js); err != nil {
			return err
		}
		if _, err := s.insertJob(ctx, q, r, js, old.Attempt+1); err != nil {
			return err
		}
	}
	return s.advance(ctx, tx, q, r.ID)
}

func allTerminal(jobs []store.PipelineJob) bool {
	for _, j := range jobs {
		if !terminal(j.Status) {
			return false
		}
	}
	return true
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return clip(s, 200)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// syncSchedules makes the project's cron schedules match the default branch's definition.
func (s *Service) syncSchedules(ctx context.Context, q *store.Queries, projectID uuid.UUID, def *spec.Definition) error {
	keep := []string{}
	for _, sc := range def.Triggers.Schedules {
		keep = append(keep, sc.Cron)
	}
	if err := q.DeleteSchedulesExcept(ctx, store.DeleteSchedulesExceptParams{ProjectID: projectID, Keep: keep}); err != nil {
		return err
	}
	for _, expr := range keep {
		c, err := spec.ParseCron(expr)
		if err != nil {
			continue // validated by the parser
		}
		next := c.Next(s.now())
		if next.IsZero() {
			continue
		}
		if err := q.UpsertSchedule(ctx, store.UpsertScheduleParams{ProjectID: projectID, Cron: expr, NextRunAt: next}); err != nil {
			return err
		}
	}
	return nil
}

// CreateRunFromDefinition creates a run without reading the Git host. It performs no
// authorization and exists for demo data (`opshub-api seed`) and tests.
func (s *Service) CreateRunFromDefinition(ctx context.Context, projectID uuid.UUID, def *spec.Definition, trigger store.RunTrigger,
	ref, sha, title, actor string, createdBy *uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		p, err := q.GetProjectForPipeline(ctx, projectID)
		if err != nil {
			return err
		}
		run, err := s.createRun(ctx, tx, q, newRun{
			project: store.Project{ID: p.ID, OrganizationID: p.OrganizationID, DefaultBranch: p.DefaultBranch},
			trigger: trigger, ref: ref, sha: sha, title: title, actor: actor, createdBy: createdBy, def: def,
		})
		id = run.ID
		return err
	})
	return id, err
}
