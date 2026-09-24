package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/auth/sso"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/ratelimit"
)

const (
	mfaCookie      = "opshub_mfa"
	ssoStateCookie = "opshub_sso"
	authCookiePath = "/api/v1/auth"
	ssoCookiePath  = "/api/v1/auth/sso"
	ssoStateTTL    = 10 * time.Minute
)

// HandlerConfig configures cookies and redirects.
type HandlerConfig struct {
	PublicURL      string
	SecureCookies  bool
	AllowedOrigins []string // for the CSRF Origin check (public URL + CORS allow-list)
	// Per-IP limits; zero means the defaults (10 logins/min, 5 email-sending requests/min).
	LoginPerMinute int
	EmailPerMinute int
}

// Handler exposes the auth service over HTTP.
type Handler struct {
	svc        *Service
	sso        *sso.Registry
	keys       *crypto.KeyRing
	cfg        HandlerConfig
	logger     *slog.Logger
	loginLimit *ratelimit.Limiter
	emailLimit *ratelimit.Limiter
}

func NewHandler(svc *Service, registry *sso.Registry, keys *crypto.KeyRing, cfg HandlerConfig, logger *slog.Logger) *Handler {
	if cfg.LoginPerMinute == 0 {
		cfg.LoginPerMinute = 10 // per IP; the account lockout additionally protects each account
	}
	if cfg.EmailPerMinute == 0 {
		cfg.EmailPerMinute = 5 // per IP; endpoints that send email or consume one-time tokens
	}
	return &Handler{
		svc: svc, sso: registry, keys: keys, cfg: cfg, logger: logger,
		loginLimit: ratelimit.PerMinute(cfg.LoginPerMinute),
		emailLimit: ratelimit.PerMinute(cfg.EmailPerMinute),
	}
}

func clientIPKey(r *http.Request) string { return "ip:" + middleware.GetClientIP(r.Context()) }

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(ratelimit.Middleware(h.loginLimit, clientIPKey))
			r.Post("/login", h.login)
			r.Post("/login/2fa", h.loginMFA)
		})
		r.Group(func(r chi.Router) {
			r.Use(ratelimit.Middleware(h.emailLimit, clientIPKey))
			r.Post("/register", h.register)
			r.Post("/verify-email", h.verifyEmail)
			r.Post("/verify-email/resend", h.resendVerification)
			r.Post("/password/forgot", h.forgotPassword)
			r.Post("/password/reset", h.resetPassword)
		})
		r.Group(func(r chi.Router) {
			r.Use(authn.CSRF(h.cfg.AllowedOrigins))
			r.Post("/refresh", h.refresh)
			r.Post("/logout", h.logout)
		})
		r.Get("/sso/{provider}/start", h.ssoStart)
		r.Get("/sso/{provider}/callback", h.ssoCallback)
	})

	r.Route("/me", func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/", h.me)
		r.Patch("/", h.updateMe)
		r.Group(func(r chi.Router) {
			r.Use(authn.RequireSession)
			r.Post("/password", h.changePassword)
			r.Get("/2fa", h.twoFactorStatus)
			r.Post("/2fa/setup", h.setupTOTP)
			r.Post("/2fa/enable", h.enableTOTP)
			r.Post("/2fa/disable", h.disableTOTP)
			r.Post("/2fa/recovery-codes", h.regenerateRecoveryCodes)
			r.Get("/sessions", h.listSessions)
			r.Delete("/sessions/{id}", h.revokeSession)
			r.Get("/tokens", h.listTokens)
			r.Post("/tokens", h.createToken)
			r.Delete("/tokens/{id}", h.revokeToken)
			r.Get("/identities", h.listIdentities)
			r.Delete("/identities/{id}", h.unlinkIdentity)
		})
	})
}

// ─────────────────────────── responses ───────────────────────────

// TokenResponse is returned on successful sign-in and refresh.
type TokenResponse struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int       `json:"expires_in"`
	ExpiresAt   time.Time `json:"expires_at"`
	User        User      `json:"user"`
}

// LoginResponse is either "authenticated" (tokens) or "mfa_required" (challenge).
type LoginResponse struct {
	Status string         `json:"status"`
	Tokens *TokenResponse `json:"tokens,omitempty"`
	MFA    *MFAChallenge  `json:"mfa,omitempty"`
}

type MFAChallenge struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

