package pipeline

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
)

// Reasons an approval is not allowed (APPROVAL_NOT_ALLOWED details, and ApprovalState).
const (
	DenyNotWaiting = "not_waiting"     // the job isn't waiting for approval
	DenyPermission = "permission"      // the caller's project role can't approve
	DenyRole       = "role"            // the environment's protection rule excludes the role
	DenySelf       = "self"            // the caller started the run
	DenyDecided    = "already_decided" // one decision per person
)

// Approval is one decision on a job.
type Approval struct {
	UserID    uuid.UUID              `json:"user_id"`
	UserName  string                 `json:"user_name"`
	Decision  store.ApprovalDecision `json:"decision"`
	Comment   string                 `json:"comment"`
	CreatedAt time.Time              `json:"created_at"`
}

// ApprovalState describes a job's approval gate and whether the caller may decide.
type ApprovalState struct {
	Required        int          `json:"required"`
	Approved        int          `json:"approved"`
	Protected       bool         `json:"protected"`
	AllowedRoles    []authz.Role `json:"allowed_roles"`
	AllowedBranches []string     `json:"allowed_branches"`
	Approvals       []Approval   `json:"approvals"`
	CanDecide       bool         `json:"can_decide"`
	DeniedReason    string       `json:"denied_reason,omitempty"`
}

