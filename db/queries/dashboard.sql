-- Dashboard (Module 12): pipeline statistics and DORA metrics, computed on request.
-- Every query takes the organization, the caller's project visibility (see_all or the
-- visible project_ids), an optional project, and the range [from_ts, to_ts).
-- Durations are in seconds; medians and p95 are -1 when there is nothing to measure.

-- name: PipelineSummary :one
-- Runs created in the range. Success rate and durations use finished runs.
SELECT count(*)::int AS runs,
       count(*) FILTER (WHERE status = 'succeeded')::int AS succeeded,
       count(*) FILTER (WHERE status = 'failed')::int AS failed,
       count(*) FILTER (WHERE status = 'canceled')::int AS canceled,
       COALESCE((percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - started_at))
         FILTER (WHERE status IN ('succeeded', 'failed') AND started_at IS NOT NULL AND finished_at IS NOT NULL)), -1)::float8 AS duration_p50,
       COALESCE((percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - started_at))
         FILTER (WHERE status IN ('succeeded', 'failed') AND started_at IS NOT NULL AND finished_at IS NOT NULL)), -1)::float8 AS duration_p95
FROM pipeline_runs
WHERE organization_id = @organization_id AND created_at >= @from_ts AND created_at < @to_ts
  AND (@see_all::boolean OR project_id = ANY (@project_ids::uuid[]))
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id));

-- name: PipelineTrend :many
-- Runs per day or week (bucket) in the time zone tz, oldest first.
SELECT to_char(date_trunc(@bucket::text, created_at AT TIME ZONE @tz::text), 'YYYY-MM-DD')::text AS period,
       count(*)::int AS runs,
       count(*) FILTER (WHERE status = 'succeeded')::int AS succeeded,
       count(*) FILTER (WHERE status = 'failed')::int AS failed,
       COALESCE((percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - started_at))
         FILTER (WHERE status IN ('succeeded', 'failed') AND started_at IS NOT NULL AND finished_at IS NOT NULL)), -1)::float8 AS duration_p50,
       COALESCE((percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - started_at))
         FILTER (WHERE status IN ('succeeded', 'failed') AND started_at IS NOT NULL AND finished_at IS NOT NULL)), -1)::float8 AS duration_p95
FROM pipeline_runs
WHERE organization_id = @organization_id AND created_at >= @from_ts AND created_at < @to_ts
  AND (@see_all::boolean OR project_id = ANY (@project_ids::uuid[]))
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id))
GROUP BY 1
ORDER BY 1;

-- name: PipelineByProject :many
-- Per project, busiest first (at most 50), with its latest run in the range.
SELECT p.id, p.name, p.slug,
       count(*)::int AS runs,
       count(*) FILTER (WHERE r.status = 'succeeded')::int AS succeeded,
       count(*) FILTER (WHERE r.status = 'failed')::int AS failed,
       COALESCE((percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM r.finished_at - r.started_at))
         FILTER (WHERE r.status IN ('succeeded', 'failed') AND r.started_at IS NOT NULL AND r.finished_at IS NOT NULL)), -1)::float8 AS duration_p50,
       (array_agg(r.status::text ORDER BY r.created_at DESC))[1]::text AS last_status,
       max(r.created_at)::timestamptz AS last_run_at
FROM pipeline_runs r
JOIN projects p ON p.id = r.project_id AND p.deleted_at IS NULL
WHERE r.organization_id = @organization_id AND r.created_at >= @from_ts AND r.created_at < @to_ts
  AND (@see_all::boolean OR r.project_id = ANY (@project_ids::uuid[]))
  AND (sqlc.narg(project_id)::uuid IS NULL OR r.project_id = sqlc.narg(project_id))
GROUP BY p.id, p.name, p.slug
ORDER BY runs DESC, p.name
LIMIT 50;

