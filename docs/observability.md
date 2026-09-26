# Monitoring OpsHub itself

The API serves Prometheus metrics at `/metrics`. Keep it on an internal network: the bundled
nginx and the Helm Ingress don't expose it.

## Metrics

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `opshub_http_requests_total` | counter | method, route, status | Requests by route pattern |
| `opshub_http_request_duration_seconds` | histogram | method, route | Latency |
| `opshub_background_jobs` | gauge | queue, state | River jobs not finished yet (available, running, retryable, scheduled) |
| `opshub_background_jobs_discarded_last_hour` | gauge | queue | Jobs that used up their attempts in the last hour |
| `opshub_pipeline_jobs` | gauge | status | Pipeline jobs queued for a runner or running |
| `opshub_runners` | gauge | status | Runners online (seen in the last 30 s), offline, disabled |
| `opshub_deployments_running` | gauge | | Deployments pending or running |
| `opshub_alerts_firing` | gauge | severity | Firing alerts |
| `opshub_log_lines_ingested_total` | counter | result | Log lines sent with ingest tokens, accepted or rejected |
| `opshub_platform_metrics_up` | gauge | | 1 when the figures above could be read from the database |
| `opshub_db_pool_connections` | gauge | state | Pool connections: acquired, idle, constructing |
| `opshub_db_pool_max_connections` | gauge | | Pool size (`OPSHUB_DB_MAX_CONNS`) |
| `opshub_db_pool_acquires_total` / `opshub_db_pool_empty_acquires_total` | counter | | Acquires, and acquires that had to wait |
| `opshub_db_pool_acquire_seconds_total` | counter | | Time spent acquiring |
| Go runtime and process metrics | | | `go_*`, `process_*` |

Some figures are read from the database at each scrape, and every API replica reports the same
values: the background jobs, pipeline jobs, runners, deployments and alerts. Aggregate them with
`max()`, not `sum()`. The HTTP and pool metrics are per instance.

## Dashboards

Grafana (`http://localhost:3001` in the compose stack) has two dashboards in the **OpsHub**
folder:

- **OpsHub API:**
  - requests per second and p95 latency by route (target < 200 ms);
  - 5xx ratio and 4xx by status;
  - goroutines, heap, CPU.
- **OpsHub Platform:**
  - health (metrics readable, firing alerts, runners, running deployments);
  - background jobs waiting, running and failed per queue, and pipeline jobs waiting for a
    runner;
  - log lines ingested per second and the rejected share;
  - database pool connections against the maximum, how often acquires wait, and the average
    wait.

The JSON is in `deploy/compose/grafana/dashboards/`. Import it into any Grafana with a
Prometheus data source whose uid is `prometheus`.

## Suggested alerts

```yaml
groups:
  - name: opshub
    rules:
      - alert: OpsHubDown
        expr: up{service="opshub-api"} == 0
        for: 2m
      - alert: OpsHubHighErrorRate
        expr: sum(rate(opshub_http_requests_total{status=~"5.."}[5m])) / sum(rate(opshub_http_requests_total[5m])) > 0.02
        for: 10m
      - alert: OpsHubSlow
        expr: histogram_quantile(0.95, sum by (le) (rate(opshub_http_request_duration_seconds_bucket{route!~"/api/v1/.*/stream|/api/v1/.*/events"}[5m]))) > 0.5
        for: 15m
      - alert: OpsHubBackgroundJobsPilingUp
        expr: max by (queue) (opshub_background_jobs{state=~"available|retryable"}) > 100
        for: 15m
      - alert: OpsHubJobsFailing
        expr: max by (queue) (opshub_background_jobs_discarded_last_hour) > 0
      - alert: OpsHubNoRunnersOnline
        expr: max(opshub_runners{status="online"}) == 0 and max(opshub_pipeline_jobs{status="queued"}) > 0
        for: 10m
      - alert: OpsHubDatabasePoolExhausted
        expr: sum by (instance) (opshub_db_pool_connections{state="acquired"}) >= max by (instance) (opshub_db_pool_max_connections)
        for: 5m
      - alert: OpsHubPlatformMetricsDown
        expr: min(opshub_platform_metrics_up) == 0
        for: 5m
```

For backups, alert when the `last-success` file in the backup directory is older than 26 hours,
or when the Kubernetes CronJob's last successful time is ([backup.md](backup.md)).

Tracing: set `OTEL_EXPORTER_OTLP_ENDPOINT` to send OpenTelemetry traces (spans are named
`METHOD /route`).
