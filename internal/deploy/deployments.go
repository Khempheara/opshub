package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/events"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
)

// Deployment is the API representation of a deployment.
type Deployment struct {
	ID              uuid.UUID              `json:"id"`
	ProjectID       uuid.UUID              `json:"project_id"`
	Number          int32                  `json:"number"`
	EnvironmentID   uuid.UUID              `json:"environment_id"`
	EnvironmentName string                 `json:"environment_name"`
	TargetID        *uuid.UUID             `json:"target_id"`
	TargetName      string                 `json:"target_name"`
	TargetKind      store.DeployTargetKind `json:"target_kind"`
	Version         string                 `json:"version"`
	PreviousVersion string                 `json:"previous_version"`
	Strategy        store.DeployStrategy   `json:"strategy"`
	Status          store.DeploymentStatus `json:"status"`
	FailureReason   *string                `json:"failure_reason"`
	Reverted        bool                   `json:"reverted"`
	Current         bool                   `json:"current"` // what the environment runs now
	Health          []HealthResult         `json:"health"`
	RunID           *uuid.UUID             `json:"run_id"`
	RunNumber       *int32                 `json:"run_number"`
	JobID           *uuid.UUID             `json:"job_id"`
	JobName         string                 `json:"job_name"`
	RollbackOfID    *uuid.UUID             `json:"rollback_of_id"`
	CreatedBy       *uuid.UUID             `json:"created_by"`
	CreatedByName   string                 `json:"created_by_name"`
	StartedAt       *time.Time             `json:"started_at"`
	FinishedAt      *time.Time             `json:"finished_at"`
	CreatedAt       time.Time              `json:"created_at"`
}

func toDeployment(d store.Deployment, env, createdBy string, runNumber int32, jobName string, current bool) Deployment {
	out := Deployment{
		ID: d.ID, ProjectID: d.ProjectID, Number: d.Number, EnvironmentID: d.EnvironmentID, EnvironmentName: env,
		TargetID: d.TargetID, TargetName: d.TargetName, TargetKind: d.TargetKind, Version: d.Version,
		PreviousVersion: d.PreviousVersion, Strategy: d.Strategy, Status: d.Status, FailureReason: d.FailureReason,
		Reverted: d.Reverted, Current: current, Health: []HealthResult{}, RunID: d.RunID, JobID: d.JobID, JobName: jobName,
		RollbackOfID: d.RollbackOfID, CreatedBy: d.CreatedBy, CreatedByName: createdBy, StartedAt: d.StartedAt,
		FinishedAt: d.FinishedAt, CreatedAt: d.CreatedAt,
	}
	_ = json.Unmarshal(d.Health, &out.Health)
	if d.RunID != nil && runNumber > 0 {
		out.RunNumber = &runNumber
	}
	return out
}

func errDeploymentNotFound() *apperr.Error {
	return apperr.New(apperr.CodeDeploymentNotFound, http.StatusNotFound, "deployment not found")
}

func errEnvironmentNotFound() *apperr.Error {
	return apperr.New(apperr.CodeEnvironmentNotFound, http.StatusNotFound, "environment not found")
}

// asNotFound replaces PROJECT_NOT_FOUND (a hidden project) with the resource's own 404.
func asNotFound(err error, nf func() *apperr.Error) error {
	if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeProjectNotFound {
		return nf()
	}
	return err
}

// DeploymentFilter narrows the history.
type DeploymentFilter struct {
	EnvironmentID *uuid.UUID
	Status        string
	From, To      *time.Time
	Current       bool // only what each environment runs now
}

type deploymentCursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

