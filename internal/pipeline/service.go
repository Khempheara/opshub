// Package pipeline runs CI/CD pipelines (Module 4): it turns `.opshub.yml` into runs of jobs,
// advances the job graph as jobs finish, gates jobs on approvals and environment protection,
// and records steps and logs reported by runners. Jobs run only on registered runners
// (Module 5), which call the runner-side methods in lifecycle.go.
//
// Every state change happens in a transaction holding the run's row lock, then signals
// listeners through events.Notify (delivered on commit).
package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/events"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
)

// DefinitionPath is where a repository's pipeline lives.
const DefinitionPath = ".opshub.yml"

// Service implements pipelines.
type Service struct {
	pool          *pgxpool.Pool
	projects      *project.Service
	logger        *slog.Logger
	now           func() time.Time
	deployStarter DeployStarter
}

// DeployStart is what OpsHub needs to perform a ready job's `deploy:` block.
type DeployStart struct {
	Run       store.PipelineRun
	Job       store.PipelineJob
	Deploy    spec.Deploy
	Variables map[string]string
}

// DeployStarter starts the deployment of a ready deploy job inside the transaction that made
// it ready. It must move the job out of "queued": to running (deployment enqueued) or failed.
type DeployStarter func(ctx context.Context, tx pgx.Tx, q *store.Queries, d DeployStart) error

// SetDeployStarter installs the deployments module's starter (Module 6). Without one, deploy
// jobs stay queued (runners never claim them) until the queue timeout fails them.
func (s *Service) SetDeployStarter(f DeployStarter) { s.deployStarter = f }

func NewService(pool *pgxpool.Pool, projects *project.Service, logger *slog.Logger) *Service {
	return &Service{pool: pool, projects: projects, logger: logger, now: time.Now}
}

func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx, q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx, store.New(tx)) })
}

// ---- API representations ----

// Run is a pipeline run.
type Run struct {
	ID            uuid.UUID         `json:"id"`
	ProjectID     uuid.UUID         `json:"project_id"`
	Number        int32             `json:"number"`
	Status        store.RunStatus   `json:"status"`
	Trigger       store.RunTrigger  `json:"trigger"`
	Ref           string            `json:"ref"`
	RefName       string            `json:"ref_name"`
	CommitSHA     string            `json:"commit_sha"`
	Title         string            `json:"title"`
	ActorName     string            `json:"actor_name"`
	CreatedBy     *uuid.UUID        `json:"created_by"`
	CreatedByName string            `json:"created_by_name"`
	RerunOf       *uuid.UUID        `json:"rerun_of"`
	Variables     map[string]string `json:"variables"`
	Problems      []spec.Problem    `json:"problems"`
	CreatedAt     time.Time         `json:"created_at"`
	StartedAt     *time.Time        `json:"started_at"`
	FinishedAt    *time.Time        `json:"finished_at"`
}

// RunDetail is a run with its stages and the current attempt of every job.
type RunDetail struct {
	Run
	Stages []string `json:"stages"`
	Jobs   []Job    `json:"jobs"`
}

// Job is a job attempt.
type Job struct {
	ID             uuid.UUID       `json:"id"`
	RunID          uuid.UUID       `json:"run_id"`
	Name           string          `json:"name"`
	Stage          string          `json:"stage"`
	StageIndex     int32           `json:"stage_index"`
	Needs          []string        `json:"needs"`
	Condition      string          `json:"condition"`
	Environment    *string         `json:"environment"`
	Image          string          `json:"image"`
	RunsOn         []string        `json:"runs_on"`
	Status         store.JobStatus `json:"status"`
	Attempt        int32           `json:"attempt"`
	FailureReason  *string         `json:"failure_reason"`
	ExitCode       *int32          `json:"exit_code"`
	TimeoutSeconds int32           `json:"timeout_seconds"`
	QueuedAt       *time.Time      `json:"queued_at"`
	StartedAt      *time.Time      `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
	Steps          []Step          `json:"steps"`
}

// Step is one command of a job.
type Step struct {
	Index      int32            `json:"index"`
	Name       string           `json:"name"`
	Command    string           `json:"command"`
	Status     store.StepStatus `json:"status"`
	ExitCode   *int32           `json:"exit_code"`
	StartedAt  *time.Time       `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at"`
}

