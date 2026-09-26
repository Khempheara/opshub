package main

import (
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/opshub/opshub/api"
	"github.com/opshub/opshub/internal/alert"
	"github.com/opshub/opshub/internal/auth"
	"github.com/opshub/opshub/internal/deploy"
	"github.com/opshub/opshub/internal/infra"
	"github.com/opshub/opshub/internal/logs"
	"github.com/opshub/opshub/internal/monitor"
	"github.com/opshub/opshub/internal/notify"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/runners"
	"github.com/opshub/opshub/internal/secret"
	"github.com/opshub/opshub/internal/server"
)

// TestOpenAPIMatchesRoutes keeps api/openapi.yaml (and the generated client) in step with
// the router: every mounted route must be documented, and every documented /api/v1
// operation must exist.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(api.OpenAPISpec, &spec))
	documented := map[string]bool{}
	for path, ops := range spec.Paths {
		if !strings.HasPrefix(path, "/api/v1/") || path == "/api/v1/meta" {
			continue
		}
		for method := range ops {
			if method != "parameters" {
				documented[strings.ToUpper(method)+" "+path] = true
			}
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := chi.NewRouter()
	for _, m := range []server.Module{
		auth.NewHandler(nil, nil, nil, auth.HandlerConfig{}, logger),
		org.NewHandler(nil),
		project.NewHandler(nil, func(h http.Handler) http.Handler { return h }),
		pipeline.NewHandler(nil, nil, func(h http.Handler) http.Handler { return h }),
		runners.NewHandler(nil),
		deploy.NewHandler(nil, nil, func(h http.Handler) http.Handler { return h }),
		infra.NewHandler(nil),
		secret.NewHandler(nil),
		monitor.NewHandler(nil),
		alert.NewHandler(nil),
		notify.NewHandler(nil),
		logs.NewHandler(nil),
	} {
		m.Mount(r)
	}
	mounted := map[string]bool{}
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		path := "/api/v1" + strings.TrimSuffix(route, "/")
		mounted[method+" "+path] = true
		return nil
	}))

	var undocumented, missing []string
	for k := range mounted {
		if !documented[k] {
			undocumented = append(undocumented, k)
		}
	}
	for k := range documented {
		if !mounted[k] {
			missing = append(missing, k)
		}
	}
	slices.Sort(undocumented)
	slices.Sort(missing)
	assert.Empty(t, undocumented, "routes missing from api/openapi.yaml")
	assert.Empty(t, missing, "documented operations with no route")
}
