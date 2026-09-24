// Package sso implements single sign-on providers: GitHub (OAuth 2.0 + REST API) and
// OpenID Connect (Google, Keycloak). All flows use PKCE and a state value; OIDC flows also
// verify the ID token signature, audience, issuer, expiry and nonce.
package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/opshub/opshub/internal/config"
)

// Identity is what a provider asserts about the user.
type Identity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// Provider is one SSO login method.
type Provider interface {
	Name() string
	// AuthURL returns the provider authorization URL for this login attempt.
	AuthURL(ctx context.Context, state, nonce, verifier string) (string, error)
	// Exchange trades the authorization code for the user's identity.
	Exchange(ctx context.Context, code, verifier, nonce string) (Identity, error)
}

// Registry holds the enabled providers.
type Registry struct {
	providers map[string]Provider
	names     []string
}

// NewRegistry enables every provider whose client ID is configured. publicURL is the
// externally visible base URL; callbacks are <publicURL>/api/v1/auth/sso/<name>/callback.
func NewRegistry(cfg config.SSOConfig, publicURL string, client *http.Client) *Registry {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	base := strings.TrimRight(publicURL, "/") + "/api/v1/auth/sso/"
	r := &Registry{providers: map[string]Provider{}}
	add := func(p Provider) {
		r.providers[p.Name()] = p
		r.names = append(r.names, p.Name())
	}
	if cfg.GitHubClientID != "" {
		add(&GitHub{
			ClientID: cfg.GitHubClientID, ClientSecret: cfg.GitHubClientSecret, RedirectURL: base + "github/callback",
			WebURL: strings.TrimRight(cfg.GitHubURL, "/"), APIURL: strings.TrimRight(cfg.GitHubAPIURL, "/"), HTTP: client,
		})
	}
	if cfg.GoogleClientID != "" {
		add(NewOIDC("google", "https://accounts.google.com", cfg.GoogleClientID, cfg.GoogleClientSecret, base+"google/callback", client))
	}
	if cfg.KeycloakClientID != "" {
		add(NewOIDC("keycloak", cfg.KeycloakIssuer, cfg.KeycloakClientID, cfg.KeycloakClientSecret, base+"keycloak/callback", client))
	}
	return r
}

// Add registers a provider (used by tests).
func (r *Registry) Add(p Provider) {
	if _, ok := r.providers[p.Name()]; !ok {
		r.names = append(r.names, p.Name())
	}
	r.providers[p.Name()] = p
}

func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Names lists enabled providers in a stable order (shown on the login page).
func (r *Registry) Names() []string { return append([]string{}, r.names...) }

// ─────────────────────────────── OIDC ───────────────────────────────

// OIDC is a generic OpenID Connect provider. Discovery happens lazily on first use so an
// unreachable IdP doesn't prevent the API from starting.
type OIDC struct {
	name, issuer, clientID, clientSecret, redirectURL string
	http                                              *http.Client

	mu       sync.Mutex
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func NewOIDC(name, issuer, clientID, clientSecret, redirectURL string, client *http.Client) *OIDC {
	return &OIDC{name: name, issuer: issuer, clientID: clientID, clientSecret: clientSecret, redirectURL: redirectURL, http: client}
}

func (p *OIDC) Name() string { return p.name }

func (p *OIDC) init(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.oauth != nil {
		return p.oauth, p.verifier, nil
	}
	prov, err := oidc.NewProvider(oidc.ClientContext(ctx, p.http), p.issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("sso %s: discovery: %w", p.name, err)
	}
	p.oauth = &oauth2.Config{
		ClientID: p.clientID, ClientSecret: p.clientSecret, RedirectURL: p.redirectURL,
		Endpoint: prov.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
	}
	p.verifier = prov.Verifier(&oidc.Config{ClientID: p.clientID})
	return p.oauth, p.verifier, nil
}

func (p *OIDC) AuthURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	cfg, _, err := p.init(ctx)
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

func (p *OIDC) Exchange(ctx context.Context, code, verifier, nonce string) (Identity, error) {
	cfg, idv, err := p.init(ctx)
	if err != nil {
		return Identity{}, err
	}
	ctx = oidc.ClientContext(ctx, p.http)
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("sso %s: code exchange: %w", p.name, err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, fmt.Errorf("sso %s: no id_token in token response", p.name)
	}
	idt, err := idv.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("sso %s: id_token: %w", p.name, err)
	}
	if idt.Nonce != nonce {
		return Identity{}, fmt.Errorf("sso %s: nonce mismatch", p.name)
	}
	var claims struct {
		Email             string       `json:"email"`
		EmailVerified     flexibleBool `json:"email_verified"`
		Name              string       `json:"name"`
		PreferredUsername string       `json:"preferred_username"`
	}
	if err := idt.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("sso %s: claims: %w", p.name, err)
	}
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	return Identity{Provider: p.name, Subject: idt.Subject, Email: claims.Email, EmailVerified: bool(claims.EmailVerified), Name: name}, nil
}

// flexibleBool accepts true/false and "true"/"false" (some IdPs send strings).
type flexibleBool bool

func (b *flexibleBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	*b = flexibleBool(v)
	return nil
}

// ─────────────────────────────── GitHub ───────────────────────────────

// GitHub signs in with a GitHub (or GitHub Enterprise Server) account. GitHub is OAuth 2.0
// only, so the identity comes from the REST API; only verified emails are used.
type GitHub struct {
	ClientID, ClientSecret, RedirectURL string
	WebURL, APIURL                      string
	HTTP                                *http.Client
}

func (g *GitHub) Name() string { return "github" }

func (g *GitHub) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID: g.ClientID, ClientSecret: g.ClientSecret, RedirectURL: g.RedirectURL,
		Endpoint: oauth2.Endpoint{AuthURL: g.WebURL + "/login/oauth/authorize", TokenURL: g.WebURL + "/login/oauth/access_token"},
		Scopes:   []string{"read:user", "user:email"},
	}
}

func (g *GitHub) AuthURL(_ context.Context, state, _, verifier string) (string, error) {
	return g.config().AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), nil
}

func (g *GitHub) Exchange(ctx context.Context, code, verifier, _ string) (Identity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.HTTP)
	cfg := g.config()
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("sso github: code exchange: %w", err)
	}
	client := cfg.Client(ctx, tok)

	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := g.get(ctx, client, "/user", &user); err != nil {
		return Identity{}, err
	}
	if user.ID == 0 {
		return Identity{}, errors.New("sso github: missing user id")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := g.get(ctx, client, "/user/emails", &emails); err != nil {
		return Identity{}, err
	}
	id := Identity{Provider: "github", Subject: strconv.FormatInt(user.ID, 10), Name: user.Name}
	if id.Name == "" {
		id.Name = user.Login
	}
	for _, e := range emails {
		if e.Verified && (e.Primary || id.Email == "") {
			id.Email, id.EmailVerified = e.Email, true
		}
	}
	return id, nil
}

func (g *GitHub) get(ctx context.Context, client *http.Client, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.APIURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sso github: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sso github: GET %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
