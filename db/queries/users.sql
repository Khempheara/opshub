-- name: CreateUser :one
INSERT INTO users (email, display_name, password_hash, locale, timezone)
VALUES (@email, @display_name, @password_hash, @locale, @timezone)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = @id AND deleted_at IS NULL;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = @email AND deleted_at IS NULL;

-- name: UpdateUserPreferences :one
UPDATE users
SET locale = @locale, timezone = @timezone, khmer_numerals = @khmer_numerals
WHERE id = @id AND deleted_at IS NULL
RETURNING *;
