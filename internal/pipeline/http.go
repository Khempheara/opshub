package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
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

// Stream timing. Streams end after maxStreamAge so the client reconnects with a fresh access
// token (authorization is checked when a stream starts).
const (
	pingInterval = 15 * time.Second
	maxStreamAge = 5 * time.Minute
)

// Handler exposes pipelines over HTTP.
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
		r.Post("/projects/{projectId}/pipeline/validate", h.validate)
		r.Get("/projects/{projectId}/runs", h.listRuns)
		r.With(h.idempotency).Post("/projects/{projectId}/runs", h.trigger)

		r.Route("/runs/{runId}", func(r chi.Router) {
			r.Get("/", h.getRun)
			r.Post("/cancel", h.cancel)
			r.With(h.idempotency).Post("/rerun", h.rerun)
			r.Get("/events", h.runEvents)
		})
		r.Route("/jobs/{jobId}", func(r chi.Router) {
			r.Get("/", h.getJob)
			r.With(h.idempotency).Post("/retry", h.retry)
			r.Get("/logs", h.logs)
			r.Get("/logs/stream", h.logStream)
			r.Post("/approvals", h.decide)
		})
	})
}

func pathID(r *http.Request, name string, notFound func() *apperr.Error) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, notFound()
	}
	return id, nil
}

func projectID(r *http.Request) (uuid.UUID, error) {
	return pathID(r, "projectId", func() *apperr.Error {
		return apperr.New(apperr.CodeProjectNotFound, http.StatusNotFound, "project not found")
	})
}

func runID(r *http.Request) (uuid.UUID, error) { return pathID(r, "runId", errRunNotFound) }
func jobID(r *http.Request) (uuid.UUID, error) { return pathID(r, "jobId", errJobNotFound) }

type validateInput struct {
	Content string `json:"content" validate:"max=262144"`
}

func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in validateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Validate(r.Context(), id, in.Content)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
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
	qs := r.URL.Query()
	res, err := h.svc.ListRuns(r.Context(), id, RunFilter{Status: qs.Get("status"), Trigger: qs.Get("trigger"), Ref: qs.Get("ref")}, page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) trigger(w http.ResponseWriter, r *http.Request) {
	id, err := projectID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in ManualRunInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.TriggerManual(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := runID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.GetRun(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	id, err := runID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CancelRun(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

type rerunInput struct {
	FailedOnly bool `json:"failed_only"`
}

func (h *Handler) rerun(w http.ResponseWriter, r *http.Request) {
	id, err := runID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in rerunInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Rerun(r.Context(), id, in.FailedOnly)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	status := http.StatusCreated
	if in.FailedOnly {
		status = http.StatusOK
	}
	httpx.JSON(w, status, res)
}

func (h *Handler) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.GetJob(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) retry(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.RetryJob(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) decide(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in DecideInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Decide(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// afterSeq reads the resume point from Last-Event-ID (SSE reconnects) or ?after_seq.
func afterSeq(r *http.Request) (int32, error) {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("after_seq")
	}
	if v == "" {
		return -1, nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < -1 {
		return 0, apperr.Validation([]apperr.FieldError{{Field: "after_seq", Rule: "min", Param: "-1"}})
	}
	return int32(n), nil
}

var fileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (h *Handler) logs(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	after, err := afterSeq(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit := int32(500)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 && n <= 1000 {
			limit = int32(n)
		}
	}
	page, err := h.svc.Logs(r.Context(), id, after, limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if r.Header.Get("Accept") != "text/plain" {
		httpx.JSON(w, http.StatusOK, page)
		return
	}
	// Plain-text download of the whole log.
	job, err := h.svc.GetJob(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	name := fileNameUnsafe.ReplaceAllString(fmt.Sprintf("run-%d-%s-attempt-%d.log", job.RunNumber, job.Name, job.Attempt), "_")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	for {
		for _, c := range page.Items {
			_, _ = w.Write([]byte(c.Content))
		}
		if len(page.Items) < int(limit) {
			return
		}
		if page, err = h.svc.logPage(r.Context(), newQueries(h.svc), id, page.NextSeq, limit); err != nil {
			return
		}
	}
}

// sse prepares a Server-Sent Events response.
type sse struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func startSSE(w http.ResponseWriter) *sse {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't buffer the stream
	w.WriteHeader(http.StatusOK)
	s := &sse{w: w, rc: http.NewResponseController(w)}
	s.write("retry: 3000\n\n")
	return s
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
		msg += "id: " + id + "\n"
	}
	return s.write(msg + "event: " + name + "\ndata: " + string(b) + "\n\n")
}

// runEvents streams "update" whenever the run or one of its jobs changes; clients re-read
// the run. It sends one update on connect so clients can rely on the first event.
func (h *Handler) runEvents(w http.ResponseWriter, r *http.Request) {
	id, err := runID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if _, err := h.svc.GetRun(r.Context(), id); err != nil { // authorizes
		httpx.Error(w, r, err)
		return
	}
	ch, unsubscribe := h.hub.Subscribe(events.RunKey(id))
	defer unsubscribe()
	stream := startSSE(w)
	ctx, cancel := context.WithTimeout(r.Context(), maxStreamAge)
	defer cancel()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	payload := map[string]string{"run_id": id.String()}
	if !stream.event("update", "", payload) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			if !stream.event("update", "", payload) {
				return
			}
		case <-ping.C:
			if !stream.write(": ping\n\n") {
				return
			}
		}
	}
}

// logStream streams log chunks as "log" events (id = seq, so reconnects resume with
// Last-Event-ID) and ends with an "end" event once the job has finished.
func (h *Handler) logStream(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	after, err := afterSeq(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	page, err := h.svc.Logs(r.Context(), id, after, 500) // authorizes
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ch, unsubscribe := h.hub.Subscribe(events.JobKey(id))
	defer unsubscribe()
	stream := startSSE(w)
	ctx, cancel := context.WithTimeout(r.Context(), maxStreamAge)
	defer cancel()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	q := newQueries(h.svc)
	for {
		for _, c := range page.Items {
			if !stream.event("log", strconv.Itoa(int(c.Seq)), c) {
				return
			}
			after = c.Seq
		}
		if page.Complete {
			stream.event("end", "", map[string]string{"job_id": id.String()})
			return
		}
		if len(page.Items) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-ch:
			case <-ping.C:
				if !stream.write(": ping\n\n") {
					return
				}
				continue
			}
		}
		if page, err = h.svc.logPage(ctx, q, id, after, 500); err != nil {
			return
		}
	}
}

func newQueries(s *Service) *store.Queries { return store.New(s.pool) }
