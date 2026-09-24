package org

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/mail"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

// InvitationTTL is how long an emailed invitation link stays valid.
const InvitationTTL = 7 * 24 * time.Hour

// Invitation is an open invitation as seen by organization admins (never the token).
type Invitation struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	InvitedByName *string   `json:"invited_by_name"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// InviteInput is POST /orgs/{id}/invitations.
type InviteInput struct {
	Email string `json:"email" validate:"required,email,max=254"`
	Role  string `json:"role" validate:"required,oneof=owner admin developer viewer"`
}

func errInvitationNotFound() *apperr.Error {
	return apperr.New(apperr.CodeInvitationNotFound, http.StatusNotFound, "invitation not found")
}

// Invite emails a single-use link that adds the recipient with the given role. Inviting
// an address again replaces its open invitation (and link).
func (s *Service) Invite(ctx context.Context, orgID uuid.UUID, in InviteInput) (Invitation, error) {
	role := authz.Role(in.Role)
	email := strings.ToLower(strings.TrimSpace(in.Email))
	var out Invitation
	err := s.inTx(ctx, func(q *store.Queries, tx pgx.Tx) error {
		m, err := authz.Require(ctx, q, orgID, authz.MemberInvite)
		if err != nil {
			return err
		}
		if !authz.AtLeast(m.MaxAssignableRole(), role) {
			return errRoleNotAllowed()
		}
		inviter, err := q.GetUserByID(ctx, m.UserID)
		if err != nil {
			return err
		}
		org, err := q.GetOrganizationForMember(ctx, store.GetOrganizationForMemberParams{ID: orgID, UserID: m.UserID})
		if err != nil {
			return err
		}
		// Email the invitee in their own language if they already have an account,
		// otherwise in the language the inviter is using.
		locale := i18n.LocaleFrom(ctx)
		if existing, err := q.GetUserByEmail(ctx, email); err == nil {
			locale = existing.Locale
			if _, err := q.GetMember(ctx, store.GetMemberParams{OrganizationID: orgID, UserID: existing.ID}); err == nil {
				return apperr.New(apperr.CodeAlreadyMember, http.StatusConflict, "this person is already a member")
			} else if !database.IsNoRows(err) {
				return err
			}
		} else if !database.IsNoRows(err) {
			return err
		}
		if err := q.RevokeOpenInvitationForEmail(ctx, store.RevokeOpenInvitationForEmailParams{OrganizationID: orgID, Email: email}); err != nil {
			return err
		}
		token := crypto.RandomToken(32)
		inv, err := q.CreateInvitation(ctx, store.CreateInvitationParams{
			OrganizationID: orgID, Email: email, Role: role, TokenHash: crypto.HashToken(token),
			InvitedBy: &m.UserID, ExpiresAt: s.now().Add(InvitationTTL),
		})
		if err != nil {
			return err
		}
		link := strings.TrimRight(s.cfg.PublicURL, "/") + "/invitations/accept#token=" + url.QueryEscape(token)
		if _, err := s.jobs.InsertTx(ctx, tx, jobs.SendEmailArgs{
			To: email, Template: mail.TemplateInvitation, Locale: locale,
			Data: map[string]any{"Org": org.Name, "Inviter": inviter.DisplayName, "Role": roleLabel(role), "URL": link},
		}, nil); err != nil {
			return err
		}
		name := inviter.DisplayName
		out = Invitation{ID: inv.ID, Email: inv.Email, Role: string(inv.Role), InvitedByName: &name, ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: string(authz.MemberInvite), ResourceType: "invitation", ResourceID: inv.ID.String(),
			After: map[string]any{"email": email, "role": string(role)},
		})
	})
	return out, err
}

// roleLabel is the role as shown in emails (role names stay English in both languages,
// see the glossary in docs/i18n.md).
func roleLabel(r authz.Role) string {
	s := string(r)
	return strings.ToUpper(s[:1]) + s[1:]
}

// ListInvitations lists open invitations, newest first (Admin+).
func (s *Service) ListInvitations(ctx context.Context, orgID uuid.UUID, page pagination.Params) (pagination.Page[Invitation], error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.MemberInvite); err != nil {
		return pagination.Page[Invitation]{}, err
	}
	params := store.ListOpenInvitationsParams{OrganizationID: orgID, PageSize: page.FetchSize()}
	var cur timeIDCursor
	if has, err := page.Decode(&cur); err != nil {
		return pagination.Page[Invitation]{}, err
	} else if has {
		params.CursorCreatedAt, params.CursorID = &cur.CreatedAt, &cur.ID
	}
	rows, err := q.ListOpenInvitations(ctx, params)
	if err != nil {
		return pagination.Page[Invitation]{}, err
	}
	return pagination.Build(rows, page.Limit, func(r store.ListOpenInvitationsRow) Invitation {
		return Invitation{ID: r.ID, Email: r.Email, Role: string(r.Role), InvitedByName: r.InvitedByName, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt}
	}, func(r store.ListOpenInvitationsRow) any { return timeIDCursor{CreatedAt: r.CreatedAt, ID: r.ID} }), nil
}

type timeIDCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

// RevokeInvitation cancels an open invitation. Outsiders get INVITATION_NOT_FOUND.
func (s *Service) RevokeInvitation(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		inv, err := q.GetInvitation(ctx, id)
		if database.IsNoRows(err) {
			return errInvitationNotFound()
		}
		if err != nil {
			return err
		}
		if _, err := authz.Require(ctx, q, inv.OrganizationID, authz.MemberInvite); err != nil {
			if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeOrgNotFound {
				return errInvitationNotFound()
			}
			return err
		}
		if inv.AcceptedAt != nil || inv.RevokedAt != nil {
			return errInvitationNotFound()
		}
		if err := q.RevokeInvitation(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &inv.OrganizationID, Action: "member.invite_revoke", ResourceType: "invitation", ResourceID: id.String(),
			Before: map[string]any{"email": inv.Email, "role": string(inv.Role)},
		})
	})
}

// InvitationPreview is what the invitee sees before accepting.
type InvitationPreview struct {
	OrganizationName string    `json:"organization_name"`
	OrganizationSlug string    `json:"organization_slug"`
	Role             string    `json:"role"`
	Email            string    `json:"email"`
	InvitedByName    *string   `json:"invited_by_name"`
	ExpiresAt        time.Time `json:"expires_at"`
}

func errInvalidInvitation() *apperr.Error {
	return apperr.New(apperr.CodeInvalidToken, http.StatusBadRequest, "the invitation is invalid or has expired")
}

// loadOpenInvitation returns the invitation for a token if it can still be accepted.
func (s *Service) loadOpenInvitation(ctx context.Context, q *store.Queries, token string) (store.GetInvitationByTokenForUpdateRow, error) {
	inv, err := q.GetInvitationByTokenForUpdate(ctx, crypto.HashToken(token))
	if database.IsNoRows(err) {
		return inv, errInvalidInvitation()
	}
	if err != nil {
		return inv, err
	}
	if inv.AcceptedAt != nil || inv.RevokedAt != nil || !inv.ExpiresAt.After(s.now()) {
		return inv, errInvalidInvitation()
	}
	return inv, nil
}

// PreviewInvitation shows an open invitation to a signed-in user holding its token.
func (s *Service) PreviewInvitation(ctx context.Context, token string) (InvitationPreview, error) {
	if _, ok := authn.PrincipalFrom(ctx); !ok {
		return InvitationPreview{}, apperr.Unauthenticated()
	}
	var out InvitationPreview
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		inv, err := s.loadOpenInvitation(ctx, q, token)
		if err != nil {
			return err
		}
		out = InvitationPreview{
			OrganizationName: inv.OrganizationName, OrganizationSlug: inv.OrganizationSlug, Role: string(inv.Role),
			Email: inv.Email, InvitedByName: inv.InvitedByName, ExpiresAt: inv.ExpiresAt,
		}
		return nil
	})
	return out, err
}

// AcceptInvitation adds the caller to the organization. The caller's (verified) email
// must be the invited address, so a forwarded link can't be used by someone else.
func (s *Service) AcceptInvitation(ctx context.Context, token string) (Organization, error) {
	p, ok := authn.PrincipalFrom(ctx)
	if !ok {
		return Organization{}, apperr.Unauthenticated()
	}
	var out Organization
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		inv, err := s.loadOpenInvitation(ctx, q, token)
		if err != nil {
			return err
		}
		u, err := q.GetUserByID(ctx, p.UserID)
		if err != nil {
			return err
		}
		if !strings.EqualFold(u.Email, inv.Email) || u.EmailVerifiedAt == nil {
			return apperr.New(apperr.CodeInvitationEmailMismatch, http.StatusForbidden, "this invitation was sent to a different email address").
				WithDetails(map[string]any{"invited_email": inv.Email})
		}
		role := inv.Role
		existing, err := q.GetMember(ctx, store.GetMemberParams{OrganizationID: inv.OrganizationID, UserID: u.ID})
		switch {
		case err == nil:
			role = existing.Role // already a member: keep the current role
		case database.IsNoRows(err):
			if err := q.AddOrganizationMember(ctx, store.AddOrganizationMemberParams{OrganizationID: inv.OrganizationID, UserID: u.ID, Role: inv.Role}); err != nil {
				return err
			}
		default:
			return err
		}
		if err := q.MarkInvitationAccepted(ctx, store.MarkInvitationAcceptedParams{ID: inv.ID, AcceptedBy: &u.ID}); err != nil {
			return err
		}
		out = Organization{ID: inv.OrganizationID, Slug: inv.OrganizationSlug, Name: inv.OrganizationName, Role: string(role)}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &inv.OrganizationID, Action: "member.join", ResourceType: "member", ResourceID: u.ID.String(),
			After: map[string]any{"role": string(role)}, Metadata: map[string]any{"invitation_id": inv.ID.String()},
		})
	})
	return out, err
}
