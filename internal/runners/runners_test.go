package runners

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/store"
)

func TestRegistration(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	_, err := e.svc.CreateRegistrationToken(e.dev.ctx, e.orgID, RegistrationInput{})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.CreateRegistrationToken(e.outsider.ctx, e.orgID, RegistrationInput{})
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))
	_, err = e.svc.CreateRegistrationToken(e.admin.ctx, e.orgID, RegistrationInput{Labels: []string{"Bad Label!"}})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))

	rt, err := e.svc.CreateRegistrationToken(e.admin.ctx, e.orgID, RegistrationInput{Labels: []string{"GPU", "linux"}})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(rt.Token, "ohr_reg_"))
	assert.Equal(t, []string{"gpu", "linux"}, rt.Labels)
	assert.WithinDuration(t, time.Now().Add(RegistrationTTL), rt.ExpiresAt, time.Minute)

	reg, err := e.svc.RegisterRunner(ctx, RegisterInput{Token: rt.Token, Name: " build-1 ", Labels: []string{"linux", "docker"}})
	require.NoError(t, err)
	assert.Equal(t, "build-1", reg.Name)
	assert.Equal(t, []string{"docker", "gpu", "linux"}, reg.Labels)
	assert.True(t, strings.HasPrefix(reg.Token, "ohr_"))
	assert.False(t, strings.HasPrefix(reg.Token, "ohr_reg_"))
	assert.Equal(t, 10, reg.HeartbeatIntervalSeconds)

	// One-time: the same registration token can't register a second runner.
	_, err = e.svc.RegisterRunner(ctx, RegisterInput{Token: rt.Token, Name: "build-2"})
	assert.Equal(t, apperr.CodeRegistrationTokenInvalid, codeOf(t, err))
	_, err = e.svc.RegisterRunner(ctx, RegisterInput{Token: "ohr_reg_nope", Name: "x"})
	assert.Equal(t, apperr.CodeRegistrationTokenInvalid, codeOf(t, err))
	_, err = e.svc.RegisterRunner(ctx, RegisterInput{Token: "something-else", Name: "x"})
	assert.Equal(t, apperr.CodeRegistrationTokenInvalid, codeOf(t, err))

	r, err := e.svc.AuthenticateRunner(ctx, reg.Token)
	require.NoError(t, err)
	assert.Equal(t, reg.ID, r.ID)
	assert.Equal(t, int32(1), r.MaxConcurrency)
	for _, bad := range []string{"", "ohr_x_y", rt.Token, "ohj_" + reg.Token} {
		_, err = e.svc.AuthenticateRunner(ctx, bad)
		assert.Equal(t, apperr.CodeRunnerTokenInvalid, codeOf(t, err), bad)
	}

	// Expired registration tokens are refused.
	rt2, err := e.svc.CreateRegistrationToken(e.admin.ctx, e.orgID, RegistrationInput{})
	require.NoError(t, err)
	_, err = pgExec(t, e, `UPDATE runner_registration_tokens SET expires_at = now() - interval '1 second'`)
	require.NoError(t, err)
	_, err = e.svc.RegisterRunner(ctx, RegisterInput{Token: rt2.Token, Name: "late"})
	assert.Equal(t, apperr.CodeRegistrationTokenInvalid, codeOf(t, err))
}

func pgExec(t *testing.T, e *env, sql string, args ...any) (int64, error) {
	t.Helper()
	tag, err := e.svc.pool.Exec(context.Background(), sql, args...)
	return tag.RowsAffected(), err
}

