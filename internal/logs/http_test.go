package logs

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
	signer, err := authn.NewJWTSigner([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{6}, 32)}}, nil)
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

// call sends a request as a user, with a bearer token, or anonymously. A string body is sent
// as is; anything else as JSON.
func (a *api) call(t *testing.T, u *user, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
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
	"GET /orgs/{orgId}/logs", "GET /orgs/{orgId}/logs/services",
	"GET /orgs/{orgId}/log-ingest-tokens", "POST /orgs/{orgId}/log-ingest-tokens",
	"DELETE /log-ingest-tokens/{tokenId}",
}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	tok, _ := e.token(t, "api")
	o := e.orgID.String()
	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"GET", "/api/v1/orgs/" + o + "/logs", nil, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/orgs/" + o + "/logs/services", nil, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/orgs/" + o + "/log-ingest-tokens", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/log-ingest-tokens", map[string]any{"name": "x", "service": "x"}, "ORG_NOT_FOUND"},
		{"DELETE", "/api/v1/log-ingest-tokens/" + tok.ID.String(), nil, "INGEST_TOKEN_NOT_FOUND"},
	}
	require.Len(t, cases, len(tenantRoutes))
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, body := a.call(t, &e.outsider, "", c.method, c.path, c.body)
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, c.code, errCode(body))
		})
	}
}

func TestRouteCoverage(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Mount(r)
	covered := map[string]bool{"POST /ingest/logs": true}
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

	status, tok := a.call(t, &e.admin, "", "POST", base+"/log-ingest-tokens", map[string]any{"name": "Shop API", "service": "shop/api"})
	require.Equal(t, http.StatusCreated, status, tok)
	token := tok["token"].(string)
	status, body := a.call(t, &e.dev, "", "POST", base+"/log-ingest-tokens", map[string]any{"name": "x", "service": "x"})
	assert.Equal(t, http.StatusForbidden, status, body)
	status, body = a.call(t, &e.admin, "", "GET", base+"/log-ingest-tokens", nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["items"], 1)
	assert.NotContains(t, body["items"].([]any)[0], "token")

	// The ingest endpoint takes only ingest tokens.
	for _, bad := range []string{"", "ohl_bad_token", "ohr_runner"} {
		status, body := a.call(t, nil, bad, "POST", "/api/v1/ingest/logs", `{"message":"x"}`)
		assert.Equal(t, http.StatusUnauthorized, status, bad)
		assert.Equal(t, "INGEST_TOKEN_INVALID", errCode(body))
	}
	status, body = a.call(t, nil, token, "POST", "/api/v1/ingest/logs", "{\"message\":\"order placed\",\"level\":\"info\"}\nbad\n")
	require.Equal(t, http.StatusOK, status, body)
	assert.InDelta(t, 1, body["accepted"], 0)
	assert.InDelta(t, 1, body["rejected"], 0)
	status, body = a.call(t, nil, token, "POST", "/api/v1/ingest/logs", strings.Repeat("{\"message\":\""+strings.Repeat("x", 1000)+"\"}\n", 1100))
	assert.Equal(t, http.StatusRequestEntityTooLarge, status)
	assert.Equal(t, "PAYLOAD_TOO_LARGE", errCode(body))
	// The ingest token doesn't open the user API.
	status, _ = a.call(t, nil, token, "GET", base+"/logs", nil)
	assert.Equal(t, http.StatusUnauthorized, status)

	status, body = a.call(t, &e.viewer, "", "GET", base+"/logs?q=order&level=info&limit=10", nil)
	require.Equal(t, http.StatusOK, status, body)
	items := body["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "order placed", items[0].(map[string]any)["message"])
	assert.NotNil(t, body["newest_cursor"])
	assert.Nil(t, body["next_cursor"])
	status, body = a.call(t, &e.viewer, "", "GET", base+"/logs?level=loud", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, body = a.call(t, &e.viewer, "", "GET", base+"/logs/services", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []any{"shop/api"}, body["items"])

	status, _ = a.call(t, &e.admin, "", "DELETE", "/api/v1/log-ingest-tokens/"+tok["id"].(string), nil)
	assert.Equal(t, http.StatusNoContent, status)
	status, _ = a.call(t, &e.admin, "", "DELETE", "/api/v1/log-ingest-tokens/not-a-uuid", nil)
	assert.Equal(t, http.StatusNotFound, status)
	status, _ = a.call(t, nil, token, "POST", "/api/v1/ingest/logs", `{"message":"x"}`)
	assert.Equal(t, http.StatusUnauthorized, status)
	status, _ = a.call(t, &e.admin, "", "GET", "/api/v1/orgs/not-a-uuid/logs", nil)
	assert.Equal(t, http.StatusNotFound, status)
}
