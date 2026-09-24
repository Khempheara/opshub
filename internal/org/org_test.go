package org

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

func newUser(t *testing.T) context.Context {
	t.Helper()
	u, err := store.New(pgtest.Pool(t)).CreateUser(context.Background(), store.CreateUserParams{
		Email: uuid.NewString() + "@example.com", DisplayName: "U", Locale: "en", Timezone: "UTC",
	})
	require.NoError(t, err)
	return authn.WithPrincipal(context.Background(), authn.Principal{Kind: authn.KindSession, UserID: u.ID, SessionID: uuid.New()})
}

func codeOf(t *testing.T, err error) apperr.Code {
	t.Helper()
	ae, ok := apperr.From(err)
	require.True(t, ok, "expected apperr, got %v", err)
	return ae.Code
}

func TestCreateListGet(t *testing.T) {
	svc := NewService(pgtest.Pool(t))
	alice := newUser(t)
	slug := "mekong-" + uuid.NewString()[:8]

	o, err := svc.Create(alice, CreateInput{Name: "Mekong Labs", Slug: slug})
	require.NoError(t, err)
	assert.Equal(t, "owner", o.Role)

	_, err = svc.Create(alice, CreateInput{Name: "Again", Slug: slug})
	assert.Equal(t, apperr.CodeSlugTaken, codeOf(t, err))

	_, err = svc.Create(alice, CreateInput{Name: "អង្គរ"})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err), "Khmer-only names need an explicit slug")
	km, err := svc.Create(alice, CreateInput{Name: "អង្គរ តិច", Slug: "angkor-" + uuid.NewString()[:8]})
	require.NoError(t, err)
	assert.Equal(t, "អង្គរ តិច", km.Name)

	page, err := svc.ListMine(alice, pagination.Params{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.NextCursor)

	got, err := svc.Get(alice, o.ID)
	require.NoError(t, err)
	assert.Equal(t, o, got)
}

// Tenant isolation: another user can neither read nor list someone else's organization,
// and the error doesn't reveal that it exists.
func TestTenantIsolation(t *testing.T) {
	svc := NewService(pgtest.Pool(t))
	alice, mallory := newUser(t), newUser(t)
	o, err := svc.Create(alice, CreateInput{Name: "Private", Slug: "private-" + uuid.NewString()[:8]})
	require.NoError(t, err)

	_, err = svc.Get(mallory, o.ID)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))
	_, err = svc.Get(mallory, uuid.New())
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err), "same error as a non-existent org")

	page, err := svc.ListMine(mallory, pagination.Params{Limit: 50})
	require.NoError(t, err)
	assert.Empty(t, page.Items)

	_, err = svc.Get(context.Background(), o.ID)
	assert.Equal(t, apperr.CodeUnauthenticated, codeOf(t, err))
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Angkor Tech":         "angkor-tech",
		"  Mekong -- Labs!! ": "mekong-labs",
		"អង្គរ":               "",
		"Angkor Tech · អង្គរ": "angkor-tech",
		"A_B.C":               "a-b-c",
	} {
		assert.Equal(t, want, Slugify(in), in)
	}
}

func TestHTTPRoutes(t *testing.T) {
	svc := NewService(pgtest.Pool(t))
	r := chi.NewRouter()
	alice := newUser(t)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("X-Test-User") == "alice" {
				p, _ := authn.PrincipalFrom(alice)
				req = req.WithContext(authn.WithPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	NewHandler(svc).Mount(r)
	call := func(method, path, body string, authed bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authed {
			req.Header.Set("X-Test-User", "alice")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	assert.Equal(t, http.StatusUnauthorized, call("GET", "/orgs", "", false).Code)
	rec := call("POST", "/orgs", `{"name":"Tonle Sap Cloud"}`, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var o Organization
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &o))
	assert.Equal(t, "tonle-sap-cloud", o.Slug)

	assert.Equal(t, http.StatusOK, call("GET", "/orgs/"+o.ID.String(), "", true).Code)
	assert.Equal(t, http.StatusNotFound, call("GET", "/orgs/not-a-uuid", "", true).Code)
	assert.Equal(t, http.StatusOK, call("GET", "/orgs?limit=5", "", true).Code)
	assert.Equal(t, http.StatusUnprocessableEntity, call("GET", "/orgs?limit=0", "", true).Code)
	assert.Equal(t, http.StatusUnprocessableEntity, call("POST", "/orgs", `{"name":""}`, true).Code)
}
