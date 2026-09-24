package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/auth/sso"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/server"
	"github.com/opshub/opshub/internal/telemetry"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

// fakeProvider asserts a fixed identity for any code.
type fakeProvider struct{ id sso.Identity }

func (f fakeProvider) Name() string { return f.id.Provider }
func (f fakeProvider) AuthURL(_ context.Context, state, nonce, verifier string) (string, error) {
	return "https://idp.example.com/authorize?" + url.Values{"state": {state}, "nonce": {nonce}, "v": {verifier}}.Encode(), nil
}
func (f fakeProvider) Exchange(_ context.Context, code, _, _ string) (sso.Identity, error) {
	if code != "good-code" {
		return sso.Identity{}, io.ErrUnexpectedEOF
	}
	return f.id, nil
}

type httpEnv struct {
	*env
	srv    *httptest.Server
	client *http.Client
}

func newHTTPEnv(t *testing.T, providers ...sso.Provider) *httpEnv {
	t.Helper()
	e := newEnv(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := sso.NewRegistry(config.SSOConfig{}, "", nil)
	for _, p := range providers {
		reg.Add(p)
	}
	var srv *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.Config.Handler.ServeHTTP(w, r) })
	srv = httptest.NewServer(handler)
	e.svc.cfg.PublicURL = srv.URL
	srv.Config.Handler = server.New(server.Deps{
		Config:        config.Config{DefaultLocale: "en", DefaultTimezone: "UTC", AllowSignup: true, RateLimitRPS: 1000, RateLimitBurst: 1000},
		Logger:        logger,
		DB:            pgtest.Pool(t),
		Metrics:       telemetry.NewMetrics(),
		Version:       "test",
		Authenticator: &authn.Authenticator{JWT: e.svc.jwt, Tokens: e.svc},
		Modules: []server.Module{NewHandler(e.svc, reg, e.keys, HandlerConfig{
			PublicURL: srv.URL, AllowedOrigins: []string{srv.URL}, EmailPerMinute: 60,
		}, logger)},
	})
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &httpEnv{env: e, srv: srv, client: client}
}

// result is a response whose body has already been read and closed.
type result struct {
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
}

func (r *result) Cookies() []*http.Cookie { return r.cookies }

func (h *httpEnv) do(t *testing.T, method, path string, body any, headers map[string]string) (*result, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return &result{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies()}, out
}

func (h *httpEnv) cookie(name string) *http.Cookie {
	u, _ := url.Parse(h.srv.URL + "/api/v1/auth/refresh")
	for _, c := range h.client.Jar.Cookies(u) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func TestHTTPLoginRefreshLogoutFlow(t *testing.T) {
	h := newHTTPEnv(t)
	email := h.registerVerified(t, "en")

	resp, body := h.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": goodPassword}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "authenticated", body["status"])
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	tokens := body["tokens"].(map[string]any)
	access := tokens["access_token"].(string)

	var rt *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == authn.RefreshCookie {
			rt = c
		}
	}
	require.NotNil(t, rt)
	assert.True(t, rt.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, rt.SameSite)
	assert.Equal(t, "/api/v1/auth", rt.Path)
	assert.NotContains(t, strings.Join(resp.Header.Values("Set-Cookie"), ";"), access, "access token never goes in a cookie")

	resp, me := h.do(t, "GET", "/api/v1/me", nil, map[string]string{"Authorization": "Bearer " + access})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, email, me["email"])
	etag := resp.Header.Get("ETag")
	assert.NotEmpty(t, etag)

	resp, body = h.do(t, "PATCH", "/api/v1/me", map[string]string{"locale": "km"}, map[string]string{"Authorization": "Bearer " + access})
	assert.Equal(t, http.StatusPreconditionRequired, resp.StatusCode)
	assert.Equal(t, "PRECONDITION_REQUIRED", errCode(body))
	resp, body = h.do(t, "PATCH", "/api/v1/me", map[string]string{"locale": "km"}, map[string]string{"Authorization": "Bearer " + access, "If-Match": etag})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "km", body["locale"])
	resp, body = h.do(t, "PATCH", "/api/v1/me", map[string]string{"locale": "en"}, map[string]string{"Authorization": "Bearer " + access, "If-Match": etag})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "VERSION_CONFLICT", errCode(body))

	// Refresh requires the CSRF header.
	resp, body = h.do(t, "POST", "/api/v1/auth/refresh", nil, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "CSRF_FAILED", errCode(body))
	csrf := h.cookie(authn.CSRFCookie)
	require.NotNil(t, csrf)
	resp, body = h.do(t, "POST", "/api/v1/auth/refresh", nil, map[string]string{authn.CSRFHeader: csrf.Value, "Origin": "https://evil.example"})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "foreign origin")
	assert.Equal(t, "CSRF_FAILED", errCode(body))
	resp, body = h.do(t, "POST", "/api/v1/auth/refresh", nil, map[string]string{authn.CSRFHeader: csrf.Value, "Origin": h.srv.URL})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.NotEqual(t, access, body["access_token"])

	resp, _ = h.do(t, "POST", "/api/v1/auth/logout", nil, map[string]string{authn.CSRFHeader: h.cookie(authn.CSRFCookie).Value})
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Nil(t, h.cookie(authn.RefreshCookie), "cookies cleared")
}