-- name: DoraSummary :one
-- Changes are finished deployments started in the range to production environments (or to
-- environment_id), not counting rollbacks. A change failed when it failed or was rolled back
-- later. Lead time runs from the commit (or, without one, the run's creation) to the finished
-- deployment of a successful change made by a pipeline.
WITH changes AS (
  SELECT d.id, d.status, d.finished_at,
         EXISTS (SELECT 1 FROM deployments rb WHERE rb.rollback_of_id = d.id) AS rolled_back,
         CASE WHEN d.status = 'succeeded' AND r.id IS NOT NULL AND d.finished_at IS NOT NULL
              THEN GREATEST(extract(epoch FROM d.finished_at - COALESCE(r.committed_at, r.created_at)), 0) END AS lead_s
  FROM deployments d
  JOIN environments e ON e.id = d.environment_id
  LEFT JOIN pipeline_runs r ON r.id = d.run_id
  WHERE d.organization_id = @organization_id AND d.created_at >= @from_ts AND d.created_at < @to_ts
    AND d.rollback_of_id IS NULL AND d.status IN ('succeeded', 'failed')
    AND (CASE WHEN sqlc.narg(environment_id)::uuid IS NULL THEN e.kind = 'production' ELSE d.environment_id = sqlc.narg(environment_id) END)
    AND (@see_all::boolean OR d.project_id = ANY (@project_ids::uuid[]))
    AND (sqlc.narg(project_id)::uuid IS NULL OR d.project_id = sqlc.narg(project_id))
)
SELECT count(*)::int AS changes,
       count(*) FILTER (WHERE status = 'succeeded')::int AS succeeded,
       count(*) FILTER (WHERE status = 'failed' OR rolled_back)::int AS failed_changes,
       count(lead_s)::int AS lead_samples,
       COALESCE((percentile_cont(0.5) WITHIN GROUP (ORDER BY lead_s) FILTER (WHERE lead_s IS NOT NULL)), -1)::float8 AS lead_p50,
       COALESCE((percentile_cont(0.95) WITHIN GROUP (ORDER BY lead_s) FILTER (WHERE lead_s IS NOT NULL)), -1)::float8 AS lead_p95
FROM changes;

-- name: DoraRestores :one
-- Time to restore, per failed change: a failed deployment counts from its start until it was
-- reverted automatically or the next successful deployment to that environment finished; a
-- change rolled back later counts from when it went live until the next successful deployment
-- (normally the rollback). Changes not restored yet are counted as open.
WITH failures AS (
  SELECT d.environment_id,
         CASE WHEN d.status = 'failed' THEN COALESCE(d.started_at, d.created_at) ELSE d.finished_at END AS began,
         CASE WHEN d.status = 'failed' AND d.reverted THEN d.finished_at
              ELSE (SELECT min(n.finished_at) FROM deployments n
                    WHERE n.environment_id = d.environment_id AND n.status = 'succeeded' AND n.id <> d.id
                      AND n.finished_at > COALESCE(d.finished_at, d.created_at)) END AS restored
  FROM deployments d
  JOIN environments e ON e.id = d.environment_id
  WHERE d.organization_id = @organization_id AND d.created_at >= @from_ts AND d.created_at < @to_ts
    AND d.rollback_of_id IS NULL
    AND (d.status = 'failed' OR (d.status = 'succeeded' AND EXISTS (SELECT 1 FROM deployments rb WHERE rb.rollback_of_id = d.id)))
    AND (CASE WHEN sqlc.narg(environment_id)::uuid IS NULL THEN e.kind = 'production' ELSE d.environment_id = sqlc.narg(environment_id) END)
    AND (@see_all::boolean OR d.project_id = ANY (@project_ids::uuid[]))
    AND (sqlc.narg(project_id)::uuid IS NULL OR d.project_id = sqlc.narg(project_id))
)
SELECT count(restored)::int AS restored,
       (count(*) - count(restored))::int AS open,
       COALESCE((percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM restored - began)) FILTER (WHERE restored IS NOT NULL)), -1)::float8 AS restore_p50
FROM failures;

-- name: DeploymentTrend :many
-- Changes per day or week (bucket) in the time zone tz, oldest first, split into those that
-- succeeded and stayed, and those that failed or were rolled back later.
SELECT to_char(date_trunc(@bucket::text, d.created_at AT TIME ZONE @tz::text), 'YYYY-MM-DD')::text AS period,
       count(*) FILTER (WHERE d.status = 'succeeded' AND NOT EXISTS (SELECT 1 FROM deployments rb WHERE rb.rollback_of_id = d.id))::int AS succeeded,
       count(*) FILTER (WHERE d.status = 'failed' OR EXISTS (SELECT 1 FROM deployments rb WHERE rb.rollback_of_id = d.id))::int AS failed_changes
FROM deployments d
JOIN environments e ON e.id = d.environment_id
WHERE d.organization_id = @organization_id AND d.created_at >= @from_ts AND d.created_at < @to_ts
  AND d.rollback_of_id IS NULL AND d.status IN ('succeeded', 'failed')
  AND (CASE WHEN sqlc.narg(environment_id)::uuid IS NULL THEN e.kind = 'production' ELSE d.environment_id = sqlc.narg(environment_id) END)
  AND (@see_all::boolean OR d.project_id = ANY (@project_ids::uuid[]))
  AND (sqlc.narg(project_id)::uuid IS NULL OR d.project_id = sqlc.narg(project_id))
GROUP BY 1
ORDER BY 1;

-- name: DoraByProject :many
-- Per project: changes, failed changes and median lead time, busiest first (at most 50).
SELECT p.id, p.name, p.slug,
       count(*)::int AS changes,
       count(*) FILTER (WHERE d.status = 'failed' OR EXISTS (SELECT 1 FROM deployments rb WHERE rb.rollback_of_id = d.id))::int AS failed_changes,
       COALESCE((percentile_cont(0.5) WITHIN GROUP (ORDER BY GREATEST(extract(epoch FROM d.finished_at - COALESCE(r.committed_at, r.created_at)), 0))
         FILTER (WHERE d.status = 'succeeded' AND r.id IS NOT NULL AND d.finished_at IS NOT NULL)), -1)::float8 AS lead_p50,
       COALESCE(max(d.finished_at), max(d.created_at))::timestamptz AS last_deployed_at
FROM deployments d
JOIN environments e ON e.id = d.environment_id
JOIN projects p ON p.id = d.project_id AND p.deleted_at IS NULL
LEFT JOIN pipeline_runs r ON r.id = d.run_id
WHERE d.organization_id = @organization_id AND d.created_at >= @from_ts AND d.created_at < @to_ts
  AND d.rollback_of_id IS NULL AND d.status IN ('succeeded', 'failed')
  AND (CASE WHEN sqlc.narg(environment_id)::uuid IS NULL THEN e.kind = 'production' ELSE d.environment_id = sqlc.narg(environment_id) END)
  AND (@see_all::boolean OR d.project_id = ANY (@project_ids::uuid[]))
  AND (sqlc.narg(project_id)::uuid IS NULL OR d.project_id = sqlc.narg(project_id))
GROUP BY p.id, p.name, p.slug
ORDER BY changes DESC, p.name
LIMIT 50;

-- name: AlertRestores :one
-- Alerts that stopped firing in the range (organization-wide; alerts aren't tied to
-- projects): how many, and the median time from firing to resolved.
SELECT count(*)::int AS resolved,
       COALESCE((percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM resolved_at - started_at))), -1)::float8 AS resolve_p50
FROM alerts
WHERE organization_id = @organization_id AND status = 'resolved' AND started_at IS NOT NULL
  AND resolved_at >= @from_ts AND resolved_at < @to_ts;
