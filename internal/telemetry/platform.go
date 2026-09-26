package telemetry

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// platformQueryTimeout bounds the database reads made during one scrape.
const platformQueryTimeout = 3 * time.Second

// RunnerOnlineWithin mirrors runners.OfflineAfter: a runner is online when it was seen this
// recently.
const RunnerOnlineWithin = 30 * time.Second

// platform reports OpsHub's own state at each scrape: job queues, pipeline jobs waiting for
// runners, runners, running deployments, firing alerts and the database connection pool. The
// figures are organization-wide and read from the database, so every API replica reports the
// same values: aggregate them with max(), not sum().
type platform struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	riverJobs     *prometheus.Desc
	riverFailed   *prometheus.Desc
	pipelineJobs  *prometheus.Desc
	runners       *prometheus.Desc
	deployments   *prometheus.Desc
	alerts        *prometheus.Desc
	up            *prometheus.Desc
	poolConns     *prometheus.Desc
	poolMax       *prometheus.Desc
	poolAcquires  *prometheus.Desc
	poolWaits     *prometheus.Desc
	poolWaitTotal *prometheus.Desc
}

// RegisterPlatform adds the platform collector (reading pool) to m.
func (m *Metrics) RegisterPlatform(pool *pgxpool.Pool, logger *slog.Logger) {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc("opshub_"+name, help, labels, nil)
	}
	m.Registry.MustRegister(&platform{
		pool: pool, logger: logger,
		riverJobs:     d("background_jobs", "Background (River) jobs not finished yet, by queue and state.", "queue", "state"),
		riverFailed:   d("background_jobs_discarded_last_hour", "Background jobs that failed for good in the last hour, by queue.", "queue"),
		pipelineJobs:  d("pipeline_jobs", "Pipeline jobs queued for a runner or running.", "status"),
		runners:       d("runners", "Runners by status (online: seen in the last 30 s).", "status"),
		deployments:   d("deployments_running", "Deployments pending or running."),
		alerts:        d("alerts_firing", "Firing alerts by severity.", "severity"),
		up:            d("platform_metrics_up", "1 when the platform figures could be read from the database."),
		poolConns:     d("db_pool_connections", "Database pool connections by state.", "state"),
		poolMax:       d("db_pool_max_connections", "Largest number of database connections the pool opens."),
		poolAcquires:  d("db_pool_acquires_total", "Connections taken from the pool."),
		poolWaits:     d("db_pool_empty_acquires_total", "Acquires that had to wait because every connection was busy."),
		poolWaitTotal: d("db_pool_acquire_seconds_total", "Total time spent acquiring connections."),
	})
}

func (p *platform) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		p.riverJobs, p.riverFailed, p.pipelineJobs, p.runners, p.deployments, p.alerts, p.up,
		p.poolConns, p.poolMax, p.poolAcquires, p.poolWaits, p.poolWaitTotal,
	} {
		ch <- d
	}
}

// platformQuery reads every figure in one round trip, as (metric, label, value) rows.
const platformQuery = `
SELECT 'river', queue || '/' || state::text, count(*) FROM river_job
  WHERE state IN ('available', 'running', 'retryable', 'scheduled') GROUP BY queue, state
UNION ALL
SELECT 'river_discarded', queue, count(*) FROM river_job
  WHERE state = 'discarded' AND finalized_at > now() - interval '1 hour' GROUP BY queue
UNION ALL
SELECT 'pipeline_jobs', status::text, count(*) FROM pipeline_jobs WHERE status IN ('queued', 'running') GROUP BY status
UNION ALL
SELECT 'runners', CASE WHEN disabled_at IS NOT NULL THEN 'disabled'
                       WHEN last_seen_at > now() - make_interval(secs => $1) THEN 'online'
                       ELSE 'offline' END, count(*) FROM runners GROUP BY 2
UNION ALL
SELECT 'deployments', '', count(*) FROM deployments WHERE status IN ('pending', 'running')
UNION ALL
SELECT 'alerts', severity::text, count(*) FROM alerts WHERE status = 'firing' GROUP BY severity`

func (p *platform) Collect(ch chan<- prometheus.Metric) {
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	counter := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v)
	}

	st := p.pool.Stat()
	gauge(p.poolConns, float64(st.AcquiredConns()), "acquired")
	gauge(p.poolConns, float64(st.IdleConns()), "idle")
	gauge(p.poolConns, float64(st.ConstructingConns()), "constructing")
	gauge(p.poolMax, float64(st.MaxConns()))
	counter(p.poolAcquires, float64(st.AcquireCount()))
	counter(p.poolWaits, float64(st.EmptyAcquireCount()))
	counter(p.poolWaitTotal, st.AcquireDuration().Seconds())

	ctx, cancel := context.WithTimeout(context.Background(), platformQueryTimeout)
	defer cancel()
	rows, err := p.pool.Query(ctx, platformQuery, RunnerOnlineWithin.Seconds())
	if err != nil {
		p.logger.Warn("platform metrics unavailable", "err", err)
		gauge(p.up, 0)
		return
	}
	defer rows.Close()
	// Known label values are always reported (as 0 when absent), so dashboards and alerts see
	// a series instead of nothing.
	pipeline := map[string]float64{"queued": 0, "running": 0}
	runners := map[string]float64{"online": 0, "offline": 0, "disabled": 0}
	alerts := map[string]float64{"info": 0, "warning": 0, "critical": 0}
	var deployments float64
	for rows.Next() {
		var kind, label string
		var n int64
		if err := rows.Scan(&kind, &label, &n); err != nil {
			p.logger.Warn("platform metrics unavailable", "err", err)
			gauge(p.up, 0)
			return
		}
		v := float64(n)
		switch kind {
		case "river":
			queue, state := splitQueueState(label)
			gauge(p.riverJobs, v, queue, state)
		case "river_discarded":
			gauge(p.riverFailed, v, label)
		case "pipeline_jobs":
			pipeline[label] = v
		case "runners":
			runners[label] = v
		case "deployments":
			deployments = v
		case "alerts":
			alerts[label] = v
		}
	}
	if err := rows.Err(); err != nil {
		p.logger.Warn("platform metrics unavailable", "err", err)
		gauge(p.up, 0)
		return
	}
	for status, v := range pipeline {
		gauge(p.pipelineJobs, v, status)
	}
	for status, v := range runners {
		gauge(p.runners, v, status)
	}
	for severity, v := range alerts {
		gauge(p.alerts, v, severity)
	}
	gauge(p.deployments, deployments)
	gauge(p.up, 1)
}

// splitQueueState splits "queue/state" (queue names never contain "/").
func splitQueueState(s string) (string, string) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}
