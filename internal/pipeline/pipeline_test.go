package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

const threeStage = `version: 1
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
  schedule:
    - cron: "0 3 * * *"
stages: [test, build, deploy]
variables:
  LEVEL: pipeline
  SHARED: pipeline
jobs:
  test:
    stage: test
    image: golang:1.23
    runs_on: [linux]
    variables: { LEVEL: job }
    steps: [go vet ./..., go test ./...]
  build:
    stage: build
    image: docker:27
    steps: [docker build .]
  deploy:
    stage: deploy
    environment: production
    image: alpine:3
    steps: [./deploy.sh]
`

func TestManualRunLifecycle(t *testing.T) {
	e := newEnv(t)
	e.environment(t, "production", nil, map[string]string{"SHARED": "environment", "REGION": "ap-southeast-1"})
	e.git.set("main", "aaaaaaa1", threeStage)

	r := e.run(t, e.dev, "")
	assert.Equal(t, int32(1), r.Number)
	assert.Equal(t, store.RunTriggerManual, r.Trigger)
	assert.Equal(t, "refs/heads/main", r.Ref)
	assert.Equal(t, "Commit aaaaaaa1", r.Title, "first line of the commit message")
	assert.Equal(t, []string{"test", "build", "deploy"}, r.Stages)
	assert.Equal(t, store.RunStatusQueued, r.Status)
	assert.Equal(t, map[string]string{"test": "queued", "build": "created", "deploy": "created"}, statuses(r))
	assert.Len(t, jobByName(t, r, "test").Steps, 2)

	// No runner with the required label: nothing to claim.
	none, err := e.svc.Claim(context.Background(), e.orgID, e.runnerID, []string{"windows"}, nil)
	require.NoError(t, err)
	assert.Nil(t, none)
	// Another organization's runners never see these jobs.
	other, err := e.svc.Claim(context.Background(), uuid.New(), e.runnerID, []string{"linux"}, nil)
	require.NoError(t, err)
	assert.Nil(t, other)

	c := e.runNext(t, true)
	assert.Equal(t, "test", c.Job.Name)
	assert.Equal(t, "job", c.Variables["LEVEL"], "job variables beat pipeline variables")
	assert.Equal(t, "aaaaaaa1", c.Variables["OPSHUB_COMMIT_SHA"])
	assert.Equal(t, "main", c.Variables["OPSHUB_REF_NAME"])
	assert.Equal(t, "1", c.Variables["OPSHUB_RUN_NUMBER"])
	assert.Equal(t, "api", c.Variables["OPSHUB_PROJECT_SLUG"])
	assert.Equal(t, "true", c.Variables["CI"])

	r = e.get(t, r.ID)
	assert.Equal(t, store.RunStatusRunning, r.Status)
	assert.NotNil(t, r.StartedAt)
	assert.Equal(t, map[string]string{"test": "succeeded", "build": "queued", "deploy": "created"}, statuses(r))
	for _, st := range jobByName(t, r, "test").Steps {
		assert.Equal(t, store.StepStatusSucceeded, st.Status)
	}

	e.runNext(t, true) // build
	c = e.runNext(t, false)
	assert.Equal(t, "deploy", c.Job.Name)
	assert.Equal(t, "pipeline", c.Variables["SHARED"], "pipeline variables beat environment variables")
	assert.Equal(t, "ap-southeast-1", c.Variables["REGION"], "environment variables reach the job")
	assert.Equal(t, "production", c.Variables["OPSHUB_ENVIRONMENT"])

	r = e.get(t, r.ID)
	assert.Equal(t, store.RunStatusFailed, r.Status)
	assert.NotNil(t, r.FinishedAt)
	assert.Equal(t, "failed/step_failed", statuses(r)["deploy"])
	assert.Equal(t, int32(1), *jobByName(t, r, "deploy").ExitCode)

	// Logs: readable by any member, idempotent by seq, complete once the job finished.
	deployID := jobByName(t, r, "deploy").ID
	page, err := e.svc.Logs(e.viewer.ctx, deployID, -1, 100)
	require.NoError(t, err)
	assert.Equal(t, []LogChunk{{Seq: 0, Content: "hello from deploy\n"}}, page.Items)
	assert.True(t, page.Complete)
	assert.Equal(t, apperr.CodeJobNotRunning, codeOf(t, e.svc.AppendLog(context.Background(), deployID, 1, "late", nil)))
	assert.Equal(t, apperr.CodeJobNotRunning, codeOf(t, e.svc.Complete(context.Background(), deployID, true, nil, "")))
}

