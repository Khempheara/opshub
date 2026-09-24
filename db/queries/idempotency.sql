-- name: ClaimIdempotencyKey :one
-- Inserts an in-flight record (or takes over an expired one); returns no row when a live
-- record already exists for this user and key.
INSERT INTO idempotency_keys (user_id, key, request_hash, expires_at)
VALUES (@user_id, @key, @request_hash, @expires_at)
ON CONFLICT (user_id, key) DO UPDATE SET
  request_hash = EXCLUDED.request_hash, response_status = NULL, response_body = NULL,
  created_at = now(), expires_at = EXCLUDED.expires_at
WHERE idempotency_keys.expires_at <= now()
RETURNING id;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE user_id = @user_id AND key = @key;

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys SET response_status = @response_status, response_body = @response_body
WHERE id = @id;

-- name: ReleaseIdempotencyKey :exec
-- The request failed with a server error: forget the key so the client can retry.
DELETE FROM idempotency_keys WHERE id = @id;

-- name: DeleteExpiredHousekeeping :exec
-- Periodic cleanup: expired idempotency keys and webhook deliveries older than 30 days.
WITH k AS (DELETE FROM idempotency_keys WHERE expires_at < now())
DELETE FROM webhook_deliveries WHERE received_at < now() - interval '30 days';