// ListDeployments is a project's release history, newest first (deployment.view).
func (s *Service) ListDeployments(ctx context.Context, projectID uuid.UUID, f DeploymentFilter, page pagination.Params) (pagination.Page[Deployment], error) {
	q := store.New(s.pool)
	if _, err := project.Authorize(ctx, q, projectID, authz.DeploymentView); err != nil {
		return pagination.Page[Deployment]{}, err
	}
	params := store.ListDeploymentsParams{ProjectID: projectID, EnvironmentID: f.EnvironmentID, FromTime: f.From, ToTime: f.To, OnlyCurrent: f.Current, PageSize: page.FetchSize()}
	if f.Status != "" {
		if !slices.Contains([]string{"pending", "running", "succeeded", "failed"}, f.Status) {
			return pagination.Page[Deployment]{}, apperr.Validation([]apperr.FieldError{{Field: "status", Rule: "oneof", Param: "pending running succeeded failed"}})
		}
		params.Status = &f.Status
	}
	var cur deploymentCursor
	if has, err := page.Decode(&cur); err != nil {
		return pagination.Page[Deployment]{}, err
	} else if has {
		params.CursorTime, params.CursorID = &cur.At, &cur.ID
	}
	rows, err := q.ListDeployments(ctx, params)
	if err != nil {
		return pagination.Page[Deployment]{}, err
	}
	return pagination.Build(rows, page.Limit, func(r store.ListDeploymentsRow) Deployment {
		return toDeployment(r.Deployment, r.EnvironmentName, r.CreatedByName, r.RunNumber, r.JobName, r.IsCurrent)
	}, func(r store.ListDeploymentsRow) any {
		return deploymentCursor{At: r.Deployment.CreatedAt, ID: r.Deployment.ID}
	}), nil
}

// loadDeployment authorizes access through the deployment's project.
func (s *Service) loadDeployment(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.Deployment, project.Access, error) {
	d, err := q.GetDeployment(ctx, id)
	if database.IsNoRows(err) {
		return d, project.Access{}, errDeploymentNotFound()
	}
	if err != nil {
		return d, project.Access{}, err
	}
	acc, err := project.Authorize(ctx, q, d.ProjectID, a)
	return d, acc, asNotFound(err, errDeploymentNotFound)
}

func (s *Service) detail(ctx context.Context, q *store.Queries, id uuid.UUID) (Deployment, error) {
	r, err := q.GetDeploymentDetail(ctx, id)
	if err != nil {
		return Deployment{}, err
	}
	return toDeployment(r.Deployment, r.EnvironmentName, r.CreatedByName, r.RunNumber, r.JobName, r.IsCurrent), nil
}

// GetDeployment returns one deployment (deployment.view).
func (s *Service) GetDeployment(ctx context.Context, id uuid.UUID) (Deployment, error) {
	q := store.New(s.pool)
	if _, _, err := s.loadDeployment(ctx, q, id, authz.DeploymentView); err != nil {
		return Deployment{}, err
	}
	return s.detail(ctx, q, id)
}

// CreateInput is POST /environments/{id}/deployments.
type CreateInput struct {
	TargetID uuid.UUID            `json:"target_id" validate:"required"`
	Version  string               `json:"version" validate:"required,max=255"`
	Strategy store.DeployStrategy `json:"strategy" validate:"omitempty,oneof=rolling blue_green"`
}

// newDeployment is what creating a deployment needs, however it is started.
type newDeployment struct {
	env        store.Environment
	target     store.DeployTarget
	version    string
	strategy   store.DeployStrategy
	runID      *uuid.UUID
	jobID      *uuid.UUID
	rollbackOf *uuid.UUID
	createdBy  *uuid.UUID
	action     string
}

// checkStrategy returns an error when the target can't do the strategy.
func checkStrategy(t store.DeployTarget, strategy store.DeployStrategy) error {
	if !supports(t.Kind, strategy) {
		return apperr.New(apperr.CodeStrategyNotSupported, http.StatusUnprocessableEntity, "blue/green is available for Kubernetes targets only").
			WithDetails(map[string]any{"kind": t.Kind, "strategy": strategy})
	}
	if strategy == store.DeployStrategyBlueGreen {
		var c KubernetesConfig
		_ = json.Unmarshal(t.Config, &c)
		if c.Service == "" {
			return apperr.New(apperr.CodeStrategyNotSupported, http.StatusUnprocessableEntity, "blue/green needs the target's service").
				WithDetails(map[string]any{"kind": t.Kind, "strategy": strategy, "reason": "service"})
		}
	}
	return nil
}

