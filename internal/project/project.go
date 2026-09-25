// Package project implements projects, project access grants, connected Git repositories
// (with webhooks) and environments with protection rules (Module 3). Every method resolves
// the caller's effective project role (docs/rbac.md) before reading or changing anything;
// callers without access to a project get PROJECT_NOT_FOUND, as if it didn't exist.
package project

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

// Config holds settings for webhook URLs.
type Config struct {
	PublicURL string // webhook payload URLs are built from it
}

// Service implements the project use cases.
type Service struct {
	pool   *pgxpool.Pool
	keys   *crypto.KeyRing
	git    gitprovider.Factory
	jobs   jobs.Inserter
	cfg    Config
	logger *slog.Logger
}

func NewService(pool *pgxpool.Pool, keys *crypto.KeyRing, git gitprovider.Factory, inserter jobs.Inserter, cfg Config, logger *slog.Logger) *Service {
	return &Service{pool: pool, keys: keys, git: git, jobs: inserter, cfg: cfg, logger: logger}
}

func (s *Service) inTx(ctx context.Context, fn func(q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(store.New(tx)) })
}

// Access is a caller's verified access to a project, for other modules (pipelines).
type Access struct {
	Project store.Project
	Role    authz.Role
	UserID  uuid.UUID
}

// Authorize resolves the caller's effective role on a project and checks the action, with
// the same 404/403 rules as this package (PROJECT_NOT_FOUND for projects they can't see).
func Authorize(ctx context.Context, q *store.Queries, projectID uuid.UUID, a authz.Action) (Access, error) {
	acc, err := load(ctx, q, projectID, a)
	if err != nil {
		return Access{}, err
	}
	return Access{Project: acc.project, Role: acc.role, UserID: acc.userID}, nil
}

// GitClient returns a client for the project's connected repository using its stored
// token. It performs no authorization: callers authorize first (or are system workers).
func (s *Service) GitClient(ctx context.Context, q *store.Queries, projectID uuid.UUID) (gitprovider.Client, store.Repository, error) {
	r, err := q.GetRepositoryByProject(ctx, projectID)
	if database.IsNoRows(err) {
		return nil, store.Repository{}, errRepositoryNotFound()
	}
	if err != nil {
		return nil, store.Repository{}, err
	}
	c, err := s.clientFor(r)
	return c, r, err
}

// GitError maps provider errors to API errors (exported for pipelines).
func GitError(err error) error { return gitError(err) }

var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
	branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
)

