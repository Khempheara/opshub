package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/auth/sso"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/store"
)

// LoginWithIdentity signs in with an identity asserted by an SSO provider:
//  1. a linked identity signs in its user;
//  2. otherwise a verified email matching an existing account links to it;
//  3. otherwise a new account is created (if sign-up is allowed).
//
// Unverified provider emails are never used to link or create accounts. Users with 2FA
// still need their second factor.
func (s *Service) LoginWithIdentity(ctx context.Context, id sso.Identity) (LoginResult, error) {
	var (
		result  LoginResult
		failErr error
	)
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		u, err := q.GetUserByIdentity(ctx, store.GetUserByIdentityParams{Provider: id.Provider, Subject: id.Subject})
		switch {
		case err == nil:
		case !database.IsNoRows(err):
			return err
		case !id.EmailVerified || id.Email == "":
			failErr = apperr.New(apperr.CodeSSOEmailUnverified, http.StatusForbidden, "the provider did not return a verified email address")
			return nil
		default:
			email := normalizeEmail(id.Email)
			existing, err := q.GetUserByEmail(ctx, email)
			switch {
			case err == nil:
				u = existing
			case database.IsNoRows(err):
				isBootstrap := s.cfg.BootstrapAdminEmail != "" && email == normalizeEmail(s.cfg.BootstrapAdminEmail)
				if !s.cfg.AllowSignup && !isBootstrap {
					failErr = apperr.New(apperr.CodeSignupDisabled, http.StatusForbidden, "self-service sign-up is disabled")
					return nil
				}
				name := strings.TrimSpace(id.Name)
				if name == "" {
					name, _, _ = strings.Cut(email, "@")
				}
				if len([]rune(name)) > 100 {
					name = string([]rune(name)[:100])
				}
				now := s.now()
				if u, err = q.CreateUser(ctx, store.CreateUserParams{
					Email: email, DisplayName: name, Locale: i18n.LocaleFrom(ctx), Timezone: s.cfg.DefaultTimezone,
					IsPlatformAdmin: isBootstrap, EmailVerifiedAt: &now,
				}); err != nil {
					return err
				}
				if err := recordAudit(ctx, q, "user.register", u.ID, map[string]any{"via": id.Provider, "platform_admin": isBootstrap}); err != nil {
					return err
				}
			default:
				return err
			}
			if _, err := q.CreateIdentity(ctx, store.CreateIdentityParams{
				UserID: u.ID, Provider: id.Provider, Subject: id.Subject, Email: strPtr(email),
			}); err != nil {
				return err
			}
			if err := q.SetUserEmailVerified(ctx, u.ID); err != nil {
				return err
			}
			if err := audit.Record(ctx, q, audit.Entry{
				Action: "identity.link", ResourceType: "user", ResourceID: u.ID.String(), ActorUserID: &u.ID,
				After: map[string]any{"provider": id.Provider},
			}); err != nil {
				return err
			}
		}
		if u.DisabledAt != nil {
			failErr = errAccountDisabled()
			return nil
		}
		if u, err = q.GetUserByIDForUpdate(ctx, u.ID); err != nil {
			return err
		}
		result, err = s.completeFirstFactor(ctx, q, u, methodOIDC)
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
