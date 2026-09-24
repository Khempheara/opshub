package project

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// Principal kinds in project access grants.
const (
	PrincipalUser = "user"
	PrincipalTeam = "team"
)

// Access sources shown in the member list.
const (
	SourceDirect       = "direct"
	SourceTeam         = "team"
	SourceOrganization = "organization"
)

// Member is one entry of a project's access list: a direct user grant, a team grant, or an
// organization Owner/Admin who inherits access (read-only here).
type Member struct {
	Principal   string     `json:"principal"` // "user:<id>" or "team:<id>"
	Type        string     `json:"type"`      // user | team
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	Email       string     `json:"email,omitempty"`
	MemberCount *int64     `json:"member_count,omitempty"` // teams only
	Role        authz.Role `json:"role"`
	Source      string     `json:"source"`
	GrantedAt   *time.Time `json:"granted_at,omitempty"`
}

// ParsePrincipal parses "user:<uuid>" or "team:<uuid>".
func ParsePrincipal(s string) (kind string, id uuid.UUID, ok bool) {
	kind, rest, found := strings.Cut(s, ":")
	if !found || (kind != PrincipalUser && kind != PrincipalTeam) {
		return "", uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	return kind, id, err == nil
}

func errGrantNotFound() *apperr.Error {
	return apperr.New(apperr.CodeMemberNotFound, http.StatusNotFound, "no such project member")
}

// ListMembers returns direct grants, team grants and the organization's Owners and Admins.
// Organization Viewers (read-only on every project) are not listed individually.
func (s *Service) ListMembers(ctx context.Context, projectID uuid.UUID) ([]Member, error) {
	q := store.New(s.pool)
	a, err := load(ctx, q, projectID, authz.ProjectView)
	if err != nil {
		return nil, err
	}
	managers, err := q.ListOrgManagers(ctx, a.project.OrganizationID)
	if err != nil {
		return nil, err
	}
	users, err := q.ListProjectUserGrants(ctx, projectID)
	if err != nil {
		return nil, err
	}
	teams, err := q.ListProjectTeamGrants(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Member, 0, len(managers)+len(users)+len(teams))
	for _, m := range managers {
		out = append(out, Member{
			Principal: PrincipalUser + ":" + m.ID.String(), Type: PrincipalUser, ID: m.ID, Name: m.DisplayName,
			Email: m.Email, Role: m.Role, Source: SourceOrganization,
		})
	}
	for _, u := range users {
		at := u.CreatedAt
		out = append(out, Member{
			Principal: PrincipalUser + ":" + u.ID.String(), Type: PrincipalUser, ID: u.ID, Name: u.DisplayName,
			Email: u.Email, Role: u.Role, Source: SourceDirect, GrantedAt: &at,
		})
	}
	for _, t := range teams {
		at, n := t.CreatedAt, t.MemberCount
		out = append(out, Member{
			Principal: PrincipalTeam + ":" + t.ID.String(), Type: PrincipalTeam, ID: t.ID, Name: t.Name,
			MemberCount: &n, Role: t.Role, Source: SourceTeam, GrantedAt: &at,
		})
	}
	return out, nil
}

// GrantInput is PUT /projects/{id}/members/{principal}.
type GrantInput struct {
	Role authz.Role `json:"role" validate:"required"`
}

// Grant gives a user (who must be an organization member) or a team of the same
// organization a role on the project, replacing any previous grant. Owner can't be granted.
func (s *Service) Grant(ctx context.Context, projectID uuid.UUID, principal string, role authz.Role) error {
	kind, id, ok := ParsePrincipal(principal)
	if !ok {
		return errGrantNotFound()
	}
	if !authz.GrantableProjectRole(role) {
		return apperr.New(apperr.CodeRoleNotAllowed, http.StatusUnprocessableEntity, "this role can't be granted on a project").
			WithDetails(map[string]any{"role": string(role)})
	}
	return s.inTx(ctx, func(q *store.Queries) error {
		a, err := load(ctx, q, projectID, authz.ProjectManageMembers)
		if err != nil {
			return err
		}
		orgID := a.project.OrganizationID
		var before any
		switch kind {
		case PrincipalUser:
			if _, err := q.GetMembership(ctx, store.GetMembershipParams{OrganizationID: orgID, UserID: id}); err != nil {
				if database.IsNoRows(err) {
					return apperr.New(apperr.CodeMemberNotFound, http.StatusNotFound, "not a member of this organization")
				}
				return err
			}
			if prev, err := q.GetProjectUserGrant(ctx, store.GetProjectUserGrantParams{ProjectID: projectID, UserID: id}); err == nil {
				before = map[string]any{"role": prev}
			}
			err = q.UpsertProjectUserGrant(ctx, store.UpsertProjectUserGrantParams{ProjectID: projectID, UserID: id, Role: role})
		case PrincipalTeam:
			if _, err := q.GetTeamInOrg(ctx, store.GetTeamInOrgParams{ID: id, OrganizationID: orgID}); err != nil {
				if database.IsNoRows(err) {
					return apperr.New(apperr.CodeTeamNotFound, http.StatusNotFound, "team not found")
				}
				return err
			}
			if prev, err := q.GetProjectTeamGrant(ctx, store.GetProjectTeamGrantParams{ProjectID: projectID, TeamID: id}); err == nil {
				before = map[string]any{"role": prev}
			}
			err = q.UpsertProjectTeamGrant(ctx, store.UpsertProjectTeamGrantParams{ProjectID: projectID, TeamID: id, Role: role})
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, ProjectID: &projectID, Action: "project.grant", ResourceType: kind,
			ResourceID: id.String(), Before: before, After: map[string]any{"role": role},
		})
	})
}

// Revoke removes a direct user grant or a team grant. Inherited organization roles can't be
// revoked per project.
func (s *Service) Revoke(ctx context.Context, projectID uuid.UUID, principal string) error {
	kind, id, ok := ParsePrincipal(principal)
	if !ok {
		return errGrantNotFound()
	}
	return s.inTx(ctx, func(q *store.Queries) error {
		a, err := load(ctx, q, projectID, authz.ProjectManageMembers)
		if err != nil {
			return err
		}
		var n int64
		if kind == PrincipalUser {
			n, err = q.DeleteProjectUserGrant(ctx, store.DeleteProjectUserGrantParams{ProjectID: projectID, UserID: id})
		} else {
			n, err = q.DeleteProjectTeamGrant(ctx, store.DeleteProjectTeamGrantParams{ProjectID: projectID, TeamID: id})
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return errGrantNotFound()
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.project.OrganizationID, ProjectID: &projectID, Action: "project.revoke",
			ResourceType: kind, ResourceID: id.String(),
		})
	})
}
