package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/events"
	"github.com/opshub/opshub/internal/idempotency"
	"github.com/opshub/opshub/internal/server"
	"github.com/opshub/opshub/internal/telemetry"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

type noTokens struct{}

func (noTokens) LookupAPIToken(context.Context, []byte) (uuid.UUID, uuid.UUID, []string, error) {
	return uuid.Nil, uuid.Nil, nil, io.EOF
}

type api struct {
	srv    *httptest.Server
	signer *authn.JWTSigner
}

func newAPI(t *testing.T, e *env) *api {
	t.Helper()
	signer, err := authn.NewJWTSigner([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{5}, 32)}}, nil)
	require.NoError(t, err)
	pool := pgtest.Pool(t)
	hub := events.NewHub(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	h := server.New(server.Deps{
		Config:        config.Config{DefaultLocale: "en", DefaultTimezone: "UTC", RateLimitRPS: 1000, RateLimitBurst: 1000},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            pool,
		Metrics:       telemetry.NewMetrics(),
		Version:       "test",
		Authenticator: &authn.Authenticator{JWT: signer, Tokens: noTokens{}},
		Modules:       []server.Module{NewHandler(e.svc, hub, idempotency.Middleware(pool))},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &api{srv: srv, signer: signer}
}

func (a *api) req(t *testing.T, ctx context.Context, u *user, method, path string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.srv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if u != nil {
		tok, _, err := a.signer.Issue(u.id, uuid.New())
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func (a *api) call(t *testing.T, u *user, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	resp := a.req(t, context.Background(), u, method, path, body, headers)
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

var tenantRoutes = []string{
	"POST /projects/{projectId}/pipeline/validate", "GET /projects/{projectId}/runs", "POST /projects/{projectId}/runs",
	"GET /runs/{runId}/", "POST /runs/{runId}/cancel", "POST /runs/{runId}/rerun", "GET /runs/{runId}/events",
	"GET /jobs/{jobId}/", "POST /jobs/{jobId}/retry", "GET /jobs/{jobId}/logs", "GET /jobs/{jobId}/logs/stream",
	"POST /jobs/{jobId}/approvals",
}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	e.git.set("main", "9999999a", threeStage)
	r := e.run(t, e.dev, "main")
	job := jobByName(t, r, "test").ID.String()
	p, run := e.projectID.String(), r.ID.String()

	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"POST", "/api/v1/projects/" + p + "/pipeline/validate", map[string]string{"content": "version: 1"}, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/projects/" + p + "/runs", nil, "PROJECT_NOT_FOUND"},
		{"POST", "/api/v1/projects/" + p + "/runs", map[string]string{"ref": "main"}, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/runs/" + run, nil, "RUN_NOT_FOUND"},
		{"POST", "/api/v1/runs/" + run + "/cancel", nil, "RUN_NOT_FOUND"},
		{"POST", "/api/v1/runs/" + run + "/rerun", map[string]bool{"failed_only": false}, "RUN_NOT_FOUND"},
		{"GET", "/api/v1/runs/" + run + "/events", nil, "RUN_NOT_FOUND"},
		{"GET", "/api/v1/jobs/" + job, nil, "JOB_NOT_FOUND"},
		{"POST", "/api/v1/jobs/" + job + "/retry", nil, "JOB_NOT_FOUND"},
		{"GET", "/api/v1/jobs/" + job + "/logs", nil, "JOB_NOT_FOUND"},
		{"GET", "/api/v1/jobs/" + job + "/logs/stream", nil, "JOB_NOT_FOUND"},
		{"POST", "/api/v1/jobs/" + job + "/approvals", map[string]string{"decision": "approved"}, "JOB_NOT_FOUND"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, body := a.call(t, &e.outsider, c.method, c.path, c.body, nil)
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, c.code, errCode(body))
		})
	}
	assert.Equal(t, "queued", string(e.get(t, r.ID).Status), "nothing changed")
}

func TestTenantIsolationCoversAllRoutes(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil, nil, func(h http.Handler) http.Handler { return h }).Mount(r)
	covered := map[string]bool{}
	for _, route := range tenantRoutes {
		covered[route] = true
	}
	var missing []string
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "{") && !covered[method+" "+route] {
			missing = append(missing, method+" "+route)
		}
		return nil
	}))
	assert.Empty(t, missing, "add these routes to TestTenantIsolation and tenantRoutes")
}

