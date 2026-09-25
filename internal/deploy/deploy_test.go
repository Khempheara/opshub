package deploy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
)

func TestTargets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cfg, _ := json.Marshal(SSHConfig{Hosts: []string{e.ssh.addr}, User: "deploy", Command: "./deploy.sh"})
	creds, _ := json.Marshal(SSHCredentials{PrivateKey: e.ssh.clientKey})
	in := TargetInput{Name: "web", Kind: KindSSH, Config: cfg, Credentials: creds}

	_, err := e.svc.CreateTarget(e.dev.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.CreateTarget(e.outsider.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))
	_, err = e.svc.CreateTarget(e.admin.ctx, e.orgID, TargetInput{Name: "Web!", Kind: KindSSH, Config: cfg, Credentials: creds})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))
	_, err = e.svc.CreateTarget(e.admin.ctx, e.orgID, TargetInput{Name: "web", Kind: KindSSH, Config: cfg})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err), "credentials are required")
	bad, _ := json.Marshal(SSHConfig{Hosts: []string{"bad host"}, User: "deploy", Command: "x"})
	_, err = e.svc.CreateTarget(e.admin.ctx, e.orgID, TargetInput{Name: "web", Kind: KindSSH, Config: bad, Credentials: json.RawMessage(`{"private_key":"nope"}`)})
	assert.ElementsMatch(t, []string{"config.hosts[0]:host", "credentials.private_key:private_key"}, fieldsOf(t, err), "all problems at once")
	_, err = e.svc.CreateTarget(e.admin.ctx, e.orgID, TargetInput{Name: "web", Kind: KindSSH, Config: json.RawMessage(`{"hosts":["x"],"bogus":1}`), Credentials: creds})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err), "unknown fields are refused")
	local, _ := json.Marshal(DockerConfig{Connection: DockerViaLocal, Container: "web"})
	_, err = e.svc.CreateTarget(e.admin.ctx, e.orgID, TargetInput{Name: "local", Kind: KindDocker, Config: local, Credentials: json.RawMessage(`{}`)})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err), "local Docker is off unless the operator allows it")

	tg, err := e.svc.CreateTarget(e.admin.ctx, e.orgID, in)
	require.NoError(t, err)
	assert.Equal(t, []string{"private_key"}, tg.Credentials, "only credential names are returned")
	assert.NotContains(t, string(tg.Config), "PRIVATE KEY")
	_, err = e.svc.CreateTarget(e.admin.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeTargetNameTaken, codeOf(t, err))

	// Viewers see targets; other tenants don't.
	list, err := e.svc.ListTargets(e.viewer.ctx, e.orgID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	_, err = e.svc.GetTarget(e.outsider.ctx, tg.ID)
	assert.Equal(t, apperr.CodeTargetNotFound, codeOf(t, err))

	// Connection test: the host key isn't pinned yet, so the fingerprint comes back.
	res, err := e.svc.TestTarget(e.admin.ctx, tg.ID)
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Equal(t, e.ssh.fingerprint, res.Checks[0].Fingerprint)
	_, err = e.svc.TestTarget(e.viewer.ctx, tg.ID)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))

	// Trusting it is an update that keeps the stored credentials.
	pinned, _ := json.Marshal(SSHConfig{Hosts: []string{e.ssh.addr}, User: "deploy", Command: "./deploy.sh",
		HostKeys: map[string]string{e.ssh.addr: res.Checks[0].Fingerprint}})
	_, err = e.svc.UpdateTarget(e.admin.ctx, tg.ID, tg.Version+3, UpdateTargetInput{Config: pinned})
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))
	up, err := e.svc.UpdateTarget(e.admin.ctx, tg.ID, tg.Version, UpdateTargetInput{Description: "web servers", Config: pinned})
	require.NoError(t, err)
	assert.Equal(t, []string{"private_key"}, up.Credentials)
	res, err = e.svc.TestTarget(e.admin.ctx, tg.ID)
	require.NoError(t, err)
	assert.True(t, res.OK, "%+v", res)
	got, err := e.svc.GetTarget(e.viewer.ctx, tg.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastTestOK)
	assert.True(t, *got.LastTestOK)

	// The stored secret is encrypted at rest.
	row, err := e.q.GetDeployTarget(ctx, tg.ID)
	require.NoError(t, err)
	assert.NotContains(t, string(row.CredentialsEnc), "PRIVATE KEY")

	require.NoError(t, e.svc.DeleteTarget(e.admin.ctx, tg.ID))
	_, err = e.svc.GetTarget(e.admin.ctx, tg.ID)
	assert.Equal(t, apperr.CodeTargetNotFound, codeOf(t, err))

	var n int
	require.NoError(t, e.svc.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action LIKE 'deploy_target.%'`, e.orgID).Scan(&n))
	assert.Equal(t, 3, n) // create, update, delete
}

func TestDeployAndRollback(t *testing.T) {
	e := newEnv(t)
	envID := e.environment(t, "staging", nil)
	tg := e.sshTarget(t, "web")

	_, err := e.svc.CreateDeployment(e.viewer.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:v1"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.CreateDeployment(e.outsider.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:v1"})
	assert.Equal(t, apperr.CodeEnvironmentNotFound, codeOf(t, err))
	_, err = e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:$(id)"})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))
	_, err = e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:v1", Strategy: store.DeployStrategyBlueGreen})
	assert.Equal(t, apperr.CodeStrategyNotSupported, codeOf(t, err))
	_, err = e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: uuid.New(), Version: "app:v1"})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))

	d1, err := e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:v1"})
	require.NoError(t, err)
	assert.Equal(t, store.DeploymentStatusPending, d1.Status)
	assert.Equal(t, int32(1), d1.Number)
	assert.Equal(t, store.DeployStrategyRolling, d1.Strategy)
	_, err = e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:v2"})
	assert.Equal(t, apperr.CodeDeploymentInProgress, codeOf(t, err), "one deployment at a time per environment")
	e.runAll(t)
	d1 = e.get(t, d1.ID)
	assert.Equal(t, store.DeploymentStatusSucceeded, d1.Status)
	assert.True(t, d1.Current)
	assert.Equal(t, "app:v1", e.deployed(t))
	_, err = e.svc.Rollback(e.dev.ctx, d1.ID)
	assert.Equal(t, apperr.CodeNothingToRollBack, codeOf(t, err), "no earlier release")

	d2, err := e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:v2"})
	require.NoError(t, err)
	assert.Equal(t, "app:v1", d2.PreviousVersion)
	e.runAll(t)
	assert.Equal(t, "app:v2", e.deployed(t))
	d1 = e.get(t, d1.ID)
	assert.False(t, d1.Current)
	_, err = e.svc.Rollback(e.dev.ctx, d1.ID)
	assert.Equal(t, apperr.CodeNothingToRollBack, codeOf(t, err), "only the current release rolls back")
	_, err = e.svc.Rollback(e.viewer.ctx, d2.ID)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))

	rb, err := e.svc.Rollback(e.dev.ctx, d2.ID)
	require.NoError(t, err)
	assert.Equal(t, "app:v1", rb.Version)
	assert.Equal(t, &d2.ID, rb.RollbackOfID)
	e.runAll(t)
	rb = e.get(t, rb.ID)
	assert.Equal(t, store.DeploymentStatusSucceeded, rb.Status)
	assert.True(t, rb.Current)
	assert.Equal(t, "app:v1", e.deployed(t))

	// A failing release is reverted and the environment keeps its current release.
	bad, err := e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:boom"})
	require.NoError(t, err)
	e.runAll(t)
	bad = e.get(t, bad.ID)
	assert.Equal(t, store.DeploymentStatusFailed, bad.Status)
	require.NotNil(t, bad.FailureReason)
	assert.Equal(t, ReasonDeployFailed, *bad.FailureReason)
	assert.True(t, bad.Reverted)
	assert.Equal(t, "app:v1", e.deployed(t))
	assert.True(t, e.get(t, rb.ID).Current)

	// History, newest first, filterable.
	page, err := e.svc.ListDeployments(e.viewer.ctx, e.projectID, DeploymentFilter{}, pagination.Params{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, bad.ID, page.Items[0].ID)
	assert.NotNil(t, page.NextCursor)
	failed, err := e.svc.ListDeployments(e.viewer.ctx, e.projectID, DeploymentFilter{Status: "failed"}, pagination.Params{Limit: 50})
	require.NoError(t, err)
	assert.Len(t, failed.Items, 1)
	current, err := e.svc.ListDeployments(e.viewer.ctx, e.projectID, DeploymentFilter{Current: true}, pagination.Params{Limit: 50})
	require.NoError(t, err)
	require.Len(t, current.Items, 1)
	assert.Equal(t, rb.ID, current.Items[0].ID)
	_, err = e.svc.ListDeployments(e.viewer.ctx, e.projectID, DeploymentFilter{Status: "bogus"}, pagination.Params{Limit: 50})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))
	_, err = e.svc.ListDeployments(e.outsider.ctx, e.projectID, DeploymentFilter{}, pagination.Params{Limit: 50})
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))

	// Logs were stored in order.
	chunks, done, status, err := e.svc.logPage(context.Background(), e.q, d2.ID, -1)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, store.DeploymentStatusSucceeded, status)
	var all strings.Builder
	for _, c := range chunks {
		all.WriteString(c.Content)
	}
	assert.Contains(t, all.String(), "now running app:v2")
	assert.Contains(t, all.String(), "Deployed app:v2")

	var n int
	require.NoError(t, e.svc.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action IN ('deployment.create', 'deployment.rollback', 'deployment.succeeded', 'deployment.failed')`, e.orgID).Scan(&n))
	assert.Equal(t, 8, n) // 4 created (incl. the rollback) + 4 finished
}