// insert creates the deployment row and enqueues the work, in tx.
func (s *Service) insert(ctx context.Context, tx pgx.Tx, q *store.Queries, nd newDeployment) (store.Deployment, error) {
	previous := ""
	if cur, err := q.GetCurrentDeployment(ctx, nd.env.ID); err == nil {
		previous = cur.Version
	} else if !database.IsNoRows(err) {
		return store.Deployment{}, err
	}
	p, err := q.GetProjectForPipeline(ctx, nd.env.ProjectID)
	if err != nil {
		return store.Deployment{}, err
	}
	number, err := q.NextDeploymentNumber(ctx, nd.env.ProjectID)
	if err != nil {
		return store.Deployment{}, err
	}
	d, err := q.CreateDeployment(ctx, store.CreateDeploymentParams{
		OrganizationID: p.OrganizationID, ProjectID: nd.env.ProjectID, EnvironmentID: nd.env.ID, Number: number,
		TargetID: &nd.target.ID, TargetName: nd.target.Name, TargetKind: nd.target.Kind, Version: nd.version,
		PreviousVersion: previous, Strategy: nd.strategy, RunID: nd.runID, JobID: nd.jobID, RollbackOfID: nd.rollbackOf,
		CreatedBy: nd.createdBy,
	})
	if database.IsUniqueViolation(err, "deployments_one_active_per_env") {
		return d, apperr.New(apperr.CodeDeploymentInProgress, http.StatusConflict, "a deployment to this environment is in progress")
	}
	if err != nil {
		return d, err
	}
	if _, err := s.jobs.InsertTx(ctx, tx, jobs.DeploymentArgs{DeploymentID: d.ID}, nil); err != nil {
		return d, err
	}
	if err := events.NotifyDeployment(ctx, tx, d.ID); err != nil {
		return d, err
	}
	return d, audit.Record(ctx, q, audit.Entry{
		OrganizationID: &p.OrganizationID, ProjectID: &p.ID, Action: nd.action, ResourceType: "deployment", ResourceID: d.ID.String(),
		After: map[string]any{
			"number": d.Number, "environment": nd.env.Name, "target": nd.target.Name, "version": d.Version,
			"previous_version": d.PreviousVersion, "strategy": d.Strategy, "rollback_of": d.RollbackOfID, "job_id": d.JobID,
		},
	})
}

// allowedByProtection checks an environment's allowed roles ("this role or higher").
func allowedByProtection(role authz.Role, allowed []string) bool {
	return slices.ContainsFunc(allowed, func(a string) bool { return authz.AtLeast(role, authz.Role(a)) })
}

func errProtected(reason string) *apperr.Error {
	msg := "your role can't deploy to this protected environment"
	if reason == "approvals" {
		msg = "this environment requires approvals: deploy through a pipeline"
	}
	return apperr.New(apperr.CodeEnvironmentProtected, http.StatusForbidden, msg).WithDetails(map[string]any{"reason": reason})
}

