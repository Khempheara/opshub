package auth

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

const (
	methodPassword = "password"
	methodOIDC     = "oidc"
)

// Login verifies email + password. Failed attempts count toward an account lockout
// (5 failures → 15 min, doubling per repeat, max 24 h). With 2FA enabled the result is a
// short-lived MFA challenge instead of a session.
func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	var (
		result  LoginResult
		failErr error // returned after commit so the failure counter is persisted
	)
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		u, err := q.GetUserByEmail(ctx, normalizeEmail(email))
		if database.IsNoRows(err) {
			s.hasher.VerifyDummy(password)
			failErr = errInvalidCredentials()
			return nil
		}
		if err != nil {
			return err
		}
		if u, err = q.GetUserByIDForUpdate(ctx, u.ID); err != nil {
			return err
		}
		now := s.now()
		if u.LockedUntil != nil && u.LockedUntil.After(now) {
			failErr = errLocked(u.LockedUntil.Sub(now).Seconds())
			return nil
		}
		if u.PasswordHash == nil {
			s.hasher.VerifyDummy(password)
			failErr = errInvalidCredentials()
			return nil
		}
		ok, rehash, err := s.hasher.Verify(password, *u.PasswordHash)
		if err != nil {
			return err
		}
		if !ok {
			failErr, err = s.recordFailure(ctx, q, u)
			return err
		}
		if u.DisabledAt != nil {
			failErr = errAccountDisabled()
			return nil
		}
		if rehash {
			if h, err := s.hasher.Hash(password); err == nil {
				if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{ID: u.ID, PasswordHash: &h}); err != nil {
					return err
				}
			}
		}
		if u.EmailVerifiedAt == nil {
			if err := q.RecordLoginSuccess(ctx, u.ID); err != nil {
				return err
			}
			failErr = apperr.New(apperr.CodeEmailNotVerified, http.StatusForbidden, "confirm your email address before signing in")
			return nil
		}
		result, err = s.completeFirstFactor(ctx, q, u, methodPassword)
		return err
	})
	if err != nil {
		return LoginResult{}, err
	}
	if failErr != nil {
		return LoginResult{}, failErr
	}
	return result, nil
}

func errLocked(seconds float64) *apperr.Error {
	return apperr.New(apperr.CodeAccountLocked, http.StatusLocked, "too many failed sign-in attempts; try again later").
		WithDetails(map[string]any{"retry_after_seconds": int(math.Ceil(seconds))})
}

func (s *Service) recordFailure(ctx context.Context, q *store.Queries, u store.User) (*apperr.Error, error) {
	count, level := u.FailedLoginCount+1, u.LockoutLevel
	params := store.RecordLoginFailureParams{ID: u.ID, FailedLoginCount: count, LockoutLevel: level}
	result := errInvalidCredentials()
	if count >= LockoutThreshold {
		level++
		d := lockoutFor(level)
		until := s.now().Add(d)
		params = store.RecordLoginFailureParams{ID: u.ID, FailedLoginCount: 0, LockoutLevel: level, LockedUntil: &until}
		result = errLocked(d.Seconds())
		if err := recordAudit(ctx, q, "auth.locked", u.ID, map[string]any{"level": level, "minutes": d.Minutes()}); err != nil {
			return nil, err
		}
	}
	if err := q.RecordLoginFailure(ctx, params); err != nil {
		return nil, err
	}
	return result, recordAudit(ctx, q, "auth.login_failed", u.ID, map[string]any{"reason": "bad_password"})
}

// completeFirstFactor starts a session, or an MFA challenge when 2FA is enabled.
func (s *Service) completeFirstFactor(ctx context.Context, q *store.Queries, u store.User, method string) (LoginResult, error) {
	if u.TotpEnabledAt != nil {
		token := crypto.RandomToken(32)
		exp := s.now().Add(MFAChallengeTTL)
		if err := q.CreateMFAChallenge(ctx, store.CreateMFAChallengeParams{
			UserID: u.ID, TokenHash: crypto.HashToken(token), AuthMethod: method, ExpiresAt: exp,
		}); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{MFAToken: token, MFAExpiresAt: exp}, nil
	}
	sess, err := s.startSession(ctx, q, u, method)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Session: sess}, nil
}

