// Package auth implements Module 1: registration, email verification, password login with
// lockout, TOTP two-factor authentication, rotating refresh sessions, password reset,
// SSO (GitHub / OIDC), profile & preferences, and personal API tokens.
package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/store"
)

// Lifetimes and limits.
const (
	RefreshTokenTTL    = 14 * 24 * time.Hour // sliding: each refresh issues a new 14-day token…
	SessionMaxAge      = 30 * 24 * time.Hour // …but a session never outlives 30 days
	EmailVerifyTTL     = 24 * time.Hour
	PasswordResetTTL   = time.Hour
	MFAChallengeTTL    = 5 * time.Minute
	MaxMFAAttempts     = 5
	LockoutThreshold   = 5
	LockoutBase        = 15 * time.Minute
	LockoutMax         = 24 * time.Hour
	RecoveryCodeCount  = 10
	MaxAPITokenTTLDays = 365
	totpIssuer         = "OpsHub"
)

// Config holds the policy settings the service needs.
type Config struct {
	PublicURL           string
	AllowSignup         bool
	BootstrapAdminEmail string
	DefaultLocale       string
	DefaultTimezone     string
}

// Service implements the auth use cases. All methods authorize and audit internally.
type Service struct {
	pool   *pgxpool.Pool
	cfg    Config
	hasher *authn.Hasher
	jwt    *authn.JWTSigner
	keys   *crypto.KeyRing
	jobs   jobs.Inserter
	logger *slog.Logger
	now    func() time.Time
}

func NewService(pool *pgxpool.Pool, cfg Config, hasher *authn.Hasher, signer *authn.JWTSigner, keys *crypto.KeyRing, inserter jobs.Inserter, logger *slog.Logger) *Service {
	return &Service{pool: pool, cfg: cfg, hasher: hasher, jwt: signer, keys: keys, jobs: inserter, logger: logger, now: time.Now}
}

// User is the public representation of an account.
type User struct {
	ID               uuid.UUID `json:"id"`
	Email            string    `json:"email"`
	DisplayName      string    `json:"display_name"`
	Locale           string    `json:"locale"`
	Timezone         string    `json:"timezone"`
	KhmerNumerals    bool      `json:"khmer_numerals"`
	EmailVerified    bool      `json:"email_verified"`
	TwoFactorEnabled bool      `json:"two_factor_enabled"`
	HasPassword      bool      `json:"has_password"`
	IsPlatformAdmin  bool      `json:"is_platform_admin"`
	CreatedAt        time.Time `json:"created_at"`
	Version          int32     `json:"version"`
}

func toUser(u store.User) User {
	return User{
		ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Locale: u.Locale, Timezone: u.Timezone,
		KhmerNumerals: u.KhmerNumerals, EmailVerified: u.EmailVerifiedAt != nil,
		TwoFactorEnabled: u.TotpEnabledAt != nil, HasPassword: u.PasswordHash != nil,
		IsPlatformAdmin: u.IsPlatformAdmin, CreatedAt: u.CreatedAt, Version: u.Version,
	}
}

// SessionTokens are issued on login and refresh. The refresh token is only ever sent to
// the browser in an httpOnly cookie.
type SessionTokens struct {
	SessionID        uuid.UUID
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	User             User
}

// LoginResult is either a session or a pending second factor.
type LoginResult struct {
	Session      *SessionTokens
	MFAToken     string
	MFAExpiresAt time.Time
}

func (s *Service) inTx(ctx context.Context, fn func(q *store.Queries, tx pgx.Tx) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(store.New(tx), tx) })
}

func (s *Service) enqueueEmail(ctx context.Context, tx pgx.Tx, to, template, locale string, data map[string]any) error {
	_, err := s.jobs.InsertTx(ctx, tx, jobs.SendEmailArgs{To: to, Template: template, Locale: locale, Data: data}, nil)
	return err
}

// link builds a SPA URL. One-time tokens go in the fragment so they never reach server
// logs or Referer headers.
func (s *Service) link(path, token string) string {
	u := strings.TrimRight(s.cfg.PublicURL, "/") + path
	if token != "" {
		u += "#token=" + url.QueryEscape(token)
	}
	return u
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func principal(ctx context.Context) (authn.Principal, error) {
	p, ok := authn.PrincipalFrom(ctx)
	if !ok {
		return authn.Principal{}, apperr.Unauthenticated()
	}
	return p, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func errInvalidCredentials() *apperr.Error {
	return apperr.New(apperr.CodeInvalidCredentials, http.StatusUnauthorized, "invalid email or password")
}

func errInvalidToken() *apperr.Error {
	return apperr.New(apperr.CodeInvalidToken, http.StatusBadRequest, "the link is invalid or has expired")
}

func errRefreshInvalid() *apperr.Error {
	return apperr.New(apperr.CodeRefreshInvalid, http.StatusUnauthorized, "session expired; sign in again")
}

func errAccountDisabled() *apperr.Error {
	return apperr.New(apperr.CodeAccountDisabled, http.StatusForbidden, "this account is disabled")
}

// recordAudit is a small wrapper that keeps call sites short.
func recordAudit(ctx context.Context, q *store.Queries, action string, userID uuid.UUID, meta map[string]any) error {
	return audit.Record(ctx, q, audit.Entry{
		Action: action, ResourceType: "user", ResourceID: userID.String(), ActorUserID: &userID, Metadata: meta,
	})
}
