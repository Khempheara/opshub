package runners

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/store"
)

// Long-poll timing for job requests.
const (
	MaxWait      = 30 * time.Second
	pollInterval = 2 * time.Second
)

func errRunnerToken() *apperr.Error {
	return apperr.New(apperr.CodeRunnerTokenInvalid, http.StatusUnauthorized, "runner token is invalid or revoked")
}

func errJobToken() *apperr.Error {
	return apperr.New(apperr.CodeJobTokenInvalid, http.StatusUnauthorized, "job token is invalid or expired")
}

func errDisabled() *apperr.Error {
	return apperr.New(apperr.CodeRunnerDisabled, http.StatusForbidden, "this runner is disabled")
}

// RegisterInput is POST /runner/register.
type RegisterInput struct {
	Token          string   `json:"token" validate:"required,max=200"`
	Name           string   `json:"name" validate:"required,min=1,max=100"`
	Labels         []string `json:"labels"`
	Version        string   `json:"version" validate:"max=50"`
	OS             string   `json:"os" validate:"max=50"`
	Arch           string   `json:"arch" validate:"max=50"`
	MaxConcurrency int32    `json:"max_concurrency" validate:"min=0,max=64"`
}

// Registration is returned once to a newly registered runner.
type Registration struct {
	ID                       uuid.UUID `json:"id"`
	Token                    string    `json:"token"`
	Name                     string    `json:"name"`
	Labels                   []string  `json:"labels"`
	HeartbeatIntervalSeconds int       `json:"heartbeat_interval_seconds"`
}

// RegisterRunner exchanges a one-time registration token for a runner token. The runner's labels
// are its own plus those attached to the registration token.
func (s *Service) RegisterRunner(ctx context.Context, in RegisterInput) (Registration, error) {
	if !strings.HasPrefix(in.Token, authn.RegistrationTokenPrefix) {
		return Registration{}, apperr.New(apperr.CodeRegistrationTokenInvalid, http.StatusUnauthorized, "registration token is invalid, used or expired")
	}
	labels, err := normalizeLabels(in.Labels)
	if err != nil {
		return Registration{}, err
	}
	if in.MaxConcurrency == 0 {
		in.MaxConcurrency = 1
	}
	var out Registration
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		rt, err := q.UseRegistrationToken(ctx, crypto.HashToken(in.Token))
		if database.IsNoRows(err) {
			return apperr.New(apperr.CodeRegistrationTokenInvalid, http.StatusUnauthorized, "registration token is invalid, used or expired")
		}
		if err != nil {
			return err
		}
		all, err := normalizeLabels(append(labels, rt.Labels...))
		if err != nil {
			return err
		}
		prefix := strings.ToLower(crypto.RandomBase32(8))
		token := authn.RunnerTokenPrefix + prefix + "_" + crypto.RandomToken(32)
		r, err := q.CreateRunner(ctx, store.CreateRunnerParams{
			OrganizationID: rt.OrganizationID, Name: strings.TrimSpace(in.Name), Labels: all,
			TokenHash: crypto.HashToken(token), TokenPrefix: authn.RunnerTokenPrefix + prefix,
			Version: in.Version, Os: in.OS, Arch: in.Arch, MaxConcurrency: in.MaxConcurrency, CreatedBy: rt.CreatedBy,
		})
		if err != nil {
			return err
		}
		if err := q.LinkRegistrationToken(ctx, store.LinkRegistrationTokenParams{ID: rt.ID, RunnerID: &r.ID}); err != nil {
			return err
		}
		out = Registration{ID: r.ID, Token: token, Name: r.Name, Labels: r.Labels, HeartbeatIntervalSeconds: int(HeartbeatInterval / time.Second)}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &rt.OrganizationID, Action: "runner.register", ResourceType: "runner", ResourceID: r.ID.String(),
			After:     map[string]any{"name": r.Name, "labels": r.Labels, "version": r.Version, "os": r.Os, "arch": r.Arch},
			Metadata:  map[string]any{"registration_token_id": rt.ID.String()},
			ActorType: "runner",
		})
	})
	return out, err
}

