package runners

import (
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
	"github.com/opshub/opshub/internal/pipeline"
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

// newAPI serves the runners module next to the pipelines module, as in production (both
// mount routes under /jobs/{jobId}).
func newAPI(t *testing.T, e *env) *api {
	t.Helper()
	signer, err := authn.NewJWTSigner([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{5}, 32)}}, nil)
	require.NoError(t, err)
	pool := pgtest.Pool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := events.NewHub(pool, logger)
	h := server.New(server.Deps{
		Config:        config.Config{DefaultLocale: "en", DefaultTimezone: "UTC", RateLimitRPS: 1000, RateLimitBurst: 1000},
		Logger:        logger,
		DB:            pool,
		Metrics:       telemetry.NewMetrics(),
		Version:       "test",
		Authenticator: &authn.Authenticator{JWT: signer, Tokens: noTokens{}},
		Modules: []server.Module{
			pipeline.NewHandler(e.pipelines, hub, idempotency.Middleware(pool)),
			NewHandler(e.svc),
		},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &api{srv: srv, signer: signer}
}

// do sends a request as a user (u), with a machine token (token), or anonymously.
func (a *api) do(t *testing.T, u *user, token, method, path string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	if body == nil {
		body = http.NoBody
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	switch {
	case u != nil:
		tok, _, err := a.signer.Issue(u.id, uuid.New())
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+tok)
	case token != "":
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func jsonBody(v any) io.Reader {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func (a *api) call(t *testing.T, u *user, token, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	resp := a.do(t, u, token, method, path, jsonBody(body), headers)
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

// Routes for signed-in users (tenant isolation) and for runners (token checks).
var (
	tenantRoutes = []string{
		"GET /orgs/{orgId}/runners", "POST /orgs/{orgId}/runner-registration-tokens",
		"PATCH /runners/{runnerId}", "DELETE /runners/{runnerId}",
		"GET /jobs/{jobId}/artifacts", "GET /artifacts/{artifactId}/download",
	}
	jobTokenRoutes = []string{
		"PATCH /runner/jobs/{jobId}/", "POST /runner/jobs/{jobId}/logs", "GET /runner/jobs/{jobId}/source",
		"POST /runner/jobs/{jobId}/artifacts", "GET /runner/jobs/{jobId}/dependencies",
		"GET /runner/jobs/{jobId}/dependencies/{artifactId}", "GET /runner/jobs/{jobId}/cache/{key}",
		"PUT /runner/jobs/{jobId}/cache/{key}",
	}
)

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	r, _ := e.register(t, "alpha", []string{"linux"}, 1)
	e.trigger(t)
	claimed := e.claim(t, r)
	require.NotNil(t, claimed)
	art, err := e.svc.UploadArtifact(context.Background(), e.job(t, claimed), "", bytes.NewReader(tarGz(t, map[string]string{"a": "b"})))
	require.NoError(t, err)
	o, rid, job := e.orgID.String(), r.ID.String(), claimed.Job.ID.String()

	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"GET", "/api/v1/orgs/" + o + "/runners", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/runner-registration-tokens", map[string]any{}, "ORG_NOT_FOUND"},
		{"PATCH", "/api/v1/runners/" + rid, map[string]any{"name": "x", "max_concurrency": 1}, "RUNNER_NOT_FOUND"},
		{"DELETE", "/api/v1/runners/" + rid, nil, "RUNNER_NOT_FOUND"},
		{"GET", "/api/v1/jobs/" + job + "/artifacts", nil, "JOB_NOT_FOUND"},
		{"GET", "/api/v1/artifacts/" + art.ID.String() + "/download", nil, "ARTIFACT_NOT_FOUND"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, body := a.call(t, &e.outsider, "", c.method, c.path, c.body, map[string]string{"If-Match": `"v1"`})
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, c.code, errCode(body))
		})
	}

	// Anonymous callers and runner tokens can't use the user API.
	for _, tok := range []string{"", "ohr_abc_def"} {
		status, body := a.call(t, nil, tok, "GET", "/api/v1/orgs/"+o+"/runners", nil, nil)
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, "UNAUTHENTICATED", errCode(body))
	}
}