func TestProtectedEnvironment(t *testing.T) {
	e := newEnv(t)
	tg := e.sshTarget(t, "web")
	adminsOnly := e.environment(t, "production", &project.Protection{RequiredApprovals: 0, AllowedRoles: []authz.Role{authz.Admin}})
	approvals := e.environment(t, "regulated", &project.Protection{RequiredApprovals: 1, AllowedRoles: []authz.Role{authz.Developer}})

	_, err := e.svc.CreateDeployment(e.dev.ctx, adminsOnly, CreateInput{TargetID: tg.ID, Version: "app:v1"})
	assert.Equal(t, apperr.CodeEnvironmentProtected, codeOf(t, err))
	d, err := e.svc.CreateDeployment(e.admin.ctx, adminsOnly, CreateInput{TargetID: tg.ID, Version: "app:v1"})
	require.NoError(t, err)
	e.runAll(t)
	assert.Equal(t, store.DeploymentStatusSucceeded, e.get(t, d.ID).Status)

	// Environments that require approvals only take pipeline deploys.
	_, err = e.svc.CreateDeployment(e.admin.ctx, approvals, CreateInput{TargetID: tg.ID, Version: "app:v1"})
	ae, _ := apperr.From(err)
	require.NotNil(t, ae)
	assert.Equal(t, apperr.CodeEnvironmentProtected, ae.Code)
	assert.Equal(t, "approvals", ae.Details["reason"])
}