// AuthenticateRunner resolves a runner token. Disabled runners are refused with 403 so the
// agent can tell "stop and tell the operator" from "token revoked".
func (s *Service) AuthenticateRunner(ctx context.Context, token string) (store.Runner, error) {
	if !strings.HasPrefix(token, authn.RunnerTokenPrefix) || strings.HasPrefix(token, authn.RegistrationTokenPrefix) {
		return store.Runner{}, errRunnerToken()
	}
	r, err := store.New(s.pool).GetRunnerByTokenHash(ctx, crypto.HashToken(token))
	if database.IsNoRows(err) {
		return r, errRunnerToken()
	}
	if err != nil {
		return r, err
	}
	if r.DisabledAt != nil {
		return r, errDisabled()
	}
	return r, nil
}

// HeartbeatInput is POST /runner/heartbeat.
type HeartbeatInput struct {
	Version       string      `json:"version" validate:"max=50"`
	OS            string      `json:"os" validate:"max=50"`
	Arch          string      `json:"arch" validate:"max=50"`
	RunningJobIDs []uuid.UUID `json:"running_job_ids" validate:"max=64"`
}

// HeartbeatResult tells the runner which of its jobs to stop (canceled, timed out, failed
// as lost, or retried elsewhere).
type HeartbeatResult struct {
	CancelJobIDs []uuid.UUID `json:"cancel_job_ids"`
}

// Heartbeat records that the runner is alive. Jobs assigned to it that it doesn't report
// fail as runner_lost.
func (s *Service) Heartbeat(ctx context.Context, r store.Runner, in HeartbeatInput) (HeartbeatResult, error) {
	q := store.New(s.pool)
	if err := q.TouchRunner(ctx, store.TouchRunnerParams{ID: r.ID, Version: in.Version, Os: in.OS, Arch: in.Arch}); err != nil {
		return HeartbeatResult{}, err
	}
	ids := in.RunningJobIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	running, err := q.RunningJobsForRunner(ctx, &r.ID)
	if err != nil {
		return HeartbeatResult{}, err
	}
	cancel := []uuid.UUID{}
	for _, id := range ids {
		if !slices.Contains(running, id) {
			cancel = append(cancel, id)
		}
	}
	orphaned, err := q.OrphanedRunnerJobs(ctx, store.OrphanedRunnerJobsParams{
		RunnerID: &r.ID, JobIds: ids, GraceSeconds: int32(OfflineAfter / time.Second),
	})
	if err != nil {
		return HeartbeatResult{}, err
	}
	for _, id := range orphaned {
		if err := s.pipelines.FailJob(ctx, id, pipeline.ReasonRunnerLost); err != nil {
			return HeartbeatResult{}, err
		}
	}
	return HeartbeatResult{CancelJobIDs: cancel}, nil
}

// AssignedJob is everything the runner needs to execute a job. Token authenticates the
// job-scoped endpoints. Secrets (and Masks, values to hide in logs) stay empty until
// secret variables exist.
type AssignedJob struct {
	pipeline.ClaimedJob
	Token   string            `json:"token"`
	Secrets map[string]string `json:"secrets"`
	Masks   []string          `json:"masks"`
}

var errNoCapacity = errors.New("runner at capacity")

// RequestJob waits up to wait (at most MaxWait) for a job the runner may take. It returns
// nil when none became available.
func (s *Service) RequestJob(ctx context.Context, r store.Runner, wait time.Duration) (*AssignedJob, error) {
	if wait <= 0 || wait > MaxWait {
		wait = MaxWait
	}
	if ctx.Err() != nil {
		return nil, nil // the runner went away
	}
	q := store.New(s.pool)
	if err := q.TouchRunner(ctx, store.TouchRunnerParams{ID: r.ID, Version: r.Version, Os: r.Os, Arch: r.Arch}); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		job, err := s.tryClaim(ctx, r)
		if err != nil && ctx.Err() != nil {
			return nil, nil //nolint:nilerr // canceled mid-claim: the transaction rolled back
		}
		if err != nil || job != nil {
			return job, err
		}
		select {
		case <-ctx.Done():
			return nil, nil //nolint:nilerr // the runner went away; nothing was claimed
		case <-deadline.C:
			return nil, nil
		case <-time.After(pollInterval):
		}
	}
}