// refName shortens refs/heads/main → main, refs/tags/v1 → v1.
func refName(ref string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/"} {
		if strings.HasPrefix(ref, p) {
			return strings.TrimPrefix(ref, p)
		}
	}
	return ref
}

func toRun(r store.PipelineRun, createdByName string) Run {
	out := Run{
		ID: r.ID, ProjectID: r.ProjectID, Number: r.Number, Status: r.Status, Trigger: r.Trigger, Ref: r.Ref,
		RefName: refName(r.Ref), CommitSHA: r.CommitSha, Title: r.Title, ActorName: r.ActorName,
		CreatedBy: r.CreatedBy, CreatedByName: createdByName, RerunOf: r.RerunOf, Variables: map[string]string{},
		Problems: []spec.Problem{}, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
	_ = json.Unmarshal(r.Variables, &out.Variables)
	if len(r.Problems) > 0 {
		_ = json.Unmarshal(r.Problems, &out.Problems)
	}
	return out
}

func toJob(j store.PipelineJob, steps []store.JobStep) Job {
	var js spec.Job
	_ = json.Unmarshal(j.Spec, &js)
	out := Job{
		ID: j.ID, RunID: j.RunID, Name: j.Name, Stage: j.Stage, StageIndex: j.StageIndex, Needs: j.Needs,
		Condition: j.Condition, Environment: j.Environment, Image: js.Image, RunsOn: j.RunsOn, Status: j.Status,
		Attempt: j.Attempt, FailureReason: j.FailureReason, ExitCode: j.ExitCode, TimeoutSeconds: j.TimeoutSeconds,
		QueuedAt: j.QueuedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt, Steps: make([]Step, 0, len(steps)),
	}
	if out.Needs == nil {
		out.Needs = []string{}
	}
	if out.RunsOn == nil {
		out.RunsOn = []string{}
	}
	for _, st := range steps {
		out.Steps = append(out.Steps, Step{
			Index: st.Index, Name: st.Name, Command: st.Command, Status: st.Status, ExitCode: st.ExitCode,
			StartedAt: st.StartedAt, FinishedAt: st.FinishedAt,
		})
	}
	return out
}

func toNodes(jobs []store.PipelineJob) []node {
	out := make([]node, 0, len(jobs))
	for _, j := range jobs {
		n := node{Name: j.Name, Status: j.Status, Needs: j.Needs, Condition: spec.When(j.Condition)}
		if j.FailureReason != nil {
			n.Reason = *j.FailureReason
		}
		out = append(out, n)
	}
	return out
}

// ---- errors ----

func errRunNotFound() *apperr.Error {
	return apperr.New(apperr.CodeRunNotFound, http.StatusNotFound, "run not found")
}

func errJobNotFound() *apperr.Error {
	return apperr.New(apperr.CodeJobNotFound, http.StatusNotFound, "job not found")
}

// asNotFound replaces PROJECT_NOT_FOUND (a hidden project) with the resource's own 404.
func asNotFound(err error, nf func() *apperr.Error) error {
	if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeProjectNotFound {
		return nf()
	}
	return err
}

// ---- loaders (authorize through the owning project) ----

func (s *Service) loadRun(ctx context.Context, q *store.Queries, runID uuid.UUID, a authz.Action) (store.PipelineRun, project.Access, error) {
	r, err := q.GetRun(ctx, runID)
	if database.IsNoRows(err) {
		return r, project.Access{}, errRunNotFound()
	}
	if err != nil {
		return r, project.Access{}, err
	}
	acc, err := project.Authorize(ctx, q, r.ProjectID, a)
	return r, acc, asNotFound(err, errRunNotFound)
}

func (s *Service) loadJob(ctx context.Context, q *store.Queries, jobID uuid.UUID, a authz.Action) (store.PipelineJob, project.Access, error) {
	j, err := q.GetJob(ctx, jobID)
	if database.IsNoRows(err) {
		return j, project.Access{}, errJobNotFound()
	}
	if err != nil {
		return j, project.Access{}, err
	}
	acc, err := project.Authorize(ctx, q, j.ProjectID, a)
	return j, acc, asNotFound(err, errJobNotFound)
}

// ---- run creation and the job graph ----

type newRun struct {
	project   store.Project
	trigger   store.RunTrigger
	ref       string
	sha       string
	title     string
	actor     string
	createdBy *uuid.UUID
	rerunOf   *uuid.UUID
	def       *spec.Definition
	problems  spec.Problems
	variables map[string]string
}

// createRun inserts a run and its jobs and advances the graph. A run for an invalid
// definition is created already failed, carrying the problems.
func (s *Service) createRun(ctx context.Context, tx pgx.Tx, q *store.Queries, nr newRun) (store.PipelineRun, error) {
	number, err := q.NextRunNumber(ctx, nr.project.ID)
	if err != nil {
		return store.PipelineRun{}, err
	}
	vars := nr.variables
	if vars == nil {
		vars = map[string]string{}
	}
	varsJSON, err := json.Marshal(vars)
	if err != nil {
		return store.PipelineRun{}, err
	}
	params := store.InsertRunParams{
		OrganizationID: nr.project.OrganizationID, ProjectID: nr.project.ID, Number: number, Status: store.RunStatusQueued,
		Trigger: nr.trigger, Ref: nr.ref, CommitSha: nr.sha, Title: clip(nr.title, 200), ActorName: clip(nr.actor, 100),
		CreatedBy: nr.createdBy, RerunOf: nr.rerunOf, Variables: varsJSON,
	}
	if len(nr.problems) > 0 || nr.def == nil {
		now := s.now()
		params.Status, params.FinishedAt = store.RunStatusFailed, &now
		if params.Problems, err = json.Marshal(nr.problems); err != nil {
			return store.PipelineRun{}, err
		}
	} else if params.Definition, err = json.Marshal(nr.def); err != nil {
		return store.PipelineRun{}, err
	}
	run, err := q.InsertRun(ctx, params)
	if err != nil {
		return run, err
	}
	if nr.def != nil && len(nr.problems) == 0 {
		for _, j := range nr.def.Jobs {
			if _, err := s.insertJob(ctx, q, run, j, 1); err != nil {
				return run, err
			}
		}
		if err := s.advance(ctx, tx, q, run.ID); err != nil {
			return run, err
		}
	}
	if err := events.Notify(ctx, tx, run.ID, uuid.Nil); err != nil {
		return run, err
	}
	return q.GetRun(ctx, run.ID)
}

func (s *Service) insertJob(ctx context.Context, q *store.Queries, run store.PipelineRun, j spec.Job, attempt int32) (store.PipelineJob, error) {
	specJSON, err := json.Marshal(j)
	if err != nil {
		return store.PipelineJob{}, err
	}
	var env *string
	var envID *uuid.UUID
	if j.Environment != "" {
		name := j.Environment
		env = &name
		if e, err := q.GetEnvironmentByName(ctx, store.GetEnvironmentByNameParams{ProjectID: run.ProjectID, Name: name}); err == nil {
			envID = &e.Environment.ID
		} else if !database.IsNoRows(err) {
			return store.PipelineJob{}, err
		}
	}
	job, err := q.InsertJob(ctx, store.InsertJobParams{
		RunID: run.ID, ProjectID: run.ProjectID, OrganizationID: run.OrganizationID, Name: j.Name, Stage: j.Stage,
		StageIndex: int32(j.StageIndex), Needs: j.Needs, Condition: string(j.When), Environment: env, // #nosec G115 -- ≤ 20 stages
		EnvironmentID: envID, RunsOn: j.RunsOn, Spec: specJSON, Status: store.JobStatusCreated, Attempt: attempt,
		TimeoutSeconds: int32(j.TimeoutSeconds), // #nosec G115 -- ≤ 6 h
	})
	if err != nil {
		return job, err
	}
	for i, st := range j.Steps {
		if err := q.InsertStep(ctx, store.InsertStepParams{JobID: job.ID, Index: int32(i), Name: st.Name, Command: st.Run}); err != nil { // #nosec G115 -- ≤ 50 steps
			return job, err
		}
	}
	return job, nil
}

// advance applies engine decisions until the graph is stable, then updates the run status.
func (s *Service) advance(ctx context.Context, tx pgx.Tx, q *store.Queries, runID uuid.UUID) error {
	run, err := q.LockRun(ctx, runID)
	if err != nil {
		return err
	}
	var jobs []store.PipelineJob
	for range 1000 { // bounded: each pass moves at least one job out of "created"
		if jobs, err = q.CurrentJobs(ctx, runID); err != nil {
			return err
		}
		ts := decide(toNodes(jobs))
		if len(ts) == 0 {
			started, err := s.startDeploys(ctx, tx, q, run, jobs)
			if err != nil {
				return err
			}
			if started == 0 {
				break
			}
			continue // a deploy job may have failed at once: settle its dependents
		}
		for _, t := range ts {
			job := findJob(jobs, t.Name)
			if !t.Ready {
				if err := s.setStatus(ctx, q, job.ID, store.JobStatusSkipped, t.Reason, nil); err != nil {
					return err
				}
				if err := q.FinishOpenSteps(ctx, store.FinishOpenStepsParams{JobID: job.ID, Status: store.StepStatusSkipped}); err != nil {
					return err
				}
				continue
			}
			if err := s.gate(ctx, q, run, job); err != nil {
				return err
			}
		}
	}
	status := runStatus(toNodes(jobs), run.StartedAt != nil)
	started, finished := run.StartedAt, run.FinishedAt
	if status == store.RunStatusRunning && started == nil {
		now := s.now()
		started = &now
	}
	switch status {
	case store.RunStatusSucceeded, store.RunStatusFailed, store.RunStatusCanceled:
		if finished == nil {
			now := s.now()
			finished = &now
		}
	default:
		finished = nil // re-opened by a retry
	}
	if status != run.Status || !sameTime(started, run.StartedAt) || !sameTime(finished, run.FinishedAt) {
		if err := q.UpdateRunStatus(ctx, store.UpdateRunStatusParams{ID: runID, Status: status, StartedAt: started, FinishedAt: finished}); err != nil {
			return err
		}
	}
	return events.Notify(ctx, tx, runID, uuid.Nil)
}

// startDeploys hands queued deploy jobs to the deploy starter and returns how many it started.
func (s *Service) startDeploys(ctx context.Context, tx pgx.Tx, q *store.Queries, run store.PipelineRun, jobs []store.PipelineJob) (int, error) {
	if s.deployStarter == nil {
		return 0, nil
	}
	n := 0
	for _, j := range jobs {
		if j.Status != store.JobStatusQueued {
			continue
		}
		var js spec.Job
		if err := json.Unmarshal(j.Spec, &js); err != nil {
			return n, err
		}
		if js.Deploy == nil {
			continue
		}
		vars, err := s.jobVariables(ctx, q, run, j, js)
		if err != nil {
			return n, err
		}
		if err := s.deployStarter(ctx, tx, q, DeployStart{Run: run, Job: j, Deploy: *js.Deploy, Variables: vars}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// gate moves a ready job to waiting_approval or queued, or fails it when its environment is
// missing or protected against this branch. Current protection rules apply.
func (s *Service) gate(ctx context.Context, q *store.Queries, run store.PipelineRun, job store.PipelineJob) error {
	required := 0
	if job.Condition == string(spec.WhenManual) {
		required = 1
	}
	if job.Environment != nil {
		env, err := q.GetEnvironmentByName(ctx, store.GetEnvironmentByNameParams{ProjectID: run.ProjectID, Name: *job.Environment})
		if database.IsNoRows(err) {
			return s.setStatus(ctx, q, job.ID, store.JobStatusFailed, ReasonEnvNotFound, nil)
		}
		if err != nil {
			return err
		}
		if env.Protected {
			if len(env.AllowedBranches) > 0 && !(&spec.BranchFilter{Branches: env.AllowedBranches}).Matches(refName(run.Ref)) {
				return s.setStatus(ctx, q, job.ID, store.JobStatusFailed, ReasonBranchNotAllowed, nil)
			}
			required = max(required, int(env.RequiredApprovals))
		}
	}
	if required > 0 {
		return s.setStatus(ctx, q, job.ID, store.JobStatusWaitingApproval, "", nil)
	}
	return s.setStatus(ctx, q, job.ID, store.JobStatusQueued, "", nil)
}

func (s *Service) setStatus(ctx context.Context, q *store.Queries, jobID uuid.UUID, st store.JobStatus, reason string, exit *int32) error {
	var r *string
	if reason != "" {
		r = &reason
	}
	return q.SetJobStatus(ctx, store.SetJobStatusParams{ID: jobID, Status: st, FailureReason: r, ExitCode: exit})
}

func findJob(jobs []store.PipelineJob, name string) store.PipelineJob {
	for _, j := range jobs {
		if j.Name == name {
			return j
		}
	}
	return store.PipelineJob{}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
