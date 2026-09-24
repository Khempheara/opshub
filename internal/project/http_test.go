package project

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
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/idempotency"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/server"
	"github.com/opshub/opshub/internal/telemetry"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

type noTokens struct{}

func (noTokens) LookupAPIToken(context.Context, []byte) (uuid.UUID, uuid.UUID, []string, error) {
	return uuid.Nil, uuid.Nil, nil, io.EOF
}

type api struct {
	handler http.Handler
	signer  *authn.JWTSigner
}

func newAPI(t *testing.T, e *env) *api {
	t.Helper()
	signer, err := authn.NewJWTSigner([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{5}, 32)}}, nil)
	require.NoError(t, err)
	pool := pgtest.Pool(t)
	h := server.New(server.Deps{
		Config:        config.Config{DefaultLocale: "en", DefaultTimezone: "UTC", RateLimitRPS: 1000, RateLimitBurst: 1000},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            pool,
		Metrics:       telemetry.NewMetrics(),
		Version:       "test",
		Authenticator: &authn.Authenticator{JWT: signer, Tokens: noTokens{}},
		Modules:       []server.Module{org.NewHandler(e.orgs), NewHandler(e.svc, idempotency.Middleware(pool))},
	})
	return &api{handler: h, signer: signer}
}

func (a *api) call(t *testing.T, u *user, method, path string, body any, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		if raw, ok := body.([]byte); ok {
			rdr = bytes.NewReader(raw)
		} else {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"v1"`)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if u != nil {
		tok, _, err := a.signer.Issue(u.id, uuid.New())
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// tenantRoutes lists every project-module route that takes a tenant-owned id.
var tenantRoutes = []string{
	"GET /orgs/{orgId}/projects", "POST /orgs/{orgId}/projects",
	"GET /projects/{projectId}/", "PATCH /projects/{projectId}/", "DELETE /projects/{projectId}/",
	"GET /projects/{projectId}/members", "PUT /projects/{projectId}/members/{principal}",
	"DELETE /projects/{projectId}/members/{principal}",
	"GET /projects/{projectId}/repository", "PUT /projects/{projectId}/repository",
	"DELETE /projects/{projectId}/repository", "POST /projects/{projectId}/repository/test",
	"GET /projects/{projectId}/repository/deliveries",
	"GET /projects/{projectId}/environments", "POST /projects/{projectId}/environments",
	"GET /environments/{environmentId}/", "PATCH /environments/{environmentId}/", "DELETE /environments/{environmentId}/",
	// Webhooks are authenticated by signature; covered by TestWebhookOverHTTP.
	"POST /webhooks/github/{repositoryId}", "POST /webhooks/gitlab/{repositoryId}",
}

// TestTenantIsolation calls every project route with another organization's ids and
// requires a 404 that doesn't reveal the resource exists.
func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	p := e.project(t, e.admin)
	envr, err := e.svc.CreateEnvironment(e.admin.ctx, p.ID, EnvironmentInput{Name: "prod", Kind: "production"})
	require.NoError(t, err)
	_, err = e.svc.ConnectRepository(e.admin.ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)

	o, pid, env := e.orgID.String(), p.ID.String(), envr.ID.String()
	member := "user:" + e.dev.id.String()
	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"GET", "/api/v1/orgs/" + o + "/projects", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/projects", map[string]string{"name": "x"}, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/projects/" + pid, nil, "PROJECT_NOT_FOUND"},
		{"PATCH", "/api/v1/projects/" + pid, map[string]string{"name": "x", "default_branch": "main"}, "PROJECT_NOT_FOUND"},
		{"DELETE", "/api/v1/projects/" + pid + "?confirm=" + p.Slug, nil, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/projects/" + pid + "/members", nil, "PROJECT_NOT_FOUND"},
		{"PUT", "/api/v1/projects/" + pid + "/members/" + member, map[string]string{"role": "admin"}, "PROJECT_NOT_FOUND"},
		{"DELETE", "/api/v1/projects/" + pid + "/members/" + member, nil, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/projects/" + pid + "/repository", nil, "PROJECT_NOT_FOUND"},
		{"PUT", "/api/v1/projects/" + pid + "/repository", map[string]string{"provider": "github", "full_name": "a/b", "access_token": goodToken}, "PROJECT_NOT_FOUND"},
		{"DELETE", "/api/v1/projects/" + pid + "/repository", nil, "PROJECT_NOT_FOUND"},
		{"POST", "/api/v1/projects/" + pid + "/repository/test", nil, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/projects/" + pid + "/repository/deliveries", nil, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/projects/" + pid + "/environments", nil, "PROJECT_NOT_FOUND"},
		{"POST", "/api/v1/projects/" + pid + "/environments", map[string]string{"name": "qa", "kind": "staging"}, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/environments/" + env, nil, "ENVIRONMENT_NOT_FOUND"},
		{"PATCH", "/api/v1/environments/" + env, map[string]string{"kind": "staging"}, "ENVIRONMENT_NOT_FOUND"},
		{"DELETE", "/api/v1/environments/" + env, nil, "ENVIRONMENT_NOT_FOUND"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec, body := a.call(t, &e.outsider, c.method, c.path, c.body, nil)
			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.Equal(t, c.code, errCode(body))
		})
	}

	// Nothing changed.
	got, err := e.svc.Get(e.admin.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.Name, got.Name)
	repo, err := e.svc.GetRepository(e.admin.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "acme/api", repo.FullName)
	envs, _ := e.svc.ListEnvironments(e.admin.ctx, p.ID)
	assert.Len(t, envs, 1)
}

// TestTenantIsolationCoversAllRoutes fails when a route is added without isolation coverage.
func TestTenantIsolationCoversAllRoutes(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil, func(h http.Handler) http.Handler { return h }).Mount(r)
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

func TestRoleEnforcementAndIdempotencyOverHTTP(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	base := "/api/v1/orgs/" + e.orgID.String() + "/projects"

	rec, body := a.call(t, &e.viewer, "POST", base, map[string]string{"name": "Nope"}, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "FORBIDDEN", errCode(body))

	// Idempotent create: a retry returns the same project and creates nothing new.
	hdr := map[string]string{"Idempotency-Key": uuid.NewString()}
	rec, first := a.call(t, &e.dev, "POST", base, map[string]string{"name": "Ledger", "slug": "ledger"}, hdr)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "admin", first["role"])
	rec, again := a.call(t, &e.dev, "POST", base, map[string]string{"name": "Ledger", "slug": "ledger"}, hdr)
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "true", rec.Header().Get("Idempotent-Replayed"))
	assert.Equal(t, first["id"], again["id"])
	rec, body = a.call(t, &e.dev, "POST", base, map[string]string{"name": "Other"}, hdr)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "IDEMPOTENCY_KEY_REUSED", errCode(body))
	// Without the header, a second create with the same slug is a normal conflict.
	rec, body = a.call(t, &e.dev, "POST", base, map[string]string{"name": "Ledger", "slug": "ledger"}, nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "SLUG_TAKEN", errCode(body))

	pid := first["id"].(string)
	rec, body = a.call(t, &e.viewer, "GET", "/api/v1/projects/"+pid, nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "viewer", body["role"])
	assert.Equal(t, `"v1"`, rec.Header().Get("ETag"))
	rec, _ = a.call(t, &e.viewer, "PATCH", "/api/v1/projects/"+pid, map[string]string{"name": "X", "default_branch": "main"}, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec, _ = a.call(t, &e.dev2, "GET", "/api/v1/projects/"+pid, nil, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "ungranted developers can't see it")

	// Grant via principal path segment, then dev2 can see it.
	rec, _ = a.call(t, &e.dev, "PUT", "/api/v1/projects/"+pid+"/members/user:"+e.dev2.id.String(), map[string]string{"role": "viewer"}, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	rec, _ = a.call(t, &e.dev2, "GET", "/api/v1/projects/"+pid, nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	rec, _ = a.call(t, &e.dev, "PATCH", "/api/v1/projects/"+pid, map[string]string{"name": "Ledger 2", "default_branch": "main"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, `"v2"`, rec.Header().Get("ETag"))
	rec, body = a.call(t, &e.dev, "PATCH", "/api/v1/projects/"+pid, map[string]string{"name": "Ledger 3", "default_branch": "main"}, nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "VERSION_CONFLICT", errCode(body))

	// Anonymous requests are rejected (webhooks aside).
	rec, _ = a.call(t, nil, "GET", "/api/v1/projects/"+pid, nil, nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestWebhookOverHTTP(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, e)
	p := e.project(t, e.admin)
	e.git.admin = false
	res, err := e.svc.ConnectRepository(e.admin.ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)
	url := "/api/v1/webhooks/github/" + res.Repository.ID.String()
	body := []byte(`{"ref":"refs/heads/main","after":"abc"}`)
	hdr := map[string]string{
		"X-GitHub-Event": "push", "X-GitHub-Delivery": "http-1",
		"X-Hub-Signature-256": gitprovider.SignGitHub([]byte(res.Webhook.Secret), body),
	}

	rec, out := a.call(t, nil, "POST", url, body, hdr)
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, "accepted", out["status"])
	rec, out = a.call(t, nil, "POST", url, body, hdr)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "duplicate", out["status"])

	hdr["X-Hub-Signature-256"] = "sha256=" + strings.Repeat("0", 64)
	rec, out = a.call(t, nil, "POST", url, body, hdr)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "WEBHOOK_SIGNATURE_INVALID", errCode(out))

	rec, out = a.call(t, nil, "POST", "/api/v1/webhooks/github/not-a-uuid", body, hdr)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "NOT_FOUND", errCode(out))

	big := bytes.Repeat([]byte("a"), MaxWebhookBody+1)
	rec, out = a.call(t, nil, "POST", url, big, hdr)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, "PAYLOAD_TOO_LARGE", errCode(out))
}
