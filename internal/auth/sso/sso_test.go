package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/opshub/opshub/internal/config"
)

// fakeIdP is a minimal OpenID Connect provider: discovery, JWKS and token endpoint.
type fakeIdP struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	clientID string
	claims   map[string]any
	nonce    string
	// wantVerifier records the PKCE check.
	gotVerifier string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	f := &fakeIdP{key: key, clientID: "opshub"}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/auth", "token_endpoint": f.srv.URL + "/token",
			"jwks_uri": f.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.gotVerifier = r.PostForm.Get("code_verifier")
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
		claims := map[string]any{
			"iss": f.srv.URL, "aud": f.clientID, "sub": "user-123", "exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(), "nonce": f.nonce,
		}
		for k, v := range f.claims {
			claims[k] = v
		}
		idToken, _ := jwt.Signed(signer).Claims(claims).Serialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
	})
	return f
}

func TestOIDCFlow(t *testing.T) {
	idp := newFakeIdP(t)
	p := NewOIDC("keycloak", idp.srv.URL, "opshub", "secret", "https://ops.example.com/cb", idp.srv.Client())
	ctx := context.Background()

	verifier := oauth2.GenerateVerifier()
	authURL, err := p.AuthURL(ctx, "state-1", "nonce-1", verifier)
	require.NoError(t, err)
	u, _ := url.Parse(authURL)
	assert.Equal(t, "state-1", u.Query().Get("state"))
	assert.Equal(t, "nonce-1", u.Query().Get("nonce"))
	assert.Equal(t, "S256", u.Query().Get("code_challenge_method"))
	assert.Contains(t, u.Query().Get("scope"), "openid")

	idp.nonce = "nonce-1"
	idp.claims = map[string]any{"email": "Dara@Example.com", "email_verified": "true", "name": "Dara"}
	id, err := p.Exchange(ctx, "code", verifier, "nonce-1")
	require.NoError(t, err)
	assert.Equal(t, Identity{Provider: "keycloak", Subject: "user-123", Email: "Dara@Example.com", EmailVerified: true, Name: "Dara"}, id)
	assert.Equal(t, verifier, idp.gotVerifier, "PKCE verifier sent to the token endpoint")

	idp.nonce = "someone-elses-nonce"
	_, err = p.Exchange(ctx, "code", verifier, "nonce-1")
	assert.ErrorContains(t, err, "nonce")

	idp.nonce = "nonce-1"
	idp.clientID = "another-client"
	_, err = p.Exchange(ctx, "code", verifier, "nonce-1")
	assert.Error(t, err, "audience must match our client id")
}

func TestOIDCDiscoveryFailureIsReported(t *testing.T) {
	p := NewOIDC("keycloak", "http://127.0.0.1:1/realms/x", "c", "s", "https://ops/cb", &http.Client{Timeout: time.Second})
	_, err := p.AuthURL(context.Background(), "s", "n", "v")
	assert.ErrorContains(t, err, "discovery")
}

func TestGitHubFlow(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var gotVerifier string
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotVerifier = r.PostForm.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gh-token","token_type":"bearer","scope":"read:user,user:email"}`))
	})
	mux.HandleFunc("/api/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id": 583231, "login": "octocat", "name": ""}`))
	})
	mux.HandleFunc("/api/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"email":"old@example.com","primary":false,"verified":true},
			{"email":"unverified@example.com","primary":false,"verified":false},
			{"email":"octo@example.com","primary":true,"verified":true}]`))
	})

	g := &GitHub{ClientID: "id", ClientSecret: "secret", RedirectURL: "https://ops/cb", WebURL: srv.URL, APIURL: srv.URL + "/api", HTTP: srv.Client()}
	authURL, err := g.AuthURL(context.Background(), "st", "", oauth2.GenerateVerifier())
	require.NoError(t, err)
	assert.Contains(t, authURL, srv.URL+"/login/oauth/authorize?")

	id, err := g.Exchange(context.Background(), "code", "the-verifier", "")
	require.NoError(t, err)
	assert.Equal(t, Identity{Provider: "github", Subject: "583231", Email: "octo@example.com", EmailVerified: true, Name: "octocat"}, id)
	assert.Equal(t, "the-verifier", gotVerifier)
}

func TestRegistryEnablesConfiguredProviders(t *testing.T) {
	r := NewRegistry(config.SSOConfig{
		GitHubClientID: "gh", GitHubURL: "https://github.com", GitHubAPIURL: "https://api.github.com",
		KeycloakIssuer: "https://sso.example.com/realms/acme", KeycloakClientID: "kc",
	}, "https://ops.example.com/", nil)
	assert.Equal(t, []string{"github", "keycloak"}, r.Names())
	p, ok := r.Get("github")
	require.True(t, ok)
	assert.Equal(t, "https://ops.example.com/api/v1/auth/sso/github/callback", p.(*GitHub).RedirectURL)
	_, ok = r.Get("google")
	assert.False(t, ok)
}
