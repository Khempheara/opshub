package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/server"
	"github.com/opshub/opshub/internal/store"
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
	h := server.New(server.Deps{
		Config:        config.Config{DefaultLocale: "en", DefaultTimezone: "UTC", RateLimitRPS: 1000, RateLimitBurst: 1000},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            pgtest.Pool(t),
		Metrics:       telemetry.NewMetrics(),
		Version:       "test",
		Authenticator: &authn.Authenticator{JWT: signer, Tokens: noTokens{}},
		Modules:       []server.Module{NewHandler(e.svc)},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &api{srv: srv, signer: signer}
}

// call sends a request as a user, with a bearer token, or anonymously.
func (a *api) call(t *testing.T, u *user, token, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, rdr)
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
	"GET /orgs/{orgId}/assets", "POST /orgs/{orgId}/assets", "GET /orgs/{orgId}/certificates",
	"GET /assets/{assetId}", "PATCH /assets/{assetId}", "DELETE /assets/{assetId}", "GET /assets/{assetId}/metrics",
	"POST /assets/{assetId}/agent-token", "GET /assets/{assetId}/certificate", "POST /assets/{assetId}/certificate/check",
}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	asset := e.asset(t, AssetInput{Kind: store.AssetKindDomain, Name: "shop", Address: "shop.example.com"}).ID.String()
	o := e.orgID.String()
	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"GET", "/api/v1/orgs/" + o + "/assets", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/assets", map[string]any{"kind": "server", "name": "x"}, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/orgs/" + o + "/certificates", nil, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/assets/" + asset, nil, "ASSET_NOT_FOUND"},
		{"PATCH", "/api/v1/assets/" + asset, map[string]any{"name": "x", "address": "x.example.com"}, "ASSET_NOT_FOUND"},
		{"DELETE", "/api/v1/assets/" + asset, nil, "ASSET_NOT_FOUND"},
		{"GET", "/api/v1/assets/" + asset + "/metrics", nil, "ASSET_NOT_FOUND"},
		{"POST", "/api/v1/assets/" + asset + "/agent-token", nil, "ASSET_NOT_FOUND"},
		{"GET", "/api/v1/assets/" + asset + "/certificate", nil, "ASSET_NOT_FOUND"},
		{"POST", "/api/v1/assets/" + asset + "/certificate/check", nil, "ASSET_NOT_FOUND"},
	}
	require.Len(t, cases, len(tenantRoutes))
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, body := a.call(t, &e.outsider, "", c.method, c.path, c.body, map[string]string{"If-Match": `"v1"`})
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, c.code, errCode(body))
		})
	}
}

func TestRouteCoverage(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Mount(r)
	covered := map[string]bool{"POST /agent/heartbeat": true}
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
	assert.Empty(t, missing)
}

func TestHTTPFlow(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	base := "/api/v1/orgs/" + e.orgID.String()

	status, asset := a.call(t, &e.dev, "", "POST", base+"/assets", map[string]any{
		"kind": "server", "name": "web-1", "address": "10.0.0.5", "tags": []string{"prod"}, "metadata": map[string]string{"rack": "a1"},
	}, nil)
	require.Equal(t, http.StatusCreated, status, asset)
	id := asset["id"].(string)
	assert.Equal(t, "no_agent", asset["status"])

	status, tok := a.call(t, &e.dev, "", "POST", "/api/v1/assets/"+id+"/agent-token", nil, nil)
	require.Equal(t, http.StatusCreated, status, tok)
	token := tok["token"].(string)

	// The agent endpoint takes only agent tokens.
	for _, bad := range []string{"", "ohi_bad_token"} {
		status, body := a.call(t, nil, bad, "POST", "/api/v1/agent/heartbeat", map[string]any{}, nil)
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, "AGENT_TOKEN_INVALID", errCode(body))
	}
	status, body := a.call(t, nil, token, "POST", "/api/v1/agent/heartbeat", map[string]any{"cpu_pct": 150}, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, body = a.call(t, nil, token, "POST", "/api/v1/agent/heartbeat", map[string]any{
		"version": "1.0.0", "hostname": "web-1", "os": "linux", "arch": "amd64", "cpu_pct": 12.5, "mem_pct": 40, "disk_pct": 55, "load1": 0.3,
	}, nil)
	require.Equal(t, http.StatusOK, status, body)
	assert.InDelta(t, 30, body["interval_seconds"], 0)
	// The agent token doesn't open the user API.
	status, _ = a.call(t, nil, token, "GET", base+"/assets", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, status)

	status, list := a.call(t, &e.viewer, "", "GET", base+"/assets?status=online&kind=server&tag=prod", nil, nil)
	require.Equal(t, http.StatusOK, status, list)
	require.Len(t, list["items"], 1)
	item := list["items"].([]any)[0].(map[string]any)
	assert.InDelta(t, 12.5, item["metrics"].(map[string]any)["cpu_pct"], 0.01)

	now := time.Now().UTC()
	status, series := a.call(t, &e.viewer, "", "GET", "/api/v1/assets/"+id+"/metrics?from="+now.Add(-time.Hour).Format(time.RFC3339)+"&to="+now.Add(time.Minute).Format(time.RFC3339)+"&step=5m", nil, nil)
	require.Equal(t, http.StatusOK, status, series)
	assert.InDelta(t, 300, series["step_seconds"], 0)
	assert.Len(t, series["points"], 1)
	status, body = a.call(t, &e.viewer, "", "GET", "/api/v1/assets/"+id+"/metrics?from=yesterday&step=soon", nil, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)

	// PATCH needs If-Match.
	status, body = a.call(t, &e.dev, "", "PATCH", "/api/v1/assets/"+id, map[string]any{"name": "web-01"}, nil)
	assert.Equal(t, http.StatusPreconditionRequired, status, body)

	status, certs := a.call(t, &e.viewer, "", "GET", base+"/certificates?expiring_within=30d", nil, nil)
	require.Equal(t, http.StatusOK, status, certs)
	assert.Empty(t, certs["items"])
	status, body = a.call(t, &e.viewer, "", "GET", base+"/certificates?expiring_within=soon", nil, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, cert := a.call(t, &e.viewer, "", "GET", "/api/v1/assets/"+id+"/certificate", nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Nil(t, cert["certificate"])
}

func TestParseHelpers(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "60": time.Minute, "5m": 5 * time.Minute, "1d": 24 * time.Hour} {
		got, ok := parseStep(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"x", "-5", "0d"} {
		_, ok := parseStep(bad)
		assert.False(t, ok, bad)
	}
	d, ok := parseWithin("30d")
	assert.True(t, ok)
	assert.Equal(t, 30*24*time.Hour, d)
	d, ok = parseWithin("72h")
	assert.True(t, ok)
	assert.Equal(t, 72*time.Hour, d)
	_, ok = parseWithin("never")
	assert.False(t, ok)
}
