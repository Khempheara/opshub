package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/events"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/store"
)

// Runner-side job lifecycle. These methods do no user authorization: Module 5 exposes them
// to runners authenticated with runner and job tokens.

// Log limits.
const (
	MaxLogChunkBytes = 256 << 10
	MaxJobLogBytes   = 10 << 20
	truncatedNotice  = "\n[OpsHub: log truncated at 10 MiB]\n"
)

// ClaimedJob is what a runner needs to execute a job.
type ClaimedJob struct {
	Job       Job               `json:"job"`
	Spec      spec.Job          `json:"spec"`
	RunID     uuid.UUID         `json:"run_id"`
	RunNumber int32             `json:"run_number"`
	ProjectID uuid.UUID         `json:"project_id"`
	Ref       string            `json:"ref"`
	CommitSHA string            `json:"commit_sha"`
	Variables map[string]string `json:"variables"`
}

// Claim assigns the organization's oldest queued job that the runner's labels satisfy.
// It returns nil when there is nothing to do.
func (s *Service) Claim(ctx context.Context, orgID, runnerID uuid.UUID, labels []string) (*ClaimedJob, error) {
	var out *ClaimedJob
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		if labels == nil {
			labels = []string{}
		}
		j, err := q.ClaimJob(ctx, store.ClaimJobParams{OrganizationID: orgID, Labels: labels})
		if database.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := q.StartJob(ctx, store.StartJobParams{ID: j.ID, RunnerID: &runnerID}); err != nil {
			return err
		}
		if err := s.advance(ctx, tx, q, j.RunID); err != nil {
			return err
		}
		if err := events.Notify(ctx, tx, j.RunID, j.ID); err != nil {
			return err
		}
		if j, err = q.GetJob(ctx, j.ID); err != nil {
			return err
		}
		r, err := q.GetRun(ctx, j.RunID)
		if err != nil {
			return err
		}
		steps, err := q.ListSteps(ctx, j.ID)
		if err != nil {
			return err
		}
		var js spec.Job
		if err := json.Unmarshal(j.Spec, &js); err != nil {
			return err
		}
		vars, err := s.jobVariables(ctx, q, r, j, js)
		if err != nil {
			return err
		}
		out = &ClaimedJob{
			Job: toJob(j, steps), Spec: js, RunID: r.ID, RunNumber: r.Number, ProjectID: r.ProjectID,
			Ref: r.Ref, CommitSHA: r.CommitSha, Variables: vars,
		}
		return nil
	})
	return out, err
}

// jobVariables merges, lowest precedence first: environment variables, pipeline variables,
// job variables, variables given when the run was started, then OPSHUB_* (always set).
func (s *Service) jobVariables(ctx context.Context, q *store.Queries, r store.PipelineRun, j store.PipelineJob, js spec.Job) (map[string]string, error) {
	vars := map[string]string{}
	if j.Environment != nil {
		env, err := q.GetEnvironmentByName(ctx, store.GetEnvironmentByNameParams{ProjectID: r.ProjectID, Name: *j.Environment})
		if err == nil {
			var ev map[string]string
			_ = json.Unmarshal(env.Environment.Variables, &ev)
			for k, v := range ev {
				vars[k] = v
			}
		} else if !database.IsNoRows(err) {
			return nil, err
		}
	}
	var def spec.Definition
	_ = json.Unmarshal(r.Definition, &def)
	for k, v := range def.Variables {
		vars[k] = v
	}
	for k, v := range js.Variables {
		vars[k] = v
	}
	var runVars map[string]string
	_ = json.Unmarshal(r.Variables, &runVars)
	for k, v := range runVars {
		vars[k] = v
	}
	p, err := q.GetProjectForPipeline(ctx, r.ProjectID)
	if err != nil {
		return nil, err
	}
	env := ""
	if j.Environment != nil {
		env = *j.Environment
	}
	for k, v := range map[string]string{
		"CI": "true", "OPSHUB": "true",
		"OPSHUB_PROJECT_SLUG": p.Slug, "OPSHUB_RUN_ID": r.ID.String(), "OPSHUB_RUN_NUMBER": strconv.Itoa(int(r.Number)),
		"OPSHUB_JOB_ID": j.ID.String(), "OPSHUB_JOB_NAME": j.Name, "OPSHUB_JOB_ATTEMPT": strconv.Itoa(int(j.Attempt)),
		"OPSHUB_STAGE": j.Stage, "OPSHUB_COMMIT_SHA": r.CommitSha, "OPSHUB_REF": r.Ref, "OPSHUB_REF_NAME": refName(r.Ref),
		"OPSHUB_TRIGGER": string(r.Trigger), "OPSHUB_ENVIRONMENT": env,
	} {
		vars[k] = v
	}
	return vars, nil
}

func errNotRunning() *apperr.Error {
	return apperr.New(apperr.CodeJobNotRunning, http.StatusConflict, "the job isn't running (it may have been canceled)")
}

// runningJob loads a job under its run's lock and checks that it's running.
func runningJob(ctx context.Context, q *store.Queries, jobID uuid.UUID) (store.PipelineJob, error) {
	j, err := q.GetJob(ctx, jobID)
	if database.IsNoRows(err) {
		return j, errJobNotFound()
	}
	if err != nil {
		return j, err
	}
	if _, err := q.LockRun(ctx, j.RunID); err != nil {
		return j, err
	}
	if j, err = q.GetJob(ctx, jobID); err != nil {
		return j, err
	}
	if j.Status != store.JobStatusRunning {
		return j, errNotRunning()
	}
	return j, nil
}