// writeSession sets the refresh + CSRF cookies and returns the access token.
func (h *Handler) writeSession(w http.ResponseWriter, s *SessionTokens) TokenResponse {
	h.setCookie(w, authn.RefreshCookie, s.RefreshToken, authCookiePath, true, http.SameSiteStrictMode, s.RefreshExpiresAt)
	h.setCookie(w, authn.CSRFCookie, crypto.RandomToken(24), "/", false, http.SameSiteStrictMode, s.RefreshExpiresAt)
	return TokenResponse{
		AccessToken: s.AccessToken, TokenType: "Bearer", ExpiresAt: s.AccessExpiresAt,
		ExpiresIn: int(time.Until(s.AccessExpiresAt).Seconds()), User: s.User,
	}
}

func (h *Handler) clearSession(w http.ResponseWriter) {
	h.setCookie(w, authn.RefreshCookie, "", authCookiePath, true, http.SameSiteStrictMode, time.Unix(0, 0))
	h.setCookie(w, authn.CSRFCookie, "", "/", false, http.SameSiteStrictMode, time.Unix(0, 0))
}

func (h *Handler) setCookie(w http.ResponseWriter, name, value, path string, httpOnly bool, site http.SameSite, exp time.Time) {
	// Secure follows the public URL scheme (always on in production, which requires
	// https); HttpOnly and SameSite are chosen per cookie by the callers.
	c := &http.Cookie{ // #nosec G124 -- Secure is tied to the https public URL; see above
		Name: name, Value: value, Path: path, HttpOnly: httpOnly, Secure: h.cfg.SecureCookies, SameSite: site, Expires: exp,
	}
	if value == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// ─────────────────────────── public auth ───────────────────────────

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in RegisterInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.Register(r.Context(), in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "verification_sent"})
}

type tokenInput struct {
	Token string `json:"token" validate:"required,max=128"`
}

type emailInput struct {
	Email string `json:"email" validate:"required,email,max=254"`
}

