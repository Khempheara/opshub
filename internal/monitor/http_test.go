package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

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

func newAPI(t *testing.T, h server.Module) *api {
	t.Helper()
	signer, err := authn.NewJWTSigner([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{5}, 32)}}, nil)
	require.NoError(t, err)
	handler := server.New(server.Deps{
		Config:        config.Config{DefaultLocale: "en", DefaultTimezone: "UTC", RateLimitRPS: 1000, RateLimitBurst: 1000},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            pgtest.Pool(t),
		Metrics:       telemetry.NewMetrics(),
		Version:       "test",
		Authenticator: &authn.Authenticator{JWT: signer, Tokens: noTokens{}},
		Modules:       []server.Module{h},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &api{srv: srv, signer: signer}
}

// call sends a request as a user and returns the status and decoded body.
func (a *api) call(t *testing.T, u *user, method, path string, body any, headers map[string]string) (int, map[string]any) {
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
	tok, _, err := a.signer.Issue(u.id, uuid.New())
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok)
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

// uncovered lists mounted routes missing from covered.
func uncovered(t *testing.T, mount func(chi.Router), covered []string) []string {
	t.Helper()
	r := chi.NewRouter()
	mount(r)
	var missing []string
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !slices.Contains(covered, method+" "+route) {
			missing = append(missing, method+" "+route)
		}
		return nil
	}))
	return missing
}

var tenantRoutes = []string{
	"GET /orgs/{orgId}/monitors", "POST /orgs/{orgId}/monitors", "GET /monitors/{monitorId}",
	"PATCH /monitors/{monitorId}", "DELETE /monitors/{monitorId}", "GET /monitors/{monitorId}/results",
}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, NewHandler(e.svc))
	m := e.monitor(t, MonitorInput{Name: "web", Kind: store.MonitorKindTcp, Target: "db:5432"}).ID.String()
	o := e.orgID.String()
	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"GET", "/api/v1/orgs/" + o + "/monitors", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/monitors", map[string]any{"name": "x", "kind": "tcp", "target": "x:1"}, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/monitors/" + m, nil, "MONITOR_NOT_FOUND"},
		{"PATCH", "/api/v1/monitors/" + m, map[string]any{"name": "x", "target": "x:1"}, "MONITOR_NOT_FOUND"},
		{"DELETE", "/api/v1/monitors/" + m, nil, "MONITOR_NOT_FOUND"},
		{"GET", "/api/v1/monitors/" + m + "/results", nil, "MONITOR_NOT_FOUND"},
	}
	require.Len(t, cases, len(tenantRoutes))
	for _, c := range cases {
		status, body := a.call(t, &e.outsider, c.method, c.path, c.body, map[string]string{"If-Match": `"1"`})
		assert.Equal(t, http.StatusNotFound, status, c.path)
		assert.Equal(t, c.code, errCode(body), c.path)
	}
}

func TestRouteCoverage(t *testing.T) {
	assert.Empty(t, uncovered(t, NewHandler(nil).Mount, tenantRoutes))
}

func TestHTTPFlow(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, NewHandler(e.svc))
	status, m := a.call(t, &e.dev, "POST", "/api/v1/orgs/"+e.orgID.String()+"/monitors", map[string]any{
		"name": "api", "kind": "http", "target": "https://api.example.com/health", "settings": map[string]any{"expected_status": []int{200}},
	}, nil)
	require.Equal(t, http.StatusCreated, status, m)
	id := m["id"].(string)
	assert.Equal(t, "pending", m["status"])
	status, body := a.call(t, &e.dev, "PATCH", "/api/v1/monitors/"+id, map[string]any{"name": "api", "target": "https://api.example.com/health", "enabled": false}, nil)
	assert.Equal(t, http.StatusPreconditionRequired, status, body)
	status, body = a.call(t, &e.dev, "PATCH", "/api/v1/monitors/"+id, map[string]any{"name": "api", "target": "https://api.example.com/health", "enabled": false},
		map[string]string{"If-Match": `"1"`})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "paused", body["status"])
	status, body = a.call(t, &e.viewer, "GET", "/api/v1/monitors/"+id+"/results?from=2026-01-01T00:00:00Z&to=2025-01-01T00:00:00Z", nil, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, body = a.call(t, &e.viewer, "GET", "/api/v1/monitors/"+id+"/results?step=5m", nil, nil)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "raw", body["source"])
	status, _ = a.call(t, &e.dev, "DELETE", "/api/v1/monitors/"+id, nil, nil)
	assert.Equal(t, http.StatusNoContent, status)
}
