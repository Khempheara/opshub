-- name: CreateAsset :one
INSERT INTO infra_assets (organization_id, kind, name, address, description, tags, metadata, tls_port, created_by)
VALUES (@organization_id, @kind, @name, @address, @description, @tags, @metadata, @tls_port, @created_by)
RETURNING *;

-- name: GetAsset :one
SELECT * FROM infra_assets WHERE id = @id;

-- name: ListAssets :many
SELECT a.*, c.not_after AS cert_not_after, coalesce(c.error, '')::text AS cert_error, c.last_checked_at AS cert_checked_at
FROM infra_assets a
LEFT JOIN ssl_certificates c ON c.asset_id = a.id
WHERE a.organization_id = @organization_id
  AND (sqlc.narg(kind)::text IS NULL OR a.kind::text = sqlc.narg(kind))
  AND (sqlc.narg(tag)::text IS NULL OR sqlc.narg(tag)::text = ANY(a.tags))
  AND (sqlc.narg(search)::text IS NULL OR a.name ILIKE '%' || sqlc.narg(search) || '%' OR a.address ILIKE '%' || sqlc.narg(search) || '%')
ORDER BY a.kind, a.name
LIMIT 1000;

-- name: UpdateAsset :one
UPDATE infra_assets SET name = @name, address = @address, description = @description, tags = @tags,
  metadata = @metadata, tls_port = @tls_port, version = version + 1
WHERE id = @id AND version = @version
RETURNING *;

-- name: DeleteAsset :execrows
DELETE FROM infra_assets WHERE id = @id;

-- name: SetAgentToken :one
UPDATE infra_assets SET agent_token_hash = @token_hash, agent_token_prefix = @token_prefix, version = version + 1
WHERE id = @id
RETURNING *;

-- name: GetAssetByAgentToken :one
SELECT * FROM infra_assets WHERE agent_token_hash = @token_hash;

-- name: RecordHeartbeat :exec
UPDATE infra_assets SET last_heartbeat_at = now(), agent_version = @agent_version, agent_hostname = @agent_hostname,
  agent_os = @agent_os, agent_arch = @agent_arch, last_metrics = @last_metrics
WHERE id = @id;

-- name: InsertMetric :exec
-- One sample per asset and second; a re-sent heartbeat within the same second is ignored.
INSERT INTO asset_metrics (asset_id, ts, cpu_pct, mem_pct, disk_pct, load1)
VALUES (@asset_id, date_trunc('second', now()), sqlc.narg(cpu_pct), sqlc.narg(mem_pct), sqlc.narg(disk_pct), sqlc.narg(load1))
ON CONFLICT (asset_id, ts) DO NOTHING;

-- name: RawMetricSeries :many
-- Averages and maxima per step (seconds) from the raw samples; -1 = no data in the bucket.
SELECT date_bin(make_interval(secs => @step_seconds::integer), ts, TIMESTAMPTZ '2000-01-01')::timestamptz AS bucket,
       coalesce(avg(cpu_pct), -1)::real AS cpu_avg, coalesce(max(cpu_pct), -1)::real AS cpu_max,
       coalesce(avg(mem_pct), -1)::real AS mem_avg, coalesce(max(mem_pct), -1)::real AS mem_max,
       coalesce(avg(disk_pct), -1)::real AS disk_avg, coalesce(max(disk_pct), -1)::real AS disk_max
FROM asset_metrics
WHERE asset_id = @asset_id AND ts >= @from_time AND ts < @to_time
GROUP BY bucket
ORDER BY bucket;

-- name: HourlyMetricSeries :many
-- The same from hourly rollups (for ranges beyond the raw retention); -1 = no data.
SELECT date_bin(make_interval(secs => @step_seconds::integer), hour, TIMESTAMPTZ '2000-01-01')::timestamptz AS bucket,
       coalesce(sum(cpu_avg * samples) FILTER (WHERE cpu_avg IS NOT NULL) / nullif(sum(samples) FILTER (WHERE cpu_avg IS NOT NULL), 0), -1)::real AS cpu_avg,
       coalesce(max(cpu_max), -1)::real AS cpu_max,
       coalesce(sum(mem_avg * samples) FILTER (WHERE mem_avg IS NOT NULL) / nullif(sum(samples) FILTER (WHERE mem_avg IS NOT NULL), 0), -1)::real AS mem_avg,
       coalesce(max(mem_max), -1)::real AS mem_max,
       coalesce(sum(disk_avg * samples) FILTER (WHERE disk_avg IS NOT NULL) / nullif(sum(samples) FILTER (WHERE disk_avg IS NOT NULL), 0), -1)::real AS disk_avg,
       coalesce(max(disk_max), -1)::real AS disk_max
