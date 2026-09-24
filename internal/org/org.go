// Package org manages organizations (the tenant boundary), their members, invitations and
// teams. Every operation authorizes through package authz; non-members always get 404.
package org

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

// Organization is an org as seen by one of its members.
type Organization struct {
	ID        uuid.UUID `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Version   int32     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// Config holds settings the service needs for outgoing links.
type Config struct {
	PublicURL string
}

// Service implements organization use cases.
type Service struct {
	pool *pgxpool.Pool
	jobs jobs.Inserter
	cfg  Config
	now  func() time.Time
}

func NewService(pool *pgxpool.Pool, inserter jobs.Inserter, cfg Config) *Service {
	return &Service{pool: pool, jobs: inserter, cfg: cfg, now: time.Now}
}

func (s *Service) inTx(ctx context.Context, fn func(q *store.Queries, tx pgx.Tx) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(store.New(tx), tx) })
}

// CreateInput is POST /orgs. Slug defaults to one derived from the name (Latin letters
// and digits only; Khmer names need an explicit slug).
type CreateInput struct {
	Name string `json:"name" validate:"required,min=1,max=100"`
	Slug string `json:"slug" validate:"omitempty,max=40"`
}

// Create makes a new organization with the caller as Owner.
func (s *Service) Create(ctx context.Context, in CreateInput) (Organization, error) {
	p, ok := authn.PrincipalFrom(ctx)
	if !ok {
		return Organization{}, apperr.Unauthenticated()
	}
	name := strings.TrimSpace(in.Name)
	slug := in.Slug
	if slug == "" {
		slug = Slugify(name)
	}
	if !slugPattern.MatchString(slug) {
		return Organization{}, apperr.Validation([]apperr.FieldError{{Field: "slug", Rule: "slug"}})
	}
	var out Organization
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		o, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{Slug: slug, Name: name})
		if database.IsUniqueViolation(err, "organizations_slug_key") {
			return errSlugTaken(slug)
		}
		if err != nil {
			return err
		}
		if err := q.AddOrganizationMember(ctx, store.AddOrganizationMemberParams{
			OrganizationID: o.ID, UserID: p.UserID, Role: authz.Owner,
		}); err != nil {
			return err
		}
		out = Organization{ID: o.ID, Slug: o.Slug, Name: o.Name, Role: string(authz.Owner), Version: o.Version, CreatedAt: o.CreatedAt}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &o.ID, Action: "org.create", ResourceType: "organization", ResourceID: o.ID.String(),
			After: map[string]any{"name": o.Name, "slug": o.Slug},
		})
	})
	return out, err
}

func errSlugTaken(slug string) *apperr.Error {
	return apperr.New(apperr.CodeSlugTaken, http.StatusConflict, "this URL name is already taken").
		WithDetails(map[string]any{"slug": slug})
}

type nameIDCursor struct {
	Name string    `json:"n"`
	ID   uuid.UUID `json:"i"`
}

// ListMine lists the caller's organizations by name.
func (s *Service) ListMine(ctx context.Context, page pagination.Params) (pagination.Page[Organization], error) {
	p, ok := authn.PrincipalFrom(ctx)
	if !ok {
		return pagination.Page[Organization]{}, apperr.Unauthenticated()
	}
	params := store.ListUserOrganizationsParams{UserID: p.UserID, PageSize: page.FetchSize()}
	var cur nameIDCursor
	if has, err := page.Decode(&cur); err != nil {
		return pagination.Page[Organization]{}, err
	} else if has {
		params.CursorName, params.CursorID = &cur.Name, &cur.ID
	}
	rows, err := store.New(s.pool).ListUserOrganizations(ctx, params)
	if err != nil {
		return pagination.Page[Organization]{}, err
	}
	return pagination.Build(rows, page.Limit, func(r store.ListUserOrganizationsRow) Organization {
		return Organization{ID: r.ID, Slug: r.Slug, Name: r.Name, Role: string(r.Role), CreatedAt: r.CreatedAt}
	}, func(r store.ListUserOrganizationsRow) any { return nameIDCursor{Name: r.Name, ID: r.ID} }), nil
}

// Get returns an organization the caller belongs to (ORG_NOT_FOUND otherwise).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Organization, error) {
	q := store.New(s.pool)
	m, err := authz.Require(ctx, q, id, authz.OrgView)
	if err != nil {
		return Organization{}, err
	}
	p, _ := authn.PrincipalFrom(ctx)
	o, err := q.GetOrganizationForMember(ctx, store.GetOrganizationForMemberParams{ID: id, UserID: p.UserID})
	if err != nil {
		return Organization{}, err
	}
	return Organization{ID: o.ID, Slug: o.Slug, Name: o.Name, Role: string(m.Role), Version: o.Version, CreatedAt: o.CreatedAt}, nil
}

// UpdateInput is PATCH /orgs/{id}.
type UpdateInput struct {
	Name string `json:"name" validate:"required,min=1,max=100"`
}

// Update renames an organization (Admin+) with optimistic locking.
func (s *Service) Update(ctx context.Context, id uuid.UUID, version int32, in UpdateInput) (Organization, error) {
	var out Organization
	err := s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		m, err := authz.Require(ctx, q, id, authz.OrgUpdate)
		if err != nil {
			return err
		}
		p, _ := authn.PrincipalFrom(ctx)
		before, err := q.GetOrganizationForMember(ctx, store.GetOrganizationForMemberParams{ID: id, UserID: p.UserID})
		if err != nil {
			return err
		}
		o, err := q.UpdateOrganization(ctx, store.UpdateOrganizationParams{ID: id, Version: version, Name: strings.TrimSpace(in.Name)})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if err != nil {
			return err
		}
		out = Organization{ID: o.ID, Slug: o.Slug, Name: o.Name, Role: string(m.Role), Version: o.Version, CreatedAt: o.CreatedAt}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &id, Action: string(authz.OrgUpdate), ResourceType: "organization", ResourceID: id.String(),
			Before: map[string]any{"name": before.Name}, After: map[string]any{"name": o.Name},
		})
	})
	return out, err
}

// Delete soft-deletes an organization (Owner only). The caller must retype the slug.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, confirmSlug string) error {
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		if _, err := authz.Require(ctx, q, id, authz.OrgDelete); err != nil {
			return err
		}
		p, _ := authn.PrincipalFrom(ctx)
		o, err := q.GetOrganizationForMember(ctx, store.GetOrganizationForMemberParams{ID: id, UserID: p.UserID})
		if err != nil {
			return err
		}
		if confirmSlug != o.Slug {
			return apperr.New(apperr.CodeConfirmationMismatch, http.StatusUnprocessableEntity, "type the organization's URL name to confirm")
		}
		if err := q.SoftDeleteOrganization(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &id, Action: string(authz.OrgDelete), ResourceType: "organization", ResourceID: id.String(),
			Before: map[string]any{"name": o.Name, "slug": o.Slug},
		})
	})
}

// Permissions is the caller's role and allowed actions in an organization (for the UI).
type Permissions struct {
	Role    string   `json:"role"`
	Actions []string `json:"actions"`
}

func (s *Service) Permissions(ctx context.Context, id uuid.UUID) (Permissions, error) {
	m, err := authz.Resolve(ctx, store.New(s.pool), id)
	if err != nil {
		return Permissions{}, err
	}
	allowed := authz.Allowed(m.Role)
	actions := make([]string, len(allowed))
	for i, a := range allowed {
		actions[i] = string(a)
	}
	return Permissions{Role: string(m.Role), Actions: actions}, nil
}

// Slugify derives a URL name: lowercase ASCII letters/digits, other runs become "-".
func Slugify(name string) string {
	return slugify(name)
}

// SlugOrFallback derives a slug from name, or "<prefix>-<random>" when the name has no
// Latin letters or digits (e.g. a name written only in Khmer).
func SlugOrFallback(name, prefix string) string {
	if s := slugify(name); s != "" {
		return s
	}
	return prefix + "-" + strings.ToLower(crypto.RandomBase32(6))
}

func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}
