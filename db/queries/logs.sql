-- name: SearchLogs :many
-- Newest first within [from_ts, to_ts). Job and deployment lines only for the given projects
-- (see_all: every project). fts: a full-text query; contains: a plain substring (for text
-- without spaces between words, such as Khmer). before_* pages backwards, after_* fetches
-- only newer lines (follow mode).
SELECT id, ts, source, source_id, project_id, service, level, message, attributes
FROM log_entries
WHERE organization_id = @organization_id
  AND ts >= @from_ts AND ts < @to_ts
  AND (project_id IS NULL OR @see_all::boolean OR project_id = ANY (@project_ids::uuid[]))
  AND (sqlc.narg(source)::log_source IS NULL OR source = sqlc.narg(source))
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(min_level)::log_level IS NULL OR level >= sqlc.narg(min_level))
  AND (sqlc.narg(fts)::text IS NULL OR search @@ websearch_to_tsquery('simple', sqlc.narg(fts)))
  AND (sqlc.narg(contains)::text IS NULL OR message ILIKE '%' || sqlc.narg(contains) || '%' ESCAPE '\')
  AND (sqlc.narg(before_ts)::timestamptz IS NULL OR (ts, id) < (sqlc.narg(before_ts), sqlc.narg(before_id)::uuid))
  AND (sqlc.narg(after_ts)::timestamptz IS NULL OR (ts, id) > (sqlc.narg(after_ts), sqlc.narg(after_id)::uuid))
ORDER BY ts DESC, id DESC
LIMIT @max_rows;

-- name: RecentLogServices :many
-- Service names seen in the last day, for the filter.
SELECT DISTINCT service FROM log_entries
WHERE organization_id = @organization_id AND ts >= now() - interval '1 day'
  AND (project_id IS NULL OR @see_all::boolean OR project_id = ANY (@project_ids::uuid[]))
ORDER BY service
LIMIT 200;

-- name: MaintainLogPartitions :exec
SELECT opshub_maintain_log_partitions(@keep_days::integer);

-- name: CreateIngestToken :one
INSERT INTO log_ingest_tokens (organization_id, name, service, token_hash, token_prefix, created_by)
VALUES (@organization_id, @name, @service, @token_hash, @token_prefix, sqlc.narg(created_by))
RETURNING *;

-- name: ListIngestTokens :many
SELECT t.*, u.display_name AS created_by_name
FROM log_ingest_tokens t LEFT JOIN users u ON u.id = t.created_by
WHERE t.organization_id = @organization_id ORDER BY t.name;

-- name: GetIngestToken :one
SELECT * FROM log_ingest_tokens WHERE id = @id;

-- name: GetIngestTokenByHash :one
SELECT * FROM log_ingest_tokens WHERE token_hash = @token_hash;

-- name: DeleteIngestToken :execrows
DELETE FROM log_ingest_tokens WHERE id = @id;

-- name: TouchIngestToken :exec
UPDATE log_ingest_tokens SET last_used_at = now()
WHERE id = @id AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: VisibleProjectIDs :many
-- The projects a member can see (all of them when see_all).
SELECT p.id FROM projects p
WHERE p.organization_id = @organization_id AND p.deleted_at IS NULL
  AND (@see_all::boolean
       OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = @user_id::uuid)
       OR EXISTS (SELECT 1 FROM project_members pm
                  JOIN team_members tm ON tm.team_id = pm.team_id AND tm.user_id = @user_id::uuid
                  JOIN teams t ON t.id = pm.team_id AND t.deleted_at IS NULL
                  WHERE pm.project_id = p.id));

-- name: InsertServiceLogs :execrows
-- One ingest batch for a token's service. The arrays have one element per line (set-returning
-- functions in a select list advance together).
INSERT INTO log_entries (organization_id, ts, source, source_id, service, level, message, attributes)
SELECT @organization_id::uuid, unnest(@ts::timestamptz[]), 'service', @source_id::uuid, @service::text,
       unnest(@levels::text[])::log_level, unnest(@messages::text[]), unnest(@attributes::jsonb[]);
