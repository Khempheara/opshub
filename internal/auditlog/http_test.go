package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	signer, err := authn.NewJWTSigner([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{7}, 32)}}, nil)
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

// reply is a response with its body read (and closed).
type reply struct {
	StatusCode int
	Header     http.Header
}

// get sends a GET as u (anonymous when nil).
func (a *api) get(t *testing.T, u *user, path string, headers map[string]string) (reply, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, a.srv.URL+path, http.NoBody)
	require.NoError(t, err)
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
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, body
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out), string(body))
	return out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

var tenantRoutes = []string{"GET /orgs/{orgId}/audit-log", "GET /orgs/{orgId}/audit-log/export"}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	for _, path := range []string{"/audit-log", "/audit-log/export"} {
		resp, body := a.get(t, &e.outsider, "/api/v1/orgs/"+e.orgID.String()+path, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, path)
		assert.Equal(t, "ORG_NOT_FOUND", errCode(decode(t, body)))
	}
	require.Len(t, tenantRoutes, 2)
}

func TestRouteCoverage(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Mount(r)
	covered := map[string]bool{"GET /me/activity": true}
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
	base := "/api/v1/orgs/" + e.orgID.String() + "/audit-log"

	resp, body := a.get(t, &e.admin, base+"?area=org&limit=10", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	page := decode(t, body)
	items := page["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "Owner Sok created the organization Audit Co", items[0].(map[string]any)["summary"])
	assert.Nil(t, page["next_cursor"])

	// The language follows ?locale, else Accept-Language.
	_, body = a.get(t, &e.admin, base+"?area=org", map[string]string{"Accept-Language": "km"})
	assert.Equal(t, "Owner Sok បានបង្កើតអង្គភាព Audit Co", decode(t, body)["items"].([]any)[0].(map[string]any)["summary"])
	_, body = a.get(t, &e.admin, base+"?area=org&locale=en", map[string]string{"Accept-Language": "km"})
	assert.Equal(t, "Owner Sok created the organization Audit Co", decode(t, body)["items"].([]any)[0].(map[string]any)["summary"])

	resp, body = a.get(t, &e.admin, base+"?area=org,Bad&limit=500", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, string(body))
	resp, body = a.get(t, &e.admin, base+"?area=Bad", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, string(body))
	resp, _ = a.get(t, &e.dev, base, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp, _ = a.get(t, nil, base, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp, _ = a.get(t, &e.admin, "/api/v1/orgs/not-a-uuid/audit-log", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// CSV export.
	resp, body = a.get(t, &e.admin, base+"/export?area=org&area=audit", map[string]string{"Accept-Language": "km"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Equal(t, "text/csv; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), `attachment; filename="audit-log-`+e.slug+"-")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.True(t, bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF, 't', 'i', 'm', 'e', ','}))
	assert.Contains(t, string(body), "Admin Dara បាននាំចេញកំណត់ត្រាសវនកម្ម")
	resp, body = a.get(t, &e.dev, base+"/export", nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "FORBIDDEN", errCode(decode(t, body)))
	resp, _ = a.get(t, &e.admin, base+"/export?from=nope", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	resp, _ = a.get(t, &e.admin, "/api/v1/orgs/not-a-uuid/audit-log/export", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Account activity: only a signed-in person's own events.
	resp, body = a.get(t, &e.owner, "/api/v1/me/activity?limit=5", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Empty(t, decode(t, body)["items"], "creating an organization is not account activity")
	resp, _ = a.get(t, &e.owner, "/api/v1/me/activity?limit=0", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	resp, _ = a.get(t, nil, "/api/v1/me/activity", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