func TestRunnerAPIAuth(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	r, runnerToken := e.register(t, "alpha", []string{"linux"}, 2)
	e.trigger(t)
	claimed := e.claim(t, r)
	require.NotNil(t, claimed)
	other := uuid.NewString()
	job := claimed.Job.ID.String()

	// Runner-token routes refuse users, job tokens and garbage.
	for _, tok := range []string{"", claimed.Token, "ohr_zzz_bad"} {
		status, body := a.call(t, nil, tok, "POST", "/api/v1/runner/heartbeat", map[string]any{}, nil)
		assert.Equal(t, http.StatusUnauthorized, status, tok)
		assert.Equal(t, "RUNNER_TOKEN_INVALID", errCode(body))
	}
	// A credential that is neither a machine token nor a valid session fails earlier.
	status, body := a.call(t, nil, "nope", "POST", "/api/v1/runner/heartbeat", map[string]any{}, nil)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "UNAUTHENTICATED", errCode(body))
	status, body = a.call(t, &e.owner, "", "POST", "/api/v1/runner/jobs/request", map[string]any{}, nil)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "RUNNER_TOKEN_INVALID", errCode(body))

	// Job-token routes: every route refuses a runner token, another job's id and a bad id.
	for _, route := range jobTokenRoutes {
		method, path, _ := strings.Cut(route, " ")
		path = strings.NewReplacer("{artifactId}", uuid.NewString(), "{key}", "deps").Replace(path)
		for name, c := range map[string]struct{ token, id string }{
			"runner token": {runnerToken, job}, "other job": {claimed.Token, other}, "bad id": {claimed.Token, "x"},
		} {
			p := "/api/v1" + strings.ReplaceAll(path, "{jobId}", c.id)
			resp := a.do(t, nil, c.token, method, p, strings.NewReader("{}"), nil)
			var out map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&out)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s", route, name)
			assert.Equal(t, "JOB_TOKEN_INVALID", errCode(out), "%s %s", route, name)
		}
	}
}

func TestRouteCoverage(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Mount(r)
	covered := map[string]bool{
		"POST /runner/register": true, "POST /runner/heartbeat": true, "POST /runner/jobs/request": true,
	}
	for _, route := range append(tenantRoutes, jobTokenRoutes...) {
		covered[route] = true
	}
	var missing []string
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !covered[method+" "+route] {
			missing = append(missing, method+" "+route)
		}
		return nil
	}))
	assert.Empty(t, missing, "add these routes to TestTenantIsolation/TestRunnerAPIAuth")
}

