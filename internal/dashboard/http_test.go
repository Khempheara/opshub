package dashboard

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
	signer, err := authn.NewJWTSigner([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{8}, 32)}}, nil)
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

// get returns the status and decoded body of a GET as u (anonymous when nil).
func (a *api) get(t *testing.T, u *user, path string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, a.srv.URL+path, http.NoBody)
	require.NoError(t, err)
	if u != nil {
		tok, _, err := a.signer.Issue(u.id, uuid.New())
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+tok)
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

var tenantRoutes = []string{"GET /orgs/{orgId}/dashboard/pipelines", "GET /orgs/{orgId}/dashboard/dora"}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	for _, path := range []string{"/dashboard/pipelines", "/dashboard/dora"} {
		status, body := a.get(t, &e.outsider, "/api/v1/orgs/"+e.orgID.String()+path)
		assert.Equal(t, http.StatusNotFound, status, path)
		assert.Equal(t, "ORG_NOT_FOUND", errCode(body))
	}
}

func TestRouteCoverage(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Mount(r)
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
	assert.Empty(t, missing)
}

func TestHTTPFlow(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	e.run(t, e.projA, "succeeded", time.Hour, time.Minute, nil)
	e.deploy(t, dep{env: e.prodA, project: e.projA, status: "succeeded", at: time.Hour, dur: time.Minute})
	base := "/api/v1/orgs/" + e.orgID.String() + "/dashboard"
	q := "?from=" + e.t0.Format(time.RFC3339) + "&to=" + e.t0.Add(48*time.Hour).Format(time.RFC3339) + "&tz=Asia/Phnom_Penh"

	status, body := a.get(t, &e.viewer, base+"/pipelines"+q)
	require.Equal(t, http.StatusOK, status, body)
	summary := body["summary"].(map[string]any)
	assert.InDelta(t, 1, summary["runs"], 0)
	assert.InDelta(t, 1, summary["success_rate"], 0)
	assert.Len(t, body["trend"], 3, "two days in Phnom Penh time span three calendar days")
	assert.Equal(t, "Asia/Phnom_Penh", body["range"].(map[string]any)["tz"])

	status, body = a.get(t, &e.dev, base+"/dora"+q+"&project="+e.projA.String())
	require.Equal(t, http.StatusOK, status, body)
	assert.InDelta(t, 1, body["deployments"].(map[string]any)["succeeded"], 0)
	assert.InDelta(t, 0, body["change_failure_rate"].(map[string]any)["rate"], 0)
	assert.Nil(t, body["time_to_restore"].(map[string]any)["median_s"])

	status, body = a.get(t, &e.dev, base+"/dora?project="+e.projB.String())
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "PROJECT_NOT_FOUND", errCode(body))
	status, _ = a.get(t, &e.owner, base+"/pipelines?tz=Nowhere/Land")
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	status, _ = a.get(t, nil, base+"/pipelines")
	assert.Equal(t, http.StatusUnauthorized, status)
	status, _ = a.get(t, &e.owner, "/api/v1/orgs/not-a-uuid/dashboard/dora")
	assert.Equal(t, http.StatusNotFound, status)
}
