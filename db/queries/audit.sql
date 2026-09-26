-- name: InsertAuditLog :one
INSERT INTO audit_log (
  organization_id, actor_user_id, actor_type, action, resource_type, resource_id,
  ip, user_agent, before, after, metadata
) VALUES (
  @organization_id, @actor_user_id, @actor_type, @action, @resource_type, @resource_id,
  @ip, @user_agent, @before, @after, @metadata
)
RETURNING *;

-- name: SearchAuditLog :many
-- An organization's entries, newest first, with the actor, the project and (for user and
-- member resources) the user named by resource_id. Filters: areas (the action's part before
-- the dot) or exact actions, actor, project, resource, and [from_ts, to_ts). Keyset paging on
-- (created_at, id).
SELECT a.id, a.created_at, a.actor_type, a.actor_user_id, a.action, a.resource_type, a.resource_id,
       a.ip, a.user_agent, a.before, a.after, a.metadata,
       au.display_name AS actor_name, COALESCE(au.email, '')::text AS actor_email,
       p.name AS project_name, ru.display_name AS resource_user_name, COALESCE(ru.email, '')::text AS resource_user_email
FROM audit_log a
LEFT JOIN users au ON au.id = a.actor_user_id
LEFT JOIN projects p ON p.id = (a.metadata->>'project_id')::uuid
LEFT JOIN users ru ON a.resource_type IN ('user', 'member') AND ru.id = (CASE WHEN a.resource_type IN ('user', 'member') THEN a.resource_id::uuid END)
WHERE a.organization_id = @organization_id
  AND (cardinality(@areas::text[]) = 0 AND cardinality(@actions::text[]) = 0
       OR split_part(a.action, '.', 1) = ANY (@areas::text[]) OR a.action = ANY (@actions::text[]))
  AND (sqlc.narg(actor_user_id)::uuid IS NULL OR a.actor_user_id = sqlc.narg(actor_user_id))
  AND (sqlc.narg(project_id)::text IS NULL OR a.metadata->>'project_id' = sqlc.narg(project_id))
  AND (sqlc.narg(resource_type)::text IS NULL OR a.resource_type = sqlc.narg(resource_type))
  AND (sqlc.narg(resource_id)::text IS NULL OR a.resource_id = sqlc.narg(resource_id))
  AND (sqlc.narg(from_ts)::timestamptz IS NULL OR a.created_at >= sqlc.narg(from_ts))
  AND (sqlc.narg(to_ts)::timestamptz IS NULL OR a.created_at < sqlc.narg(to_ts))
  AND (sqlc.narg(before_created_at)::timestamptz IS NULL
       OR (a.created_at, a.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY a.created_at DESC, a.id DESC
LIMIT @max_rows;

-- name: AccountActivity :many
-- A user's own account events (sign-ins, 2FA, password, tokens, sessions): entries without
-- an organization that the user did or that are about the user. Newest first, keyset paging.
SELECT a.id, a.created_at, a.actor_type, a.actor_user_id, a.action, a.resource_type, a.resource_id,
       a.ip, a.user_agent, a.before, a.after, a.metadata,
       au.display_name AS actor_name, COALESCE(au.email, '')::text AS actor_email
FROM audit_log a
LEFT JOIN users au ON au.id = a.actor_user_id
WHERE a.organization_id IS NULL
  AND (a.actor_user_id = @user_id OR (a.resource_type = 'user' AND a.resource_id = @user_id::text))
  AND (sqlc.narg(before_created_at)::timestamptz IS NULL
       OR (a.created_at, a.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY a.created_at DESC, a.id DESC
LIMIT @max_rows;

-- name: AuditTeamNames :many
-- Names of teams mentioned by audit entries (deleted teams included).
SELECT id, name FROM teams WHERE id = ANY (@ids::uuid[]);

-- name: AuditUserNames :many
-- Names of users mentioned by audit entries.
SELECT id, display_name, email::text AS email FROM users WHERE id = ANY (@ids::uuid[]);

-- name: AuditAssetNames :many
-- Names of infrastructure assets mentioned by audit entries.
SELECT id, name FROM infra_assets WHERE id = ANY (@ids::uuid[]);
