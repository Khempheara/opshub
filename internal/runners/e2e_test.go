package runners

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/dockerapi"
	"github.com/opshub/opshub/internal/runner"
	"github.com/opshub/opshub/internal/store"
)

const e2ePipeline = `version: 1
stages: [build, test]
jobs:
  build:
    stage: build
    image: alpine:3.20
    steps:
      - test -f README.md && echo "source ok"
      - mkdir -p dist .cache && echo "built $OPSHUB_JOB_NAME" > dist/app.txt && echo cached > .cache/c.txt
    artifacts:
      paths: [dist/]
    cache:
      key: deps
      paths: [.cache/]
  test:
    stage: test
    image: alpine:3.20
    cache:
      key: deps
      paths: [.cache/]
    steps:
      - cat dist/app.txt
      - cat .cache/c.txt
      - echo "about to fail" && exit 3
`

// dockerSocket finds a local Docker daemon, or skips the test.
func dockerSocket(t *testing.T) string {
	t.Helper()
	if os.Getenv("OPSHUB_SKIP_DOCKER_TESTS") != "" {
		t.Skip("OPSHUB_SKIP_DOCKER_TESTS is set")
	}
	candidates := []string{strings.TrimPrefix(os.Getenv("DOCKER_HOST"), "unix://"), "/var/run/docker.sock"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker/run/docker.sock"))
	}
	for _, c := range candidates {
		if c != "" && !strings.Contains(c, "://") {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	t.Skip("no Docker socket")
	return ""
}

// TestEndToEndWithDocker runs two jobs through the HTTP API with the real agent executor
// and Docker: checkout, steps, logs, artifacts, dependencies, cache and a failing step.
func TestEndToEndWithDocker(t *testing.T) {
	socket := dockerSocket(t)
	e := newEnv(t)
	e.git.mu.Lock()
	e.git.tarball = tarGz(t, map[string]string{"acme-api-abc123/README.md": "hello", "acme-api-abc123/src/main.go": "package main"})
	e.git.mu.Unlock()
	setPipeline(t, e, e2ePipeline)
	a := newAPI(t, e)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	docker, err := dockerapi.NewUnix(ctx, socket)
	require.NoError(t, err)

	rt, err := e.svc.CreateRegistrationToken(e.admin.ctx, e.orgID, RegistrationInput{})
	require.NoError(t, err)
	client := runner.NewClient(a.srv.URL, "test")
	reg, err := client.Register(ctx, runner.RegisterInput{Token: rt.Token, Name: "e2e", Labels: []string{"linux"}, MaxConcurrency: 1})
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	exec := runner.NewExecutor(docker, client, runner.ExecConfig{
		RunnerID: reg.ID, DefaultImage: "alpine:3.20", PidsLimit: 256, MemoryBytes: 256 << 20, TempDir: t.TempDir(),
	}, logger)
	t.Cleanup(func() { exec.Cleanup(context.Background()) })

	run := e.trigger(t)
	for _, want := range []string{"build", "test"} {
		j, err := client.RequestJob(ctx, reg.Token, 5*time.Second)
		require.NoError(t, err)
		require.NotNil(t, j, "expected job %s", want)
		require.Equal(t, want, j.Job.Name)
		exec.Run(ctx, j)
	}

	detail, err := e.pipelines.GetRun(e.admin.ctx, run.ID)
	require.NoError(t, err)
	byName := map[string]store.JobStatus{}
	for _, j := range detail.Jobs {
		byName[j.Name] = j.Status
	}
	assert.Equal(t, store.JobStatusSucceeded, byName["build"])
	assert.Equal(t, store.JobStatusFailed, byName["test"])

	logOf := func(name string) string {
		for _, j := range detail.Jobs {
			if j.Name == name {
				page, err := e.pipelines.Logs(e.admin.ctx, j.ID, -1, 1000)
				require.NoError(t, err)
				var b strings.Builder
				for _, c := range page.Items {
					b.WriteString(c.Content)
				}
				return b.String()
			}
		}
		t.Fatalf("no job %s", name)
		return ""
	}
	build := logOf("build")
	assert.Contains(t, build, "source ok")
	assert.Contains(t, build, "Uploaded artifacts")
	assert.Contains(t, build, "Saved cache deps")
	test := logOf("test")
	assert.Contains(t, test, "built build", "dependency artifacts restored")
	assert.Contains(t, test, "cached", "cache restored")
	assert.Contains(t, test, "about to fail")
	assert.Contains(t, test, "exited with code 3")

	for _, j := range detail.Jobs {
		if j.Name == "test" {
			require.NotNil(t, j.ExitCode)
			assert.Equal(t, int32(3), *j.ExitCode)
			require.NotNil(t, j.FailureReason)
			assert.Equal(t, "step_failed", *j.FailureReason)
			assert.Equal(t, store.StepStatusSucceeded, j.Steps[0].Status)
			assert.Equal(t, store.StepStatusFailed, j.Steps[2].Status)
		}
	}
}

// TestCancelStopsContainer cancels a run while a step sleeps; the heartbeat path cancels
// the executor, which kills the container quickly.
func TestCancelStopsContainer(t *testing.T) {
	socket := dockerSocket(t)
	e := newEnv(t)
	setPipeline(t, e, `version: 1
stages: [build]
jobs:
  build:
    stage: build
    image: alpine:3.20
    steps: [sleep 120]
`)
	a := newAPI(t, e)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker, err := dockerapi.NewUnix(ctx, socket)
	require.NoError(t, err)
	rt, err := e.svc.CreateRegistrationToken(e.admin.ctx, e.orgID, RegistrationInput{})
	require.NoError(t, err)
	client := runner.NewClient(a.srv.URL, "test")
	reg, err := client.Register(ctx, runner.RegisterInput{Token: rt.Token, Name: "e2e", Labels: []string{"linux"}})
	require.NoError(t, err)
	exec := runner.NewExecutor(docker, client, runner.ExecConfig{RunnerID: reg.ID, DefaultImage: "alpine:3.20", TempDir: t.TempDir()},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { exec.Cleanup(context.Background()) })

	run := e.trigger(t)
	j, err := client.RequestJob(ctx, reg.Token, 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, j)

	jobCtx, stop := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() { exec.Run(jobCtx, j); close(done) }()
	time.Sleep(3 * time.Second)
	_, err = e.pipelines.CancelRun(e.admin.ctx, run.ID)
	require.NoError(t, err)
	stopIDs, err := client.Heartbeat(ctx, reg.Token, "test", "linux", "amd64", []string{j.Job.ID})
	require.NoError(t, err)
	require.Equal(t, []string{j.Job.ID}, stopIDs)
	start := time.Now()
	stop(runner.ErrCanceledByServer)
	select {
	case <-done:
	case <-time.After(45 * time.Second):
		t.Fatal("executor didn't stop")
	}
	assert.Less(t, time.Since(start), 30*time.Second)
	st, _ := e.jobStatus(t, detailJobID(t, e, run.ID))
	assert.Equal(t, store.JobStatusCanceled, st)
}

func detailJobID(t *testing.T, e *env, runID uuid.UUID) (id uuid.UUID) {
	t.Helper()
	require.NoError(t, e.svc.pool.QueryRow(context.Background(), `SELECT id FROM pipeline_jobs WHERE run_id = $1`, runID).Scan(&id))
	return id
}