// Project is the API representation of a project.
type Project struct {
	ID             uuid.UUID `json:"id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	Slug           string    `json:"slug"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	DefaultBranch  string    `json:"default_branch"`
	Version        int32     `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Detail is a project with the caller's effective role and allowed actions (for the UI).
type Detail struct {
	Project
	Role    authz.Role     `json:"role"`
	Actions []authz.Action `json:"actions"`
}

func toProject(p store.Project) Project {
	return Project{
		ID: p.ID, OrganizationID: p.OrganizationID, Slug: p.Slug, Name: p.Name, Description: p.Description,
		DefaultBranch: p.DefaultBranch, Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func errProjectNotFound() *apperr.Error {
	return apperr.New(apperr.CodeProjectNotFound, http.StatusNotFound, "project not found")
}

// access is the caller's verified access to a project.
type access struct {
	project store.Project
	orgRole authz.Role
	role    authz.Role
	userID  uuid.UUID
}

// load resolves a project and the caller's effective role, and checks the action.
// Projects that don't exist, were deleted, or that the caller can't see are
// PROJECT_NOT_FOUND; visible projects without the permission are FORBIDDEN.
func load(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (access, error) {
	p, ok := authn.PrincipalFrom(ctx)
	if !ok {
		return access{}, apperr.Unauthenticated()
	}
	row, err := q.GetProjectAccess(ctx, store.GetProjectAccessParams{ID: id, UserID: p.UserID})
	if database.IsNoRows(err) {
		return access{}, errProjectNotFound()
	}
	if err != nil {
		return access{}, err
	}
	role, ok := authz.EffectiveProjectRole(authz.Role(row.OrgRole), authz.Role(row.DirectRole), authz.Role(row.TeamRole))
	if !ok {
		return access{}, errProjectNotFound()
	}
	if !authz.CanProject(role, a) {
		return access{}, apperr.Forbidden().WithDetails(map[string]any{"action": string(a)})
	}
	return access{project: row.Project, orgRole: authz.Role(row.OrgRole), role: role, userID: p.UserID}, nil
}

// CreateInput is POST /orgs/{orgId}/projects.
type CreateInput struct {
	Name          string `json:"name" validate:"required,min=1,max=100"`
	Slug          string `json:"slug" validate:"omitempty,max=40"`
	Description   string `json:"description" validate:"max=500"`
	DefaultBranch string `json:"default_branch" validate:"omitempty,max=255"`
}

// Create creates a project (Developers and up). A Developer who creates a project gets a
// direct Admin grant on it; Owners and Admins already inherit their role.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in CreateInput) (Detail, error) {
	name := strings.TrimSpace(in.Name)
	slug := in.Slug
	if slug == "" {
		slug = org.SlugOrFallback(name, "project")
	}
	branch := strings.TrimSpace(in.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	var fields []apperr.FieldError
	if !slugPattern.MatchString(slug) {
		fields = append(fields, apperr.FieldError{Field: "slug", Rule: "slug"})
	}
	if !branchPattern.MatchString(branch) {
		fields = append(fields, apperr.FieldError{Field: "default_branch", Rule: "branch"})
	}
	if len(fields) > 0 {
		return Detail{}, apperr.Validation(fields)
	}
	var out Detail
	err := s.inTx(ctx, func(q *store.Queries) error {
		m, err := authz.Require(ctx, q, orgID, authz.ProjectCreate)
		if err != nil {
			return err
		}
		p, err := q.CreateProject(ctx, store.CreateProjectParams{
			OrganizationID: orgID, Slug: slug, Name: name, Description: strings.TrimSpace(in.Description),
			DefaultBranch: branch, CreatedBy: &m.UserID,
		})
		if database.IsUniqueViolation(err, "projects_org_slug_key") {
			return apperr.New(apperr.CodeSlugTaken, http.StatusConflict, "this URL name is already taken").
				WithDetails(map[string]any{"slug": slug})
		}
		if err != nil {
			return err
		}
		role, inherited := authz.InheritedProjectRole(m.Role)
		if !inherited {
			role = authz.Admin
			if err := q.UpsertProjectUserGrant(ctx, store.UpsertProjectUserGrantParams{ProjectID: p.ID, UserID: m.UserID, Role: authz.Admin}); err != nil {
				return err
			}
		}
		out = Detail{Project: toProject(p), Role: role, Actions: authz.AllowedProject(role)}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, ProjectID: &p.ID, Action: "project.create", ResourceType: "project",
			ResourceID: p.ID.String(), After: map[string]any{"name": p.Name, "slug": p.Slug},
		})
	})
	return out, err
}

type nameIDCursor struct {
	Name string    `json:"n"`
	ID   uuid.UUID `json:"i"`
}

// List returns the organization's projects the caller can see, by name.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, search string, page pagination.Params) (pagination.Page[Project], error) {
	q := store.New(s.pool)
	m, err := authz.Require(ctx, q, orgID, authz.OrgView)
	if err != nil {
		return pagination.Page[Project]{}, err
	}
	_, seeAll := authz.InheritedProjectRole(m.Role)
	params := store.ListProjectsParams{OrganizationID: orgID, UserID: m.UserID, SeeAll: seeAll, PageSize: page.FetchSize()}
	if search = strings.TrimSpace(search); search != "" {
		escaped := likeEscaper.Replace(search)
		params.Search = &escaped
	}
	var cur nameIDCursor
	if has, err := page.Decode(&cur); err != nil {
		return pagination.Page[Project]{}, err
	} else if has {
		params.CursorName, params.CursorID = &cur.Name, &cur.ID
	}
	rows, err := q.ListProjects(ctx, params)
	if err != nil {
		return pagination.Page[Project]{}, err
	}
	return pagination.Build(rows, page.Limit, toProject,
		func(p store.Project) any { return nameIDCursor{Name: p.Name, ID: p.ID} }), nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Get returns a project with the caller's role and allowed actions.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Detail, error) {
	a, err := load(ctx, store.New(s.pool), id, authz.ProjectView)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Project: toProject(a.project), Role: a.role, Actions: authz.AllowedProject(a.role)}, nil
}

// UpdateInput is PATCH /projects/{id}.
type UpdateInput struct {
	Name          string `json:"name" validate:"required,min=1,max=100"`
	Description   string `json:"description" validate:"max=500"`
	DefaultBranch string `json:"default_branch" validate:"required,max=255"`
}

// Update changes a project's settings with optimistic locking (If-Match).
func (s *Service) Update(ctx context.Context, id uuid.UUID, version int32, in UpdateInput) (Project, error) {
	branch := strings.TrimSpace(in.DefaultBranch)
	if !branchPattern.MatchString(branch) {
		return Project{}, apperr.Validation([]apperr.FieldError{{Field: "default_branch", Rule: "branch"}})
	}
	var out Project
	err := s.inTx(ctx, func(q *store.Queries) error {
		a, err := load(ctx, q, id, authz.ProjectUpdate)
		if err != nil {
			return err
		}
		p, err := q.UpdateProject(ctx, store.UpdateProjectParams{
			ID: id, Version: version, Name: strings.TrimSpace(in.Name),
			Description: strings.TrimSpace(in.Description), DefaultBranch: branch,
		})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if err != nil {
			return err
		}
		out = toProject(p)
		before, after := a.project, p
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &p.OrganizationID, ProjectID: &p.ID, Action: string(authz.ProjectUpdate),
			ResourceType: "project", ResourceID: p.ID.String(),
			Before: map[string]any{"name": before.Name, "description": before.Description, "default_branch": before.DefaultBranch},
			After:  map[string]any{"name": after.Name, "description": after.Description, "default_branch": after.DefaultBranch},
		})
	})
	return out, err
}

// Delete soft-deletes a project after the caller confirms its slug. The connected
// repository (and its encrypted token) is removed, and OpsHub's webhook is deleted from the
// Git host on a best-effort basis.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, confirmSlug string) error {
	var removed *store.Repository
	err := s.inTx(ctx, func(q *store.Queries) error {
		a, err := load(ctx, q, id, authz.ProjectDelete)
		if err != nil {
			return err
		}
		if confirmSlug != a.project.Slug {
			return apperr.New(apperr.CodeConfirmationMismatch, http.StatusUnprocessableEntity, "type the project's URL name to confirm")
		}
		if err := q.SoftDeleteProject(ctx, id); err != nil {
			return err
		}
		repo, err := q.DeleteRepositoryByProject(ctx, id)
		switch {
		case err == nil:
			removed = &repo
		case !database.IsNoRows(err):
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.project.OrganizationID, ProjectID: &id, Action: string(authz.ProjectDelete),
			ResourceType: "project", ResourceID: id.String(), Before: map[string]any{"name": a.project.Name, "slug": a.project.Slug},
		})
	})
	if err == nil && removed != nil {
		s.removeHook(ctx, *removed)
	}
	return err
}
