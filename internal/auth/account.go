package auth

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/mail"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

// Me returns the caller's profile.
func (s *Service) Me(ctx context.Context) (User, error) {
	p, err := principal(ctx)
	if err != nil {
		return User{}, err
	}
	u, err := store.New(s.pool).GetUserByID(ctx, p.UserID)
	if database.IsNoRows(err) {
		return User{}, apperr.Unauthenticated()
	}
	if err != nil {
		return User{}, err
	}
	return toUser(u), nil
}

// UpdateProfileInput is PATCH /me. Omitted fields keep their current values.
type UpdateProfileInput struct {
	DisplayName   *string `json:"display_name" validate:"omitempty,min=1,max=100"`
	Locale        *string `json:"locale" validate:"omitempty,oneof=en km"`
	Timezone      *string `json:"timezone" validate:"omitempty,timezone"`
	KhmerNumerals *bool   `json:"khmer_numerals"`
}

// UpdateProfile applies changes if expectedVersion still matches (optimistic locking).
func (s *Service) UpdateProfile(ctx context.Context, expectedVersion int32, in UpdateProfileInput) (User, error) {
	p, err := principal(ctx)
	if err != nil {
		return User{}, err
	}
	var out User
	err = s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		cur, err := q.GetUserByID(ctx, p.UserID)
		if err != nil {
			return err
		}
		params := store.UpdateUserProfileParams{
			ID: cur.ID, Version: expectedVersion, DisplayName: cur.DisplayName, Locale: cur.Locale,
			Timezone: cur.Timezone, KhmerNumerals: cur.KhmerNumerals,
		}
		if in.DisplayName != nil {
			params.DisplayName = *in.DisplayName
		}
		if in.Locale != nil {
			params.Locale = *in.Locale
		}
		if in.Timezone != nil {
			params.Timezone = *in.Timezone
		}
		if in.KhmerNumerals != nil {
			params.KhmerNumerals = *in.KhmerNumerals
		}
		updated, err := q.UpdateUserProfile(ctx, params)
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if err != nil {
			return err
		}
		out = toUser(updated)
		before := profileSnapshot(cur)
		return audit.Record(ctx, q, audit.Entry{
			Action: "user.update_profile", ResourceType: "user", ResourceID: cur.ID.String(),
			Before: before, After: profileSnapshot(updated),
		})
	})
	return out, err
}

func profileSnapshot(u store.User) map[string]any {
	return map[string]any{"display_name": u.DisplayName, "locale": u.Locale, "timezone": u.Timezone, "khmer_numerals": u.KhmerNumerals}
}

// ChangePassword sets a new password (the current one is required when set) and signs
// out every other session.
func (s *Service) ChangePassword(ctx context.Context, current, next string) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *store.Queries, tx pgx.Tx) error {
		u, err := q.GetUserByIDForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if err := s.verifyPassword(u, current); err != nil {
			return err
		}
		if perr := authn.CheckPasswordPolicy(next, u.Email); perr != nil {
			return perr
		}
		hash, err := s.hasher.Hash(next)
		if err != nil {
			return err
		}
		if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{ID: u.ID, PasswordHash: &hash}); err != nil {
			return err
		}
		if err := q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: u.ID, KeepID: &p.SessionID, Reason: strPtr("password_changed")}); err != nil {
			return err
		}
		if err := recordAudit(ctx, q, "auth.password_changed", u.ID, nil); err != nil {
			return err
		}
		return s.enqueueEmail(ctx, tx, u.Email, mail.TemplatePasswordChanged, u.Locale,
			map[string]any{"Name": u.DisplayName, "URL": s.link("/forgot-password", "")})
	})
}

// verifyPassword re-authenticates sensitive actions. Accounts without a password
// (SSO-only) pass: they have no password to confirm.
func (s *Service) verifyPassword(u store.User, password string) error {
	if u.PasswordHash == nil {
		return nil
	}
	ok, _, err := s.hasher.Verify(password, *u.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.New(apperr.CodePasswordIncorrect, http.StatusUnprocessableEntity, "current password is incorrect")
	}
	return nil
}

