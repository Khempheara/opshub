// Package telemetry wires Prometheus metrics and OpenTelemetry tracing for the platform itself.
package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Metrics owns a private registry so tests can create isolated instances.
type Metrics struct {
	Registry     *prometheus.Registry
	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
}

func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := &Metrics{
		Registry: reg,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "opshub", Name: "http_requests_total", Help: "HTTP requests by route and status.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "opshub", Name: "http_request_duration_seconds", Help: "HTTP request latency by route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	reg.MustRegister(m.httpRequests, m.httpDuration)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}

// Middleware records request metrics labelled by chi route pattern (not raw path, to keep
// cardinality bounded) and renames the active span to "METHOD /pattern".
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		route := RoutePattern(r)
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		m.httpRequests.WithLabelValues(r.Method, route, strconv.Itoa(status)).Inc()
		m.httpDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		trace.SpanFromContext(r.Context()).SetName(r.Method + " " + route)
	})
}

// RoutePattern returns the matched chi pattern, or "unmatched".
func RoutePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}

// SetupTracing installs a global OTLP/HTTP tracer provider when endpoint is set
// (exporter options come from the standard OTEL_* env vars). It always installs the
// W3C propagator. The returned func flushes spans on shutdown.
func SetupTracing(ctx context.Context, endpoint, serviceName, version string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}
	// Schemaless attributes avoid schema-URL conflicts with resource.Default().
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", serviceName), attribute.String("service.version", version)))
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