// TestRunnerFlow drives a runner through the HTTP API: register, request, report, log,
// source, artifacts, dependencies and cache.
func TestRunnerFlow(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	orgs := "/api/v1/orgs/" + e.orgID.String()

	status, rt := a.call(t, &e.admin, "", "POST", orgs+"/runner-registration-tokens", map[string]any{"labels": []string{"linux"}}, nil)
	require.Equal(t, http.StatusCreated, status, rt)
	status, reg := a.call(t, nil, "", "POST", "/api/v1/runner/register", map[string]any{
		"token": rt["token"], "name": "ci-1", "version": "1.0.0", "os": "linux", "arch": "amd64", "max_concurrency": 1,
	}, nil)
	require.Equal(t, http.StatusCreated, status, reg)
	token := reg["token"].(string)
	status, body := a.call(t, nil, "", "POST", "/api/v1/runner/register", map[string]any{"token": rt["token"], "name": "again"}, nil)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "REGISTRATION_TOKEN_INVALID", errCode(body))

	// Nothing queued: 204 after the wait.
	resp := a.do(t, nil, token, "POST", "/api/v1/runner/jobs/request", jsonBody(map[string]int{"wait_seconds": 1}), nil)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	e.trigger(t)
	status, job := a.call(t, nil, token, "POST", "/api/v1/runner/jobs/request", map[string]int{"wait_seconds": 1}, nil)
	require.Equal(t, http.StatusOK, status, job)
	jobToken := job["token"].(string)
	jobID := job["job"].(map[string]any)["id"].(string)
	base := "/api/v1/runner/jobs/" + jobID
	assert.Equal(t, "build", job["job"].(map[string]any)["name"])
	assert.Equal(t, sha, job["commit_sha"])
	assert.NotNil(t, job["spec"].(map[string]any)["steps"])

	status, hb := a.call(t, nil, token, "POST", "/api/v1/runner/heartbeat", map[string]any{"running_job_ids": []string{jobID}}, nil)
	require.Equal(t, http.StatusOK, status, hb)
	assert.Empty(t, hb["cancel_job_ids"])

	status, body = a.call(t, nil, jobToken, "PATCH", base, map[string]any{"step": map[string]any{"index": 0, "status": "running"}}, nil)
	assert.Equal(t, http.StatusNoContent, status, body)
	status, body = a.call(t, nil, jobToken, "POST", base+"/logs", map[string]any{"seq": 1, "content": "compiling\n"}, nil)
	assert.Equal(t, http.StatusNoContent, status, body)

	resp = a.do(t, nil, jobToken, "GET", base+"/source", nil, nil)
	src, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, e.git.tarball, src)

	archive := tarGz(t, map[string]string{"dist/app": "bin"})
	resp = a.do(t, nil, jobToken, "POST", base+"/artifacts?name=app.tar.gz", bytes.NewReader(archive), map[string]string{"Content-Type": "application/gzip"})
	var art map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&art)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, art)
	assert.Equal(t, "app.tar.gz", art["name"])

	resp = a.do(t, nil, jobToken, "PUT", base+"/cache/deps", strings.NewReader("cached"), nil)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp = a.do(t, nil, jobToken, "GET", base+"/cache/deps", nil, nil)
	cached, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "cached", string(cached))
	assert.Len(t, resp.Header.Get("X-Content-SHA256"), 64)
	status, body = a.call(t, nil, jobToken, "GET", base+"/cache/missing", nil, nil)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "CACHE_NOT_FOUND", errCode(body))

	status, body = a.call(t, nil, jobToken, "PATCH", base, map[string]any{"complete": map[string]any{"success": true, "exit_code": 0}}, nil)
	assert.Equal(t, http.StatusNoContent, status, body)
	// After completion the job token opens nothing.
	status, body = a.call(t, nil, jobToken, "POST", base+"/logs", map[string]any{"seq": 2, "content": "late"}, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "JOB_NOT_RUNNING", errCode(body))

	// Users see and download the artifact.
	status, list := a.call(t, &e.viewer, "", "GET", "/api/v1/jobs/"+jobID+"/artifacts", nil, nil)
	require.Equal(t, http.StatusOK, status, list)
	require.Len(t, list["items"], 1)
	resp = a.do(t, &e.viewer, "", "GET", "/api/v1/artifacts/"+art["id"].(string)+"/download", nil, nil)
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, archive, got)
	assert.Contains(t, resp.Header.Get("Content-Disposition"), `filename=app.tar.gz`)
	// The pipeline module's job routes still work next to this module's.
	status, _ = a.call(t, &e.viewer, "", "GET", "/api/v1/jobs/"+jobID, nil, nil)
	assert.Equal(t, http.StatusOK, status)

	// The next job gets build's artifact as a dependency.
	status, next := a.call(t, nil, token, "POST", "/api/v1/runner/jobs/request", map[string]int{"wait_seconds": 1}, nil)
	require.Equal(t, http.StatusOK, status, next)
	nextBase := "/api/v1/runner/jobs/" + next["job"].(map[string]any)["id"].(string)
	nextToken := next["token"].(string)
	status, deps := a.call(t, nil, nextToken, "GET", nextBase+"/dependencies", nil, nil)
	require.Equal(t, http.StatusOK, status, deps)
	items := deps["items"].([]any)
	require.Len(t, items, 1)
	resp = a.do(t, nil, nextToken, "GET", nextBase+"/dependencies/"+items[0].(map[string]any)["id"].(string), nil, nil)
	dep, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, archive, dep)

	// Managing the runner: If-Match is required; disabling locks the runner out.
	status, runners := a.call(t, &e.admin, "", "GET", orgs+"/runners", nil, nil)
	require.Equal(t, http.StatusOK, status)
	rn := runners["items"].([]any)[0].(map[string]any)
	assert.Equal(t, float64(1), rn["running_jobs"])
	patch := map[string]any{"name": "ci-1", "labels": []string{"linux"}, "max_concurrency": 1, "disabled": true}
	status, body = a.call(t, &e.admin, "", "PATCH", "/api/v1/runners/"+reg["id"].(string), patch, nil)
	assert.Equal(t, http.StatusPreconditionRequired, status, body)
	resp = a.do(t, &e.admin, "", "PATCH", "/api/v1/runners/"+reg["id"].(string), jsonBody(patch), map[string]string{"If-Match": `"v1"`})
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `"v2"`, resp.Header.Get("ETag"))
	status, body = a.call(t, nil, token, "POST", "/api/v1/runner/heartbeat", map[string]any{}, nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "RUNNER_DISABLED", errCode(body))
}

func TestRequestJobHonorsClientDisconnect(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	_, token := e.register(t, "alpha", []string{"linux"}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.srv.URL+"/api/v1/runner/jobs/request", strings.NewReader(`{"wait_seconds":30}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}
