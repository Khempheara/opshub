-- name: CreateUser :one
INSERT INTO users (email, display_name, password_hash, locale, timezone, is_platform_admin, email_verified_at)
VALUES (@email, @display_name, sqlc.narg(password_hash), @locale, @timezone, @is_platform_admin, sqlc.narg(email_verified_at))
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = @id AND deleted_at IS NULL;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = @email AND deleted_at IS NULL;

-- name: GetUserByIDForUpdate :one
SELECT * FROM users WHERE id = @id AND deleted_at IS NULL FOR UPDATE;

-- name: UpdateUserProfile :one
-- Optimistic locking: no row is returned when the version does not match.
UPDATE users
SET display_name = @display_name, locale = @locale, timezone = @timezone,
    khmer_numerals = @khmer_numerals, version = version + 1
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserPreferences :one
UPDATE users
SET locale = @locale, timezone = @timezone, khmer_numerals = @khmer_numerals, version = version + 1
WHERE id = @id AND deleted_at IS NULL
RETURNING *;

-- name: SetUserEmailVerified :exec
UPDATE users SET email_verified_at = coalesce(email_verified_at, now()) WHERE id = @id;

-- name: SetUserPassword :exec
UPDATE users
SET password_hash = @password_hash, password_changed_at = now(), version = version + 1,
    failed_login_count = 0, lockout_level = 0, locked_until = NULL
WHERE id = @id;

-- name: RecordLoginFailure :exec
UPDATE users
SET failed_login_count = @failed_login_count, lockout_level = @lockout_level, locked_until = sqlc.narg(locked_until)
WHERE id = @id;

-- name: RecordLoginSuccess :exec
UPDATE users
SET failed_login_count = 0, lockout_level = 0, locked_until = NULL, last_login_at = now()
WHERE id = @id;

-- name: SetTOTPPending :exec
UPDATE users SET totp_pending_enc = @totp_pending_enc WHERE id = @id;

-- name: EnableTOTP :exec
UPDATE users
SET totp_secret_enc = totp_pending_enc, totp_pending_enc = NULL, totp_enabled_at = now(),
    totp_last_step = @totp_last_step, version = version + 1
WHERE id = @id AND totp_pending_enc IS NOT NULL;

-- name: DisableTOTP :exec
UPDATE users
SET totp_secret_enc = NULL, totp_pending_enc = NULL, totp_enabled_at = NULL, totp_last_step = NULL,
    version = version + 1
WHERE id = @id;

-- name: AdvanceTOTPStep :one
-- Accepts a TOTP time step only once (replay protection); returns no row if already used.
UPDATE users SET totp_last_step = @step
WHERE id = @id AND (totp_last_step IS NULL OR totp_last_step < @step)
RETURNING id;

-- name: CreateEmailToken :exec
INSERT INTO email_tokens (user_id, purpose, token_hash, expires_at)
VALUES (@user_id, @purpose, @token_hash, @expires_at);

-- name: InvalidateEmailTokens :exec
UPDATE email_tokens SET used_at = now()
WHERE user_id = @user_id AND purpose = @purpose AND used_at IS NULL;

-- name: ConsumeEmailToken :one
UPDATE email_tokens SET used_at = now()
WHERE token_hash = @token_hash AND purpose = @purpose AND used_at IS NULL AND expires_at > now()
RETURNING user_id;

-- name: DeleteRecoveryCodes :exec
DELETE FROM user_recovery_codes WHERE user_id = @user_id;

-- name: InsertRecoveryCodes :copyfrom
INSERT INTO user_recovery_codes (user_id, code_hash) VALUES (@user_id, @code_hash);

-- name: ConsumeRecoveryCode :one
UPDATE user_recovery_codes SET used_at = now()
WHERE user_id = @user_id AND code_hash = @code_hash AND used_at IS NULL
RETURNING id;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM user_recovery_codes WHERE user_id = @user_id AND used_at IS NULL;