FROM asset_metrics_hourly
WHERE asset_id = @asset_id AND hour >= @from_time AND hour < @to_time
GROUP BY bucket
ORDER BY bucket;

-- name: RollupMetrics :execrows
-- Summarizes raw samples of whole hours in [from, to) into hourly rows (idempotent).
INSERT INTO asset_metrics_hourly (asset_id, hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_avg, disk_max, samples)
SELECT asset_id, date_trunc('hour', ts), avg(cpu_pct), max(cpu_pct), avg(mem_pct), max(mem_pct), avg(disk_pct), max(disk_pct), count(*)
FROM asset_metrics
WHERE ts >= @from_time AND ts < @to_time
GROUP BY asset_id, date_trunc('hour', ts)
ON CONFLICT (asset_id, hour) DO UPDATE SET
  cpu_avg = EXCLUDED.cpu_avg, cpu_max = EXCLUDED.cpu_max, mem_avg = EXCLUDED.mem_avg, mem_max = EXCLUDED.mem_max,
  disk_avg = EXCLUDED.disk_avg, disk_max = EXCLUDED.disk_max, samples = EXCLUDED.samples;

-- name: DeleteOldHourlyMetrics :execrows
DELETE FROM asset_metrics_hourly WHERE hour < @before;

-- name: MaintainMetricPartitions :exec
SELECT opshub_maintain_metric_partitions(@keep_months::integer);

-- name: DueCertificateChecks :many
-- Domain assets whose certificate is due for a check (or was never checked).
SELECT a.id, a.address, a.tls_port FROM infra_assets a
LEFT JOIN ssl_certificates c ON c.asset_id = a.id
WHERE a.kind = 'domain' AND a.address <> '' AND (c.asset_id IS NULL OR c.next_check_at <= now())
ORDER BY c.next_check_at NULLS FIRST
LIMIT 200;

-- name: UpsertCertificate :one
INSERT INTO ssl_certificates (asset_id, host, port, subject, issuer, dns_names, serial, fingerprint, not_before, not_after,
  error, last_checked_at, next_check_at)
VALUES (@asset_id, @host, @port, @subject, @issuer, @dns_names, @serial, @fingerprint, sqlc.narg(not_before),
  sqlc.narg(not_after), @error, now(), @next_check_at)
ON CONFLICT (asset_id) DO UPDATE SET host = EXCLUDED.host, port = EXCLUDED.port, subject = EXCLUDED.subject,
  issuer = EXCLUDED.issuer, dns_names = EXCLUDED.dns_names, serial = EXCLUDED.serial, fingerprint = EXCLUDED.fingerprint,
  -- A failed connection keeps what was known about the certificate.
  not_before = coalesce(EXCLUDED.not_before, ssl_certificates.not_before),
  not_after = coalesce(EXCLUDED.not_after, ssl_certificates.not_after),
  error = EXCLUDED.error, last_checked_at = now(), next_check_at = EXCLUDED.next_check_at
RETURNING *;

-- name: GetCertificate :one
SELECT * FROM ssl_certificates WHERE asset_id = @asset_id;

-- name: DeleteCertificate :exec
DELETE FROM ssl_certificates WHERE asset_id = @asset_id;

-- name: ListCertificates :many
-- Certificates of the organization's domains, soonest expiry first; expiring_before (optional)
-- keeps those expiring earlier or failing.
SELECT sqlc.embed(c), a.name AS asset_name, a.tags AS asset_tags
FROM ssl_certificates c
JOIN infra_assets a ON a.id = c.asset_id
WHERE a.organization_id = @organization_id
  AND (sqlc.narg(expiring_before)::timestamptz IS NULL OR c.not_after < sqlc.narg(expiring_before) OR c.error <> '')
ORDER BY c.not_after NULLS FIRST, a.name;
