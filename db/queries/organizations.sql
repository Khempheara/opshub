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

-- name: GetMembership :one
-- The caller's role in a live organization (tenant check for every org-scoped request).
SELECT m.role FROM organization_members m
JOIN organizations o ON o.id = m.organization_id AND o.deleted_at IS NULL
WHERE m.organization_id = @organization_id AND m.user_id = @user_id;

-- name: LockOrganization :one
-- Serializes membership changes (last-owner checks) within one organization.
SELECT id FROM organizations WHERE id = @id AND deleted_at IS NULL FOR UPDATE;

-- name: UpdateOrganization :one
UPDATE organizations SET name = @name, version = version + 1
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteOrganization :exec
UPDATE organizations SET deleted_at = now() WHERE id = @id AND deleted_at IS NULL;

-- name: CountOwners :one
SELECT count(*) FROM organization_members WHERE organization_id = @organization_id AND role = 'owner';

-- name: ListMembers :many
SELECT u.id AS user_id, u.email, u.display_name, (u.totp_enabled_at IS NOT NULL)::boolean AS two_factor_enabled,
       u.last_login_at, m.role, m.created_at AS joined_at
FROM organization_members m
JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
WHERE m.organization_id = @organization_id
  AND (sqlc.narg(role)::member_role IS NULL OR m.role = sqlc.narg(role)::member_role)
  AND (sqlc.narg(search)::text IS NULL
       OR u.email ILIKE '%' || sqlc.narg(search)::text || '%'
       OR u.display_name ILIKE '%' || sqlc.narg(search)::text || '%')
  AND (sqlc.narg(cursor_name)::text IS NULL
       OR (u.display_name, u.id) > (sqlc.narg(cursor_name)::text, sqlc.narg(cursor_id)::uuid))
ORDER BY u.display_name, u.id
LIMIT @page_size;

-- name: GetMember :one
SELECT u.id AS user_id, u.email, u.display_name, (u.totp_enabled_at IS NOT NULL)::boolean AS two_factor_enabled,
       u.last_login_at, m.role, m.created_at AS joined_at
FROM organization_members m
JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
WHERE m.organization_id = @organization_id AND m.user_id = @user_id;

-- name: UpdateMemberRole :exec
UPDATE organization_members SET role = @role WHERE organization_id = @organization_id AND user_id = @user_id;

-- name: RemoveMember :exec
DELETE FROM organization_members WHERE organization_id = @organization_id AND user_id = @user_id;

-- name: RemoveUserFromOrgTeams :exec
DELETE FROM team_members tm USING teams t
WHERE tm.team_id = t.id AND t.organization_id = @organization_id AND tm.user_id = @user_id;
