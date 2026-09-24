-- name: CreateInvitation :one
INSERT INTO invitations (organization_id, email, role, token_hash, invited_by, expires_at)
VALUES (@organization_id, @email, @role, @token_hash, @invited_by, @expires_at)
RETURNING *;

-- name: RevokeOpenInvitationForEmail :exec
UPDATE invitations SET revoked_at = now()
WHERE organization_id = @organization_id AND email = @email AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: ListOpenInvitations :many
SELECT i.*, u.display_name AS invited_by_name
FROM invitations i
LEFT JOIN users u ON u.id = i.invited_by
WHERE i.organization_id = @organization_id AND i.accepted_at IS NULL AND i.revoked_at IS NULL
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (i.created_at, i.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY i.created_at DESC, i.id DESC
LIMIT @page_size;

-- name: GetInvitation :one
SELECT * FROM invitations WHERE id = @id;

-- name: RevokeInvitation :exec
UPDATE invitations SET revoked_at = now() WHERE id = @id AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: GetInvitationByTokenForUpdate :one
SELECT i.*, o.name AS organization_name, o.slug AS organization_slug, u.display_name AS invited_by_name
FROM invitations i
JOIN organizations o ON o.id = i.organization_id AND o.deleted_at IS NULL
LEFT JOIN users u ON u.id = i.invited_by
WHERE i.token_hash = @token_hash
FOR UPDATE OF i;

-- name: MarkInvitationAccepted :exec
UPDATE invitations SET accepted_at = now(), accepted_by = @accepted_by WHERE id = @id;

-- name: GetOpenInvitationEmailByToken :one
-- Used by sign-up when self-service registration is disabled: an open invitation for the
-- same address lets the invitee create an account.
SELECT i.email
FROM invitations i
JOIN organizations o ON o.id = i.organization_id AND o.deleted_at IS NULL
WHERE i.token_hash = @token_hash
  AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now();