// CreateDeployment starts a manual deployment (deployment.create). Protected environments
// need an allowed role; environments that require approvals only take pipeline deploys.
func (s *Service) CreateDeployment(ctx context.Context, envID uuid.UUID, in CreateInput) (Deployment, error) {
	in.Version = strings.TrimSpace(in.Version)
	if !ImagePattern.MatchString(in.Version) {
		return Deployment{}, apperr.Validation([]apperr.FieldError{{Field: "version", Rule: "image"}})
	}
	if in.Strategy == "" {
		in.Strategy = store.DeployStrategyRolling
	}
	var out Deployment
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		env, err := q.GetEnvironment(ctx, envID)
		if database.IsNoRows(err) {
			return errEnvironmentNotFound()
		}
		if err != nil {
			return err
		}
		acc, err := project.Authorize(ctx, q, env.Environment.ProjectID, authz.DeploymentCreate)
		if err != nil {
			return asNotFound(err, errEnvironmentNotFound)
		}
		if env.Protected {
			if !allowedByProtection(acc.Role, env.AllowedRoles) {
				return errProtected("role")
			}
			if env.RequiredApprovals > 0 {
				return errProtected("approvals")
			}
		}
		t, err := q.GetDeployTarget(ctx, in.TargetID)
		if database.IsNoRows(err) || (err == nil && t.OrganizationID != acc.Project.OrganizationID) {
			return apperr.Validation([]apperr.FieldError{{Field: "target_id", Rule: "exists"}})
		}
		if err != nil {
			return err
		}
		if err := checkStrategy(t, in.Strategy); err != nil {
			return err
		}
		d, err := s.insert(ctx, tx, q, newDeployment{
			env: env.Environment, target: t, version: in.Version, strategy: in.Strategy, createdBy: &acc.UserID,
			action: string(authz.DeploymentCreate),
		})
		if err != nil {
			return err
		}
		out, err = s.detail(ctx, q, d.ID)
		return err
	})
	return out, err
}

// Rollback redeploys the release that ran before the environment's current one
// (deployment.rollback). It needs an allowed role on protected environments but no
// approvals: it is the emergency path.
func (s *Service) Rollback(ctx context.Context, id uuid.UUID) (Deployment, error) {
	var out Deployment
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		d, acc, err := s.loadDeployment(ctx, q, id, authz.DeploymentRollback)
		if err != nil {
			return err
		}
		env, err := q.GetEnvironment(ctx, d.EnvironmentID)
		if database.IsNoRows(err) {
			return errEnvironmentNotFound()
		}
		if err != nil {
			return err
		}
		if env.Protected && !allowedByProtection(acc.Role, env.AllowedRoles) {
			return errProtected("role")
		}
		cur := env.Environment.CurrentDeploymentID
		if d.Status != store.DeploymentStatusSucceeded || cur == nil || *cur != d.ID {
			return apperr.New(apperr.CodeNothingToRollBack, http.StatusConflict, "only the environment's current deployment can be rolled back").
				WithDetails(map[string]any{"reason": "not_current"})
		}
		prev, err := q.PreviousSuccessfulDeployment(ctx, store.PreviousSuccessfulDeploymentParams{
			EnvironmentID: d.EnvironmentID, Before: d.CreatedAt, ExcludeID: d.ID,
		})
		if database.IsNoRows(err) {
			return apperr.New(apperr.CodeNothingToRollBack, http.StatusConflict, "there is no earlier successful deployment").
				WithDetails(map[string]any{"reason": "no_previous"})
		}
		if err != nil {
			return err
		}
		if prev.TargetID == nil {
			return apperr.New(apperr.CodeTargetNotFound, http.StatusConflict, "the previous release's target was deleted")
		}
		t, err := q.GetDeployTarget(ctx, *prev.TargetID)
		if database.IsNoRows(err) {
			return apperr.New(apperr.CodeTargetNotFound, http.StatusConflict, "the previous release's target was deleted")
		}
		if err != nil {
			return err
		}
		nd, err := s.insert(ctx, tx, q, newDeployment{
			env: env.Environment, target: t, version: prev.Version, strategy: prev.Strategy, rollbackOf: &d.ID,
			createdBy: &acc.UserID, action: string(authz.DeploymentRollback),
		})
		if err != nil {
			return err
		}
		out, err = s.detail(ctx, q, nd.ID)
		return err
	})
	return out, err
}

// expandVersion replaces ${VAR} and $VAR with the job's variables; unknown names are
// reported.
func expandVersion(tmpl string, vars map[string]string) (string, []string) {
	var missing []string
	out := os.Expand(tmpl, func(name string) string {
		v, ok := vars[name]
		if !ok && !slices.Contains(missing, name) {
			missing = append(missing, name)
		}
		return v
	})
	return strings.TrimSpace(out), missing
}