func TestHTTPRegisterValidationAndErrors(t *testing.T) {
	h := newHTTPEnv(t)
	resp, body := h.do(t, "POST", "/api/v1/auth/register", map[string]string{"email": "nope", "password": "x", "display_name": ""}, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Equal(t, "VALIDATION_FAILED", errCode(body))

	resp, body = h.do(t, "POST", "/api/v1/auth/register", map[string]any{"email": uniqueEmail("a"), "password": goodPassword, "display_name": "A", "is_platform_admin": true}, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "unknown fields (mass assignment) rejected")
	assert.Equal(t, "BAD_REQUEST", errCode(body))

	resp, body = h.do(t, "POST", "/api/v1/auth/register", map[string]string{"email": uniqueEmail("a"), "password": goodPassword, "display_name": "A"}, map[string]string{"Accept-Language": "km-KH"})
	assert.Equal(t, http.StatusAccepted, resp.StatusCode, body)
	assert.Equal(t, "km", h.jobs.emails[len(h.jobs.emails)-1].Locale, "Accept-Language picks the account locale")

	resp, _ = h.do(t, "GET", "/api/v1/me", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestHTTPAPITokenRestrictions(t *testing.T) {
	h := newHTTPEnv(t)
	sess := h.login(t, h.registerVerified(t, "en"))
	bearer := map[string]string{"Authorization": "Bearer " + sess.AccessToken}

	resp, body := h.do(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "ci", "scopes": []string{"api:read"}}, bearer)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	token := body["token"].(string)
	tokenAuth := map[string]string{"Authorization": "Bearer " + token}

	resp, _ = h.do(t, "GET", "/api/v1/me", nil, tokenAuth)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "read scope can read")
	resp, body = h.do(t, "PATCH", "/api/v1/me", map[string]string{"locale": "km"}, map[string]string{"Authorization": "Bearer " + token, "If-Match": `"v1"`})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "INSUFFICIENT_SCOPE", errCode(body))
	resp, body = h.do(t, "GET", "/api/v1/me/tokens", nil, tokenAuth)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "SESSION_REQUIRED", errCode(body), "tokens can't manage tokens")
}

func TestHTTPLoginRateLimit(t *testing.T) {
	h := newHTTPEnv(t)
	var last *result
	var body map[string]any
	for range 11 {
		last, body = h.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": uniqueEmail("x"), "password": "whatever-password"}, nil)
	}
	assert.Equal(t, http.StatusTooManyRequests, last.StatusCode)
	assert.Equal(t, "RATE_LIMITED", errCode(body))
	assert.NotEmpty(t, last.Header.Get("Retry-After"))
}

