-- name: CreateOrganization :one
INSERT INTO organizations (slug, name) VALUES (@slug, @name) RETURNING *;

-- name: AddOrganizationMember :exec
INSERT INTO organization_members (organization_id, user_id, role) VALUES (@organization_id, @user_id, @role);

-- name: ListUserOrganizations :many
SELECT o.id, o.slug, o.name, o.created_at, m.role
FROM organizations o
JOIN organization_members m ON m.organization_id = o.id
WHERE m.user_id = @user_id AND o.deleted_at IS NULL
  AND (sqlc.narg(cursor_name)::text IS NULL
       OR (o.name, o.id) > (sqlc.narg(cursor_name)::text, sqlc.narg(cursor_id)::uuid))
ORDER BY o.name, o.id
LIMIT @page_size;

-- name: GetOrganizationForMember :one
-- Tenant-scoped read: returns no row unless the user is a member.
SELECT o.*, m.role
FROM organizations o
JOIN organization_members m ON m.organization_id = o.id AND m.user_id = @user_id
WHERE o.id = @id AND o.deleted_at IS NULL;