func (h *Handler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var in tokenInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.VerifyEmail(r.Context(), in.Token); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) resendVerification(w http.ResponseWriter, r *http.Request) {
	var in emailInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.ResendVerification(r.Context(), in.Email); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var in emailInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.ForgotPassword(r.Context(), in.Email); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

type resetInput struct {
	Token    string `json:"token" validate:"required,max=128"`
	Password string `json:"password" validate:"required"`
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	var in resetInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.ResetPassword(r.Context(), in.Token, in.Password); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type loginInput struct {
	Email    string `json:"email" validate:"required,max=254"`
	Password string `json:"password" validate:"required,max=1024"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in loginInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, h.loginResponse(w, res))
}

func (h *Handler) loginResponse(w http.ResponseWriter, res LoginResult) LoginResponse {
	if res.Session == nil {
		return LoginResponse{Status: "mfa_required", MFA: &MFAChallenge{Token: res.MFAToken, ExpiresAt: res.MFAExpiresAt}}
	}
	t := h.writeSession(w, res.Session)
	return LoginResponse{Status: "authenticated", Tokens: &t}
}

type mfaInput struct {
	MFAToken     string `json:"mfa_token" validate:"omitempty,max=128"`
	Code         string `json:"code" validate:"required_without=RecoveryCode,omitempty,max=16"`
	RecoveryCode string `json:"recovery_code" validate:"required_without=Code,omitempty,max=32"`
}

func (h *Handler) loginMFA(w http.ResponseWriter, r *http.Request) {
	var in mfaInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	token := in.MFAToken
	if token == "" { // SSO logins carry the challenge in an httpOnly cookie
		if c, err := r.Cookie(mfaCookie); err == nil {
			token = c.Value
		}
	}
	sess, err := h.svc.LoginMFA(r.Context(), token, in.Code, in.RecoveryCode)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	h.setCookie(w, mfaCookie, "", authCookiePath, true, http.SameSiteStrictMode, time.Unix(0, 0))
	noStore(w)
	httpx.JSON(w, http.StatusOK, h.writeSession(w, sess))
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var token string
	if c, err := r.Cookie(authn.RefreshCookie); err == nil {
		token = c.Value
	}
	sess, err := h.svc.Refresh(r.Context(), token)
	if err != nil {
		h.clearSession(w)
		httpx.Error(w, r, err)
		return
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, h.writeSession(w, sess))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(authn.RefreshCookie); err == nil {
		if err := h.svc.Logout(r.Context(), c.Value); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	h.clearSession(w)
	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────── SSO ───────────────────────────

type ssoState struct {
	Provider string    `json:"p"`
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	Next     string    `json:"x"`
	Expires  time.Time `json:"e"`
}

var ssoAAD = []byte("sso_state")

// safeNext only allows same-site absolute paths (no open redirects).
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") || strings.ContainsAny(next, "\r\n") {
		return "/"
	}
	return next
}

func (h *Handler) redirectToSPA(w http.ResponseWriter, r *http.Request, path string, q url.Values) {
	u := strings.TrimRight(h.cfg.PublicURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func (h *Handler) ssoError(w http.ResponseWriter, r *http.Request, code apperr.Code) {
	h.redirectToSPA(w, r, "/login", url.Values{"error": {string(code)}})
}

func (h *Handler) ssoStart(w http.ResponseWriter, r *http.Request) {
	p, ok := h.sso.Get(chi.URLParam(r, "provider"))
	if !ok {
		h.ssoError(w, r, apperr.CodeSSOUnknownProvider)
		return
	}
	st := ssoState{
		Provider: p.Name(), State: crypto.RandomToken(32), Nonce: crypto.RandomToken(32),
		Verifier: oauth2.GenerateVerifier(), Next: safeNext(r.URL.Query().Get("next")), Expires: time.Now().Add(ssoStateTTL),
	}
	authURL, err := p.AuthURL(r.Context(), st.State, st.Nonce, st.Verifier)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "sso start", "provider", p.Name(), "error", err)
		h.ssoError(w, r, apperr.CodeSSOFailed)
		return
	}
	raw, _ := json.Marshal(st)
	enc, err := h.keys.Encrypt(raw, ssoAAD)
	if err != nil {
		httpx.Error(w, r, apperr.Internal(err))
		return
	}
	// SameSite=Lax: the callback is a top-level cross-site navigation from the provider.
	h.setCookie(w, ssoStateCookie, base64.RawURLEncoding.EncodeToString(enc), ssoCookiePath, true, http.SameSiteLaxMode, st.Expires)
	// authURL is built from the operator-configured provider endpoints, not request input.
	http.Redirect(w, r, authURL, http.StatusFound) // #nosec G710 -- target is the configured provider, not request input
}

func (h *Handler) ssoCallback(w http.ResponseWriter, r *http.Request) {
	h.setCookie(w, ssoStateCookie, "", ssoCookiePath, true, http.SameSiteLaxMode, time.Unix(0, 0))
	provider := chi.URLParam(r, "provider")
	p, ok := h.sso.Get(provider)
	if !ok {
		h.ssoError(w, r, apperr.CodeSSOUnknownProvider)
		return
	}
	st, ok := h.readSSOState(r)
	q := r.URL.Query()
	if !ok || st.Provider != provider || time.Now().After(st.Expires) ||
		subtle.ConstantTimeCompare([]byte(st.State), []byte(q.Get("state"))) != 1 {
		h.ssoError(w, r, apperr.CodeSSOFailed)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		h.ssoError(w, r, apperr.CodeSSOFailed)
		return
	}
	identity, err := p.Exchange(r.Context(), q.Get("code"), st.Verifier, st.Nonce)
	if err != nil {
		h.logger.WarnContext(r.Context(), "sso exchange failed", "provider", provider, "error", err)
		h.ssoError(w, r, apperr.CodeSSOFailed)
		return
	}
	res, err := h.svc.LoginWithIdentity(r.Context(), identity)
	if err != nil {
		if ae, ok := apperr.From(err); ok && ae.Status < http.StatusInternalServerError {
			h.ssoError(w, r, ae.Code)
			return
		}
		h.logger.ErrorContext(r.Context(), "sso login", "provider", provider, "error", err)
		h.ssoError(w, r, apperr.CodeSSOFailed)
		return
	}
	next := url.Values{"next": {st.Next}}
	if res.Session == nil {
		h.setCookie(w, mfaCookie, res.MFAToken, authCookiePath, true, http.SameSiteStrictMode, res.MFAExpiresAt)
		next.Set("sso", "1")
		h.redirectToSPA(w, r, "/login/2fa", next)
		return
	}
	h.writeSession(w, res.Session)
	h.redirectToSPA(w, r, "/auth/sso-complete", next)
}

func (h *Handler) readSSOState(r *http.Request) (ssoState, bool) {
	c, err := r.Cookie(ssoStateCookie)
	if err != nil {
		return ssoState{}, false
	}
	enc, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return ssoState{}, false
	}
	raw, err := h.keys.Decrypt(enc, ssoAAD)
	if err != nil {
		return ssoState{}, false
	}
	var st ssoState
	return st, json.Unmarshal(raw, &st) == nil
}

// ─────────────────────────── /me ───────────────────────────

func etag(version int32) string { return `"v` + strconv.Itoa(int(version)) + `"` }

// parseIfMatch accepts `"v3"`, `W/"v3"` or `3`.
func parseIfMatch(r *http.Request) (int32, error) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		return 0, apperr.PreconditionRequired()
	}
	v = strings.TrimPrefix(v, "W/")
	v = strings.TrimPrefix(strings.Trim(v, `"`), "v")
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return 0, apperr.BadRequest("malformed If-Match header")
	}
	return int32(n), nil
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, err := h.svc.Me(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(u.Version))
	httpx.JSON(w, http.StatusOK, u)
}

func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) {
	version, err := parseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in UpdateProfileInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	u, err := h.svc.UpdateProfile(r.Context(), version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(u.Version))
	httpx.JSON(w, http.StatusOK, u)
}