func TestLogsMaskingAndLimits(t *testing.T) {
	e := newEnv(t)
	e.git.set("main", "bbbbbbb1", "version: 1\nstages: [a]\njobs:\n  only:\n    stage: a\n    image: alpine\n    steps: [echo]\n")
	e.run(t, e.dev, "main")
	c, err := e.svc.Claim(context.Background(), e.orgID, e.runnerID, nil, nil)
	require.NoError(t, err)
	ctx := context.Background()
	mask := Masker{"s3cr3t-value", "abc"} // too-short values aren't masked
	require.NoError(t, e.svc.AppendLog(ctx, c.Job.ID, 0, "token=s3cr3t-value abc\n", mask))
	require.NoError(t, e.svc.AppendLog(ctx, c.Job.ID, 0, "duplicate seq is ignored\n", mask))
	require.NoError(t, e.svc.AppendLog(ctx, c.Job.ID, 1, "line two\n", mask))
	page, err := e.svc.Logs(e.dev.ctx, c.Job.ID, -1, 10)
	require.NoError(t, err)
	assert.Equal(t, []LogChunk{{Seq: 0, Content: "token=•••••• abc\n"}, {Seq: 1, Content: "line two\n"}}, page.Items)
	assert.False(t, page.Complete, "job still running")
	page, err = e.svc.Logs(e.dev.ctx, c.Job.ID, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int32(1), page.NextSeq)

	big := make([]byte, MaxLogChunkBytes+1)
	assert.Equal(t, apperr.CodePayloadTooLarge, codeOf(t, e.svc.AppendLog(ctx, c.Job.ID, 2, string(big), nil)))
	// Past the per-job limit, output is cut with a notice.
	_, err = pgtest.Pool(t).Exec(ctx, "UPDATE pipeline_jobs SET log_bytes = $1 WHERE id = $2", MaxJobLogBytes-5, c.Job.ID)
	require.NoError(t, err)
	require.NoError(t, e.svc.AppendLog(ctx, c.Job.ID, 2, "0123456789", nil))
	require.NoError(t, e.svc.AppendLog(ctx, c.Job.ID, 3, "dropped", nil))
	page, err = e.svc.Logs(e.dev.ctx, c.Job.ID, 1, 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "01234"+truncatedNotice, page.Items[0].Content)

	assert.Equal(t, "a •••••• b", Masker{"xyzzy"}.Mask("a xyzzy b"))
	assert.Equal(t, "••••••", Masker{"pass", "password"}.Mask("password"), "longest first")
}

