package runners

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/ratelimit"
	"github.com/opshub/opshub/internal/store"
)

// uploadDeadline bounds a single artifact, cache or log upload (the server's ReadTimeout
// is for ordinary requests).
const uploadDeadline = 30 * time.Minute

// Handler exposes runner management (users) and the runner API (runner and job tokens).
type Handler struct {
	svc           *Service
	registerLimit *ratelimit.Limiter
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc, registerLimit: ratelimit.PerMinute(10)}
}

type ctxKey int

const (
	runnerKey ctxKey = iota
	jobKey
)

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/orgs/{orgId}/runners", h.list)
		r.Post("/orgs/{orgId}/runner-registration-tokens", h.createRegistrationToken)
		r.Patch("/runners/{runnerId}", h.update)
		r.Delete("/runners/{runnerId}", h.delete)
		r.Get("/jobs/{jobId}/artifacts", h.listArtifacts)
		r.Get("/artifacts/{artifactId}/download", h.download)
	})

	r.Route("/runner", func(r chi.Router) {
		r.With(ratelimit.Middleware(h.registerLimit, func(r *http.Request) string {
			return "ip:" + middleware.GetClientIP(r.Context())
		})).Post("/register", h.register)
		r.Group(func(r chi.Router) {
			r.Use(h.runnerAuth)
			r.Post("/heartbeat", h.heartbeat)
			r.Post("/jobs/request", h.requestJob)
		})
		r.Route("/jobs/{jobId}", func(r chi.Router) {
			r.Use(h.jobAuth)
			r.Patch("/", h.report)
			r.Post("/logs", h.appendLog)
			r.Get("/source", h.source)
			r.Post("/artifacts", h.uploadArtifact)
			r.Get("/dependencies", h.dependencies)
			r.Get("/dependencies/{artifactId}", h.downloadDependency)
			r.Get("/cache/{key}", h.getCache)
			r.Put("/cache/{key}", h.putCache)
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

func errJobNotFound() *apperr.Error {
	return apperr.New(apperr.CodeJobNotFound, http.StatusNotFound, "job not found")
}

func bearer(r *http.Request) string {
	scheme, cred, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return cred
}

func (h *Handler) runnerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rn, err := h.svc.AuthenticateRunner(r.Context(), bearer(r))
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), runnerKey, rn)))
	})
}

func (h *Handler) jobAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "jobId", errJobToken)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		j, err := h.svc.AuthenticateJob(r.Context(), bearer(r), id)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), jobKey, j)))
	})
}

func runnerFrom(r *http.Request) store.Runner { return r.Context().Value(runnerKey).(store.Runner) }

func jobFrom(r *http.Request) store.PipelineJob {
	return r.Context().Value(jobKey).(store.PipelineJob)
}

// ---- user API ----

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "orgId", func() *apperr.Error {
		return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListRunners(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) createRegistrationToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "orgId", func() *apperr.Error {
		return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in RegistrationInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CreateRegistrationToken(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "runnerId", errRunnerNotFound)
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
	res, err := h.svc.UpdateRunner(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.RowVersion))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "runnerId", errRunnerNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteRunner(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listArtifacts(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "jobId", errJobNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListJobArtifacts(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "artifactId", errArtifactNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	d, err := h.svc.OpenArtifact(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	sendFile(w, d, true)
}

// sendFile streams a gzip-compressed tar archive.
func sendFile(w http.ResponseWriter, d Download, attachment bool) {
	defer func() { _ = d.Body.Close() }()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(d.Size, 10))
	w.Header().Set("Cache-Control", "private, no-store")
	if d.SHA256 != "" {
		w.Header().Set("X-Content-SHA256", d.SHA256)
	}
	if attachment {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.Name}))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, d.Body)
}

// ---- runner API ----

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in RegisterInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.RegisterRunner(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	var in HeartbeatInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Heartbeat(r.Context(), runnerFrom(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

type requestInput struct {
	WaitSeconds int `json:"wait_seconds" validate:"min=0,max=30"`
}

func (h *Handler) requestJob(w http.ResponseWriter, r *http.Request) {
	var in requestInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	job, err := h.svc.RequestJob(r.Context(), runnerFrom(r), time.Duration(in.WaitSeconds)*time.Second)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, job)
}

func (h *Handler) report(w http.ResponseWriter, r *http.Request) {
	var in ReportInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.Report(r.Context(), jobFrom(r), in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) appendLog(w http.ResponseWriter, r *http.Request) {
	var in LogInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.AppendLog(r.Context(), jobFrom(r), in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) source(w http.ResponseWriter, r *http.Request) {
	body, err := h.svc.Source(r.Context(), jobFrom(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// A copy error (size limit, provider hiccup) truncates the stream; the runner then fails
	// to unpack it and reports a runner_error.
	_, _ = io.Copy(w, body)
}

// uploadBody extends the read deadline and returns the raw request body.
func uploadBody(w http.ResponseWriter, r *http.Request) io.Reader {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadDeadline))
	return r.Body
}

func (h *Handler) uploadArtifact(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.UploadArtifact(r.Context(), jobFrom(r), r.URL.Query().Get("name"), uploadBody(w, r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) dependencies(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.Dependencies(r.Context(), jobFrom(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) downloadDependency(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "artifactId", errArtifactNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	d, err := h.svc.OpenDependency(r.Context(), jobFrom(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	sendFile(w, d, false)
}

func (h *Handler) getCache(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetCache(r.Context(), jobFrom(r), chi.URLParam(r, "key"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	sendFile(w, d, false)
}

func (h *Handler) putCache(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.PutCache(r.Context(), jobFrom(r), chi.URLParam(r, "key"), uploadBody(w, r)); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
