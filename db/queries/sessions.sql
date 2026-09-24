-- name: CreateSession :one
INSERT INTO sessions (user_id, auth_method, ip, user_agent, expires_at)
VALUES (@user_id, @auth_method, sqlc.narg(ip), @user_agent, @expires_at)
RETURNING *;

-- name: ListActiveSessions :many
-- Keyset pagination (newest first).
SELECT * FROM sessions
WHERE user_id = @user_id AND revoked_at IS NULL AND expires_at > now()
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- name: RevokeSession :one
UPDATE sessions SET revoked_at = now(), revoke_reason = @reason
WHERE id = @id AND user_id = @user_id AND revoked_at IS NULL
RETURNING id;

-- name: RevokeUserSessions :exec
-- Revokes every session of a user, optionally keeping one (the caller's).
UPDATE sessions SET revoked_at = now(), revoke_reason = @reason
WHERE user_id = @user_id AND revoked_at IS NULL
  AND (sqlc.narg(keep_id)::uuid IS NULL OR id <> sqlc.narg(keep_id)::uuid);

-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (session_id, parent_id, token_hash, expires_at)
VALUES (@session_id, sqlc.narg(parent_id), @token_hash, @expires_at)
RETURNING *;

-- name: GetRefreshTokenForUpdate :one
SELECT rt.id, rt.session_id, rt.expires_at, rt.used_at,
       s.user_id, s.revoked_at AS session_revoked_at, s.expires_at AS session_expires_at
FROM refresh_tokens rt
JOIN sessions s ON s.id = rt.session_id
WHERE rt.token_hash = @token_hash
FOR UPDATE OF rt, s;

-- name: MarkRefreshTokenUsed :exec
UPDATE refresh_tokens SET used_at = now() WHERE id = @id;

-- name: TouchSession :exec
UPDATE sessions SET last_used_at = now(), ip = coalesce(sqlc.narg(ip), ip), user_agent = @user_agent
WHERE id = @id;

-- name: CreateMFAChallenge :exec
INSERT INTO mfa_challenges (user_id, token_hash, auth_method, expires_at)
VALUES (@user_id, @token_hash, @auth_method, @expires_at);

-- name: GetMFAChallengeForUpdate :one
SELECT * FROM mfa_challenges WHERE token_hash = @token_hash FOR UPDATE;

-- name: IncrementMFAAttempts :exec
UPDATE mfa_challenges SET attempts = attempts + 1 WHERE id = @id;

-- name: UseMFAChallenge :exec
UPDATE mfa_challenges SET used_at = now() WHERE id = @id;

-- name: DeleteExpiredAuthRecords :exec
-- Periodic cleanup. Sessions are kept 30 days after expiry/revocation for the security page history.
WITH s AS (
  DELETE FROM sessions WHERE coalesce(revoked_at, expires_at) < now() - interval '30 days'
), m AS (
  DELETE FROM mfa_challenges WHERE expires_at < now() - interval '1 day'
)
DELETE FROM email_tokens WHERE expires_at < now() - interval '7 days';
