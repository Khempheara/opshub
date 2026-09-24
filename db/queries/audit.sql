-- name: InsertAuditLog :one
INSERT INTO audit_log (
  organization_id, actor_user_id, actor_type, action, resource_type, resource_id,
  ip, user_agent, before, after, metadata
) VALUES (
  @organization_id, @actor_user_id, @actor_type, @action, @resource_type, @resource_id,
  @ip, @user_agent, @before, @after, @metadata
)
RETURNING *;

-- name: ListAuditLog :many
-- Keyset pagination on (created_at, id) newest first.
SELECT * FROM audit_log
WHERE organization_id = @organization_id
  AND (sqlc.narg(before_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;