func TestInterruptedDeployment(t *testing.T) {
	e := newEnv(t)
	envID := e.environment(t, "staging", nil)
	tg := e.sshTarget(t, "web")
	d, err := e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:v1"})
	require.NoError(t, err)
	require.NoError(t, e.svc.Run(context.Background(), d.ID, 2)) // River's second attempt after a crash
	got := e.get(t, d.ID)
	assert.Equal(t, store.DeploymentStatusFailed, got.Status)
	assert.Equal(t, ReasonInterrupted, *got.FailureReason)
	assert.Empty(t, e.deployed(t), "nothing ran on the target")
}

func TestTargetDeletedBeforeRun(t *testing.T) {
	e := newEnv(t)
	envID := e.environment(t, "staging", nil)
	tg := e.sshTarget(t, "web")
	d, err := e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:v1"})
	require.NoError(t, err)
	assert.Equal(t, apperr.CodeTargetInUse, codeOf(t, e.svc.DeleteTarget(e.admin.ctx, tg.ID)))
	_, err = e.svc.pool.Exec(context.Background(), `UPDATE deployments SET target_id = NULL WHERE id = $1`, d.ID)
	require.NoError(t, err)
	e.runAll(t)
	got := e.get(t, d.ID)
	assert.Equal(t, ReasonTargetMissing, *got.FailureReason)
	assert.Equal(t, "web", got.TargetName, "history keeps the name")
}

const deployPipeline = `version: 1
stages: [deploy]
variables:
  REGISTRY: ghcr.io/acme
jobs:
  ship:
    stage: deploy
    environment: staging
    deploy:
      target: web
      version: ${REGISTRY}/api:${OPSHUB_COMMIT_SHA}
`

