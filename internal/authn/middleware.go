package authn

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/httpx"
)

// APITokenPrefix marks personal API tokens ("ohp_<id>_<secret>").
const APITokenPrefix = "ohp_"

// APITokenLookup resolves a hashed personal API token.
type APITokenLookup interface {
	LookupAPIToken(ctx context.Context, hash []byte) (userID, tokenID uuid.UUID, scopes []string, err error)
}

// Authenticator resolves the Authorization header into a Principal.
type Authenticator struct {
	JWT    *JWTSigner
	Tokens APITokenLookup
}

// Middleware authenticates requests that carry a bearer credential. Requests without one
// continue anonymously (routes opt in to RequireAuth); an invalid credential is always 401.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			next.ServeHTTP(w, r)
			return
		}
		scheme, cred, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || cred == "" {
			httpx.Error(w, r, apperr.Unauthenticated())
			return
		}
		if IsMachineToken(cred) {
			// Runner and job tokens are verified by the runner API itself; to every other
			// route the request is anonymous.
			next.ServeHTTP(w, r)
			return
		}
		var p Principal
		if strings.HasPrefix(cred, APITokenPrefix) {
			userID, tokenID, scopes, err := a.Tokens.LookupAPIToken(r.Context(), crypto.HashToken(cred))
			if err != nil {
				httpx.Error(w, r, apperr.Unauthenticated())
				return
			}
			p = Principal{Kind: KindAPIToken, UserID: userID, TokenID: tokenID, Scopes: scopes}
			if !isSafeMethod(r.Method) && !p.CanWrite() {
				httpx.Error(w, r, apperr.New(apperr.CodeScope, http.StatusForbidden, "token lacks the api:write scope"))
				return
			}
		} else {
			userID, sessionID, err := a.JWT.Parse(cred)
			if err != nil {
				httpx.Error(w, r, apperr.Unauthenticated())
				return
			}
			p = Principal{Kind: KindSession, UserID: userID, SessionID: sessionID}
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// Token prefixes for machine credentials (runners, jobs).
const (
	RunnerTokenPrefix       = "ohr_"
	RegistrationTokenPrefix = "ohr_reg_" // #nosec G101 -- a token prefix, not a credential
	JobTokenPrefix          = "ohj_"
	AgentTokenPrefix        = "ohi_" // infra agents (Module 7)
	IngestTokenPrefix       = "ohl_" // log ingest (Module 10)
)

// IsMachineToken reports whether a bearer credential belongs to a runner or a job.
func IsMachineToken(cred string) bool {
	return strings.HasPrefix(cred, RunnerTokenPrefix) || strings.HasPrefix(cred, JobTokenPrefix) ||
		strings.HasPrefix(cred, AgentTokenPrefix) || strings.HasPrefix(cred, IngestTokenPrefix)
}

// RequireAuth rejects anonymous requests with 401.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := PrincipalFrom(r.Context()); !ok {
			httpx.Error(w, r, apperr.Unauthenticated())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireSession allows only interactive sessions: account-security endpoints (password,
// 2FA, API tokens, sessions) cannot be driven by an API token.
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			httpx.Error(w, r, apperr.Unauthenticated())
			return
		}
		if !p.IsSession() {
			httpx.Error(w, r, apperr.New(apperr.CodeSessionRequired, http.StatusForbidden, "this endpoint requires an interactive session"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// Cookie names.
const (
	RefreshCookie = "opshub_rt"
	CSRFCookie    = "opshub_csrf"
	CSRFHeader    = "X-CSRF-Token"
)

// CSRF protects cookie-authenticated endpoints (refresh, logout) with a double-submit
// token: the X-CSRF-Token header must equal the opshub_csrf cookie. It also rejects
// requests whose Origin is not an allowed origin, and cross-site fetches.
func CSRF(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if u, err := url.Parse(o); err == nil {
			allowed[u.Scheme+"://"+u.Host] = true
		}
	}
	fail := func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, apperr.New(apperr.CodeCSRF, http.StatusForbidden, "CSRF validation failed"))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" && !allowed[origin] {
				fail(w, r)
				return
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, r)
				return
			}
			c, err := r.Cookie(CSRFCookie)
			header := r.Header.Get(CSRFHeader)
			if err != nil || c.Value == "" || header == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(header)) != 1 {
				fail(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