// LoginMFA completes a login with a TOTP code or a single-use recovery code.
func (s *Service) LoginMFA(ctx context.Context, mfaToken, code, recoveryCode string) (*SessionTokens, error) {
	var (
		sess    *SessionTokens
		failErr error
	)
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		ch, err := q.GetMFAChallengeForUpdate(ctx, crypto.HashToken(mfaToken))
		if database.IsNoRows(err) {
			failErr = errMFAExpired()
			return nil
		}
		if err != nil {
			return err
		}
		if ch.UsedAt != nil || !ch.ExpiresAt.After(s.now()) || ch.Attempts >= MaxMFAAttempts {
			failErr = errMFAExpired()
			return nil
		}
		u, err := q.GetUserByIDForUpdate(ctx, ch.UserID)
		if err != nil {
			return err
		}
		ok, err := s.checkSecondFactor(ctx, q, u, code, recoveryCode)
		if err != nil {
			return err
		}
		if !ok {
			if err := q.IncrementMFAAttempts(ctx, ch.ID); err != nil {
				return err
			}
			failErr = errMFAInvalid()
			return recordAudit(ctx, q, "auth.login_failed", u.ID, map[string]any{"reason": "bad_second_factor"})
		}
		if err := q.UseMFAChallenge(ctx, ch.ID); err != nil {
			return err
		}
		sess, err = s.startSession(ctx, q, u, ch.AuthMethod)
		return err
	})
	if err != nil {
		return nil, err
	}
	if failErr != nil {
		return nil, failErr
	}
	return sess, nil
}

func errMFAExpired() *apperr.Error {
	return apperr.New(apperr.CodeMFAChallengeExpired, http.StatusUnauthorized, "the sign-in attempt expired; start again")
}

func errMFAInvalid() *apperr.Error {
	return apperr.New(apperr.CodeMFAInvalidCode, http.StatusUnauthorized, "invalid authentication code")
}

// checkSecondFactor validates a TOTP code (each time step accepted once) or consumes a
// recovery code.
func (s *Service) checkSecondFactor(ctx context.Context, q *store.Queries, u store.User, code, recoveryCode string) (bool, error) {
	if u.TotpSecretEnc == nil {
		return false, nil
	}
	if recoveryCode != "" {
		_, err := q.ConsumeRecoveryCode(ctx, store.ConsumeRecoveryCodeParams{UserID: u.ID, CodeHash: crypto.HashToken(normalizeRecoveryCode(recoveryCode))})
		if database.IsNoRows(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, recordAudit(ctx, q, "auth.recovery_code_used", u.ID, nil)
	}
	secret, err := s.keys.Decrypt(u.TotpSecretEnc, totpAAD(u.ID))
	if err != nil {
		return false, fmt.Errorf("decrypt totp secret: %w", err)
	}
	step, ok := authn.ValidateTOTP(string(secret), code, s.now())
	if !ok {
		return false, nil
	}
	_, err = q.AdvanceTOTPStep(ctx, store.AdvanceTOTPStepParams{ID: u.ID, Step: &step})
	if database.IsNoRows(err) {
		return false, nil // code already used (replay)
	}
	return err == nil, err
}

func totpAAD(userID uuid.UUID) []byte { return []byte("totp:" + userID.String()) }

// startSession creates a session with its first refresh token and an access token.
func (s *Service) startSession(ctx context.Context, q *store.Queries, u store.User, method string) (*SessionTokens, error) {
	now := s.now()
	ua := audit.RequestUserAgent(ctx)
	session, err := q.CreateSession(ctx, store.CreateSessionParams{
		UserID: u.ID, AuthMethod: method, Ip: audit.RequestIP(ctx), UserAgent: strPtr(ua), ExpiresAt: now.Add(SessionMaxAge),
	})
	if err != nil {
		return nil, err
	}
	refresh, refreshExp, err := s.issueRefresh(ctx, q, session.ID, nil, session.ExpiresAt)
	if err != nil {
		return nil, err
	}
	access, accessExp, err := s.jwt.Issue(u.ID, session.ID)
	if err != nil {
		return nil, err
	}
	if err := q.RecordLoginSuccess(ctx, u.ID); err != nil {
		return nil, err
	}
	if err := recordAudit(ctx, q, "auth.login", u.ID, map[string]any{"method": method, "session_id": session.ID.String()}); err != nil {
		return nil, err
	}
	return &SessionTokens{
		SessionID: session.ID, AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: refresh, RefreshExpiresAt: refreshExp, User: toUser(u),
	}, nil
}

func (s *Service) issueRefresh(ctx context.Context, q *store.Queries, sessionID uuid.UUID, parent *uuid.UUID, sessionExpires time.Time) (string, time.Time, error) {
	token := crypto.RandomToken(32)
	exp := s.now().Add(RefreshTokenTTL)
	if sessionExpires.Before(exp) {
		exp = sessionExpires
	}
	_, err := q.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{
		SessionID: sessionID, ParentID: parent, TokenHash: crypto.HashToken(token), ExpiresAt: exp,
	})
	return token, exp, err
}

