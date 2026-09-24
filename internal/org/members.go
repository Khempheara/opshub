package org

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

// Member is one person in an organization.
type Member struct {
	UserID           uuid.UUID  `json:"user_id"`
	Email            string     `json:"email"`
	DisplayName      string     `json:"display_name"`
	TwoFactorEnabled bool       `json:"two_factor_enabled"`
	LastLoginAt      *time.Time `json:"last_login_at"`
	Role             string     `json:"role"`
	JoinedAt         time.Time  `json:"joined_at"`
}

// MemberFilter narrows ListMembers.
type MemberFilter struct {
	Role   string
	Search string
}

// ListMembers lists members by name (any member may view).
func (s *Service) ListMembers(ctx context.Context, orgID uuid.UUID, f MemberFilter, page pagination.Params) (pagination.Page[Member], error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.MemberView); err != nil {
		return pagination.Page[Member]{}, err
	}
	params := store.ListMembersParams{OrganizationID: orgID, PageSize: page.FetchSize()}
	if f.Role != "" {
		r := authz.Role(f.Role)
		if !authz.ValidRole(r) {
			return pagination.Page[Member]{}, apperr.Validation([]apperr.FieldError{{Field: "role", Rule: "oneof", Param: "owner admin developer viewer"}})
		}
		params.Role = &r
	}
	if search := strings.TrimSpace(f.Search); search != "" {
		// Escape LIKE wildcards so the search is literal.
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
		params.Search = &escaped
	}
	var cur nameIDCursor
	if has, err := page.Decode(&cur); err != nil {
		return pagination.Page[Member]{}, err
	} else if has {
		params.CursorName, params.CursorID = &cur.Name, &cur.ID
	}
	rows, err := q.ListMembers(ctx, params)
	if err != nil {
		return pagination.Page[Member]{}, err
	}
	return pagination.Build(rows, page.Limit, func(r store.ListMembersRow) Member {
		return Member{UserID: r.UserID, Email: r.Email, DisplayName: r.DisplayName, TwoFactorEnabled: r.TwoFactorEnabled,
			LastLoginAt: r.LastLoginAt, Role: string(r.Role), JoinedAt: r.JoinedAt}
	}, func(r store.ListMembersRow) any { return nameIDCursor{Name: r.DisplayName, ID: r.UserID} }), nil
}

func errMemberNotFound() *apperr.Error {
	return apperr.New(apperr.CodeMemberNotFound, http.StatusNotFound, "member not found")
}

func errLastOwner() *apperr.Error {
	return apperr.New(apperr.CodeLastOwner, http.StatusConflict, "an organization needs at least one owner")
}

func errRoleNotAllowed() *apperr.Error {
	return apperr.New(apperr.CodeRoleNotAllowed, http.StatusForbidden, "you can't assign this role")
}

// ensureOwnerRemains fails when removing/demoting target would leave no owner. The
// organization row is locked by the caller, so concurrent changes are serialized.
func ensureOwnerRemains(ctx context.Context, q *store.Queries, orgID uuid.UUID, target store.MemberRole) error {
	if target != authz.Owner {
		return nil
	}
	n, err := q.CountOwners(ctx, orgID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return errLastOwner()
	}
	return nil
}

// UpdateMemberRole changes a member's role. Owners may change anyone; Admins may change
// Developers and Viewers (and step down themselves), and never assign Admin or Owner.
func (s *Service) UpdateMemberRole(ctx context.Context, orgID, userID uuid.UUID, role string) (Member, error) {
	newRole := authz.Role(role)
	if !authz.ValidRole(newRole) {
		return Member{}, apperr.Validation([]apperr.FieldError{{Field: "role", Rule: "oneof", Param: "owner admin developer viewer"}})
	}
	var out Member
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		if _, err := q.LockOrganization(ctx, orgID); database.IsNoRows(err) {
			return authz.ErrOrgNotFound()
		} else if err != nil {
			return err
		}
		m, err := authz.Require(ctx, q, orgID, authz.MemberUpdateRole)
		if err != nil {
			return err
		}
		target, err := q.GetMember(ctx, store.GetMemberParams{OrganizationID: orgID, UserID: userID})
		if database.IsNoRows(err) {
			return errMemberNotFound()
		}
		if err != nil {
			return err
		}
		if !m.CanManageMember(target.Role, target.UserID) {
			return apperr.Forbidden()
		}
		if !authz.AtLeast(m.MaxAssignableRole(), newRole) {
			return errRoleNotAllowed()
		}
		if target.Role == newRole {
			out = toMember(target)
			return nil
		}
		if newRole != authz.Owner {
			if err := ensureOwnerRemains(ctx, q, orgID, target.Role); err != nil {
				return err
			}
		}
		if err := q.UpdateMemberRole(ctx, store.UpdateMemberRoleParams{OrganizationID: orgID, UserID: userID, Role: newRole}); err != nil {
			return err
		}
		oldRole := target.Role
		target.Role = newRole
		out = toMember(target)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: string(authz.MemberUpdateRole), ResourceType: "member", ResourceID: userID.String(),
			Before: map[string]any{"role": string(oldRole)}, After: map[string]any{"role": string(newRole)},
			Metadata: map[string]any{"email": target.Email},
		})
	})
	return out, err
}

