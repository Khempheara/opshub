package deploy

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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := events.NewHub(pool, logger)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	h := server.New(server.Deps{
		Config:        config.Config{DefaultLocale: "en", DefaultTimezone: "UTC", RateLimitRPS: 1000, RateLimitBurst: 1000},
		Logger:        logger,
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
	"GET /orgs/{orgId}/deploy-targets", "POST /orgs/{orgId}/deploy-targets",
	"GET /deploy-targets/{targetId}", "PATCH /deploy-targets/{targetId}", "DELETE /deploy-targets/{targetId}",
	"POST /deploy-targets/{targetId}/test", "GET /projects/{projectId}/deployments",
	"POST /environments/{environmentId}/deployments", "GET /deployments/{deploymentId}",
	"GET /deployments/{deploymentId}/logs/stream", "POST /deployments/{deploymentId}/rollback",
}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	envID := e.environment(t, "staging", nil)
	tg := e.sshTarget(t, "web")
	d, err := e.svc.CreateDeployment(e.dev.ctx, envID, CreateInput{TargetID: tg.ID, Version: "app:v1"})
	require.NoError(t, err)
	o, tid, dep := e.orgID.String(), tg.ID.String(), d.ID.String()

	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"GET", "/api/v1/orgs/" + o + "/deploy-targets", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/deploy-targets", map[string]any{"name": "x", "kind": "ssh"}, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/deploy-targets/" + tid, nil, "DEPLOY_TARGET_NOT_FOUND"},
		{"PATCH", "/api/v1/deploy-targets/" + tid, map[string]any{"config": map[string]any{}}, "DEPLOY_TARGET_NOT_FOUND"},
		{"DELETE", "/api/v1/deploy-targets/" + tid, nil, "DEPLOY_TARGET_NOT_FOUND"},
		{"POST", "/api/v1/deploy-targets/" + tid + "/test", nil, "DEPLOY_TARGET_NOT_FOUND"},
		{"GET", "/api/v1/projects/" + e.projectID.String() + "/deployments", nil, "PROJECT_NOT_FOUND"},
		{"POST", "/api/v1/environments/" + envID.String() + "/deployments", map[string]any{"target_id": tid, "version": "app:v2"}, "ENVIRONMENT_NOT_FOUND"},
		{"GET", "/api/v1/deployments/" + dep, nil, "DEPLOYMENT_NOT_FOUND"},
		{"GET", "/api/v1/deployments/" + dep + "/logs/stream", nil, "DEPLOYMENT_NOT_FOUND"},
		{"POST", "/api/v1/deployments/" + dep + "/rollback", nil, "DEPLOYMENT_NOT_FOUND"},
	}
	require.Len(t, cases, len(tenantRoutes))
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, body := a.call(t, &e.outsider, c.method, c.path, c.body, map[string]string{"If-Match": `"v1"`})
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, c.code, errCode(body))
		})
	}
}

func TestRouteCoverage(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil, nil, func(h http.Handler) http.Handler { return h }).Mount(r)
	covered := map[string]bool{}
	for _, route := range tenantRoutes {
		covered[route] = true
	}
	var missing []string
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !covered[method+" "+route] {
			missing = append(missing, method+" "+route)
		}
		return nil
	}))
	assert.Empty(t, missing, "add these routes to TestTenantIsolation and tenantRoutes")
}