// startFromJob is the pipeline's DeployStarter: a ready job with a `deploy:` block becomes a
// deployment (job running) or fails with a reason written to the job's log.
func (s *Service) startFromJob(ctx context.Context, tx pgx.Tx, q *store.Queries, ds pipeline.DeployStart) error {
	j := ds.Job
	failJob := func(reason, message string) error {
		if err := jobLog(ctx, q, j.ID, "\x1b[31;1m"+message+"\x1b[0m\n"); err != nil {
			return err
		}
		r := reason
		if err := q.SetJobStatus(ctx, store.SetJobStatusParams{ID: j.ID, Status: store.JobStatusFailed, FailureReason: &r}); err != nil {
			return err
		}
		return q.FinishOpenSteps(ctx, store.FinishOpenStepsParams{JobID: j.ID, Status: store.StepStatusSkipped})
	}
	tmpl := ds.Deploy.Version
	if tmpl == "" {
		tmpl = spec.DefaultDeployVersion
	}
	version, missing := expandVersion(tmpl, ds.Variables)
	if len(missing) > 0 {
		return failJob(pipeline.ReasonDeployInvalid, fmt.Sprintf("deploy.version %q uses undefined variables: %s", tmpl, strings.Join(missing, ", ")))
	}
	if !ImagePattern.MatchString(version) {
		return failJob(pipeline.ReasonDeployInvalid, fmt.Sprintf("deploy.version %q isn't an image reference", version))
	}
	t, err := q.GetDeployTargetByName(ctx, store.GetDeployTargetByNameParams{OrganizationID: ds.Run.OrganizationID, Name: ds.Deploy.Target})
	if database.IsNoRows(err) {
		return failJob(pipeline.ReasonTargetNotFound, fmt.Sprintf("There is no deploy target named %q in this organization.", ds.Deploy.Target))
	}
	if err != nil {
		return err
	}
	strategy := store.DeployStrategy(ds.Deploy.Strategy)
	if err := checkStrategy(t, strategy); err != nil {
		return failJob(pipeline.ReasonDeployInvalid, err.Error())
	}
	if j.Environment == nil {
		return failJob(pipeline.ReasonDeployInvalid, "a deploy job needs an environment")
	}
	env, err := q.GetEnvironmentByName(ctx, store.GetEnvironmentByNameParams{ProjectID: ds.Run.ProjectID, Name: *j.Environment})
	if database.IsNoRows(err) {
		return failJob(pipeline.ReasonEnvNotFound, fmt.Sprintf("Environment %q doesn't exist.", *j.Environment))
	}
	if err != nil {
		return err
	}
	// A savepoint keeps a refused insert (another deployment in progress) from aborting the
	// engine's transaction.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	_, err = s.insert(ctx, sp, store.New(sp), newDeployment{
		env: env.Environment, target: t, version: version, strategy: strategy, runID: &ds.Run.ID, jobID: &j.ID,
		createdBy: ds.Run.CreatedBy, action: string(authz.DeploymentCreate),
	})
	if err != nil {
		_ = sp.Rollback(ctx)
		if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeDeploymentInProgress {
			return failJob(pipeline.ReasonDeployFailed, fmt.Sprintf("Another deployment to %s is in progress; retry this job when it has finished.", env.Environment.Name))
		}
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		return err
	}
	if err := jobLog(ctx, q, j.ID, fmt.Sprintf("Deploying %s to %s via %s (%s)\n", version, env.Environment.Name, t.Name, strategy)); err != nil {
		return err
	}
	return q.StartJob(ctx, store.StartJobParams{ID: j.ID})
}

// jobLog appends a line to a job's log inside the engine's transaction.
func jobLog(ctx context.Context, q *store.Queries, jobID uuid.UUID, line string) error {
	seq, err := q.NextJobLogSeq(ctx, jobID)
	if err != nil {
		return err
	}
	if _, err := q.InsertLogChunk(ctx, store.InsertLogChunkParams{JobID: jobID, Seq: seq, Content: line}); err != nil {
		return err
	}
	_, err = q.AddLogBytes(ctx, store.AddLogBytesParams{ID: jobID, Bytes: int64(len(line))})
	return err
}
