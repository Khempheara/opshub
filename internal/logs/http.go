package logs

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/store"
)

// Handler exposes log search, ingest tokens and the ingest endpoint.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

type ctxKey struct{}

var tokenKey ctxKey

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/orgs/{orgId}/logs", h.search)
		r.Get("/orgs/{orgId}/logs/services", h.services)
		r.Get("/orgs/{orgId}/log-ingest-tokens", h.listTokens)
		r.Post("/orgs/{orgId}/log-ingest-tokens", h.createToken)
		r.Delete("/log-ingest-tokens/{tokenId}", h.revokeToken)
	})
	r.With(h.ingestAuth).Post("/ingest/logs", h.ingest)
}

func orgID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "orgId"))
	if err != nil {
		return uuid.Nil, apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	}
	return id, nil
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	qs := r.URL.Query()
	res, err := h.svc.Search(r.Context(), id, Query{
		From: qs.Get("from"), To: qs.Get("to"), Q: qs.Get("q"), Source: qs.Get("source"), Service: qs.Get("service"),
		Level: qs.Get("level"), Before: qs.Get("before"), After: qs.Get("after"), Limit: qs.Get("limit"),
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) services(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Services(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListTokens(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in TokenInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CreateToken(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "tokenId"))
	if err != nil {
		httpx.Error(w, r, errTokenNotFound())
		return
	}
	if err := h.svc.RevokeToken(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ingestAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		t, err := h.svc.Authenticate(r.Context(), token)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey, t)))
	})
}

func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) {
	t, _ := r.Context().Value(tokenKey).(store.LogIngestToken)
	res, err := h.svc.Ingest(r.Context(), t, r.Body)
	if errors.Is(err, ErrTooLarge) {
		err = apperr.New(apperr.CodePayloadTooLarge, http.StatusRequestEntityTooLarge, "at most 1 MiB and 5000 lines per request")
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