func TestHTTPSSOFlow(t *testing.T) {
	email := uniqueEmail("sso")
	h := newHTTPEnv(t, fakeProvider{id: sso.Identity{Provider: "keycloak", Subject: "kc-" + email, Email: email, EmailVerified: true, Name: "Kosal"}})

	resp, _ := h.do(t, "GET", "/api/v1/auth/sso/keycloak/start?next=/settings/profile", nil, nil)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	assert.Equal(t, "idp.example.com", loc.Host)
	assert.NotEmpty(t, loc.Query().Get("state"))

	resp, _ = h.do(t, "GET", "/api/v1/auth/sso/keycloak/callback?code=good-code&state=wrong", nil, nil)
	assert.Contains(t, resp.Header.Get("Location"), "/login?error=SSO_FAILED", "state mismatch")

	// The failed attempt cleared the state cookie; start again.
	resp, _ = h.do(t, "GET", "/api/v1/auth/sso/keycloak/start?next=//evil.example/x", nil, nil)
	loc, _ = url.Parse(resp.Header.Get("Location"))
	state := loc.Query().Get("state")
	resp, _ = h.do(t, "GET", "/api/v1/auth/sso/keycloak/callback?code=good-code&state="+state, nil, nil)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	dest := resp.Header.Get("Location")
	assert.True(t, strings.HasPrefix(dest, h.srv.URL+"/auth/sso-complete?next=%2F"), "open redirect neutralized: %s", dest)
	require.NotNil(t, h.cookie(authn.RefreshCookie), "session cookie set")

	resp, body := h.do(t, "POST", "/api/v1/auth/refresh", nil, map[string]string{authn.CSRFHeader: h.cookie(authn.CSRFCookie).Value})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "Kosal", body["user"].(map[string]any)["display_name"])

	resp, _ = h.do(t, "GET", "/api/v1/auth/sso/unknown/start", nil, nil)
	assert.Contains(t, resp.Header.Get("Location"), "error=SSO_PROVIDER_UNKNOWN")
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "/",
		"/settings":         "/settings",
		"//evil.com":        "/",
		"/\\evil.com":       "/",
		"https://evil.com":  "/",
		"/ok?x=1":           "/ok?x=1",
		"/a\r\nSet-Cookie:": "/",
	} {
		assert.Equal(t, want, safeNext(in), in)
	}
}

