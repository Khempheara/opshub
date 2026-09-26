package telemetry

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/testutil/pgtest"
)

func TestPlatformMetrics(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	var org uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, 'Metrics Co') RETURNING id`,
		"mx-"+uuid.NewString()[:8]).Scan(&org))
	runner := func(seen string, disabled bool) {
		_, err := pool.Exec(ctx, `INSERT INTO runners (organization_id, name, token_hash, token_prefix, last_seen_at, disabled_at)
			VALUES ($1, $2, $3, 'ohr_x', now() - $4::interval, CASE WHEN $5 THEN now() END)`,
			org, "r-"+uuid.NewString()[:6], []byte(uuid.NewString()), seen, disabled)
		require.NoError(t, err)
	}
	runner("5 seconds", false)
	runner("5 seconds", false)
	runner("10 minutes", false)
	runner("1 second", true)
	_, err := pool.Exec(ctx, `INSERT INTO alerts (organization_id, rule_name, rule_kind, severity, subject_type, subject_id, subject_name, status, started_at)
		VALUES ($1, 'Down', 'monitor_down', 'critical', 'monitor', $2, 'web', 'firing', now())`, org, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO river_job (kind, queue, state, args, max_attempts) VALUES
		('send_email', 'email', 'available', '{}', 3), ('send_email', 'email', 'available', '{}', 3),
		('monitor_checks', 'monitoring', 'running', '{}', 3)`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO river_job (kind, queue, state, args, max_attempts, finalized_at)
		VALUES ('notify', 'monitoring', 'discarded', '{}', 3, now())`)
	require.NoError(t, err)

	m := NewMetrics()
	m.RegisterPlatform(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.ObserveLogLines(5, 2)
	m.ObserveLogLines(1, 0)

	expected := `
# HELP opshub_runners Runners by status (online: seen in the last 30 s).
# TYPE opshub_runners gauge
opshub_runners{status="disabled"} 1
opshub_runners{status="offline"} 1
opshub_runners{status="online"} 2
# HELP opshub_alerts_firing Firing alerts by severity.
# TYPE opshub_alerts_firing gauge
opshub_alerts_firing{severity="critical"} 1
opshub_alerts_firing{severity="info"} 0
opshub_alerts_firing{severity="warning"} 0
# HELP opshub_background_jobs Background (River) jobs not finished yet, by queue and state.
# TYPE opshub_background_jobs gauge
opshub_background_jobs{queue="email",state="available"} 2
opshub_background_jobs{queue="monitoring",state="running"} 1
# HELP opshub_background_jobs_discarded_last_hour Background jobs that failed for good in the last hour, by queue.
# TYPE opshub_background_jobs_discarded_last_hour gauge
opshub_background_jobs_discarded_last_hour{queue="monitoring"} 1
# HELP opshub_pipeline_jobs Pipeline jobs queued for a runner or running.
# TYPE opshub_pipeline_jobs gauge
opshub_pipeline_jobs{status="queued"} 0
opshub_pipeline_jobs{status="running"} 0
# HELP opshub_deployments_running Deployments pending or running.
# TYPE opshub_deployments_running gauge
opshub_deployments_running 0
# HELP opshub_platform_metrics_up 1 when the platform figures could be read from the database.
# TYPE opshub_platform_metrics_up gauge
opshub_platform_metrics_up 1
# HELP opshub_log_lines_ingested_total Log lines sent with ingest tokens, accepted or rejected.
# TYPE opshub_log_lines_ingested_total counter
opshub_log_lines_ingested_total{result="accepted"} 6
opshub_log_lines_ingested_total{result="rejected"} 2
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry, strings.NewReader(expected),
		"opshub_runners", "opshub_alerts_firing", "opshub_background_jobs", "opshub_background_jobs_discarded_last_hour",
		"opshub_pipeline_jobs", "opshub_deployments_running", "opshub_platform_metrics_up", "opshub_log_lines_ingested_total"))

	// The pool figures are there, and /metrics serves everything.
	n, err := testutil.GatherAndCount(m.Registry, "opshub_db_pool_connections", "opshub_db_pool_max_connections")
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Contains(t, rec.Body.String(), "opshub_db_pool_acquires_total")
}

func TestPlatformMetricsDown(t *testing.T) {
	// A closed pool can't answer: the figures are skipped and up reports 0.
	closed, err := pgxpool.NewWithConfig(context.Background(), pgtest.Pool(t).Config())
	require.NoError(t, err)
	closed.Close()
	m2 := NewMetrics()
	m2.RegisterPlatform(closed, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, testutil.GatherAndCompare(m2.Registry, strings.NewReader(`
# HELP opshub_platform_metrics_up 1 when the platform figures could be read from the database.
# TYPE opshub_platform_metrics_up gauge
opshub_platform_metrics_up 0
`), "opshub_platform_metrics_up"))
	assert.Equal(t, "email", func() string { q, _ := splitQueueState("email/available"); return q }())
	assert.Equal(t, [2]string{"odd", ""}, func() [2]string { q, s := splitQueueState("odd"); return [2]string{q, s} }())
}
