-- name: CreateAPIToken :one
INSERT INTO api_tokens (user_id, name, token_prefix, token_hash, scopes, expires_at)
VALUES (@user_id, @name, @token_prefix, @token_hash, @scopes, sqlc.narg(expires_at))
RETURNING *;

-- name: ListAPITokens :many
SELECT * FROM api_tokens
WHERE user_id = @user_id AND revoked_at IS NULL
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- name: RevokeAPIToken :one
UPDATE api_tokens SET revoked_at = now()
WHERE id = @id AND user_id = @user_id AND revoked_at IS NULL
RETURNING id;

-- name: GetActiveAPITokenByHash :one
SELECT t.id, t.user_id, t.scopes, t.expires_at
FROM api_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.token_hash = @token_hash AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > now())
  AND u.deleted_at IS NULL AND u.disabled_at IS NULL;

-- name: TouchAPIToken :exec
-- Throttled to one write per minute per token.
UPDATE api_tokens SET last_used_at = now()
WHERE id = @id AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');
