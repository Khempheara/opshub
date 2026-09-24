package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/server"
	"github.com/opshub/opshub/internal/telemetry"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func newHandler(db server.Pinger) http.Handler {
	return server.New(server.Deps{
		Config:  config.Config{DefaultLocale: "en", DefaultTimezone: "Asia/Phnom_Penh", AllowSignup: true, RateLimitRPS: 100, RateLimitBurst: 100},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:      db,
		Metrics: telemetry.NewMetrics(),
		Version: "test",
	})
}

func do(h http.Handler, method, path string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Error.Code
}

func TestProbes(t *testing.T) {
	h := newHandler(fakeDB{})
	assert.Equal(t, http.StatusOK, do(h, http.MethodGet, "/healthz", nil).Code)
	assert.Equal(t, http.StatusOK, do(h, http.MethodGet, "/readyz", nil).Code)

	down := newHandler(fakeDB{err: errors.New("connection refused")})
	rec := do(down, http.MethodGet, "/readyz", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "SERVICE_UNAVAILABLE", errorCode(t, rec))
	assert.NotContains(t, rec.Body.String(), "connection refused", "internal cause must not leak")
	// Liveness must not depend on the database.
	assert.Equal(t, http.StatusOK, do(down, http.MethodGet, "/healthz", nil).Code)
}

func TestUnknownRouteAndMethodUseErrorEnvelope(t *testing.T) {
	h := newHandler(fakeDB{})
	rec := do(h, http.MethodGet, "/api/v1/nope", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "ROUTE_NOT_FOUND", errorCode(t, rec))

	rec = do(h, http.MethodDelete, "/api/v1/meta", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "METHOD_NOT_ALLOWED", errorCode(t, rec))
}

func TestMeta(t *testing.T) {
	rec := do(newHandler(fakeDB{}), http.MethodGet, "/api/v1/meta", map[string]string{"Accept-Language": "km-KH"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "km", rec.Header().Get("Content-Language"))
	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	var meta server.MetaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &meta))
	assert.Equal(t, server.MetaResponse{
		Version: "test", Locales: []string{"en", "km"}, DefaultLocale: "en", DefaultTimezone: "Asia/Phnom_Penh",
		SignupEnabled: true, SSOProviders: []string{},
	}, meta)
}

func TestRequestID(t *testing.T) {
	h := newHandler(fakeDB{})
	generated := do(h, http.MethodGet, "/healthz", nil).Header().Get("X-Request-Id")
	assert.Regexp(t, `^[0-9a-f-]{36}$`, generated, "must be a UUID, not hostname-derived")

	kept := do(h, http.MethodGet, "/healthz", map[string]string{"X-Request-Id": "edge-abc123XYZ"})
	assert.Equal(t, "edge-abc123XYZ", kept.Header().Get("X-Request-Id"))

	replaced := do(h, http.MethodGet, "/healthz", map[string]string{"X-Request-Id": "evil\r\nSet-Cookie: x"})
	assert.NotContains(t, replaced.Header().Get("X-Request-Id"), "evil")
}

func TestMetricsUseRoutePatterns(t *testing.T) {
	h := newHandler(fakeDB{})
	do(h, http.MethodGet, "/api/v1/meta", nil)
	body := do(h, http.MethodGet, "/metrics", nil).Body.String()
	assert.Contains(t, body, `opshub_http_requests_total{method="GET",route="/api/v1/meta",status="200"} 1`)
}

func TestDocs(t *testing.T) {
	h := newHandler(fakeDB{})
	rec := do(h, http.MethodGet, "/api/openapi.yaml", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "openapi: 3.1.0")

	rec = do(h, http.MethodGet, "/docs", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "script-src 'self' https://cdn.jsdelivr.net")
}
