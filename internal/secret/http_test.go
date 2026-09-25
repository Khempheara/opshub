package secret

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

// call sends a request as a user and returns the status, the raw body and the decoded body.
func (a *api) call(t *testing.T, u *user, method, path string, body any, headers map[string]string) (int, string, map[string]any) {
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
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, string(raw), out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

var tenantRoutes = []string{
	"GET /projects/{projectId}/secrets", "POST /projects/{projectId}/secrets",
	"GET /secrets/{secretId}", "PATCH /secrets/{secretId}", "DELETE /secrets/{secretId}",
	"GET /secrets/{secretId}/versions", "POST /secrets/{secretId}/versions",
}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	s := e.create(t, "TOKEN", &e.staging, "hidden").ID.String()
	p := e.projectID.String()
	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"GET", "/api/v1/projects/" + p + "/secrets", nil, "PROJECT_NOT_FOUND"},
		{"POST", "/api/v1/projects/" + p + "/secrets", map[string]any{"name": "X", "value": "v"}, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/secrets/" + s, nil, "SECRET_NOT_FOUND"},
		{"PATCH", "/api/v1/secrets/" + s, map[string]any{"description": "x"}, "SECRET_NOT_FOUND"},
		{"DELETE", "/api/v1/secrets/" + s, nil, "SECRET_NOT_FOUND"},
		{"GET", "/api/v1/secrets/" + s + "/versions", nil, "SECRET_NOT_FOUND"},
		{"POST", "/api/v1/secrets/" + s + "/versions", map[string]any{"value": "v"}, "SECRET_NOT_FOUND"},
	}
	require.Len(t, cases, len(tenantRoutes))
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, _, body := a.call(t, &e.outsider, c.method, c.path, c.body, map[string]string{"If-Match": `"v1"`})
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, c.code, errCode(body))
		})
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
	env := newEnv(t)
	a := newAPI(t, env)
	base := "/api/v1/projects/" + env.projectID.String() + "/secrets"
	const value = "sk_live_very-secret-value"

	status, raw, created := a.call(t, &env.dev, "POST", base, map[string]any{
		"name": "STRIPE_KEY", "environment_id": env.staging, "description": "Payments", "value": value,
	}, nil)
	require.Equal(t, http.StatusCreated, status, raw)
	assert.NotContains(t, raw, value, "values are never returned")
	id := created["id"].(string)
	assert.Equal(t, "staging", created["environment_name"])
	assert.Equal(t, true, created["can_manage"])

	status, raw, _ = a.call(t, &env.dev, "POST", base, map[string]any{"name": "bad name", "value": value}, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.NotContains(t, raw, value, "not even in validation errors")

	status, _, list := a.call(t, &env.dev, "GET", base+"?environment_id="+env.staging.String(), nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, list["items"], 1)
	status, _, list = a.call(t, &env.dev, "GET", base+"?environment_id=none", nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, list["items"])
	status, _, body := a.call(t, &env.dev, "GET", base+"?environment_id=nope", nil, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)

	status, _, body = a.call(t, &env.dev, "PATCH", "/api/v1/secrets/"+id, map[string]any{"description": "x"}, nil)
	assert.Equal(t, http.StatusPreconditionRequired, status, body)
	status, _, body = a.call(t, &env.dev, "PATCH", "/api/v1/secrets/"+id, map[string]any{"description": "Stripe (test)"}, map[string]string{"If-Match": `"1"`})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "Stripe (test)", body["description"])

	status, raw, body = a.call(t, &env.dev, "POST", "/api/v1/secrets/"+id+"/versions", map[string]any{"value": value + "-2"}, nil)
	require.Equal(t, http.StatusCreated, status, raw)
	assert.InDelta(t, 2, body["current_version"], 0)
	assert.NotContains(t, raw, value)

	status, raw, body = a.call(t, &env.dev, "GET", "/api/v1/secrets/"+id+"/versions", nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, body["items"], 2)
	assert.NotContains(t, raw, value)

	status, _, body = a.call(t, &env.viewer, "GET", "/api/v1/secrets/"+id, nil, nil)
	assert.Equal(t, http.StatusForbidden, status, body)

	status, _, _ = a.call(t, &env.dev, "DELETE", "/api/v1/secrets/"+id, nil, nil)
	assert.Equal(t, http.StatusNoContent, status)
	status, _, body = a.call(t, &env.dev, "GET", "/api/v1/secrets/"+id, nil, nil)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "SECRET_NOT_FOUND", errCode(body))

	// A value too large for its limit is refused; one within fits in a request.
	status, _, body = a.call(t, &env.admin, "POST", base, map[string]any{"name": "BIG", "value": strings.Repeat("x", MaxValueBytes+1)}, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, _, body = a.call(t, &env.admin, "POST", base, map[string]any{"name": "BIG", "value": strings.Repeat("x", MaxValueBytes)}, nil)
	assert.Equal(t, http.StatusCreated, status, body)
}