func (s *Service) tryClaim(ctx context.Context, r store.Runner) (*AssignedJob, error) {
	var token string
	hook := func(ctx context.Context, q *store.Queries, j store.PipelineJob) error {
		cur, err := q.LockRunner(ctx, r.ID)
		if err != nil {
			return err
		}
		if cur.DisabledAt != nil {
			return errDisabled()
		}
		// Counts the job just started in this transaction.
		n, err := q.CountRunningJobs(ctx, &r.ID)
		if err != nil {
			return err
		}
		if n > int64(cur.MaxConcurrency) {
			return errNoCapacity
		}
		token = authn.JobTokenPrefix + crypto.RandomToken(32)
		return q.UpsertJobToken(ctx, store.UpsertJobTokenParams{
			JobID: j.ID, TokenHash: crypto.HashToken(token),
			ExpiresAt: s.now().Add(time.Duration(j.TimeoutSeconds)*time.Second + jobTokenGrace),
		})
	}
	c, err := s.pipelines.Claim(ctx, r.OrganizationID, r.ID, r.Labels, hook)
	if errors.Is(err, errNoCapacity) {
		return nil, nil
	}
	if err != nil || c == nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "job assigned", "job_id", c.Job.ID, "runner_id", r.ID)
	return &AssignedJob{ClaimedJob: *c, Token: token, Secrets: map[string]string{}, Masks: []string{}}, nil
}

// AuthenticateJob resolves a job token for the job named in the path. The job must still be
// running: once it finishes (or is canceled) its token opens nothing.
func (s *Service) AuthenticateJob(ctx context.Context, token string, jobID uuid.UUID) (store.PipelineJob, error) {
	if !strings.HasPrefix(token, authn.JobTokenPrefix) {
		return store.PipelineJob{}, errJobToken()
	}
	j, err := store.New(s.pool).GetJobByToken(ctx, crypto.HashToken(token))
	if database.IsNoRows(err) || (err == nil && j.ID != jobID) {
		return store.PipelineJob{}, errJobToken()
	}
	if err != nil {
		return j, err
	}
	if j.Status != store.JobStatusRunning {
		return j, apperr.New(apperr.CodeJobNotRunning, http.StatusConflict, "the job isn't running (it may have been canceled)")
	}
	return j, nil
}

// StepReport is a step starting or finishing.
type StepReport struct {
	Index    int32            `json:"index" validate:"min=0,max=1000"`
	Status   store.StepStatus `json:"status" validate:"required,oneof=running succeeded failed skipped canceled"`
	ExitCode *int32           `json:"exit_code"`
}

// Completion ends the job. Reason is one of step_failed, timeout, runner_error when failed.
type Completion struct {
	Success  bool   `json:"success"`
	ExitCode *int32 `json:"exit_code"`
	Reason   string `json:"reason" validate:"omitempty,oneof=step_failed timeout runner_error"`
}

// ReportInput is PATCH /runner/jobs/{id}: exactly one of Step or Complete.
type ReportInput struct {
	Step     *StepReport `json:"step"`
	Complete *Completion `json:"complete"`
}

// Report records step progress or the job's result.
func (s *Service) Report(ctx context.Context, j store.PipelineJob, in ReportInput) error {
	switch {
	case in.Step != nil && in.Complete == nil:
		return s.pipelines.ReportStep(ctx, j.ID, in.Step.Index, in.Step.Status, in.Step.ExitCode)
	case in.Complete != nil && in.Step == nil:
		c := in.Complete
		reason := c.Reason
		if reason == pipeline.ReasonTimeout || reason == pipeline.ReasonRunnerError || reason == pipeline.ReasonStepFailed || reason == "" {
			return s.pipelines.Complete(ctx, j.ID, c.Success, c.ExitCode, reason)
		}
		return apperr.Validation([]apperr.FieldError{{Field: "complete.reason", Rule: "oneof"}})
	default:
		return apperr.Validation([]apperr.FieldError{{Field: "step", Rule: "required_without", Param: "complete"}})
	}
}

// LogInput is POST /runner/jobs/{id}/logs.
type LogInput struct {
	Seq     int32  `json:"seq" validate:"min=0"`
	Content string `json:"content"`
}

// AppendLog stores output. The runner masks Masks before sending.
func (s *Service) AppendLog(ctx context.Context, j store.PipelineJob, in LogInput) error {
	// PostgreSQL text can't hold NUL bytes.
	return s.pipelines.AppendLog(ctx, j.ID, in.Seq, strings.ReplaceAll(in.Content, "\x00", ""), nil)
}
