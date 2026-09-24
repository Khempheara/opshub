// Package org manages organizations, the tenant boundary. Module 1 covers creating an
// organization and listing/reading the caller's memberships; members, invitations and
// teams arrive with RBAC (Module 2).
package org

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

// Organization is an org as seen by one of its members.
type Organization struct {
	ID        uuid.UUID `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// Service implements organization use cases.
type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

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
	err := database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		o, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{Slug: slug, Name: name})
		if database.IsUniqueViolation(err, "organizations_slug_key") {
			return apperr.New(apperr.CodeSlugTaken, http.StatusConflict, "this URL name is already taken").
				WithDetails(map[string]any{"slug": slug})
		}
		if err != nil {
			return err
		}
		if err := q.AddOrganizationMember(ctx, store.AddOrganizationMemberParams{
			OrganizationID: o.ID, UserID: p.UserID, Role: store.MemberRoleOwner,
		}); err != nil {
			return err
		}
		out = Organization{ID: o.ID, Slug: o.Slug, Name: o.Name, Role: string(store.MemberRoleOwner), CreatedAt: o.CreatedAt}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &o.ID, Action: "org.create", ResourceType: "organization", ResourceID: o.ID.String(),
			After: map[string]any{"name": o.Name, "slug": o.Slug},
		})
	})
	return out, err
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

// Get returns an organization the caller belongs to. Non-members get ORG_NOT_FOUND (not
// FORBIDDEN) so the existence of other tenants is not revealed.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Organization, error) {
	p, ok := authn.PrincipalFrom(ctx)
	if !ok {
		return Organization{}, apperr.Unauthenticated()
	}
	o, err := store.New(s.pool).GetOrganizationForMember(ctx, store.GetOrganizationForMemberParams{ID: id, UserID: p.UserID})
	if database.IsNoRows(err) {
		return Organization{}, errNotFound()
	}
	if err != nil {
		return Organization{}, err
	}
	return Organization{ID: o.ID, Slug: o.Slug, Name: o.Name, Role: string(o.Role), CreatedAt: o.CreatedAt}, nil
}

func errNotFound() *apperr.Error {
	return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
}

// Slugify derives a URL name: lowercase ASCII letters/digits, other runs become "-".
func Slugify(name string) string {
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

// Handler exposes organizations over HTTP.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Mount registers /orgs routes (callers must be authenticated).
func (h *Handler) Mount(r chi.Router) {
	r.Route("/orgs", func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Get("/{orgId}", h.get)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListMine(r.Context(), page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	o, err := h.svc.Create(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, o)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "orgId"))
	if err != nil {
		httpx.Error(w, r, errNotFound())
		return
	}
	o, err := h.svc.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}
