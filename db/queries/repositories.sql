-- name: GetRepositoryByProject :one
SELECT * FROM repositories WHERE project_id = @project_id;

-- name: GetRepositoryForWebhook :one
-- Webhook receiver lookup: the repository and whether its project still exists.
SELECT r.* FROM repositories r
JOIN projects p ON p.id = r.project_id AND p.deleted_at IS NULL
WHERE r.id = @id AND r.provider = @provider;

-- name: InsertRepository :one
INSERT INTO repositories (
  id, project_id, provider, base_url, full_name, external_id, web_url, clone_url, default_branch,
  access_token_enc, webhook_secret_enc, webhook_mode, webhook_id, connected_by
) VALUES (
  @id, @project_id, @provider, @base_url, @full_name, @external_id, @web_url, @clone_url, @default_branch,
  @access_token_enc, @webhook_secret_enc, @webhook_mode, @webhook_id, @connected_by
)
RETURNING *;

-- name: DeleteRepositoryByProject :one
DELETE FROM repositories WHERE project_id = @project_id RETURNING *;

-- name: UpdateRepositoryDefaultBranch :exec
UPDATE repositories SET default_branch = @default_branch WHERE id = @id;

-- name: InsertWebhookDelivery :one
-- Returns no row when a valid delivery with the same id was already recorded (a redelivery).
INSERT INTO webhook_deliveries (repository_id, delivery_id, event, ref, commit_sha, signature_valid, payload)
VALUES (@repository_id, @delivery_id, @event, @ref, @commit_sha, @signature_valid, @payload)
ON CONFLICT (repository_id, delivery_id) WHERE signature_valid DO NOTHING
RETURNING id;

-- name: TouchRepositoryDelivery :exec
UPDATE repositories SET last_delivery_at = now() WHERE id = @id;

-- name: ListWebhookDeliveries :many
SELECT id, delivery_id, event, ref, commit_sha, signature_valid, received_at
FROM webhook_deliveries
WHERE repository_id = @repository_id
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (received_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY received_at DESC, id DESC
LIMIT @page_size;
