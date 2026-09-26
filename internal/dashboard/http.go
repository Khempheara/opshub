package dashboard

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
)

// Handler exposes the dashboard.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/orgs/{orgId}/dashboard/pipelines", h.pipelines)
		r.Get("/orgs/{orgId}/dashboard/dora", h.dora)
	})
}

func request(r *http.Request) (uuid.UUID, Query, error) {
	id, err := uuid.Parse(chi.URLParam(r, "orgId"))
	if err != nil {
		return uuid.Nil, Query{}, apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	}
	qs := r.URL.Query()
	return id, Query{From: qs.Get("from"), To: qs.Get("to"), Project: qs.Get("project"), Environment: qs.Get("environment"), TZ: qs.Get("tz")}, nil
}

func (h *Handler) pipelines(w http.ResponseWriter, r *http.Request) {
	id, q, err := request(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Pipelines(r.Context(), id, q)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) dora(w http.ResponseWriter, r *http.Request) {
	id, q, err := request(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Dora(r.Context(), id, q)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