// ReportStep records a step starting (StepStatusRunning) or finishing.
func (s *Service) ReportStep(ctx context.Context, jobID uuid.UUID, index int32, status store.StepStatus, exitCode *int32) error {
	return s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		j, err := runningJob(ctx, q, jobID)
		if err != nil {
			return err
		}
		n, err := q.UpdateStep(ctx, store.UpdateStepParams{JobID: jobID, Index: index, Status: status, ExitCode: exitCode})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.Validation([]apperr.FieldError{{Field: "index", Rule: "range"}})
		}
		return events.Notify(ctx, tx, j.RunID, jobID)
	})
}

// Masker hides secret values in log output.
type Masker []string

// Mask replaces every secret (4 characters or longer) with ••••••. Longer secrets are
// replaced first so one secret containing another is masked completely.
func (m Masker) Mask(s string) string {
	vals := slices.Clone(m)
	slices.SortFunc(vals, func(a, b string) int { return len(b) - len(a) })
	for _, v := range vals {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "••••••")
		}
	}
	return s
}

// AppendLog stores a log chunk. seq is chosen by the runner, so re-sending a chunk is
// harmless. Output past MaxJobLogBytes is dropped with a notice.
func (s *Service) AppendLog(ctx context.Context, jobID uuid.UUID, seq int32, content string, mask Masker) error {
	if len(content) > MaxLogChunkBytes {
		return apperr.New(apperr.CodePayloadTooLarge, http.StatusRequestEntityTooLarge, "log chunk too large").
			WithDetails(map[string]any{"max_bytes": MaxLogChunkBytes})
	}
	if seq < 0 {
		return apperr.Validation([]apperr.FieldError{{Field: "seq", Rule: "min", Param: "0"}})
	}
	content = mask.Mask(content)
	return s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		j, err := runningJob(ctx, q, jobID)
		if err != nil {
			return err
		}
		if j.LogBytes >= MaxJobLogBytes {
			return nil
		}
		if remaining := MaxJobLogBytes - j.LogBytes; int64(len(content)) > remaining {
			content = clip(content, int(remaining)) + truncatedNotice
		}
		n, err := q.InsertLogChunk(ctx, store.InsertLogChunkParams{JobID: jobID, Seq: seq, Content: content})
		if err != nil || n == 0 {
			return err // n == 0: duplicate seq
		}
		if _, err := q.AddLogBytes(ctx, store.AddLogBytesParams{ID: jobID, Bytes: int64(len(content))}); err != nil {
			return err
		}
		return events.Notify(ctx, tx, j.RunID, jobID)
	})
}

// Complete finishes a running job. Steps still open are marked skipped (after a failure)
// or succeeded.
func (s *Service) Complete(ctx context.Context, jobID uuid.UUID, success bool, exitCode *int32, reason string) error {
	return s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		j, err := runningJob(ctx, q, jobID)
		if err != nil {
			return err
		}
		status, stepStatus := store.JobStatusSucceeded, store.StepStatusSucceeded
		if !success {
			status, stepStatus = store.JobStatusFailed, store.StepStatusSkipped
			if reason == "" {
				reason = ReasonStepFailed
			}
		} else {
			reason = ""
		}
		if err := s.setStatus(ctx, q, jobID, status, reason, exitCode); err != nil {
			return err
		}
		if err := q.FinishOpenSteps(ctx, store.FinishOpenStepsParams{JobID: jobID, Status: stepStatus}); err != nil {
			return err
		}
		if err := s.advance(ctx, tx, q, j.RunID); err != nil {
			return err
		}
		return events.Notify(ctx, tx, j.RunID, jobID)
	})
}

// LogChunk is a piece of job output.
type LogChunk struct {
	Seq     int32  `json:"seq"`
	Content string `json:"content"`
}

// LogPage is GET /jobs/{id}/logs. Complete is true once the job has finished and every
// chunk has been returned.
type LogPage struct {
	Items    []LogChunk `json:"items"`
	NextSeq  int32      `json:"next_seq"`
	Complete bool       `json:"complete"`
}

// Logs returns chunks after afterSeq (run.view).
func (s *Service) Logs(ctx context.Context, jobID uuid.UUID, afterSeq, limit int32) (LogPage, error) {
	q := store.New(s.pool)
	if _, _, err := s.loadJob(ctx, q, jobID, authz.RunView); err != nil {
		return LogPage{}, err
	}
	return s.logPage(ctx, q, jobID, afterSeq, limit)
}

func (s *Service) logPage(ctx context.Context, q *store.Queries, jobID uuid.UUID, afterSeq, limit int32) (LogPage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	// Read the status first: if the job was finished then, the chunks read next are final.
	j, err := q.GetJob(ctx, jobID)
	if err != nil {
		return LogPage{}, err
	}
	rows, err := q.ListLogChunks(ctx, store.ListLogChunksParams{JobID: jobID, AfterSeq: afterSeq, PageSize: limit})
	if err != nil {
		return LogPage{}, err
	}
	out := LogPage{Items: make([]LogChunk, 0, len(rows)), NextSeq: afterSeq}
	for _, r := range rows {
		out.Items = append(out.Items, LogChunk{Seq: r.Seq, Content: r.Content})
		out.NextSeq = r.Seq
	}
	out.Complete = terminal(j.Status) && int32(len(rows)) < limit // #nosec G115 -- ≤ 1000
	return out, nil
}