func toMember(r store.GetMemberRow) Member {
	return Member{UserID: r.UserID, Email: r.Email, DisplayName: r.DisplayName, TwoFactorEnabled: r.TwoFactorEnabled,
		LastLoginAt: r.LastLoginAt, Role: string(r.Role), JoinedAt: r.JoinedAt}
}

// RemoveMember removes someone from the organization and its teams. Any member may leave
// (remove themselves); removing others needs member.remove and the manage rules above.
func (s *Service) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		if _, err := q.LockOrganization(ctx, orgID); database.IsNoRows(err) {
			return authz.ErrOrgNotFound()
		} else if err != nil {
			return err
		}
		m, err := authz.Resolve(ctx, q, orgID)
		if err != nil {
			return err
		}
		self := userID == m.UserID
		if !self && !m.Can(authz.MemberRemove) {
			return apperr.Forbidden()
		}
		target, err := q.GetMember(ctx, store.GetMemberParams{OrganizationID: orgID, UserID: userID})
		if database.IsNoRows(err) {
			return errMemberNotFound()
		}
		if err != nil {
			return err
		}
		if !self && !m.CanManageMember(target.Role, target.UserID) {
			return apperr.Forbidden()
		}
		if err := ensureOwnerRemains(ctx, q, orgID, target.Role); err != nil {
			return err
		}
		if err := q.RemoveUserFromOrgTeams(ctx, store.RemoveUserFromOrgTeamsParams{OrganizationID: orgID, UserID: userID}); err != nil {
			return err
		}
		if err := q.RemoveMember(ctx, store.RemoveMemberParams{OrganizationID: orgID, UserID: userID}); err != nil {
			return err
		}
		action := string(authz.MemberRemove)
		if self {
			action = "member.leave"
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: action, ResourceType: "member", ResourceID: userID.String(),
			Before: map[string]any{"role": string(target.Role)}, Metadata: map[string]any{"email": target.Email},
		})
	})
}

// TransferOwnership makes another member an Owner and steps the caller down to Admin.
func (s *Service) TransferOwnership(ctx context.Context, orgID, toUserID uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		if _, err := q.LockOrganization(ctx, orgID); database.IsNoRows(err) {
			return authz.ErrOrgNotFound()
		} else if err != nil {
			return err
		}
		m, err := authz.Require(ctx, q, orgID, authz.OrgTransfer)
		if err != nil {
			return err
		}
		if toUserID == m.UserID {
			return apperr.BadRequest("choose another member")
		}
		target, err := q.GetMember(ctx, store.GetMemberParams{OrganizationID: orgID, UserID: toUserID})
		if database.IsNoRows(err) {
			return errMemberNotFound()
		}
		if err != nil {
			return err
		}
		if err := q.UpdateMemberRole(ctx, store.UpdateMemberRoleParams{OrganizationID: orgID, UserID: toUserID, Role: authz.Owner}); err != nil {
			return err
		}
		if err := q.UpdateMemberRole(ctx, store.UpdateMemberRoleParams{OrganizationID: orgID, UserID: m.UserID, Role: authz.Admin}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: string(authz.OrgTransfer), ResourceType: "organization", ResourceID: orgID.String(),
			Before: map[string]any{"new_owner_role": string(target.Role)},
			After:  map[string]any{"new_owner": target.Email, "previous_owner_role": string(authz.Admin)},
		})
	})
}