func TestApprovalGateAndProtection(t *testing.T) {
	e := newEnv(t)
	e.environment(t, "production", &project.Protection{RequiredApprovals: 2, AllowedBranches: []string{"main"}, AllowedRoles: []authz.Role{authz.Admin}}, nil)
	def := "version: 1\nstages: [test, deploy]\njobs:\n  test:\n    stage: test\n    image: i\n    steps: [t]\n  deploy:\n    stage: deploy\n    environment: production\n    when: manual\n    image: i\n    steps: [d]\n"
	e.git.set("main", "ccccccc1", def)
	e.git.set("feature", "ccccccc2", def)

	r := e.run(t, e.admin, "main")
	e.runNext(t, true)
	r = e.get(t, r.ID)
	assert.Equal(t, store.RunStatusWaiting, r.Status)
	deploy := jobByName(t, r, "deploy")
	assert.Equal(t, store.JobStatusWaitingApproval, deploy.Status)

	jd, err := e.svc.GetJob(e.dev.ctx, deploy.ID)
	require.NoError(t, err)
	require.NotNil(t, jd.Approval)
	assert.Equal(t, 2, jd.Approval.Required)
	assert.True(t, jd.Approval.Protected)
	assert.False(t, jd.Approval.CanDecide)
	assert.Equal(t, DenyRole, jd.Approval.DeniedReason, "Developers aren't in the allowed roles")

	decide := func(u user, d store.ApprovalDecision) error {
		_, err := e.svc.Decide(u.ctx, deploy.ID, DecideInput{Decision: d, Comment: "ok"})
		return err
	}
	err = decide(e.dev, store.ApprovalDecisionApproved)
	assert.Equal(t, apperr.CodeApprovalNotAllowed, codeOf(t, err))
	assert.Equal(t, DenyRole, detail(t, err)["reason"])
	err = decide(e.admin, store.ApprovalDecisionApproved)
	assert.Equal(t, DenySelf, detail(t, err)["reason"], "whoever started the run can't approve it")
	err = decide(e.viewer, store.ApprovalDecisionApproved)
	assert.Equal(t, DenyPermission, detail(t, err)["reason"])
	_, err = e.svc.Decide(e.outsider.ctx, deploy.ID, DecideInput{Decision: store.ApprovalDecisionApproved})
	assert.Equal(t, apperr.CodeJobNotFound, codeOf(t, err))

	// Owners count as "Admin or higher".
	require.NoError(t, decide(e.owner, store.ApprovalDecisionApproved))
	assert.Equal(t, DenyDecided, detail(t, decide(e.owner, store.ApprovalDecisionApproved))["reason"])
	assert.Equal(t, store.JobStatusWaitingApproval, jobByName(t, e.get(t, r.ID), "deploy").Status, "1 of 2 approvals")

	// A second admin completes the approvals.
	admin2 := newUser(t)
	require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: e.orgID, UserID: admin2.id, Role: authz.Admin}))
	jd, err = e.svc.Decide(admin2.ctx, deploy.ID, DecideInput{Decision: store.ApprovalDecisionApproved})
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusQueued, jd.Status)
	assert.Len(t, jd.Approval.Approvals, 2)
	assert.Equal(t, DenyNotWaiting, jd.Approval.DeniedReason)
	assert.Equal(t, store.RunStatusRunning, e.get(t, r.ID).Status)
	assert.Equal(t, "deploy", e.runNext(t, true).Job.Name, "the approved job is picked up")
	assert.Equal(t, store.RunStatusSucceeded, e.get(t, r.ID).Status)

	// Another branch may not deploy to production.
	r2 := e.run(t, e.admin, "feature")
	e.runNext(t, true)
	assert.Equal(t, "failed/branch_not_allowed", statuses(e.get(t, r2.ID))["deploy"])

	// A rejection fails the job and the run.
	r3 := e.run(t, e.dev, "main")
	e.runNext(t, true)
	deploy3 := jobByName(t, e.get(t, r3.ID), "deploy")
	_, err = e.svc.Decide(e.owner.ctx, deploy3.ID, DecideInput{Decision: store.ApprovalDecisionRejected, Comment: "not today"})
	require.NoError(t, err)
	r3 = e.get(t, r3.ID)
	assert.Equal(t, "failed/rejected", statuses(r3)["deploy"])
	assert.Equal(t, store.RunStatusFailed, r3.Status)
}

func TestCancelRetryRerun(t *testing.T) {
	e := newEnv(t)
	e.git.set("main", "ddddddd1", threeStage)
	e.environment(t, "production", nil, nil)

	r := e.run(t, e.dev, "main")
	_, err := e.svc.CancelRun(e.viewer.ctx, r.ID)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	r, err = e.svc.CancelRun(e.dev.ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RunStatusCanceled, r.Status)
	assert.Equal(t, map[string]string{"test": "canceled", "build": "canceled", "deploy": "canceled"}, statuses(r))
	_, err = e.svc.CancelRun(e.dev.ctx, r.ID)
	assert.Equal(t, apperr.CodeRunNotCancelable, codeOf(t, err))

	// Retrying a job re-opens the run with the jobs after it.
	j, err := e.svc.RetryJob(e.dev.ctx, jobByName(t, r, "test").ID)
	require.NoError(t, err)
	assert.Equal(t, int32(2), j.Attempt)
	assert.Equal(t, store.JobStatusQueued, j.Status)
	r = e.get(t, r.ID)
	assert.Equal(t, store.RunStatusQueued, r.Status)
	assert.Nil(t, r.FinishedAt)
	assert.Equal(t, map[string]string{"test": "queued", "build": "created", "deploy": "created"}, statuses(r))
	_, err = e.svc.RetryJob(e.dev.ctx, j.ID)
	assert.Equal(t, apperr.CodeJobNotRetryable, codeOf(t, err), "still queued")

	// Fail it, then re-run failed jobs in place.
	e.runNext(t, false)
	r = e.get(t, r.ID)
	assert.Equal(t, "skipped/upstream_failed", statuses(r)["build"])
	r, err = e.svc.Rerun(e.dev.ctx, r.ID, true)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"test": "queued", "build": "created", "deploy": "created"}, statuses(r))
	jd, err := e.svc.GetJob(e.dev.ctx, jobByName(t, r, "test").ID)
	require.NoError(t, err)
	assert.Len(t, jd.Attempts, 3)
	_, err = e.svc.Rerun(e.dev.ctx, r.ID, true)
	assert.Equal(t, apperr.CodeJobNotRetryable, codeOf(t, err), "nothing failed and the run is going")

	// A full re-run is a new run from the same snapshot.
	again, err := e.svc.Rerun(e.admin.ctx, r.ID, false)
	require.NoError(t, err)
	assert.Equal(t, int32(2), again.Number)
	assert.Equal(t, &r.ID, again.RerunOf)
	assert.Equal(t, "ddddddd1", again.CommitSHA)
	assert.Equal(t, &e.admin.id, again.CreatedBy)
}

