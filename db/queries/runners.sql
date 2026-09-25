-- name: CreateRegistrationToken :one
INSERT INTO runner_registration_tokens (organization_id, token_hash, labels, created_by, expires_at)
VALUES (@organization_id, @token_hash, @labels, @created_by, @expires_at)
RETURNING *;

-- name: UseRegistrationToken :one
-- Consumes an unused, unexpired token (one-time).
UPDATE runner_registration_tokens SET used_at = now()
WHERE token_hash = @token_hash AND used_at IS NULL AND expires_at > now()
RETURNING *;

-- name: LinkRegistrationToken :exec
UPDATE runner_registration_tokens SET runner_id = @runner_id WHERE id = @id;

-- name: CreateRunner :one
INSERT INTO runners (organization_id, name, labels, token_hash, token_prefix, version, os, arch, max_concurrency, created_by, last_seen_at)
VALUES (@organization_id, @name, @labels, @token_hash, @token_prefix, @version, @os, @arch, @max_concurrency, @created_by, now())
RETURNING *;

-- name: GetRunnerByTokenHash :one
SELECT * FROM runners WHERE token_hash = @token_hash;

-- name: GetRunner :one
SELECT * FROM runners WHERE id = @id;

-- name: ListRunners :many
SELECT r.*,
       (SELECT count(*) FROM pipeline_jobs j WHERE j.runner_id = r.id AND j.status = 'running')::bigint AS running_jobs
FROM runners r
WHERE r.organization_id = @organization_id
ORDER BY r.name, r.id;

-- name: UpdateRunner :one
UPDATE runners SET name = @name, labels = @labels, max_concurrency = @max_concurrency,
  disabled_at = CASE WHEN @disabled::boolean THEN coalesce(disabled_at, now()) ELSE NULL END,
  row_version = row_version + 1
WHERE id = @id AND row_version = @row_version
RETURNING *;

-- name: DeleteRunner :execrows
DELETE FROM runners WHERE id = @id;

-- name: TouchRunner :exec
UPDATE runners SET last_seen_at = now(), version = @version, os = @os, arch = @arch WHERE id = @id;

-- name: CountRunningJobs :one
SELECT count(*) FROM pipeline_jobs WHERE runner_id = @runner_id AND status = 'running';

-- name: RunningJobsForRunner :many
SELECT id FROM pipeline_jobs WHERE runner_id = @runner_id AND status = 'running';

-- name: LostRunnerJobs :many
-- Running jobs whose runner stopped sending heartbeats.
SELECT j.id FROM pipeline_jobs j
JOIN runners r ON r.id = j.runner_id
WHERE j.status = 'running' AND (r.last_seen_at IS NULL OR r.last_seen_at < now() - make_interval(secs => @stale_seconds::integer))
LIMIT 100;

-- name: UpsertJobToken :exec
INSERT INTO job_tokens (job_id, token_hash, expires_at) VALUES (@job_id, @token_hash, @expires_at)
ON CONFLICT (job_id) DO UPDATE SET token_hash = EXCLUDED.token_hash, expires_at = EXCLUDED.expires_at;

-- name: GetJobByToken :one
SELECT j.* FROM job_tokens t JOIN pipeline_jobs j ON j.id = t.job_id
WHERE t.token_hash = @token_hash AND t.expires_at > now();

-- name: InsertArtifact :one
INSERT INTO artifacts (job_id, project_id, organization_id, name, size_bytes, sha256, storage_key, expires_at)
VALUES (@job_id, @project_id, @organization_id, @name, @size_bytes, @sha256, @storage_key, @expires_at)
ON CONFLICT (job_id) DO NOTHING
RETURNING *;

-- name: GetArtifact :one
SELECT * FROM artifacts WHERE id = @id AND expires_at > now();

-- name: ListJobArtifacts :many
SELECT * FROM artifacts WHERE job_id = @job_id AND expires_at > now() ORDER BY created_at;

-- name: DependencyArtifacts :many
-- Artifacts of the latest successful attempt of each named job in a run.
SELECT a.*, j.name AS job_name FROM artifacts a
JOIN pipeline_jobs j ON j.id = a.job_id
WHERE j.run_id = @run_id AND j.name = ANY(@names::text[]) AND j.status = 'succeeded' AND a.expires_at > now()
  AND j.attempt = (SELECT max(j2.attempt) FROM pipeline_jobs j2 WHERE j2.run_id = j.run_id AND j2.name = j.name)
ORDER BY j.name;

-- name: ExpiredArtifacts :many
DELETE FROM artifacts WHERE expires_at <= now() RETURNING storage_key;

-- name: GetCacheEntry :one
UPDATE cache_entries SET last_used_at = now() WHERE project_id = @project_id AND key = @key RETURNING *;

-- name: UpsertCacheEntry :one
-- Returns the replaced blob's key (if any) so the caller can delete it.
WITH old AS (SELECT storage_key FROM cache_entries WHERE project_id = @project_id AND key = @key)
INSERT INTO cache_entries (project_id, key, storage_key, size_bytes, sha256)
VALUES (@project_id, @key, @storage_key, @size_bytes, @sha256)
ON CONFLICT (project_id, key) DO UPDATE SET storage_key = EXCLUDED.storage_key, size_bytes = EXCLUDED.size_bytes,
  sha256 = EXCLUDED.sha256, last_used_at = now()
RETURNING coalesce((SELECT storage_key FROM old), '')::text AS previous_key;

-- name: EvictCache :many
-- Removes the least recently used entries beyond a project's quota.
DELETE FROM cache_entries c
WHERE c.id IN (
  SELECT id FROM (
    SELECT id, sum(size_bytes) OVER (PARTITION BY project_id ORDER BY last_used_at DESC, id DESC) AS running_total
    FROM cache_entries
  ) ranked WHERE running_total > @quota_bytes::bigint
)
RETURNING storage_key;

-- name: LockRunner :one
-- Serializes job claims per runner so max_concurrency holds under concurrent requests.
SELECT * FROM runners WHERE id = @id FOR NO KEY UPDATE;

-- name: DeleteExpiredRegistrationTokens :execrows
DELETE FROM runner_registration_tokens WHERE expires_at < now() - interval '7 days';

-- name: DeleteExpiredJobTokens :execrows
DELETE FROM job_tokens WHERE expires_at < now();

-- name: OrphanedRunnerJobs :many
-- Running jobs assigned to a runner that it no longer reports (the agent restarted, or
-- never received the assignment). The grace period covers a claim racing a heartbeat.
SELECT id FROM pipeline_jobs
WHERE runner_id = @runner_id AND status = 'running' AND NOT (id = ANY(@job_ids::uuid[]))
  AND started_at < now() - make_interval(secs => @grace_seconds::integer);
