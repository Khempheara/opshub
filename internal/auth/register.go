package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/mail"
	"github.com/opshub/opshub/internal/store"
)

// RegisterInput is a sign-up request.
type RegisterInput struct {
	Email       string `json:"email" validate:"required,email,max=254"`
	Password    string `json:"password" validate:"required"`
	DisplayName string `json:"display_name" validate:"required,min=1,max=100"`
	Locale      string `json:"locale" validate:"omitempty,oneof=en km"`
	Timezone    string `json:"timezone" validate:"omitempty,timezone"`
	// InvitationToken lets an invitee sign up when self-service sign-up is disabled.
	InvitationToken string `json:"invitation_token" validate:"omitempty,max=128"`
}

// invitedEmail reports whether token belongs to an open invitation sent to email. The
// invitee still has to verify the address before signing in, which proves they received it.
func (s *Service) invitedEmail(ctx context.Context, email, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	invited, err := store.New(s.pool).GetOpenInvitationEmailByToken(ctx, crypto.HashToken(token))
	if database.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return normalizeEmail(invited) == email, nil
}

// Register creates an account and emails a verification link. To avoid revealing which
// emails are registered, an existing address gets the same response and an "account
// exists" email instead.
func (s *Service) Register(ctx context.Context, in RegisterInput) error {
	email := normalizeEmail(in.Email)
	isBootstrap := s.cfg.BootstrapAdminEmail != "" && email == normalizeEmail(s.cfg.BootstrapAdminEmail)
	if !s.cfg.AllowSignup && !isBootstrap {
		invited, err := s.invitedEmail(ctx, email, in.InvitationToken)
		if err != nil {
			return err
		}
		if !invited {
			return apperr.New(apperr.CodeSignupDisabled, http.StatusForbidden, "self-service sign-up is disabled")
		}
	}
	if perr := authn.CheckPasswordPolicy(in.Password, email); perr != nil {
		return perr
	}
	locale := in.Locale
	if locale == "" {
		locale = i18n.LocaleFrom(ctx)
	}
	tz := in.Timezone
	if tz == "" {
		tz = s.cfg.DefaultTimezone
	}
	// Hash before the existence check so both paths take the same time.
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return apperr.Internal(err)
	}

	return s.inTx(ctx, func(q *store.Queries, tx pgx.Tx) error {
		existing, err := q.GetUserByEmail(ctx, email)
		if err == nil {
			return s.enqueueEmail(ctx, tx, existing.Email, mail.TemplateAccountExists, existing.Locale,
				map[string]any{"Name": existing.DisplayName, "URL": s.link("/login", "")})
		}
		if !database.IsNoRows(err) {
			return err
		}
		u, err := q.CreateUser(ctx, store.CreateUserParams{
			Email: email, DisplayName: in.DisplayName, PasswordHash: &hash,
			Locale: locale, Timezone: tz, IsPlatformAdmin: isBootstrap,
		})
		if database.IsUniqueViolation(err, "users_email_key") {
			return nil // concurrent registration of the same address: behave as "exists"
		}
		if err != nil {
			return err
		}
		if err := s.sendVerification(ctx, q, tx, u); err != nil {
			return err
		}
		return recordAudit(ctx, q, "user.register", u.ID, map[string]any{"platform_admin": isBootstrap})
	})
}

func (s *Service) sendVerification(ctx context.Context, q *store.Queries, tx pgx.Tx, u store.User) error {
	if err := q.InvalidateEmailTokens(ctx, store.InvalidateEmailTokensParams{UserID: u.ID, Purpose: store.EmailTokenPurposeVerifyEmail}); err != nil {
		return err
	}
	token := crypto.RandomToken(32)
	if err := q.CreateEmailToken(ctx, store.CreateEmailTokenParams{
		UserID: u.ID, Purpose: store.EmailTokenPurposeVerifyEmail, TokenHash: crypto.HashToken(token),
		ExpiresAt: s.now().Add(EmailVerifyTTL),
	}); err != nil {
		return err
	}
	return s.enqueueEmail(ctx, tx, u.Email, mail.TemplateVerifyEmail, u.Locale,
		map[string]any{"Name": u.DisplayName, "URL": s.link("/verify-email", token)})
}