// SessionInfo describes one signed-in device.
type SessionInfo struct {
	ID         uuid.UUID `json:"id"`
	AuthMethod string    `json:"auth_method"`
	IP         *string   `json:"ip"`
	UserAgent  *string   `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

type timeIDCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func ipString(a *netip.Addr) *string {
	if a == nil {
		return nil
	}
	s := a.String()
	return &s
}

// ListSessions lists the caller's active sessions, newest first.
func (s *Service) ListSessions(ctx context.Context, page pagination.Params) (pagination.Page[SessionInfo], error) {
	p, err := principal(ctx)
	if err != nil {
		return pagination.Page[SessionInfo]{}, err
	}
	params := store.ListActiveSessionsParams{UserID: p.UserID, PageSize: page.FetchSize()}
	var cur timeIDCursor
	if ok, err := page.Decode(&cur); err != nil {
		return pagination.Page[SessionInfo]{}, err
	} else if ok {
		params.CursorCreatedAt, params.CursorID = &cur.CreatedAt, &cur.ID
	}
	rows, err := store.New(s.pool).ListActiveSessions(ctx, params)
	if err != nil {
		return pagination.Page[SessionInfo]{}, err
	}
	return pagination.Build(rows, page.Limit, func(r store.Session) SessionInfo {
		return SessionInfo{
			ID: r.ID, AuthMethod: r.AuthMethod, IP: ipString(r.Ip), UserAgent: r.UserAgent, CreatedAt: r.CreatedAt,
			LastUsedAt: r.LastUsedAt, ExpiresAt: r.ExpiresAt, Current: r.ID == p.SessionID,
		}
	}, func(r store.Session) any { return timeIDCursor{CreatedAt: r.CreatedAt, ID: r.ID} }), nil
}

// RevokeSession signs out one of the caller's sessions.
func (s *Service) RevokeSession(ctx context.Context, id uuid.UUID) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		_, err := q.RevokeSession(ctx, store.RevokeSessionParams{ID: id, UserID: p.UserID, Reason: strPtr("revoked_by_user")})
		if database.IsNoRows(err) {
			return apperr.New(apperr.CodeSessionNotFound, http.StatusNotFound, "session not found")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "session.revoke", ResourceType: "session", ResourceID: id.String()})
	})
}

// Identity is a linked SSO login.
type Identity struct {
	ID        uuid.UUID `json:"id"`
	Provider  string    `json:"provider"`
	Email     *string   `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// ListIdentities lists the caller's linked SSO accounts.
func (s *Service) ListIdentities(ctx context.Context) ([]Identity, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := store.New(s.pool).ListIdentities(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]Identity, 0, len(rows))
	for _, r := range rows {
		out = append(out, Identity{ID: r.ID, Provider: r.Provider, Email: r.Email, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// UnlinkIdentity removes a linked SSO account, unless it is the only way to sign in.
func (s *Service) UnlinkIdentity(ctx context.Context, id uuid.UUID) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		u, err := q.GetUserByIDForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		n, err := q.CountIdentities(ctx, u.ID)
		if err != nil {
			return err
		}
		removed, err := q.DeleteIdentity(ctx, store.DeleteIdentityParams{ID: id, UserID: u.ID})
		if database.IsNoRows(err) {
			return apperr.New(apperr.CodeIdentityNotFound, http.StatusNotFound, "linked account not found")
		}
		if err != nil {
			return err
		}
		if u.PasswordHash == nil && n <= 1 {
			return apperr.New(apperr.CodeLastLoginMethod, http.StatusConflict, "set a password before unlinking your only sign-in method")
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "identity.unlink", ResourceType: "user_identity", ResourceID: id.String(),
			Before: map[string]any{"provider": removed.Provider},
		})
	})
}
