-- name: GetUserByIdentity :one
SELECT u.* FROM user_identities i
JOIN users u ON u.id = i.user_id
WHERE i.provider = @provider AND i.subject = @subject AND u.deleted_at IS NULL;

-- name: CreateIdentity :one
INSERT INTO user_identities (user_id, provider, subject, email)
VALUES (@user_id, @provider, @subject, sqlc.narg(email))
RETURNING *;

-- name: ListIdentities :many
SELECT * FROM user_identities WHERE user_id = @user_id ORDER BY created_at, id;

-- name: CountIdentities :one
SELECT count(*) FROM user_identities WHERE user_id = @user_id;

-- name: DeleteIdentity :one
DELETE FROM user_identities WHERE id = @id AND user_id = @user_id RETURNING *;
