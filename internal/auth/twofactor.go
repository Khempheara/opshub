package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/mail"
	"github.com/opshub/opshub/internal/store"
)

// TOTPSetup is shown once while enrolling an authenticator app.
type TOTPSetup struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauth_uri"`
}

// SetupTOTP generates a pending secret (after re-authenticating with the password).
// 2FA is only turned on once EnableTOTP confirms a code from the app.
func (s *Service) SetupTOTP(ctx context.Context, password string) (TOTPSetup, error) {
	p, err := principal(ctx)
	if err != nil {
		return TOTPSetup{}, err
	}
	var out TOTPSetup
	err = s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		u, err := q.GetUserByIDForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if u.TotpEnabledAt != nil {
			return apperr.New(apperr.CodeMFAAlreadyEnabled, http.StatusConflict, "two-factor authentication is already enabled")
		}
		if err := s.verifyPassword(u, password); err != nil {
			return err
		}
		secret := authn.NewTOTPSecret()
		enc, err := s.keys.Encrypt([]byte(secret), totpAAD(u.ID))
		if err != nil {
			return err
		}
		if err := q.SetTOTPPending(ctx, store.SetTOTPPendingParams{ID: u.ID, TotpPendingEnc: enc}); err != nil {
			return err
		}
		out = TOTPSetup{Secret: secret, OTPAuthURI: authn.TOTPURI(secret, u.Email, totpIssuer)}
		return nil
	})
	return out, err
}

// EnableTOTP confirms the pending secret with a code and returns fresh recovery codes
// (shown once; only hashes are stored).
func (s *Service) EnableTOTP(ctx context.Context, code string) ([]string, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var codes []string
	err = s.inTx(ctx, func(q *store.Queries, tx pgx.Tx) error {
		u, err := q.GetUserByIDForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if u.TotpEnabledAt != nil {
			return apperr.New(apperr.CodeMFAAlreadyEnabled, http.StatusConflict, "two-factor authentication is already enabled")
		}
		if u.TotpPendingEnc == nil {
			return apperr.New(apperr.CodeMFASetupRequired, http.StatusConflict, "start two-factor setup first")
		}
		secret, err := s.keys.Decrypt(u.TotpPendingEnc, totpAAD(u.ID))
		if err != nil {
			return err
		}
		step, ok := authn.ValidateTOTP(string(secret), code, s.now())
		if !ok {
			return errMFAInvalid()
		}
		if err := q.EnableTOTP(ctx, store.EnableTOTPParams{ID: u.ID, TotpLastStep: &step}); err != nil {
			return err
		}
		if codes, err = s.replaceRecoveryCodes(ctx, q, u); err != nil {
			return err
		}
		if err := recordAudit(ctx, q, "auth.2fa_enabled", u.ID, nil); err != nil {
			return err
		}
		return s.notifyTwoFactor(ctx, tx, u, true)
	})
	return codes, err
}

// DisableTOTP turns 2FA off; requires the password and a current code (or recovery code).
func (s *Service) DisableTOTP(ctx context.Context, password, code, recoveryCode string) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *store.Queries, tx pgx.Tx) error {
		u, err := q.GetUserByIDForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if u.TotpEnabledAt == nil {
			return apperr.New(apperr.CodeMFANotEnabled, http.StatusConflict, "two-factor authentication is not enabled")
		}
		if err := s.verifyPassword(u, password); err != nil {
			return err
		}
		ok, err := s.checkSecondFactor(ctx, q, u, code, recoveryCode)
		if err != nil {
			return err
		}
		if !ok {
			return errMFAInvalid()
		}
		if err := q.DisableTOTP(ctx, u.ID); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, u.ID); err != nil {
			return err
		}
		if err := recordAudit(ctx, q, "auth.2fa_disabled", u.ID, nil); err != nil {
			return err
		}
		return s.notifyTwoFactor(ctx, tx, u, false)
	})
}

// RegenerateRecoveryCodes invalidates old recovery codes; requires a current TOTP code.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, code string) ([]string, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var codes []string
	err = s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		u, err := q.GetUserByIDForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if u.TotpEnabledAt == nil {
			return apperr.New(apperr.CodeMFANotEnabled, http.StatusConflict, "two-factor authentication is not enabled")
		}
		ok, err := s.checkSecondFactor(ctx, q, u, code, "")
		if err != nil {
			return err
		}
		if !ok {
			return errMFAInvalid()
		}
		if codes, err = s.replaceRecoveryCodes(ctx, q, u); err != nil {
			return err
		}
		return recordAudit(ctx, q, "auth.recovery_codes_regenerated", u.ID, nil)
	})
	return codes, err
}

// RecoveryCodesRemaining reports how many unused recovery codes the caller has.
func (s *Service) RecoveryCodesRemaining(ctx context.Context) (int64, error) {
	p, err := principal(ctx)
	if err != nil {
		return 0, err
	}
	return store.New(s.pool).CountUnusedRecoveryCodes(ctx, p.UserID)
}

func (s *Service) replaceRecoveryCodes(ctx context.Context, q *store.Queries, u store.User) ([]string, error) {
	if err := q.DeleteRecoveryCodes(ctx, u.ID); err != nil {
		return nil, err
	}
	codes := make([]string, RecoveryCodeCount)
	rows := make([]store.InsertRecoveryCodesParams, RecoveryCodeCount)
	for i := range codes {
		codes[i] = newRecoveryCode()
		rows[i] = store.InsertRecoveryCodesParams{UserID: u.ID, CodeHash: crypto.HashToken(normalizeRecoveryCode(codes[i]))}
	}
	if _, err := q.InsertRecoveryCodes(ctx, rows); err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Service) notifyTwoFactor(ctx context.Context, tx pgx.Tx, u store.User, enabled bool) error {
	tmpl := mail.TemplateTwoFactorDisabled
	if enabled {
		tmpl = mail.TemplateTwoFactorEnabled
	}
	return s.enqueueEmail(ctx, tx, u.Email, tmpl, u.Locale, map[string]any{"Name": u.DisplayName})
}

// newRecoveryCode returns a 10-character code formatted "xxxxx-xxxxx" (base32, ~50 bits).
func newRecoveryCode() string {
	raw := strings.ToLower(crypto.RandomBase32(10))
	return raw[:5] + "-" + raw[5:]
}

func normalizeRecoveryCode(c string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(c), "-", ""), " ", ""))
}
