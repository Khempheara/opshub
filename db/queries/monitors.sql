-- name: CreateMonitor :one
INSERT INTO monitors (organization_id, name, kind, target, interval_seconds, timeout_ms, config, labels, enabled, created_by)
VALUES (@organization_id, @name, @kind, @target, @interval_seconds, @timeout_ms, @config, @labels, @enabled, sqlc.narg(created_by))
RETURNING *;

-- name: GetMonitor :one
SELECT * FROM monitors WHERE id = @id;

-- name: ListMonitors :many
SELECT * FROM monitors
WHERE organization_id = @organization_id
  AND (sqlc.narg(label)::text IS NULL OR sqlc.narg(label) = ANY (labels))
  AND (sqlc.narg(search)::text IS NULL OR name ILIKE '%' || sqlc.narg(search) || '%' OR target ILIKE '%' || sqlc.narg(search) || '%')
ORDER BY name;

-- name: UpdateMonitor :one
-- Edits check again right away and forget the old state when what is checked changed.
UPDATE monitors SET name = @name, target = @target, interval_seconds = @interval_seconds, timeout_ms = @timeout_ms,
  config = @config, labels = @labels, enabled = @enabled, next_check_at = now(), version = version + 1,
  last_up = CASE WHEN target = @target AND config = @config THEN last_up END,
  down_since = CASE WHEN target = @target AND config = @config THEN down_since END,
  last_error = CASE WHEN target = @target AND config = @config THEN last_error ELSE '' END
WHERE id = @id AND version = @expected_version
RETURNING *;

-- name: DeleteMonitor :execrows
DELETE FROM monitors WHERE id = @id;

-- name: ClaimDueMonitors :many
-- Takes due monitors and schedules their next check, so concurrent workers never run the
-- same check twice.
UPDATE monitors SET next_check_at = now() + make_interval(secs => interval_seconds)
WHERE id IN (
  SELECT id FROM monitors WHERE enabled AND next_check_at <= now()
  ORDER BY next_check_at LIMIT @max_monitors FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: InsertMonitorResult :exec
INSERT INTO monitor_results (monitor_id, ts, up, latency_ms, status_code, error)
VALUES (@monitor_id, date_trunc('milliseconds', now()), @up, sqlc.narg(latency_ms), sqlc.narg(status_code), @error)
ON CONFLICT DO NOTHING;

-- name: SetMonitorState :exec
UPDATE monitors SET last_up = @up, last_checked_at = now(), last_latency_ms = sqlc.narg(latency_ms), last_error = @error,
  down_since = CASE WHEN @up::boolean THEN NULL ELSE coalesce(down_since, now()) END
WHERE id = @id;

-- name: RawMonitorSeries :many
-- Buckets of step_seconds; latency is averaged over successful checks. -1 = no value.
SELECT date_bin(make_interval(secs => @step_seconds::integer), ts, @from_ts::timestamptz)::timestamptz AS bucket,
       count(*)::integer AS checks,
       count(*) FILTER (WHERE up)::integer AS up_checks,
       coalesce(avg(latency_ms) FILTER (WHERE up), -1)::real AS latency_avg,
       coalesce(max(latency_ms) FILTER (WHERE up), -1)::integer AS latency_max
FROM monitor_results
WHERE monitor_id = @monitor_id AND ts >= @from_ts AND ts < @to_ts
GROUP BY bucket ORDER BY bucket;

-- name: HourlyMonitorSeries :many
SELECT date_bin(make_interval(secs => @step_seconds::integer), hour, @from_ts::timestamptz)::timestamptz AS bucket,
       sum(checks)::integer AS checks,
       sum(up_checks)::integer AS up_checks,
       coalesce(sum(latency_avg * up_checks) / nullif(sum(up_checks) FILTER (WHERE latency_avg IS NOT NULL), 0), -1)::real AS latency_avg,
       coalesce(max(latency_max), -1)::integer AS latency_max
FROM monitor_results_hourly
WHERE monitor_id = @monitor_id AND hour >= @from_ts AND hour < @to_ts
GROUP BY bucket ORDER BY bucket;

-- name: RecentMonitorResults :many
SELECT ts, up, latency_ms, status_code, error FROM monitor_results
WHERE monitor_id = @monitor_id ORDER BY ts DESC LIMIT @max_results;

-- name: RollupMonitorResults :exec
-- Rolls up the whole hours in [from_ts, to_ts); re-running replaces them.
INSERT INTO monitor_results_hourly (monitor_id, hour, checks, up_checks, latency_avg, latency_max)
SELECT monitor_id, date_trunc('hour', ts), count(*), count(*) FILTER (WHERE up),
       avg(latency_ms) FILTER (WHERE up), max(latency_ms) FILTER (WHERE up)
FROM monitor_results
WHERE ts >= @from_ts AND ts < @to_ts
GROUP BY monitor_id, date_trunc('hour', ts)
ON CONFLICT (monitor_id, hour) DO UPDATE SET checks = EXCLUDED.checks, up_checks = EXCLUDED.up_checks,
  latency_avg = EXCLUDED.latency_avg, latency_max = EXCLUDED.latency_max;

-- name: DeleteOldMonitorHourly :execrows
DELETE FROM monitor_results_hourly WHERE hour < @before;

-- name: MaintainMonitorPartitions :exec
SELECT opshub_maintain_monitor_partitions(@keep_months::integer);

-- name: MonitorUptime24h :many
SELECT monitor_id, count(*)::integer AS checks, count(*) FILTER (WHERE up)::integer AS up_checks
FROM monitor_results
WHERE monitor_id IN (SELECT id FROM monitors WHERE organization_id = @organization_id) AND ts >= now() - interval '24 hours'
GROUP BY monitor_id;
