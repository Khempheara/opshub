package notify

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
)

// Handler exposes notification channels.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/orgs/{orgId}/notification-channels", h.list)
		r.Post("/orgs/{orgId}/notification-channels", h.create)
		r.Patch("/notification-channels/{channelId}", h.update)
		r.Delete("/notification-channels/{channelId}", h.delete)
		r.Post("/notification-channels/{channelId}/test", h.test)
	})
}

func orgID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "orgId"))
	if err != nil {
		return uuid.Nil, errOrgNotFound()
	}
	return id, nil
}

func channelID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "channelId"))
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
	res, err := h.svc.ListChannels(r.Context(), id)
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
	var in ChannelInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CreateChannel(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := channelID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	version, err := httpx.ParseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in ChannelInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.UpdateChannel(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := channelID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteChannel(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) test(w http.ResponseWriter, r *http.Request) {
	id, err := channelID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.TestChannel(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