func TestManageRunners(t *testing.T) {
	e := newEnv(t)
	r, _ := e.register(t, "alpha", []string{"linux"}, 2)

	list, err := e.svc.ListRunners(e.dev.ctx, e.orgID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "online", list[0].Status) // just registered and touched
	assert.Equal(t, "linux", list[0].OS)
	_, err = e.svc.ListRunners(e.viewer.ctx, e.orgID)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.ListRunners(e.outsider.ctx, e.orgID)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))

	in := UpdateInput{Name: "alpha-2", Labels: []string{"linux", "arm64"}, MaxConcurrency: 3, Disabled: true}
	_, err = e.svc.UpdateRunner(e.dev.ctx, r.ID, r.RowVersion, in)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.UpdateRunner(e.outsider.ctx, r.ID, r.RowVersion, in)
	assert.Equal(t, apperr.CodeRunnerNotFound, codeOf(t, err))
	_, err = e.svc.UpdateRunner(e.admin.ctx, r.ID, r.RowVersion+5, in)
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))
	up, err := e.svc.UpdateRunner(e.admin.ctx, r.ID, r.RowVersion, in)
	require.NoError(t, err)
	assert.Equal(t, "disabled", up.Status)
	assert.Equal(t, []string{"arm64", "linux"}, up.Labels)
	assert.Equal(t, int32(3), up.MaxConcurrency)
	assert.Equal(t, r.RowVersion+1, up.RowVersion)

	assert.Equal(t, apperr.CodeForbidden, codeOf(t, e.svc.DeleteRunner(e.dev.ctx, r.ID)))
	assert.Equal(t, apperr.CodeRunnerNotFound, codeOf(t, e.svc.DeleteRunner(e.admin.ctx, uuid.New())))
	require.NoError(t, e.svc.DeleteRunner(e.admin.ctx, r.ID))
	list, err = e.svc.ListRunners(e.admin.ctx, e.orgID)
	require.NoError(t, err)
	assert.Empty(t, list)

	var n int
	require.NoError(t, e.svc.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action LIKE 'runner.%'`, e.orgID).Scan(&n))
	assert.Equal(t, 4, n) // registration_token, register, update, delete
}

func TestDisabledRunner(t *testing.T) {
	e := newEnv(t)
	r, token := e.register(t, "alpha", []string{"linux"}, 1)
	_, err := e.svc.UpdateRunner(e.admin.ctx, r.ID, r.RowVersion, UpdateInput{Name: "alpha", Labels: r.Labels, MaxConcurrency: 1, Disabled: true})
	require.NoError(t, err)
	_, err = e.svc.AuthenticateRunner(context.Background(), token)
	assert.Equal(t, apperr.CodeRunnerDisabled, codeOf(t, err))

	// A request that authenticated just before the runner was disabled claims nothing.
	e.trigger(t)
	_, err = e.svc.tryClaim(context.Background(), r)
	assert.Equal(t, apperr.CodeRunnerDisabled, codeOf(t, err))
}

func TestJobLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r, _ := e.register(t, "alpha", []string{"linux"}, 1)
	run := e.trigger(t)

	a := e.claim(t, r)
	require.NotNil(t, a)
	assert.Equal(t, "build", a.Job.Name)
	assert.Equal(t, sha, a.CommitSHA)
	assert.Equal(t, run.ID, a.RunID)
	assert.True(t, strings.HasPrefix(a.Token, "ohj_"))
	assert.Empty(t, a.Secrets)
	assert.Equal(t, "true", a.Variables["CI"])
	assert.NotNil(t, a.Spec.Artifacts)

	// max_concurrency 1: nothing more for this runner while build runs.
	e.trigger(t)
	assert.Nil(t, e.claim(t, r))

	j := e.job(t, a)
	_, err := e.svc.AuthenticateJob(ctx, a.Token, uuid.New())
	assert.Equal(t, apperr.CodeJobTokenInvalid, codeOf(t, err))
	_, err = e.svc.AuthenticateJob(ctx, "ohj_bogus", a.Job.ID)
	assert.Equal(t, apperr.CodeJobTokenInvalid, codeOf(t, err))

	require.NoError(t, e.svc.Report(ctx, j, ReportInput{Step: &StepReport{Index: 0, Status: store.StepStatusRunning}}))
	require.NoError(t, e.svc.AppendLog(ctx, j, LogInput{Seq: 1, Content: "building\n"}))
	assert.Equal(t, apperr.CodeValidation, codeOf(t, e.svc.Report(ctx, j, ReportInput{})))
	assert.Equal(t, apperr.CodeValidation, codeOf(t, e.svc.Report(ctx, j, ReportInput{
		Step: &StepReport{Index: 0, Status: store.StepStatusRunning}, Complete: &Completion{Success: true},
	})))
	assert.Equal(t, apperr.CodeValidation, codeOf(t, e.svc.Report(ctx, j, ReportInput{Complete: &Completion{Reason: "rejected"}})))

	// Source: the commit's tarball.
	src, err := e.svc.Source(ctx, j)
	require.NoError(t, err)
	body, err := io.ReadAll(src)
	require.NoError(t, err)
	require.NoError(t, src.Close())
	assert.Equal(t, e.git.tarball, body)

	zero := int32(0)
	require.NoError(t, e.svc.Report(ctx, j, ReportInput{Complete: &Completion{Success: true, ExitCode: &zero}}))
	st, _ := e.jobStatus(t, a.Job.ID)
	assert.Equal(t, store.JobStatusSucceeded, st)

	// The finished job's token opens nothing.
	_, err = e.svc.AuthenticateJob(ctx, a.Token, a.Job.ID)
	assert.Equal(t, apperr.CodeJobNotRunning, codeOf(t, err))

	// Now the runner has room again: the oldest queued job is the second run's build.
	next := e.claim(t, r)
	require.NotNil(t, next)
	assert.Equal(t, "build", next.Job.Name)
	assert.NotEqual(t, run.ID, next.RunID)
}

func TestLabelsSelectJobs(t *testing.T) {
	e := newEnv(t)
	arm, _ := e.register(t, "arm", []string{"arm64"}, 4)
	e.trigger(t)
	// build has no runs_on: any runner can take it.
	a := e.claim(t, arm)
	require.NotNil(t, a)
	zero := int32(0)
	require.NoError(t, e.svc.Report(context.Background(), e.job(t, a), ReportInput{Complete: &Completion{Success: true, ExitCode: &zero}}))
	// test needs "linux".
	assert.Nil(t, e.claim(t, arm))
	linux, _ := e.register(t, "x86", []string{"linux"}, 1)
	b := e.claim(t, linux)
	require.NotNil(t, b)
	assert.Equal(t, "test", b.Job.Name)
}

func TestConcurrentClaimsRespectCapacity(t *testing.T) {
	e := newEnv(t)
	r, _ := e.register(t, "alpha", []string{"linux"}, 2)
	for range 4 {
		e.trigger(t) // four build jobs queued
	}
	var mu sync.Mutex
	got := 0
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := e.svc.tryClaim(context.Background(), r)
			assert.NoError(t, err)
			if j != nil {
				mu.Lock()
				got++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 2, got)
	n, err := e.q.CountRunningJobs(context.Background(), &r.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestRequestJobLongPoll(t *testing.T) {
	e := newEnv(t)
	r, _ := e.register(t, "alpha", []string{"linux"}, 1)

	start := time.Now()
	j, err := e.svc.RequestJob(context.Background(), r, time.Second)
	require.NoError(t, err)
	assert.Nil(t, j)
	assert.GreaterOrEqual(t, time.Since(start), time.Second)

	done := make(chan *AssignedJob, 1)
	go func() {
		j, err := e.svc.RequestJob(context.Background(), r, 10*time.Second)
		assert.NoError(t, err)
		done <- j
	}()
	time.Sleep(500 * time.Millisecond)
	e.trigger(t)
	select {
	case j := <-done:
		require.NotNil(t, j)
		assert.Equal(t, "build", j.Job.Name)
	case <-time.After(8 * time.Second):
		t.Fatal("long poll didn't pick up the new job")
	}

	// A canceled request returns promptly with nothing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j, err = e.svc.RequestJob(ctx, r, 10*time.Second)
	require.NoError(t, err)
	assert.Nil(t, j)
}

func TestHeartbeat(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r, _ := e.register(t, "alpha", []string{"linux"}, 2)
	run := e.trigger(t)
	a := e.claim(t, r)
	require.NotNil(t, a)

	res, err := e.svc.Heartbeat(ctx, r, HeartbeatInput{Version: "1.1.0", OS: "linux", Arch: "arm64", RunningJobIDs: []uuid.UUID{a.Job.ID}})
	require.NoError(t, err)
	assert.Empty(t, res.CancelJobIDs)
	got, err := e.q.GetRunner(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", got.Version)
	assert.Equal(t, "arm64", got.Arch)

	// Canceling the run tells the runner to stop the job; unknown ids are stopped too.
	_, err = e.pipelines.CancelRun(e.admin.ctx, run.ID)
	require.NoError(t, err)
	stranger := uuid.New()
	res, err = e.svc.Heartbeat(ctx, r, HeartbeatInput{RunningJobIDs: []uuid.UUID{a.Job.ID, stranger}})
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{a.Job.ID, stranger}, res.CancelJobIDs)

	// A job assigned to the runner that it doesn't report (after the grace period) is lost.
	e.trigger(t)
	b := e.claim(t, r)
	require.NotNil(t, b)
	res, err = e.svc.Heartbeat(ctx, r, HeartbeatInput{})
	require.NoError(t, err)
	assert.Empty(t, res.CancelJobIDs)
	st, _ := e.jobStatus(t, b.Job.ID)
	assert.Equal(t, store.JobStatusRunning, st, "within the grace period")
	_, err = pgExec(t, e, `UPDATE pipeline_jobs SET started_at = now() - interval '1 minute' WHERE id = $1`, b.Job.ID)
	require.NoError(t, err)
	_, err = e.svc.Heartbeat(ctx, r, HeartbeatInput{})
	require.NoError(t, err)
	st, reason := e.jobStatus(t, b.Job.ID)
	assert.Equal(t, store.JobStatusFailed, st)
	assert.Equal(t, pipeline.ReasonRunnerLost, reason)
}

func TestDeleteRunnerFailsItsJobs(t *testing.T) {
	e := newEnv(t)
	r, token := e.register(t, "alpha", []string{"linux"}, 1)
	e.trigger(t)
	a := e.claim(t, r)
	require.NotNil(t, a)
	require.NoError(t, e.svc.DeleteRunner(e.admin.ctx, r.ID))
	st, reason := e.jobStatus(t, a.Job.ID)
	assert.Equal(t, store.JobStatusFailed, st)
	assert.Equal(t, pipeline.ReasonRunnerLost, reason)
	_, err := e.svc.AuthenticateRunner(context.Background(), token)
	assert.Equal(t, apperr.CodeRunnerTokenInvalid, codeOf(t, err))
}

func TestHousekeepLostRunner(t *testing.T) {
	e := newEnv(t)
	r, _ := e.register(t, "alpha", []string{"linux"}, 1)
	e.trigger(t)
	a := e.claim(t, r)
	require.NotNil(t, a)
	require.NoError(t, e.svc.Housekeep(context.Background()))
	st, _ := e.jobStatus(t, a.Job.ID)
	assert.Equal(t, store.JobStatusRunning, st)

	_, err := pgExec(t, e, `UPDATE runners SET last_seen_at = now() - interval '5 minutes' WHERE id = $1`, r.ID)
	require.NoError(t, err)
	list, err := e.svc.ListRunners(e.admin.ctx, e.orgID)
	require.NoError(t, err)
	assert.Equal(t, "offline", list[0].Status)
	assert.Equal(t, int64(1), list[0].RunningJobs)
	require.NoError(t, e.svc.Housekeep(context.Background()))
	st, reason := e.jobStatus(t, a.Job.ID)
	assert.Equal(t, store.JobStatusFailed, st)
	assert.Equal(t, pipeline.ReasonRunnerLost, reason)
}

func TestArtifacts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r, _ := e.register(t, "alpha", []string{"linux"}, 1)
	e.trigger(t)
	a := e.claim(t, r)
	require.NotNil(t, a)
	j := e.job(t, a)
	archive := tarGz(t, map[string]string{"dist/app": "binary"})

	_, err := e.svc.UploadArtifact(ctx, j, "../evil", bytes.NewReader(archive))
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))
	_, err = e.svc.UploadArtifact(ctx, j, "", bytes.NewReader(make([]byte, 2<<20)))
	assert.Equal(t, apperr.CodePayloadTooLarge, codeOf(t, err))

	art, err := e.svc.UploadArtifact(ctx, j, "", bytes.NewReader(archive))
	require.NoError(t, err)
	assert.Equal(t, "build.tar.gz", art.Name)
	assert.Equal(t, int64(len(archive)), art.SizeBytes)
	assert.Len(t, art.SHA256, 64)
	assert.WithinDuration(t, time.Now().AddDate(0, 0, 3), art.ExpiresAt, time.Minute)
	_, err = e.svc.UploadArtifact(ctx, j, "", bytes.NewReader(archive))
	assert.Equal(t, apperr.CodeConflict, codeOf(t, err))

	// Users with run.view list and download them; others can't see the job.
	list, err := e.svc.ListJobArtifacts(e.viewer.ctx, j.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	_, err = e.svc.ListJobArtifacts(e.outsider.ctx, j.ID)
	assert.Equal(t, apperr.CodeJobNotFound, codeOf(t, err))
	_, err = e.svc.OpenArtifact(e.outsider.ctx, art.ID)
	assert.Equal(t, apperr.CodeArtifactNotFound, codeOf(t, err))
	d, err := e.svc.OpenArtifact(e.viewer.ctx, art.ID)
	require.NoError(t, err)
	got, err := io.ReadAll(d.Body)
	require.NoError(t, err)
	require.NoError(t, d.Body.Close())
	assert.Equal(t, archive, got)

	// The next stage's job receives build's artifacts as a dependency.
	zero := int32(0)
	require.NoError(t, e.svc.Report(ctx, j, ReportInput{Complete: &Completion{Success: true, ExitCode: &zero}}))
	b := e.claim(t, r)
	require.NotNil(t, b)
	bj := e.job(t, b)
	deps, err := e.svc.Dependencies(ctx, bj)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, "build", deps[0].JobName)
	dd, err := e.svc.OpenDependency(ctx, bj, deps[0].ID)
	require.NoError(t, err)
	require.NoError(t, dd.Body.Close())
	_, err = e.svc.OpenDependency(ctx, bj, uuid.New())
	assert.Equal(t, apperr.CodeArtifactNotFound, codeOf(t, err))

	// Expired artifacts disappear, blob included.
	_, err = pgExec(t, e, `UPDATE artifacts SET expires_at = now() - interval '1 second' WHERE id = $1`, art.ID)
	require.NoError(t, err)
	var key string
	require.NoError(t, e.svc.pool.QueryRow(ctx, `SELECT storage_key FROM artifacts WHERE id = $1`, art.ID).Scan(&key))
	require.NoError(t, e.svc.Housekeep(ctx))
	_, _, err = e.blobs.Get(ctx, key)
	assert.Error(t, err)
	_, err = e.svc.OpenArtifact(e.viewer.ctx, art.ID)
	assert.Equal(t, apperr.CodeArtifactNotFound, codeOf(t, err))
}

func TestCache(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r, _ := e.register(t, "alpha", []string{"linux"}, 1)
	e.trigger(t)
	a := e.claim(t, r)
	require.NotNil(t, a)
	j := e.job(t, a)

	_, err := e.svc.GetCache(ctx, j, "deps")
	assert.Equal(t, apperr.CodeCacheNotFound, codeOf(t, err))
	for _, bad := range []string{"", "..", "a/b", strings.Repeat("k", 201)} {
		assert.Equal(t, apperr.CodeValidation, codeOf(t, e.svc.PutCache(ctx, j, bad, strings.NewReader("x"))), bad)
	}
	assert.Equal(t, apperr.CodePayloadTooLarge, codeOf(t, e.svc.PutCache(ctx, j, "deps", bytes.NewReader(make([]byte, 2<<20)))))

	require.NoError(t, e.svc.PutCache(ctx, j, "deps", strings.NewReader("v1")))
	var firstKey string
	require.NoError(t, e.svc.pool.QueryRow(ctx, `SELECT storage_key FROM cache_entries WHERE project_id = $1`, e.projectID).Scan(&firstKey))
	require.NoError(t, e.svc.PutCache(ctx, j, "deps", strings.NewReader("v2")))
	_, _, err = e.blobs.Get(ctx, firstKey)
	assert.Error(t, err, "the replaced blob is removed")
	d, err := e.svc.GetCache(ctx, j, "deps")
	require.NoError(t, err)
	got, _ := io.ReadAll(d.Body)
	require.NoError(t, d.Body.Close())
	assert.Equal(t, "v2", string(got))

	// Beyond the project quota (1 MiB here), the least recently used entries go.
	big := bytes.Repeat([]byte("z"), 700<<10)
	require.NoError(t, e.svc.PutCache(ctx, j, "old", bytes.NewReader(big)))
	require.NoError(t, e.svc.PutCache(ctx, j, "new", bytes.NewReader(big)))
	require.NoError(t, e.svc.Housekeep(ctx))
	_, err = e.svc.GetCache(ctx, j, "old")
	assert.Equal(t, apperr.CodeCacheNotFound, codeOf(t, err))
	_, err = e.svc.GetCache(ctx, j, "new")
	require.NoError(t, err)
}

func TestSourceWithoutRepository(t *testing.T) {
	e := newEnv(t)
	r, _ := e.register(t, "alpha", []string{"linux"}, 1)
	e.trigger(t)
	a := e.claim(t, r)
	require.NotNil(t, a)
	require.NoError(t, e.projects.DisconnectRepository(e.admin.ctx, e.projectID))
	_, err := e.svc.Source(context.Background(), e.job(t, a))
	assert.Equal(t, apperr.CodeRepositoryNotFound, codeOf(t, err))
}

func TestSourceSizeLimit(t *testing.T) {
	e := newEnv(t)
	e.svc.cfg.SourceMaxBytes = 10
	r, _ := e.register(t, "alpha", []string{"linux"}, 1)
	e.trigger(t)
	a := e.claim(t, r)
	require.NotNil(t, a)
	src, err := e.svc.Source(context.Background(), e.job(t, a))
	require.NoError(t, err)
	_, err = io.ReadAll(src)
	assert.ErrorContains(t, err, "size limit")
	require.NoError(t, src.Close())
}
