package project

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/pagination"
)

// Handler exposes projects, members, repositories, environments and webhook receivers.
type Handler struct {
	svc         *Service
	idempotency func(http.Handler) http.Handler
}

// NewHandler wires the service; idempotency is applied to the create endpoints.
func NewHandler(svc *Service, idempotency func(http.Handler) http.Handler) *Handler {
	return &Handler{svc: svc, idempotency: idempotency}
}

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	// Git hosts authenticate with the webhook signature, not a user session.
	r.Post("/webhooks/github/{repositoryId}", h.webhook(gitprovider.GitHub))
	r.Post("/webhooks/gitlab/{repositoryId}", h.webhook(gitprovider.GitLab))

	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)

		r.Get("/orgs/{orgId}/projects", h.list)
		r.With(h.idempotency).Post("/orgs/{orgId}/projects", h.create)

		r.Route("/projects/{projectId}", func(r chi.Router) {
			r.Get("/", h.get)
			r.Patch("/", h.update)
			r.Delete("/", h.delete)
			r.Get("/members", h.listMembers)
			r.Put("/members/{principal}", h.grant)
			r.Delete("/members/{principal}", h.revoke)
			r.Get("/repository", h.getRepository)
			r.Put("/repository", h.connectRepository)
			r.Delete("/repository", h.disconnectRepository)
			r.Post("/repository/test", h.testRepository)
			r.Get("/repository/deliveries", h.listDeliveries)
			r.Get("/environments", h.listEnvironments)
			r.With(h.idempotency).Post("/environments", h.createEnvironment)
		})

		r.Route("/environments/{environmentId}", func(r chi.Router) {
			r.Get("/", h.getEnvironment)
			r.Patch("/", h.updateEnvironment)
			r.Delete("/", h.deleteEnvironment)
		})
	})
}

// pathID parses a UUID path parameter; malformed IDs are reported as the resource's 404.
func pathID(r *http.Request, name string, notFound func() *apperr.Error) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, notFound()
	}
	return id, nil
}

func projectID(r *http.Request) (uuid.UUID, error) {
	return pathID(r, "projectId", errProjectNotFound)
}

func environmentID(r *http.Request) (uuid.UUID, error) {
	return pathID(r, "environmentId", errEnvironmentNotFound)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	orgID, err := pathID(r, "orgId", func() *apperr.Error {
		return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	page, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.List(r.Context(), orgID, r.URL.Query().Get("q"), page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	orgID, err := pathID(r, "orgId", func() *apperr.Error {
		return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in CreateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	p, err := h.svc.Create(r.Context(), orgID, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(p.Version))
	httpx.JSON(w, http.StatusCreated, p)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	p, err := h.svc.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(p.Version))
	httpx.JSON(w, http.StatusOK, p)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
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
	p, err := h.svc.Update(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(p.Version))
	httpx.JSON(w, http.StatusOK, p)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err == nil {
		err = h.svc.Delete(r.Context(), id, r.URL.Query().Get("confirm"))
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListMembers(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) grant(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in GrantInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.Grant(r.Context(), id, chi.URLParam(r, "principal"), in.Role); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err == nil {
		err = h.svc.Revoke(r.Context(), id, chi.URLParam(r, "principal"))
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getRepository(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	repo, err := h.svc.GetRepository(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, repo)
}

func (h *Handler) connectRepository(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in ConnectInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ConnectRepository(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // may carry the webhook secret
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) disconnectRepository(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err == nil {
		err = h.svc.DisconnectRepository(r.Context(), id)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testRepository(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.TestRepository(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	page, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListDeliveries(r.Context(), id, page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) listEnvironments(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, err := h.svc.ListEnvironments(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in EnvironmentInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	e, err := h.svc.CreateEnvironment(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(e.Version))
	httpx.JSON(w, http.StatusCreated, e)
}

func (h *Handler) getEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := environmentID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	e, err := h.svc.GetEnvironment(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(e.Version))
	httpx.JSON(w, http.StatusOK, e)
}

func (h *Handler) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := environmentID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	version, err := httpx.ParseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in EnvironmentInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	e, err := h.svc.UpdateEnvironment(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(e.Version))
	httpx.JSON(w, http.StatusOK, e)
}

func (h *Handler) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	id, err := environmentID(r)
	if err == nil {
		err = h.svc.DeleteEnvironment(r.Context(), id)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) webhook(provider gitprovider.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "repositoryId", errWebhookNotFound)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxWebhookBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				httpx.Error(w, r, apperr.New(apperr.CodePayloadTooLarge, http.StatusRequestEntityTooLarge, "webhook body too large"))
				return
			}
			httpx.Error(w, r, apperr.New(apperr.CodeBadRequest, http.StatusBadRequest, "could not read webhook body"))
			return
		}
		outcome, err := h.svc.ReceiveWebhook(r.Context(), provider, id, r.Header, body)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		status := http.StatusAccepted
		if outcome == WebhookDuplicate {
			status = http.StatusOK
		}
		httpx.JSON(w, status, map[string]string{"status": string(outcome)})
	}
}
