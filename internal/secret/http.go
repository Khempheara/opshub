package secret

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
)

// Handler exposes secret management. No route returns a value.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/projects/{projectId}/secrets", h.list)
		r.Post("/projects/{projectId}/secrets", h.create)
		r.Get("/secrets/{secretId}", h.get)
		r.Patch("/secrets/{secretId}", h.update)
		r.Delete("/secrets/{secretId}", h.delete)
		r.Get("/secrets/{secretId}/versions", h.versions)
		r.Post("/secrets/{secretId}/versions", h.rotate)
	})
}

func projectID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "projectId"))
	if err != nil {
		return uuid.Nil, apperr.New(apperr.CodeProjectNotFound, http.StatusNotFound, "project not found")
	}
	return id, nil
}

func secretID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "secretId"))
	if err != nil {
		return uuid.Nil, errNotFound()
	}
	return id, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var f Filter
	switch v := r.URL.Query().Get("environment_id"); v {
	case "":
	case "none":
		f.ProjectWide = true
	default:
		env, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, r, apperr.Validation([]apperr.FieldError{{Field: "environment_id", Rule: "uuid"}}))
			return
		}
		f.EnvironmentID = &env
	}
	res, err := h.svc.List(r.Context(), id, f)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in CreateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Create(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := secretID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := secretID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	version, err := httpx.ParseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in UpdateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Update(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := secretID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) versions(w http.ResponseWriter, r *http.Request) {
	id, err := secretID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Versions(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) rotate(w http.ResponseWriter, r *http.Request) {
	id, err := secretID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in RotateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Rotate(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusCreated, res)
}