func TestInvalidDefinitionsAndRefs(t *testing.T) {
	e := newEnv(t)
	e.git.set("main", "eeeeeee1", "version: 1\nstages: [a]\njobs:\n  x:\n    stage: b\n    image: i\n    steps: [ls]\n")
	e.git.set("empty", "eeeeeee2", "")

	_, err := e.svc.TriggerManual(e.dev.ctx, e.projectID, ManualRunInput{Ref: "main"})
	assert.Equal(t, apperr.CodePipelineInvalid, codeOf(t, err))
	problems := detail(t, err)["problems"]
	assert.NotEmpty(t, problems)
	_, err = e.svc.TriggerManual(e.dev.ctx, e.projectID, ManualRunInput{Ref: "empty"})
	assert.Equal(t, apperr.CodePipelineFileNotFound, codeOf(t, err))
	_, err = e.svc.TriggerManual(e.dev.ctx, e.projectID, ManualRunInput{Ref: "nope"})
	assert.Equal(t, apperr.CodeRefNotFound, codeOf(t, err))
	_, err = e.svc.TriggerManual(e.dev.ctx, e.projectID, ManualRunInput{Ref: "a b"})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))
	_, err = e.svc.TriggerManual(e.dev.ctx, e.projectID, ManualRunInput{Ref: "main", Variables: map[string]string{"OPSHUB_X": "1"}})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))
	_, err = e.svc.TriggerManual(e.viewer.ctx, e.projectID, ManualRunInput{Ref: "main"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.TriggerManual(e.outsider.ctx, e.projectID, ManualRunInput{Ref: "main"})
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))
	e.git.down = true
	_, err = e.svc.TriggerManual(e.dev.ctx, e.projectID, ManualRunInput{Ref: "main"})
	assert.Equal(t, apperr.CodeGitProviderUnreachable, codeOf(t, err))

	res, err := e.svc.Validate(e.viewer.ctx, e.projectID, "version: 1\nstages: [a]\njobs: {}\n")
	require.NoError(t, err)
	assert.False(t, res.Valid)
	assert.Equal(t, "empty", res.Problems[0].Rule)
	res, err = e.svc.Validate(e.viewer.ctx, e.projectID, threeStage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
	assert.Len(t, res.Definition.Jobs, 3)
}

func TestRunsFromWebhookEvents(t *testing.T) {
	e := newEnv(t)
	repo, err := e.projects.GetRepository(e.dev.ctx, e.projectID)
	require.NoError(t, err)
	event := func(kind, ref, sha, base string) jobs.PipelineFromEventArgs {
		return jobs.PipelineFromEventArgs{RepositoryID: repo.ID, ProjectID: e.projectID, Event: kind, Ref: ref, SHA: sha, BaseRef: base, Title: "Add feature", Actor: "octocat"}
	}
	list := func() []Run {
		page, err := e.svc.ListRuns(e.viewer.ctx, e.projectID, RunFilter{}, pagination.Params{Limit: 50})
		require.NoError(t, err)
		return page.Items
	}
	ctx := context.Background()
	e.git.files["fffffff1"] = threeStage

	require.NoError(t, e.svc.RunFromEvent(ctx, event("push", "refs/heads/main", "fffffff1", "")))
	runs := list()
	require.Len(t, runs, 1)
	assert.Equal(t, store.RunTriggerPush, runs[0].Trigger)
	assert.Equal(t, "octocat", runs[0].ActorName)
	assert.Equal(t, "Add feature", runs[0].Title)
	// Pushing to the default branch syncs cron schedules.
	schedules, err := e.q.ListSchedules(ctx, e.projectID)
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	assert.Equal(t, "0 3 * * *", schedules[0].Cron)

	require.NoError(t, e.svc.RunFromEvent(ctx, event("push", "refs/heads/main", "fffffff1", "")))
	assert.Len(t, list(), 1, "a retried event doesn't create a second run")
	require.NoError(t, e.svc.RunFromEvent(ctx, event("push", "refs/heads/feature", "fffffff1", "")))
	assert.Len(t, list(), 1, "push trigger is limited to main")
	require.NoError(t, e.svc.RunFromEvent(ctx, event("pull_request", "refs/heads/feature", "fffffff1", "main")))
	assert.Len(t, list(), 2, "pull requests into main run")
	require.NoError(t, e.svc.RunFromEvent(ctx, event("tag_push", "refs/tags/v1", "fffffff1", "")))
	assert.Len(t, list(), 2, "no tag trigger")

	e.git.files["fffffff2"] = "version: 1\nstages: [a]\n"
	require.NoError(t, e.svc.RunFromEvent(ctx, event("push", "refs/heads/main", "fffffff2", "")))
	runs = list()
	require.Len(t, runs, 3)
	assert.Equal(t, store.RunStatusFailed, runs[0].Status, "an invalid file makes a failed run")
	assert.NotEmpty(t, runs[0].Problems)
	_, err = e.svc.Rerun(e.dev.ctx, runs[0].ID, false)
	assert.Equal(t, apperr.CodePipelineInvalid, codeOf(t, err))

	require.NoError(t, e.svc.RunFromEvent(ctx, event("push", "refs/heads/main", "fffffff3", "")), "no .opshub.yml: ignored")
	assert.Len(t, list(), 3)
	e.git.down = true
	assert.Error(t, e.svc.RunFromEvent(ctx, event("push", "refs/heads/main", "fffffff4", "")), "Git host errors are retried")

	// Filters.
	page, err := e.svc.ListRuns(e.viewer.ctx, e.projectID, RunFilter{Trigger: "pull_request"}, pagination.Params{Limit: 50})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	page, err = e.svc.ListRuns(e.viewer.ctx, e.projectID, RunFilter{Status: "failed", Ref: "main"}, pagination.Params{Limit: 50})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	_, err = e.svc.ListRuns(e.viewer.ctx, e.projectID, RunFilter{Status: "bogus"}, pagination.Params{Limit: 50})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))
	_, err = e.svc.ListRuns(e.outsider.ctx, e.projectID, RunFilter{}, pagination.Params{Limit: 50})
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))
	_, err = e.svc.GetRun(e.outsider.ctx, runs[0].ID)
	assert.Equal(t, apperr.CodeRunNotFound, codeOf(t, err))
}