// Attempt summarizes one attempt of a job.
type Attempt struct {
	ID         uuid.UUID       `json:"id"`
	Attempt    int32           `json:"attempt"`
	Status     store.JobStatus `json:"status"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
}

// JobDetail is a job with its attempts and approval gate.
type JobDetail struct {
	Job
	RunNumber int32          `json:"run_number"`
	Attempts  []Attempt      `json:"attempts"`
	Approval  *ApprovalState `json:"approval"`
}

// GetJob returns a job attempt with steps, attempts and approval state (run.view).
func (s *Service) GetJob(ctx context.Context, jobID uuid.UUID) (JobDetail, error) {
	q := store.New(s.pool)
	j, acc, err := s.loadJob(ctx, q, jobID, authz.RunView)
	if err != nil {
		return JobDetail{}, err
	}
	r, err := q.GetRun(ctx, j.RunID)
	if err != nil {
		return JobDetail{}, err
	}
	return s.jobDetail(ctx, q, acc, r, j)
}

func (s *Service) jobDetail(ctx context.Context, q *store.Queries, acc project.Access, r store.PipelineRun, j store.PipelineJob) (JobDetail, error) {
	steps, err := q.ListSteps(ctx, j.ID)
	if err != nil {
		return JobDetail{}, err
	}
	attempts, err := q.ListJobAttempts(ctx, store.ListJobAttemptsParams{RunID: j.RunID, Name: j.Name})
	if err != nil {
		return JobDetail{}, err
	}
	out := JobDetail{Job: toJob(j, steps), RunNumber: r.Number, Attempts: make([]Attempt, 0, len(attempts))}
	for _, a := range attempts {
		out.Attempts = append(out.Attempts, Attempt{ID: a.ID, Attempt: a.Attempt, Status: a.Status, StartedAt: a.StartedAt, FinishedAt: a.FinishedAt})
	}
	if j.Condition == string(spec.WhenManual) || j.Environment != nil {
		st, err := s.approvalState(ctx, q, acc, r, j)
		if err != nil {
			return JobDetail{}, err
		}
		if st.Required > 0 || len(st.Approvals) > 0 {
			out.Approval = &st
		}
	}
	return out, nil
}

// approvalState evaluates the gate with the environment's current protection rules.
func (s *Service) approvalState(ctx context.Context, q *store.Queries, acc project.Access, r store.PipelineRun, j store.PipelineJob) (ApprovalState, error) {
	st := ApprovalState{AllowedRoles: []authz.Role{}, AllowedBranches: []string{}, Approvals: []Approval{}}
	if j.Condition == string(spec.WhenManual) {
		st.Required = 1
	}
	if j.Environment != nil {
		env, err := q.GetEnvironmentByName(ctx, store.GetEnvironmentByNameParams{ProjectID: r.ProjectID, Name: *j.Environment})
		switch {
		case err == nil && env.Protected:
			st.Protected = true
			st.Required = max(st.Required, int(env.RequiredApprovals))
			st.AllowedBranches = env.AllowedBranches
			for _, role := range env.AllowedRoles {
				st.AllowedRoles = append(st.AllowedRoles, authz.Role(role))
			}
		case err != nil && !database.IsNoRows(err):
			return st, err
		}
	}
	rows, err := q.ListApprovals(ctx, j.ID)
	if err != nil {
		return st, err
	}
	decided := false
	for _, a := range rows {
		st.Approvals = append(st.Approvals, Approval{UserID: a.UserID, UserName: a.DisplayName, Decision: a.Decision, Comment: a.Comment, CreatedAt: a.CreatedAt})
		if a.Decision == store.ApprovalDecisionApproved {
			st.Approved++
		}
		if a.UserID == acc.UserID {
			decided = true
		}
	}
	switch {
	case j.Status != store.JobStatusWaitingApproval:
		st.DeniedReason = DenyNotWaiting
	case !authz.CanProject(acc.Role, authz.ApprovalDecide):
		st.DeniedReason = DenyPermission
	case st.Protected && !roleAllowed(acc.Role, st.AllowedRoles):
		st.DeniedReason = DenyRole
	case r.CreatedBy != nil && *r.CreatedBy == acc.UserID:
		st.DeniedReason = DenySelf
	case decided:
		st.DeniedReason = DenyDecided
	default:
		st.CanDecide = true
	}
	return st, nil
}

// roleAllowed: a protection rule's roles mean "this role or higher".
func roleAllowed(role authz.Role, allowed []authz.Role) bool {
	return slices.ContainsFunc(allowed, func(a authz.Role) bool { return authz.AtLeast(role, a) })
}

// DecideInput is POST /jobs/{id}/approvals.
type DecideInput struct {
	Decision store.ApprovalDecision `json:"decision" validate:"required,oneof=approved rejected"`
	Comment  string                 `json:"comment" validate:"max=500"`
}

// Decide records an approval or rejection. Enough approvals queue the job; a rejection
// fails it (and skips the jobs after it).
func (s *Service) Decide(ctx context.Context, jobID uuid.UUID, in DecideInput) (JobDetail, error) {
	var out JobDetail
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		j, acc, err := s.loadJob(ctx, q, jobID, authz.RunView)
		if err != nil {
			return err
		}
		r, err := q.LockRun(ctx, j.RunID)
		if err != nil {
			return err
		}
		if j, err = q.GetJob(ctx, jobID); err != nil { // re-read under the run lock
			return err
		}
		st, err := s.approvalState(ctx, q, acc, r, j)
		if err != nil {
			return err
		}
		if !st.CanDecide {
			status := http.StatusForbidden
			if st.DeniedReason == DenyNotWaiting || st.DeniedReason == DenyDecided {
				status = http.StatusConflict
			}
			return apperr.New(apperr.CodeApprovalNotAllowed, status, "you can't approve or reject this job").
				WithDetails(map[string]any{"reason": st.DeniedReason})
		}
		if _, err := q.InsertApproval(ctx, store.InsertApprovalParams{JobID: jobID, UserID: acc.UserID, Decision: in.Decision, Comment: in.Comment}); err != nil {
			if database.IsNoRows(err) {
				return apperr.New(apperr.CodeApprovalNotAllowed, http.StatusConflict, "you already decided").
					WithDetails(map[string]any{"reason": DenyDecided})
			}
			return err
		}
		switch {
		case in.Decision == store.ApprovalDecisionRejected:
			if err := s.setStatus(ctx, q, jobID, store.JobStatusFailed, ReasonRejected, nil); err != nil {
				return err
			}
			if err := q.FinishOpenSteps(ctx, store.FinishOpenStepsParams{JobID: jobID, Status: store.StepStatusSkipped}); err != nil {
				return err
			}
		case st.Approved+1 >= st.Required:
			if err := s.setStatus(ctx, q, jobID, store.JobStatusQueued, "", nil); err != nil {
				return err
			}
		}
		if err := s.advance(ctx, tx, q, r.ID); err != nil {
			return err
		}
		if err := audit.Record(ctx, q, audit.Entry{
			OrganizationID: &acc.Project.OrganizationID, ProjectID: &r.ProjectID, Action: string(authz.ApprovalDecide),
			ResourceType: "job", ResourceID: jobID.String(),
			After: map[string]any{"decision": in.Decision, "run_number": r.Number, "job": j.Name, "environment": j.Environment},
		}); err != nil {
			return err
		}
		fresh, err := q.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		freshRun, err := q.GetRun(ctx, r.ID)
		if err != nil {
			return err
		}
		out, err = s.jobDetail(ctx, q, acc, freshRun, fresh)
		return err
	})
	return out, err
}
