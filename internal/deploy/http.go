package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/events"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

// Stream timing (as for pipeline logs).
const (
	pingInterval = 15 * time.Second
	maxStreamAge = 5 * time.Minute
)

// Handler exposes deploy targets and deployments.
type Handler struct {
	svc         *Service
	hub         *events.Hub
	idempotency func(http.Handler) http.Handler
}

func NewHandler(svc *Service, hub *events.Hub, idempotency func(http.Handler) http.Handler) *Handler {
	return &Handler{svc: svc, hub: hub, idempotency: idempotency}
}

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/orgs/{orgId}/deploy-targets", h.listTargets)
		r.Post("/orgs/{orgId}/deploy-targets", h.createTarget)
		r.Get("/deploy-targets/{targetId}", h.getTarget)
		r.Patch("/deploy-targets/{targetId}", h.updateTarget)
		r.Delete("/deploy-targets/{targetId}", h.deleteTarget)
		r.Post("/deploy-targets/{targetId}/test", h.testTarget)
		r.Get("/projects/{projectId}/deployments", h.listDeployments)
		r.With(h.idempotency).Post("/environments/{environmentId}/deployments", h.createDeployment)
		r.Get("/deployments/{deploymentId}", h.getDeployment)
		r.Get("/deployments/{deploymentId}/logs/stream", h.stream)
		r.With(h.idempotency).Post("/deployments/{deploymentId}/rollback", h.rollback)
	})
}

func pathID(r *http.Request, name string, notFound func() *apperr.Error) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, notFound()
	}
	return id, nil
}

func orgID(r *http.Request) (uuid.UUID, error) {
	return pathID(r, "orgId", func() *apperr.Error {
		return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	})
}

func (h *Handler) listTargets(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListTargets(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) createTarget(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in TargetInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CreateTarget(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) getTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "targetId", errTargetNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.GetTarget(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) updateTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "targetId", errTargetNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	version, err := httpx.ParseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in UpdateTargetInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.UpdateTarget(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) deleteTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "targetId", errTargetNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteTarget(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "targetId", errTargetNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.TestTarget(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func parseTime(r *http.Request, name string, fields *[]apperr.FieldError) *time.Time {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		*fields = append(*fields, apperr.FieldError{Field: name, Rule: "datetime"})
		return nil
	}
	return &t
}

func (h *Handler) listDeployments(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "projectId", func() *apperr.Error {
		return apperr.New(apperr.CodeProjectNotFound, http.StatusNotFound, "project not found")
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
	var fields []apperr.FieldError
	f := DeploymentFilter{
		Status: r.URL.Query().Get("status"), From: parseTime(r, "from", &fields), To: parseTime(r, "to", &fields),
		Current: r.URL.Query().Get("current") == "true",
	}
	if v := r.URL.Query().Get("environment_id"); v != "" {
		env, err := uuid.Parse(v)
		if err != nil {
			fields = append(fields, apperr.FieldError{Field: "environment_id", Rule: "uuid"})
		}
		f.EnvironmentID = &env
	}
	if len(fields) > 0 {
		httpx.Error(w, r, apperr.Validation(fields))
		return
	}
	res, err := h.svc.ListDeployments(r.Context(), id, f, page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) createDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "environmentId", errEnvironmentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in CreateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CreateDeployment(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, res)
}

func (h *Handler) getDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "deploymentId", errDeploymentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.GetDeployment(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "deploymentId", errDeploymentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Rollback(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, res)
}

type sse struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (s *sse) write(raw string) bool {
	if _, err := s.w.Write([]byte(raw)); err != nil {
		return false
	}
	return s.rc.Flush() == nil
}

func (s *sse) event(name, id string, data any) bool {
	b, err := json.Marshal(data)
	if err != nil {
		return false
	}
	msg := ""
	if id != "" {
		msg = "id: " + id + "\n"
	}
	return s.write(msg + "event: " + name + "\ndata: " + string(b) + "\n\n")
}

// stream sends "status" events (on connect and on every change), "log" events (id = seq, so
// reconnects resume with Last-Event-ID) and "end" once the deployment finished and every
// chunk was sent.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "deploymentId", errDeploymentNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	after := int32(-1)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n >= -1 {
			after = int32(n)
		}
	}
	if err := h.svc.authorizeView(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	ch, unsubscribe := h.hub.Subscribe(events.DeploymentKey(id))
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sse{w: w, rc: http.NewResponseController(w)}
	s.write("retry: 3000\n\n")
	ctx, cancel := context.WithTimeout(r.Context(), maxStreamAge)
	defer cancel()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	q := store.New(h.svc.pool)
	var lastStatus store.DeploymentStatus
	for {
		chunks, done, status, err := h.svc.logPage(ctx, q, id, after)
		if err != nil {
			return
		}
		if status != lastStatus {
			if !s.event("status", "", map[string]string{"deployment_id": id.String(), "status": string(status)}) {
				return
			}
			lastStatus = status
		}
		for _, c := range chunks {
			if !s.event("log", strconv.Itoa(int(c.Seq)), c) {
				return
			}
			after = c.Seq
		}
		if done {
			s.event("end", "", map[string]string{"deployment_id": id.String()})
			return
		}
		if len(chunks) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-ch:
			case <-ping.C:
				if !s.write(": ping\n\n") {
					return
				}
			}
		}
	}
}
