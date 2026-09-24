// Package server assembles the HTTP router: middleware stack, operational endpoints,
// API docs and the versioned /api/v1 routes.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/opshub/opshub/api"
	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/telemetry"
)

// Pinger is the readiness dependency (satisfied by *pgxpool.Pool).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are the collaborators the router needs. Module handlers are added here as
// each module lands (auth, projects, pipelines, ...).
type Deps struct {
	Config  config.Config
	Logger  *slog.Logger
	DB      Pinger
	Metrics *telemetry.Metrics
	Version string
}

// New returns the fully wired root handler.
func New(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(requestID)
	r.Use(clientIP(d.Config.TrustedProxies))
	r.Use(d.Metrics.Middleware)
	r.Use(requestLogger(d.Logger))
	r.Use(recoverer(d.Logger))
	r.Use(securityHeaders)
	r.Use(i18n.Middleware)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, apperr.New(apperr.CodeRouteNotFound, http.StatusNotFound, "route not found"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, apperr.New(apperr.CodeMethodNotAllowed, http.StatusMethodNotAllowed, "method not allowed"))
	})

	// Operational endpoints (not under /api/v1; not versioned).
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", readyz(d.DB))
	r.Method(http.MethodGet, "/metrics", d.Metrics.Handler())

	// API documentation.
	r.Get("/api/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(api.OpenAPISpec)
	})
	r.Get("/docs", swaggerUI)
	r.Get("/docs/init.js", swaggerInit)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/meta", meta(d))
	})

	return otelhttp.NewHandler(r, "opshub-api",
		otelhttp.WithFilter(func(r *http.Request) bool {
			// Don't trace probe and scrape traffic.
			switch r.URL.Path {
			case "/healthz", "/readyz", "/metrics":
				return false
			}
			return true
		}))
}

func readyz(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			httpx.Error(w, r, apperr.New(apperr.CodeUnavailable, http.StatusServiceUnavailable, "database unavailable").Wrap(err))
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

// MetaResponse describes public, unauthenticated platform info used by the UI bootstrap.
type MetaResponse struct {
	Version         string   `json:"version"`
	Locales         []string `json:"locales"`
	DefaultLocale   string   `json:"default_locale"`
	DefaultTimezone string   `json:"default_timezone"`
}

func meta(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, MetaResponse{
			Version:         d.Version,
			Locales:         config.SupportedLocales,
			DefaultLocale:   d.Config.DefaultLocale,
			DefaultTimezone: d.Config.DefaultTimezone,
		})
	}
}

// requestID assigns every request an ID (echoed in X-Request-Id and readable with
// middleware.GetReqID). A well-formed inbound ID from an upstream proxy is kept so traces
// correlate; anything else is replaced. chi's default generator is not used because it
// embeds the server hostname.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(middleware.RequestIDHeader)
		if !validRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(middleware.RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.RequestIDKey, id)))
	})
}

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
				return
			}
			logger.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"route", telemetry.RoutePattern(r),
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
				"client_ip", middleware.GetClientIP(r.Context()),
			)
		})
	}
}

func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(rec)
					}
					logger.ErrorContext(r.Context(), "panic recovered",
						"panic", rec, "stack", string(debug.Stack()), "request_id", middleware.GetReqID(r.Context()))
					httpx.Error(w, r, apperr.New(apperr.CodeInternal, http.StatusInternalServerError, "internal server error"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if r.URL.Path != "/docs" {
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}
