package monitor

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
)

// Handler exposes monitors.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/orgs/{orgId}/monitors", h.list)
		r.Post("/orgs/{orgId}/monitors", h.create)
		r.Get("/monitors/{monitorId}", h.get)
		r.Patch("/monitors/{monitorId}", h.update)
		r.Delete("/monitors/{monitorId}", h.delete)
		r.Get("/monitors/{monitorId}/results", h.results)
	})
}

func orgID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "orgId"))
	if err != nil {
		return uuid.Nil, apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	}
	return id, nil
}

func monitorID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "monitorId"))
	if err != nil {
		return uuid.Nil, errNotFound()
	}
	return id, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	qs := r.URL.Query()
	res, err := h.svc.ListMonitors(r.Context(), id, qs.Get("label"), qs.Get("q"), qs.Get("status"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in MonitorInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CreateMonitor(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := monitorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.GetMonitor(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := monitorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	version, err := httpx.ParseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in MonitorInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.UpdateMonitor(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := monitorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteMonitor(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) results(w http.ResponseWriter, r *http.Request) {
	id, err := monitorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var rq ResultsQuery
	var fields []apperr.FieldError
	qs := r.URL.Query()
	for name, dst := range map[string]*time.Time{"from": &rq.From, "to": &rq.To} {
		if v := qs.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				fields = append(fields, apperr.FieldError{Field: name, Rule: "datetime"})
			}
			*dst = t
		}
	}
	if v := qs.Get("step"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			fields = append(fields, apperr.FieldError{Field: "step", Rule: "duration"})
		}
		rq.Step = d
	}
	if len(fields) > 0 {
		httpx.Error(w, r, apperr.Validation(fields))
		return
	}
	res, err := h.svc.GetResults(r.Context(), id, rq)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
