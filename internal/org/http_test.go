package org

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
	"github.com/opshub/opshub/internal/authz"
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
	handler http.Handler
	signer  *authn.JWTSigner
}

func newAPI(t *testing.T, svc *Service) *api {
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
		Modules:       []server.Module{NewHandler(svc)},
	})
	return &api{handler: h, signer: signer}
}

func (a *api) call(t *testing.T, u user, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"v1"`)
	tok, _, err := a.signer.Issue(u.id, uuid.New())
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// TestTenantIsolation calls every tenant-scoped route with another organization's IDs and
// requires a 404 that doesn't reveal the resource exists. New tenant-scoped routes must be
// added here (TestTenantIsolationCoversAllRoutes enforces it).
func TestTenantIsolation(t *testing.T) {
	svc, _ := newService(t)
	a := newAPI(t, svc)
	alice, bob, bobMember := newUser(t), newUser(t), newUser(t)
	orgWith(t, svc, alice, nil)
	bobOrg := orgWith(t, svc, bob, map[*user]authz.Role{&bobMember: authz.Developer})
	team, err := svc.CreateTeam(bob.ctx, bobOrg.ID, TeamInput{Name: "Secret team"})
	require.NoError(t, err)
	inv, err := svc.Invite(bob.ctx, bobOrg.ID, InviteInput{Email: "someone@example.com", Role: "viewer"})
	require.NoError(t, err)

	o, tm, m, i := bobOrg.ID.String(), team.ID.String(), bobMember.id.String(), inv.ID.String()
	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"GET", "/api/v1/orgs/" + o, nil, "ORG_NOT_FOUND"},
		{"PATCH", "/api/v1/orgs/" + o, map[string]string{"name": "pwned"}, "ORG_NOT_FOUND"},
		{"DELETE", "/api/v1/orgs/" + o + "?confirm=" + bobOrg.Slug, nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/transfer-ownership", map[string]string{"user_id": alice.id.String()}, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/orgs/" + o + "/permissions", nil, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/orgs/" + o + "/members", nil, "ORG_NOT_FOUND"},
		{"PATCH", "/api/v1/orgs/" + o + "/members/" + m, map[string]string{"role": "viewer"}, "ORG_NOT_FOUND"},
		{"DELETE", "/api/v1/orgs/" + o + "/members/" + m, nil, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/orgs/" + o + "/invitations", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/invitations", map[string]string{"email": "x@example.com", "role": "viewer"}, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/orgs/" + o + "/teams", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/teams", map[string]string{"name": "x"}, "ORG_NOT_FOUND"},
		{"DELETE", "/api/v1/invitations/" + i, nil, "INVITATION_NOT_FOUND"},
		{"GET", "/api/v1/teams/" + tm, nil, "TEAM_NOT_FOUND"},
		{"PATCH", "/api/v1/teams/" + tm, map[string]string{"name": "x"}, "TEAM_NOT_FOUND"},
		{"DELETE", "/api/v1/teams/" + tm, nil, "TEAM_NOT_FOUND"},
		{"PUT", "/api/v1/teams/" + tm + "/members/" + alice.id.String(), nil, "TEAM_NOT_FOUND"},
		{"DELETE", "/api/v1/teams/" + tm + "/members/" + m, nil, "TEAM_NOT_FOUND"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, body := a.call(t, alice, c.method, c.path, c.body)
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, c.code, errCode(body))
		})
	}

	// Nothing changed in Bob's organization.
	got, err := svc.Get(bob.ctx, bobOrg.ID)
	require.NoError(t, err)
	assert.Equal(t, bobOrg.Name, got.Name)
	_, members, err := svc.GetTeam(bob.ctx, team.ID)
	require.NoError(t, err)
	assert.Empty(t, members)
	list, _ := svc.ListInvitations(bob.ctx, bobOrg.ID, pageAll)
	assert.Len(t, list.Items, 1)
}

// TestTenantIsolationCoversAllRoutes fails when a tenant-scoped route exists that the
// isolation test above does not exercise.
func TestTenantIsolationCoversAllRoutes(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Mount(r)
	covered := map[string]bool{}
	for _, route := range []string{
		"GET /orgs/{orgId}/", "PATCH /orgs/{orgId}/", "DELETE /orgs/{orgId}/",
		"POST /orgs/{orgId}/transfer-ownership", "GET /orgs/{orgId}/permissions",
		"GET /orgs/{orgId}/members", "PATCH /orgs/{orgId}/members/{userId}", "DELETE /orgs/{orgId}/members/{userId}",
		"GET /orgs/{orgId}/invitations", "POST /orgs/{orgId}/invitations",
		"GET /orgs/{orgId}/teams", "POST /orgs/{orgId}/teams",
		"DELETE /invitations/{invitationId}",
		"GET /teams/{teamId}/", "PATCH /teams/{teamId}/", "DELETE /teams/{teamId}/",
		"PUT /teams/{teamId}/members/{userId}", "DELETE /teams/{teamId}/members/{userId}",
	} {
		covered[route] = true
	}
	var missing []string
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "{") && !covered[method+" "+route] {
			missing = append(missing, method+" "+route)
		}
		return nil
	}))
	assert.Empty(t, missing, "add these routes to TestTenantIsolation")
}

// TestRoleEnforcementOverHTTP checks the matrix end to end for the mutating endpoints.
func TestRoleEnforcementOverHTTP(t *testing.T) {
	svc, _ := newService(t)
	a := newAPI(t, svc)
	owner, admin, dev, viewer := newUser(t), newUser(t), newUser(t), newUser(t)
	o := orgWith(t, svc, owner, map[*user]authz.Role{&admin: authz.Admin, &dev: authz.Developer, &viewer: authz.Viewer})
	base := "/api/v1/orgs/" + o.ID.String()

	for _, u := range []user{viewer, dev} {
		status, body := a.call(t, u, "POST", base+"/invitations", map[string]string{"email": "n@example.com", "role": "viewer"})
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "FORBIDDEN", errCode(body))
		status, _ = a.call(t, u, "POST", base+"/teams", map[string]string{"name": "Nope"})
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = a.call(t, u, "GET", base+"/members", nil)
		assert.Equal(t, http.StatusOK, status, "members can see who else is in the org")
	}
	status, body := a.call(t, admin, "PATCH", base+"/members/"+owner.id.String(), map[string]string{"role": "viewer"})
	assert.Equal(t, http.StatusForbidden, status, "admins can't demote owners")
	assert.Equal(t, "FORBIDDEN", errCode(body))
	status, body = a.call(t, owner, "DELETE", base+"/members/"+owner.id.String(), nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "LAST_OWNER", errCode(body))

	status, body = a.call(t, viewer, "GET", base+"/permissions", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "viewer", body["role"])

	status, _ = a.call(t, owner, "PATCH", base, map[string]string{"name": "Renamed"})
	assert.Equal(t, http.StatusOK, status, "If-Match v1 matches a fresh org")
	status, body = a.call(t, owner, "PATCH", base, map[string]string{"name": "Again"})
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "VERSION_CONFLICT", errCode(body))
}