type changePasswordInput struct {
	CurrentPassword string `json:"current_password" validate:"max=1024"`
	NewPassword     string `json:"new_password" validate:"required"`
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	var in changePasswordInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.ChangePassword(r.Context(), in.CurrentPassword, in.NewPassword); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TwoFactorStatus summarizes 2FA for the security settings page.
type TwoFactorStatus struct {
	Enabled                bool  `json:"enabled"`
	RecoveryCodesRemaining int64 `json:"recovery_codes_remaining"`
}

func (h *Handler) twoFactorStatus(w http.ResponseWriter, r *http.Request) {
	u, err := h.svc.Me(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	n, err := h.svc.RecoveryCodesRemaining(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, TwoFactorStatus{Enabled: u.TwoFactorEnabled, RecoveryCodesRemaining: n})
}

type passwordInput struct {
	Password string `json:"password" validate:"max=1024"`
}

func (h *Handler) setupTOTP(w http.ResponseWriter, r *http.Request) {
	var in passwordInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	setup, err := h.svc.SetupTOTP(r.Context(), in.Password)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, setup)
}

type codeInput struct {
	Code string `json:"code" validate:"required,max=16"`
}

// RecoveryCodes are shown once.
type RecoveryCodes struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func (h *Handler) enableTOTP(w http.ResponseWriter, r *http.Request) {
	var in codeInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	codes, err := h.svc.EnableTOTP(r.Context(), in.Code)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, RecoveryCodes{RecoveryCodes: codes})
}

type disableInput struct {
	Password     string `json:"password" validate:"max=1024"`
	Code         string `json:"code" validate:"required_without=RecoveryCode,omitempty,max=16"`
	RecoveryCode string `json:"recovery_code" validate:"required_without=Code,omitempty,max=32"`
}

func (h *Handler) disableTOTP(w http.ResponseWriter, r *http.Request) {
	var in disableInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DisableTOTP(r.Context(), in.Password, in.Code, in.RecoveryCode); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var in codeInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	codes, err := h.svc.RegenerateRecoveryCodes(r.Context(), in.Code)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, RecoveryCodes{RecoveryCodes: codes})
}

func pathUUID(r *http.Request, name string, notFound apperr.Code) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, apperr.New(notFound, http.StatusNotFound, "not found")
	}
	return id, nil
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	page, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListSessions(r.Context(), page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id", apperr.CodeSessionNotFound)
	if err == nil {
		err = h.svc.RevokeSession(r.Context(), id)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	page, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListAPITokens(r.Context(), page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	var in CreateAPITokenInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	tok, err := h.svc.CreateAPIToken(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	noStore(w)
	httpx.JSON(w, http.StatusCreated, tok)
}

func (h *Handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id", apperr.CodeTokenNotFound)
	if err == nil {
		err = h.svc.RevokeAPIToken(r.Context(), id)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listIdentities(w http.ResponseWriter, r *http.Request) {
	ids, err := h.svc.ListIdentities(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": ids})
}

func (h *Handler) unlinkIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id", apperr.CodeIdentityNotFound)
	if err == nil {
		err = h.svc.UnlinkIdentity(r.Context(), id)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