func TestHTTPFlows(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	e.git.set("main", "8888888a", threeStage)
	e.environment(t, "production", nil, nil)
	base := "/api/v1/projects/" + e.projectID.String()

	// Idempotent manual trigger.
	hdr := map[string]string{"Idempotency-Key": uuid.NewString()}
	status, first := a.call(t, &e.dev, "POST", base+"/runs", map[string]any{"ref": "main", "variables": map[string]string{"DEBUG": "1"}}, hdr)
	require.Equal(t, http.StatusCreated, status, first)
	status, again := a.call(t, &e.dev, "POST", base+"/runs", map[string]any{"ref": "main", "variables": map[string]string{"DEBUG": "1"}}, hdr)
	assert.Equal(t, http.StatusCreated, status)
	assert.Equal(t, first["id"], again["id"], "replayed, not a second run")

	status, body := a.call(t, &e.viewer, "POST", base+"/runs", map[string]string{"ref": "main"}, nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "FORBIDDEN", errCode(body))
	status, body = a.call(t, &e.viewer, "GET", base+"/runs?status=queued", nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, body["items"], 1)

	status, body = a.call(t, &e.dev, "POST", base+"/pipeline/validate", map[string]string{"content": "version: 1\nstages: [a]\njobs:\n  x: {stage: z, image: i, steps: [ls]}\n"}, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, false, body["valid"])
	problems := body["problems"].([]any)
	assert.Equal(t, "unknown_stage", problems[0].(map[string]any)["rule"])
	assert.EqualValues(t, 4, problems[0].(map[string]any)["line"])

	e.git.set("broken", "8888888b", "version: 1\nstages: [a]\n")
	status, body = a.call(t, &e.dev, "POST", base+"/runs", map[string]string{"ref": "broken"}, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "PIPELINE_INVALID", errCode(body))

	runID := first["id"].(string)
	status, body = a.call(t, &e.viewer, "GET", "/api/v1/runs/"+runID, nil, nil)
	require.Equal(t, http.StatusOK, status)
	jobs := body["jobs"].([]any)
	require.Len(t, jobs, 3)
	testJob := jobs[0].(map[string]any)["id"].(string)

	// Logs over HTTP, and the plain-text download.
	c := e.runNext(t, true)
	require.Equal(t, testJob, c.Job.ID.String())
	status, body = a.call(t, &e.viewer, "GET", "/api/v1/jobs/"+testJob+"/logs", nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, true, body["complete"])
	resp := a.req(t, context.Background(), &e.viewer, "GET", "/api/v1/jobs/"+testJob+"/logs", nil, map[string]string{"Accept": "text/plain"})
	text, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, "hello from test\n", string(text))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), `filename="run-1-test-attempt-1.log"`)

	status, body = a.call(t, &e.dev, "POST", "/api/v1/runs/"+runID+"/cancel", nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "canceled", body["status"])
	status, body = a.call(t, &e.dev, "POST", "/api/v1/runs/"+runID+"/cancel", nil, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "RUN_NOT_CANCELABLE", errCode(body))
	status, body = a.call(t, &e.dev, "POST", "/api/v1/runs/"+runID+"/rerun", map[string]bool{"failed_only": false}, nil)
	assert.Equal(t, http.StatusCreated, status)
	assert.EqualValues(t, 2, body["number"])
}

// sseEvents reads events from a stream until n are collected or the deadline passes.
func sseEvents(t *testing.T, resp *http.Response, n int, deadline time.Duration) []map[string]string {
	t.Helper()
	out := []map[string]string{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(resp.Body)
		ev := map[string]string{}
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if ev["event"] != "" {
					out = append(out, ev)
					if len(out) == n {
						return
					}
				}
				ev = map[string]string{}
				continue
			}
			if k, v, ok := strings.Cut(line, ": "); ok && !strings.HasPrefix(line, ":") {
				ev[k] = v
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Errorf("timed out with %d of %d events", len(out), n)
	}
	return out
}

func TestServerSentEvents(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	e.git.set("main", "7777777a", "version: 1\nstages: [a]\njobs:\n  only: {stage: a, image: i, steps: [echo]}\n")
	r := e.run(t, e.dev, "main")
	jobID := jobByName(t, r, "only").ID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runStream := a.req(t, ctx, &e.viewer, "GET", "/api/v1/runs/"+r.ID.String()+"/events", nil, nil)
	defer func() { _ = runStream.Body.Close() }()
	require.Equal(t, http.StatusOK, runStream.StatusCode)
	assert.Equal(t, "text/event-stream", runStream.Header.Get("Content-Type"))
	assert.Equal(t, "no", runStream.Header.Get("X-Accel-Buffering"))
	logStream := a.req(t, ctx, &e.viewer, "GET", "/api/v1/jobs/"+jobID.String()+"/logs/stream", nil, nil)
	defer func() { _ = logStream.Body.Close() }()
	require.Equal(t, http.StatusOK, logStream.StatusCode)

	first := sseEvents(t, runStream, 1, 5*time.Second)
	require.Len(t, first, 1)
	assert.Equal(t, "update", first[0]["event"], "an update on connect")

	// A runner works the job: both streams hear about it.
	c, err := e.svc.Claim(context.Background(), e.orgID, e.runnerID, nil)
	require.NoError(t, err)
	require.NoError(t, e.svc.AppendLog(context.Background(), c.Job.ID, 0, "building…\n", nil))
	require.NoError(t, e.svc.AppendLog(context.Background(), c.Job.ID, 1, "done\n", nil))
	require.NoError(t, e.svc.Complete(context.Background(), c.Job.ID, true, nil, ""))

	assert.NotEmpty(t, sseEvents(t, runStream, 1, 5*time.Second), "run update after the claim")
	logs := sseEvents(t, logStream, 3, 5*time.Second)
	require.Len(t, logs, 3)
	assert.Equal(t, "log", logs[0]["event"])
	assert.Equal(t, "0", logs[0]["id"])
	assert.JSONEq(t, `{"seq":0,"content":"building…\n"}`, logs[0]["data"])
	assert.Equal(t, "1", logs[1]["id"])
	assert.Equal(t, "end", logs[2]["event"])

	// Resuming with Last-Event-ID replays only what came after.
	resumed := a.req(t, ctx, &e.viewer, "GET", "/api/v1/jobs/"+jobID.String()+"/logs/stream", nil, map[string]string{"Last-Event-ID": "0"})
	defer func() { _ = resumed.Body.Close() }()
	events := sseEvents(t, resumed, 2, 5*time.Second)
	require.Len(t, events, 2)
	assert.Equal(t, "1", events[0]["id"])
	assert.Equal(t, "end", events[1]["event"])
}