// Refresh rotates a refresh token. Presenting a token that was already used is treated
// as theft: the whole session is revoked (the attacker and the victim are both signed out).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*SessionTokens, error) {
	if refreshToken == "" {
		return nil, errRefreshInvalid()
	}
	var (
		sess    *SessionTokens
		failErr error
	)
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		rt, err := q.GetRefreshTokenForUpdate(ctx, crypto.HashToken(refreshToken))
		if database.IsNoRows(err) {
			failErr = errRefreshInvalid()
			return nil
		}
		if err != nil {
			return err
		}
		now := s.now()
		if rt.SessionRevokedAt != nil || !rt.SessionExpiresAt.After(now) {
			failErr = errRefreshInvalid()
			return nil
		}
		if rt.UsedAt != nil {
			if _, err := q.RevokeSession(ctx, store.RevokeSessionParams{ID: rt.SessionID, UserID: rt.UserID, Reason: strPtr("refresh_token_reuse")}); err != nil && !database.IsNoRows(err) {
				return err
			}
			failErr = apperr.New(apperr.CodeRefreshReused, http.StatusUnauthorized, "session revoked: refresh token reuse detected")
			return recordAudit(ctx, q, "auth.refresh_reuse", rt.UserID, map[string]any{"session_id": rt.SessionID.String()})
		}
		if !rt.ExpiresAt.After(now) {
			failErr = errRefreshInvalid()
			return nil
		}
		u, err := q.GetUserByID(ctx, rt.UserID)
		if database.IsNoRows(err) || (err == nil && u.DisabledAt != nil) {
			failErr = errRefreshInvalid()
			return nil
		}
		if err != nil {
			return err
		}
		if err := q.MarkRefreshTokenUsed(ctx, rt.ID); err != nil {
			return err
		}
		refresh, refreshExp, err := s.issueRefresh(ctx, q, rt.SessionID, &rt.ID, rt.SessionExpiresAt)
		if err != nil {
			return err
		}
		if err := q.TouchSession(ctx, store.TouchSessionParams{ID: rt.SessionID, Ip: audit.RequestIP(ctx), UserAgent: strPtr(audit.RequestUserAgent(ctx))}); err != nil {
			return err
		}
		access, accessExp, err := s.jwt.Issue(u.ID, rt.SessionID)
		if err != nil {
			return err
		}
		sess = &SessionTokens{
			SessionID: rt.SessionID, AccessToken: access, AccessExpiresAt: accessExp,
			RefreshToken: refresh, RefreshExpiresAt: refreshExp, User: toUser(u),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if failErr != nil {
		return nil, failErr
	}
	return sess, nil
}

// Logout revokes the session owning refreshToken. Unknown tokens are ignored.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		rt, err := q.GetRefreshTokenForUpdate(ctx, crypto.HashToken(refreshToken))
		if database.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := q.RevokeSession(ctx, store.RevokeSessionParams{ID: rt.SessionID, UserID: rt.UserID, Reason: strPtr("logout")}); err != nil && !database.IsNoRows(err) {
			return err
		}
		return recordAudit(ctx, q, "auth.logout", rt.UserID, map[string]any{"session_id": rt.SessionID.String()})
	})
}