// VerifyEmail confirms an address with a token from the verification email.
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		userID, err := q.ConsumeEmailToken(ctx, store.ConsumeEmailTokenParams{
			TokenHash: crypto.HashToken(token), Purpose: store.EmailTokenPurposeVerifyEmail,
		})
		if database.IsNoRows(err) {
			return errInvalidToken()
		}
		if err != nil {
			return err
		}
		if err := q.SetUserEmailVerified(ctx, userID); err != nil {
			return err
		}
		return recordAudit(ctx, q, "user.email_verified", userID, nil)
	})
}

// ResendVerification re-sends the verification email if the account exists and is
// unverified. It always succeeds (no account enumeration).
func (s *Service) ResendVerification(ctx context.Context, email string) error {
	return s.inTx(ctx, func(q *store.Queries, tx pgx.Tx) error {
		u, err := q.GetUserByEmail(ctx, normalizeEmail(email))
		if database.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if u.EmailVerifiedAt != nil || u.DisabledAt != nil {
			return nil
		}
		return s.sendVerification(ctx, q, tx, u)
	})
}

// ForgotPassword emails a single-use reset link. Always succeeds (no enumeration).
func (s *Service) ForgotPassword(ctx context.Context, email string) error {
	return s.inTx(ctx, func(q *store.Queries, tx pgx.Tx) error {
		u, err := q.GetUserByEmail(ctx, normalizeEmail(email))
		if database.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if u.DisabledAt != nil {
			return nil
		}
		if err := q.InvalidateEmailTokens(ctx, store.InvalidateEmailTokensParams{UserID: u.ID, Purpose: store.EmailTokenPurposeResetPassword}); err != nil {
			return err
		}
		token := crypto.RandomToken(32)
		if err := q.CreateEmailToken(ctx, store.CreateEmailTokenParams{
			UserID: u.ID, Purpose: store.EmailTokenPurposeResetPassword, TokenHash: crypto.HashToken(token),
			ExpiresAt: s.now().Add(PasswordResetTTL),
		}); err != nil {
			return err
		}
		if err := recordAudit(ctx, q, "auth.password_reset_requested", u.ID, nil); err != nil {
			return err
		}
		return s.enqueueEmail(ctx, tx, u.Email, mail.TemplateResetPassword, u.Locale,
			map[string]any{"Name": u.DisplayName, "URL": s.link("/reset-password", token)})
	})
}

// ResetPassword sets a new password with a reset token, signs out every session and
// clears any lockout. Completing a reset also proves ownership of the email address.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	return s.inTx(ctx, func(q *store.Queries, tx pgx.Tx) error {
		userID, err := q.ConsumeEmailToken(ctx, store.ConsumeEmailTokenParams{
			TokenHash: crypto.HashToken(token), Purpose: store.EmailTokenPurposeResetPassword,
		})
		if database.IsNoRows(err) {
			return errInvalidToken()
		}
		if err != nil {
			return err
		}
		u, err := q.GetUserByIDForUpdate(ctx, userID)
		if err != nil {
			return err
		}
		if perr := authn.CheckPasswordPolicy(newPassword, u.Email); perr != nil {
			return perr // rolls back: the token stays usable for a second attempt
		}
		hash, err := s.hasher.Hash(newPassword)
		if err != nil {
			return err
		}
		if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{ID: u.ID, PasswordHash: &hash}); err != nil {
			return err
		}
		if err := q.SetUserEmailVerified(ctx, u.ID); err != nil {
			return err
		}
		if err := q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: u.ID, Reason: strPtr("password_reset")}); err != nil {
			return err
		}
		if err := recordAudit(ctx, q, "auth.password_reset", u.ID, nil); err != nil {
			return err
		}
		return s.enqueueEmail(ctx, tx, u.Email, mail.TemplatePasswordChanged, u.Locale,
			map[string]any{"Name": u.DisplayName, "URL": s.link("/forgot-password", "")})
	})
}

// lockoutFor returns how long an account is locked at the given level (1-based).
func lockoutFor(level int32) time.Duration {
	d := LockoutBase
	for i := int32(1); i < level && d < LockoutMax; i++ {
		d *= 2
	}
	return min(d, LockoutMax)
}