func TestHTTPFlow(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	envID := e.environment(t, "staging", nil)
	base := "/api/v1/orgs/" + e.orgID.String()

	// Create a target; credentials never come back.
	status, tg := a.call(t, &e.admin, "POST", base+"/deploy-targets", map[string]any{
		"name": "web", "kind": "ssh",
		"config":      map[string]any{"hosts": []string{e.ssh.addr}, "user": "deploy", "command": `printf %s "$OPSHUB_VERSION" > ` + e.versionFile},
		"credentials": map[string]any{"private_key": e.ssh.clientKey},
	}, nil)
	require.Equal(t, http.StatusCreated, status, tg)
	raw, _ := json.Marshal(tg)
	assert.NotContains(t, string(raw), "PRIVATE KEY")
	tid := tg["id"].(string)

	// Test shows the fingerprint; trusting it is a PATCH with If-Match.
	status, res := a.call(t, &e.admin, "POST", "/api/v1/deploy-targets/"+tid+"/test", nil, nil)
	require.Equal(t, http.StatusOK, status)
	fp := res["checks"].([]any)[0].(map[string]any)["fingerprint"].(string)
	cfg := tg["config"].(map[string]any)
	cfg["host_keys"] = map[string]string{e.ssh.addr: fp}
	status, body := a.call(t, &e.admin, "PATCH", "/api/v1/deploy-targets/"+tid, map[string]any{"config": cfg}, nil)
	assert.Equal(t, http.StatusPreconditionRequired, status, body)
	status, body = a.call(t, &e.admin, "PATCH", "/api/v1/deploy-targets/"+tid, map[string]any{"config": cfg}, map[string]string{"If-Match": `"v1"`})
	require.Equal(t, http.StatusOK, status, body)
	status, res = a.call(t, &e.admin, "POST", "/api/v1/deploy-targets/"+tid+"/test", nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, true, res["ok"], res)

	// Deploy with an Idempotency-Key: a retry replays, it doesn't deploy twice.
	hdr := map[string]string{"Idempotency-Key": uuid.NewString()}
	path := "/api/v1/environments/" + envID.String() + "/deployments"
	status, d := a.call(t, &e.dev, "POST", path, map[string]any{"target_id": tid, "version": "app:v7"}, hdr)
	require.Equal(t, http.StatusAccepted, status, d)
	status, again := a.call(t, &e.dev, "POST", path, map[string]any{"target_id": tid, "version": "app:v7"}, hdr)
	assert.Equal(t, http.StatusAccepted, status)
	assert.Equal(t, d["id"], again["id"])
	assert.Len(t, e.jobs.deployments(), 1)
	status, body = a.call(t, &e.viewer, "POST", path, map[string]any{"target_id": tid, "version": "app:v7"}, nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "FORBIDDEN", errCode(body))

	// The stream shows status and logs live, then ends.
	id := d["id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp := a.req(t, ctx, &e.viewer, "GET", "/api/v1/deployments/"+id+"/logs/stream", nil, nil)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	go e.runAll(t)
	var events []string
	var logText strings.Builder
	sc := bufio.NewScanner(resp.Body)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
			events = append(events, event)
		case strings.HasPrefix(line, "data: ") && event == "log":
			var c LogChunk
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &c)
			logText.WriteString(c.Content)
		}
		if event == "end" {
			break
		}
	}
	assert.Equal(t, "status", events[0])
	assert.Contains(t, events, "log")
	assert.Equal(t, "end", events[len(events)-1])
	assert.Contains(t, logText.String(), "Deployed app:v7")

	status, got := a.call(t, &e.viewer, "GET", "/api/v1/deployments/"+id, nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "succeeded", got["status"])
	assert.Equal(t, true, got["current"])

	status, list := a.call(t, &e.viewer, "GET", "/api/v1/projects/"+e.projectID.String()+"/deployments?status=succeeded&environment_id="+envID.String(), nil, nil)
	require.Equal(t, http.StatusOK, status, list)
	assert.Len(t, list["items"], 1)
	status, body = a.call(t, &e.viewer, "GET", "/api/v1/projects/"+e.projectID.String()+"/deployments?from=yesterday", nil, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)

	status, body = a.call(t, &e.dev, "POST", "/api/v1/deployments/"+id+"/rollback", nil, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "NOTHING_TO_ROLL_BACK", errCode(body))
	status, body = a.call(t, &e.admin, "DELETE", "/api/v1/deploy-targets/"+tid, nil, nil)
	assert.Equal(t, http.StatusNoContent, status, body)
}