// TestHTTPAccountLifecycle drives every account-security endpoint through HTTP.
func TestHTTPAccountLifecycle(t *testing.T) {
	h := newHTTPEnv(t)
	email := uniqueEmail("life")

	resp, body := h.do(t, "POST", "/api/v1/auth/register", map[string]string{"email": email, "password": goodPassword, "display_name": "Life"}, nil)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, body)
	resp, _ = h.do(t, "POST", "/api/v1/auth/verify-email/resend", map[string]string{"email": email}, nil)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	msg, _ := h.jobs.last(email, "verify_email")
	resp, body = h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{"token": "bogus"}, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "INVALID_OR_EXPIRED_TOKEN", errCode(body))
	resp, _ = h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{"token": tokenFrom(t, msg)}, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	sess := h.login(t, email)
	auth := map[string]string{"Authorization": "Bearer " + sess.AccessToken}

	// 2FA over HTTP.
	resp, body = h.do(t, "GET", "/api/v1/me/2fa", nil, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, false, body["enabled"])
	resp, body = h.do(t, "POST", "/api/v1/me/2fa/setup", map[string]string{"password": goodPassword}, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	secret := body["secret"].(string)
	resp, body = h.do(t, "POST", "/api/v1/me/2fa/enable", map[string]string{"code": h.totpCode(t, secret)}, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Len(t, body["recovery_codes"], RecoveryCodeCount)
	*h.clock = h.clock.Add(time.Minute)
	resp, body = h.do(t, "POST", "/api/v1/me/2fa/recovery-codes", map[string]string{"code": h.totpCode(t, secret)}, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	// Login now needs the second factor.
	resp, body = h.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": goodPassword}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "mfa_required", body["status"])
	mfaToken := body["mfa"].(map[string]any)["token"].(string)
	resp, body = h.do(t, "POST", "/api/v1/auth/login/2fa", map[string]string{"mfa_token": mfaToken}, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Equal(t, "VALIDATION_FAILED", errCode(body), "code or recovery code required")
	*h.clock = h.clock.Add(time.Minute)
	resp, body = h.do(t, "POST", "/api/v1/auth/login/2fa", map[string]string{"mfa_token": mfaToken, "code": h.totpCode(t, secret)}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	second := map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}

	// Sessions: two devices; revoke the first from the second.
	resp, body = h.do(t, "GET", "/api/v1/me/sessions?limit=10", nil, second)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Len(t, body["items"], 2)
	resp, _ = h.do(t, "DELETE", "/api/v1/me/sessions/"+sess.SessionID.String(), nil, second)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp, body = h.do(t, "DELETE", "/api/v1/me/sessions/not-a-uuid", nil, second)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "SESSION_NOT_FOUND", errCode(body))

	// Tokens.
	resp, body = h.do(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "deploy", "scopes": []string{"api:write"}, "expires_in_days": 7}, second)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	tokenID := body["id"].(string)
	resp, body = h.do(t, "GET", "/api/v1/me/tokens", nil, second)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Len(t, body["items"], 1)
	assert.Nil(t, body["items"].([]any)[0].(map[string]any)["token"], "secret never listed")
	resp, _ = h.do(t, "DELETE", "/api/v1/me/tokens/"+tokenID, nil, second)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Identities (none linked).
	resp, body = h.do(t, "GET", "/api/v1/me/identities", nil, second)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, body["items"])
	resp, body = h.do(t, "DELETE", "/api/v1/me/identities/"+tokenID, nil, second)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "IDENTITY_NOT_FOUND", errCode(body))

	// Disable 2FA, change password, then reset it by email.
	*h.clock = h.clock.Add(time.Minute)
	resp, body = h.do(t, "POST", "/api/v1/me/2fa/disable", map[string]string{"password": goodPassword, "code": h.totpCode(t, secret)}, second)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, body)
	resp, body = h.do(t, "POST", "/api/v1/me/password", map[string]string{"current_password": goodPassword, "new_password": "changed-passphrase-2026"}, second)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, body)
	resp, _ = h.do(t, "POST", "/api/v1/auth/password/forgot", map[string]string{"email": email}, nil)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	reset, _ := h.jobs.last(email, "reset_password")
	resp, body = h.do(t, "POST", "/api/v1/auth/password/reset", map[string]string{"token": tokenFrom(t, reset), "password": "reset-passphrase-2026"}, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, body)
	resp, body = h.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": "reset-passphrase-2026"}, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "authenticated", body["status"])
}

func TestDisabledAccountCannotSignIn(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := e.registerVerified(t, "en")
	sess := e.login(t, email)
	_, err := pgtest.Pool(t).Exec(ctx, "UPDATE users SET disabled_at = now() WHERE email = $1", email)
	require.NoError(t, err)

	_, err = e.svc.Login(ctx, email, goodPassword)
	assert.Equal(t, "ACCOUNT_DISABLED", string(codeOf(t, err)))
	_, err = e.svc.Refresh(ctx, sess.RefreshToken)
	assert.Equal(t, "REFRESH_TOKEN_INVALID", string(codeOf(t, err)), "existing sessions stop refreshing")
	require.NoError(t, e.svc.ResendVerification(ctx, email), "no-op, no error")
	require.NoError(t, e.svc.ForgotPassword(ctx, email), "no-op, no error")
	_, ok := e.jobs.last(email, "reset_password")
	assert.False(t, ok, "disabled accounts get no reset links")
}