func (e *env) runPipeline(t *testing.T, file string) pipeline.Job {
	t.Helper()
	e.git.mu.Lock()
	e.git.pipeline = file
	e.git.mu.Unlock()
	r, err := e.pipelines.TriggerManual(e.admin.ctx, e.projectID, pipeline.ManualRunInput{Ref: "main"})
	require.NoError(t, err)
	require.Len(t, r.Jobs, 1)
	return r.Jobs[0]
}

func (e *env) job(t *testing.T, id uuid.UUID) store.PipelineJob {
	t.Helper()
	j, err := e.q.GetJob(context.Background(), id)
	require.NoError(t, err)
	return j
}

func jobLogText(t *testing.T, e *env, id uuid.UUID) string {
	t.Helper()
	page, err := e.pipelines.Logs(e.admin.ctx, id, -1, 1000)
	require.NoError(t, err)
	var b strings.Builder
	for _, c := range page.Items {
		b.WriteString(c.Content)
	}
	return b.String()
}

func TestPipelineDeployJob(t *testing.T) {
	e := newEnv(t)
	e.environment(t, "staging", nil)
	e.sshTarget(t, "web")

	j := e.runPipeline(t, deployPipeline)
	assert.Equal(t, store.JobStatusRunning, e.job(t, j.ID).Status, "OpsHub took the job; no runner needed")
	e.runAll(t)
	job := e.job(t, j.ID)
	assert.Equal(t, store.JobStatusSucceeded, job.Status)
	assert.Equal(t, "ghcr.io/acme/api:"+sha, e.deployed(t))
	d, err := e.q.DeploymentByJob(context.Background(), &j.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DeploymentStatusSucceeded, d.Status)
	assert.NotNil(t, d.RunID)
	log := jobLogText(t, e, j.ID)
	assert.Contains(t, log, "Deploying ghcr.io/acme/api:"+sha+" to staging via web")
	assert.Contains(t, log, "now running ghcr.io/acme/api:"+sha)

	// A failed deployment fails the job.
	j = e.runPipeline(t, strings.Replace(deployPipeline, "${REGISTRY}/api:${OPSHUB_COMMIT_SHA}", "app:boom", 1))
	e.runAll(t)
	job = e.job(t, j.ID)
	assert.Equal(t, store.JobStatusFailed, job.Status)
	assert.Equal(t, pipeline.ReasonDeployFailed, *job.FailureReason)

	// Problems found when the job starts fail it with a reason in its log.
	j = e.runPipeline(t, strings.Replace(deployPipeline, "target: web", "target: nope", 1))
	job = e.job(t, j.ID)
	assert.Equal(t, pipeline.ReasonTargetNotFound, *job.FailureReason)
	assert.Contains(t, jobLogText(t, e, j.ID), `no deploy target named "nope"`)
	j = e.runPipeline(t, strings.Replace(deployPipeline, "${OPSHUB_COMMIT_SHA}", "${UNDEFINED}", 1))
	job = e.job(t, j.ID)
	assert.Equal(t, pipeline.ReasonDeployInvalid, *job.FailureReason)
	assert.Contains(t, jobLogText(t, e, j.ID), "UNDEFINED")
	// Without a version, ${DEPLOY_VERSION} is used.
	j = e.runPipeline(t, strings.Replace(deployPipeline, "      version: ${REGISTRY}/api:${OPSHUB_COMMIT_SHA}\n", "", 1))
	assert.Contains(t, jobLogText(t, e, j.ID), "DEPLOY_VERSION")
}

func TestPipelineDeployAfterApproval(t *testing.T) {
	e := newEnv(t)
	e.environment(t, "staging", &project.Protection{RequiredApprovals: 1, AllowedRoles: []authz.Role{authz.Developer}})
	e.sshTarget(t, "web")
	j := e.runPipeline(t, deployPipeline)
	assert.Equal(t, store.JobStatusWaitingApproval, e.job(t, j.ID).Status)
	assert.Empty(t, e.jobs.deployments())
	_, err := e.pipelines.Decide(e.dev.ctx, j.ID, pipeline.DecideInput{Decision: store.ApprovalDecisionApproved})
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusRunning, e.job(t, j.ID).Status)
	require.Len(t, e.jobs.deployments(), 1)
	e.runAll(t)
	assert.Equal(t, store.JobStatusSucceeded, e.job(t, j.ID).Status)
}