func TestTickSchedulesAndReaper(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pool := pgtest.Pool(t)
	e.git.set("main", "1111111a", threeStage)
	r := e.run(t, e.dev, "main") // syncs the schedule (manual run on the default branch)

	// A due schedule is enqueued once and moved to its next time.
	_, err := pool.Exec(ctx, "UPDATE pipeline_schedules SET next_run_at = now() - interval '1 minute' WHERE project_id = $1", e.projectID)
	require.NoError(t, err)
	var enqueued []jobs.PipelineScheduleArgs
	enqueue := func(_ context.Context, _ pgx.Tx, a jobs.PipelineScheduleArgs) error {
		enqueued = append(enqueued, a)
		return nil
	}
	require.NoError(t, e.svc.Tick(ctx, enqueue))
	require.Len(t, enqueued, 1)
	require.NoError(t, e.svc.Tick(ctx, enqueue))
	assert.Len(t, enqueued, 1, "not due again")
	schedules, err := e.q.ListSchedules(ctx, e.projectID)
	require.NoError(t, err)
	assert.True(t, schedules[0].NextRunAt.After(time.Now()))

	require.NoError(t, e.svc.RunFromSchedule(ctx, enqueued[0]))
	page, err := e.svc.ListRuns(e.dev.ctx, e.projectID, RunFilter{Trigger: "schedule"}, pagination.Params{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "refs/heads/main", page.Items[0].Ref)

	// Removing the schedule from the file stops it.
	e.git.set("main", "1111111b", "version: 1\nstages: [a]\njobs:\n  x:\n    stage: a\n    image: i\n    steps: [ls]\n")
	require.NoError(t, e.svc.RunFromSchedule(ctx, enqueued[0]))
	schedules, err = e.q.ListSchedules(ctx, e.projectID)
	require.NoError(t, err)
	assert.Empty(t, schedules)

	// Timeouts and jobs nobody picked up.
	c, err := e.svc.Claim(ctx, e.orgID, e.runnerID, []string{"linux"}, nil)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE pipeline_jobs SET started_at = now() - interval '2 hours' WHERE id = $1", c.Job.ID)
	require.NoError(t, err)
	queuedJob := jobByName(t, e.get(t, page.Items[0].ID), "test")
	_, err = pool.Exec(ctx, "UPDATE pipeline_jobs SET queued_at = now() - interval '25 hours' WHERE id = $1", queuedJob.ID)
	require.NoError(t, err)
	require.NoError(t, e.svc.Tick(ctx, enqueue))
	assert.Equal(t, "failed/timeout", statuses(e.get(t, r.ID))["test"])
	assert.Equal(t, "failed/no_runner", statuses(e.get(t, page.Items[0].ID))["test"])
}
