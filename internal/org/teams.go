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

// Team groups members (used for project access from Module 3).
type Team struct {
	ID          uuid.UUID `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	MemberCount int64     `json:"member_count"`
	Version     int32     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
}

// TeamMember is a member of a team.
type TeamMember struct {
	UserID      uuid.UUID `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	AddedAt     time.Time `json:"added_at"`
}

// TeamInput creates or updates a team.
type TeamInput struct {
	Name        string `json:"name" validate:"required,min=1,max=100"`
	Slug        string `json:"slug" validate:"omitempty,max=40"`
	Description string `json:"description" validate:"max=500"`
}

func errTeamNotFound() *apperr.Error {
	return apperr.New(apperr.CodeTeamNotFound, http.StatusNotFound, "team not found")
}

func toTeam(t store.Team, members int64) Team {
	return Team{ID: t.ID, Slug: t.Slug, Name: t.Name, Description: t.Description, MemberCount: members, Version: t.Version, CreatedAt: t.CreatedAt}
}

// loadTeam resolves a team and authorizes the action on its organization. Teams in other
// organizations are reported as TEAM_NOT_FOUND.
func loadTeam(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.Team, authz.Membership, error) {
	t, err := q.GetTeam(ctx, id)
	if database.IsNoRows(err) {
		return t, authz.Membership{}, errTeamNotFound()
	}
	if err != nil {
		return t, authz.Membership{}, err
	}
	m, err := authz.Require(ctx, q, t.OrganizationID, a)
	if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeOrgNotFound {
		return t, m, errTeamNotFound()
	}
	return t, m, err
}

// ListTeams lists teams by name (any member).
func (s *Service) ListTeams(ctx context.Context, orgID uuid.UUID, page pagination.Params) (pagination.Page[Team], error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.TeamView); err != nil {
		return pagination.Page[Team]{}, err
	}
	params := store.ListTeamsParams{OrganizationID: orgID, PageSize: page.FetchSize()}
	var cur nameIDCursor
	if has, err := page.Decode(&cur); err != nil {
		return pagination.Page[Team]{}, err
	} else if has {
		params.CursorName, params.CursorID = &cur.Name, &cur.ID
	}
	rows, err := q.ListTeams(ctx, params)
	if err != nil {
		return pagination.Page[Team]{}, err
	}
	return pagination.Build(rows, page.Limit, func(r store.ListTeamsRow) Team {
		return Team{ID: r.ID, Slug: r.Slug, Name: r.Name, Description: r.Description, MemberCount: r.MemberCount, Version: r.Version, CreatedAt: r.CreatedAt}
	}, func(r store.ListTeamsRow) any { return nameIDCursor{Name: r.Name, ID: r.ID} }), nil
}

// CreateTeam creates a team (Admin+).
func (s *Service) CreateTeam(ctx context.Context, orgID uuid.UUID, in TeamInput) (Team, error) {
	name := strings.TrimSpace(in.Name)
	slug := in.Slug
	if slug == "" {
		slug = Slugify(name)
	}
	if !slugPattern.MatchString(slug) {
		return Team{}, apperr.Validation([]apperr.FieldError{{Field: "slug", Rule: "slug"}})
	}
	var out Team
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		if _, err := authz.Require(ctx, q, orgID, authz.TeamManage); err != nil {
			return err
		}
		t, err := q.CreateTeam(ctx, store.CreateTeamParams{OrganizationID: orgID, Slug: slug, Name: name, Description: strings.TrimSpace(in.Description)})
		if database.IsUniqueViolation(err, "teams_org_slug_key") {
			return errSlugTaken(slug)
		}
		if err != nil {
			return err
		}
		out = toTeam(t, 0)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: "team.create", ResourceType: "team", ResourceID: t.ID.String(),
			After: map[string]any{"name": t.Name, "slug": t.Slug},
		})
	})
	return out, err
}

// GetTeam returns a team with its members (any member of the organization).
func (s *Service) GetTeam(ctx context.Context, id uuid.UUID) (Team, []TeamMember, error) {
	q := store.New(s.pool)
	t, _, err := loadTeam(ctx, q, id, authz.TeamView)
	if err != nil {
		return Team{}, nil, err
	}
	rows, err := q.ListTeamMembers(ctx, id)
	if err != nil {
		return Team{}, nil, err
	}
	members := make([]TeamMember, 0, len(rows))
	for _, r := range rows {
		members = append(members, TeamMember{UserID: r.UserID, Email: r.Email, DisplayName: r.DisplayName, AddedAt: r.AddedAt})
	}
	return toTeam(t, int64(len(members))), members, nil
}

// UpdateTeam renames or re-describes a team (Admin+) with optimistic locking.
func (s *Service) UpdateTeam(ctx context.Context, id uuid.UUID, version int32, in TeamInput) (Team, error) {
	var out Team
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		before, _, err := loadTeam(ctx, q, id, authz.TeamManage)
		if err != nil {
			return err
		}
		t, err := q.UpdateTeam(ctx, store.UpdateTeamParams{ID: id, Version: version, Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description)})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if err != nil {
			return err
		}
		out = toTeam(t, 0)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &t.OrganizationID, Action: "team.update", ResourceType: "team", ResourceID: id.String(),
			Before: map[string]any{"name": before.Name, "description": before.Description},
			After:  map[string]any{"name": t.Name, "description": t.Description},
		})
	})
	return out, err
}

// DeleteTeam soft-deletes a team (Admin+).
func (s *Service) DeleteTeam(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		t, _, err := loadTeam(ctx, q, id, authz.TeamManage)
		if err != nil {
			return err
		}
		if err := q.SoftDeleteTeam(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &t.OrganizationID, Action: "team.delete", ResourceType: "team", ResourceID: id.String(),
			Before: map[string]any{"name": t.Name, "slug": t.Slug},
		})
	})
}

// AddTeamMember adds an organization member to a team (Admin+).
func (s *Service) AddTeamMember(ctx context.Context, teamID, userID uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		t, _, err := loadTeam(ctx, q, teamID, authz.TeamManage)
		if err != nil {
			return err
		}
		member, err := q.GetMember(ctx, store.GetMemberParams{OrganizationID: t.OrganizationID, UserID: userID})
		if database.IsNoRows(err) {
			return errMemberNotFound() // only people in the organization can join its teams
		}
		if err != nil {
			return err
		}
		if err := q.AddTeamMember(ctx, store.AddTeamMemberParams{TeamID: teamID, UserID: userID}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &t.OrganizationID, Action: "team.add_member", ResourceType: "team", ResourceID: teamID.String(),
			After: map[string]any{"user_id": userID.String(), "email": member.Email},
		})
	})
}

// RemoveTeamMember removes someone from a team (Admin+).
func (s *Service) RemoveTeamMember(ctx context.Context, teamID, userID uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		t, _, err := loadTeam(ctx, q, teamID, authz.TeamManage)
		if err != nil {
			return err
		}
		n, err := q.RemoveTeamMember(ctx, store.RemoveTeamMemberParams{TeamID: teamID, UserID: userID})
		if err != nil {
			return err
		}
		if n == 0 {
			return errMemberNotFound()
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &t.OrganizationID, Action: "team.remove_member", ResourceType: "team", ResourceID: teamID.String(),
			Before: map[string]any{"user_id": userID.String()},
		})
	})
}
