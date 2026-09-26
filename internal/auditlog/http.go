package auditlog

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/pagination"
)

// Handler exposes the audit log and account activity.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/orgs/{orgId}/audit-log", h.list)
		r.Get("/orgs/{orgId}/audit-log/export", h.export)
	})
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth, authn.RequireSession)
		r.Get("/me/activity", h.activity)
	})
}

func orgID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "orgId"))
	if err != nil {
		return uuid.Nil, apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	}
	return id, nil
}

// list reads repeated or comma-separated values: ?area=secret&area=monitor or ?area=secret,monitor.
func list(r *http.Request, key string) []string {
	var out []string
	for _, v := range r.URL.Query()[key] {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func filter(r *http.Request) Filter {
	qs := r.URL.Query()
	return Filter{
		Areas: list(r, "area"), Actions: list(r, "action"), Actor: qs.Get("actor"), Project: qs.Get("project"),
		ResourceType: qs.Get("resource_type"), ResourceID: qs.Get("resource_id"), From: qs.Get("from"), To: qs.Get("to"),
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	pg, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.List(r.Context(), id, filter(r), pg, Locale(r.Context(), r.URL.Query().Get("locale")))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	exp, err := h.svc.PrepareExport(r.Context(), id, filter(r), Locale(r.Context(), r.URL.Query().Get("locale")))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+exp.Filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := exp.Write(w); err != nil {
		// The status is sent; the client sees a truncated file. Log it for the operator.
		h.svc.logger.ErrorContext(r.Context(), "audit export interrupted", "err", err, "org_id", id)
	}
}

func (h *Handler) activity(w http.ResponseWriter, r *http.Request) {
	pg, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Activity(r.Context(), pg, Locale(r.Context(), r.URL.Query().Get("locale")))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
