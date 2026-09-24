-- name: CreateProject :one
INSERT INTO projects (organization_id, slug, name, description, default_branch, created_by)
VALUES (@organization_id, @slug, @name, @description, @default_branch, @created_by)
RETURNING *;

-- name: GetProjectAccess :one
-- A project with the caller's organization role and project grants (docs/rbac.md); '' means
-- none. The member_role enum is declared owner → viewer, so min() is the most privileged role.
SELECT sqlc.embed(p),
       coalesce(om.role::text, '')::text AS org_role,
       coalesce((SELECT pm.role::text FROM project_members pm
                  WHERE pm.project_id = p.id AND pm.user_id = @user_id::uuid), '')::text AS direct_role,
       coalesce((SELECT min(pm.role)::text FROM project_members pm
                  JOIN team_members tm ON tm.team_id = pm.team_id AND tm.user_id = @user_id::uuid
                  JOIN teams t ON t.id = pm.team_id AND t.deleted_at IS NULL
                  WHERE pm.project_id = p.id), '')::text AS team_role
FROM projects p
JOIN organizations o ON o.id = p.organization_id AND o.deleted_at IS NULL
LEFT JOIN organization_members om ON om.organization_id = p.organization_id AND om.user_id = @user_id::uuid
WHERE p.id = @id AND p.deleted_at IS NULL;

-- name: ListProjects :many
-- Projects the caller can see: all of them when their organization role inherits a project
-- role (owner/admin/viewer), otherwise those granted directly or through a team.
SELECT p.* FROM projects p
WHERE p.organization_id = @organization_id AND p.deleted_at IS NULL
  AND (@see_all::boolean
       OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = @user_id::uuid)
       OR EXISTS (SELECT 1 FROM project_members pm
                  JOIN team_members tm ON tm.team_id = pm.team_id AND tm.user_id = @user_id::uuid
                  JOIN teams t ON t.id = pm.team_id AND t.deleted_at IS NULL
                  WHERE pm.project_id = p.id))
  AND (sqlc.narg(search)::text IS NULL
       OR p.name ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\'
       OR p.slug ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\')
  AND (sqlc.narg(cursor_name)::text IS NULL
       OR (p.name, p.id) > (sqlc.narg(cursor_name)::text, sqlc.narg(cursor_id)::uuid))
ORDER BY p.name, p.id
LIMIT @page_size;

-- name: UpdateProject :one
UPDATE projects SET name = @name, description = @description, default_branch = @default_branch,
  version = version + 1
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteProject :exec
UPDATE projects SET deleted_at = now() WHERE id = @id AND deleted_at IS NULL;

-- name: UpsertProjectUserGrant :exec
INSERT INTO project_members (project_id, user_id, role) VALUES (@project_id, @user_id::uuid, @role)
ON CONFLICT (project_id, user_id) WHERE user_id IS NOT NULL DO UPDATE SET role = EXCLUDED.role;

-- name: UpsertProjectTeamGrant :exec
INSERT INTO project_members (project_id, team_id, role) VALUES (@project_id, @team_id::uuid, @role)
ON CONFLICT (project_id, team_id) WHERE team_id IS NOT NULL DO UPDATE SET role = EXCLUDED.role;

-- name: GetProjectUserGrant :one
SELECT role FROM project_members WHERE project_id = @project_id AND user_id = @user_id::uuid;

-- name: GetProjectTeamGrant :one
SELECT role FROM project_members WHERE project_id = @project_id AND team_id = @team_id::uuid;

-- name: DeleteProjectUserGrant :execrows
DELETE FROM project_members WHERE project_id = @project_id AND user_id = @user_id::uuid;

-- name: DeleteProjectTeamGrant :execrows
DELETE FROM project_members WHERE project_id = @project_id AND team_id = @team_id::uuid;

-- name: ListProjectUserGrants :many
SELECT u.id, u.email, u.display_name, pm.role, pm.created_at
FROM project_members pm
JOIN users u ON u.id = pm.user_id AND u.deleted_at IS NULL
WHERE pm.project_id = @project_id
ORDER BY u.display_name, u.id;

-- name: ListProjectTeamGrants :many
SELECT t.id, t.name, t.slug, pm.role, pm.created_at,
       (SELECT count(*) FROM team_members tm WHERE tm.team_id = t.id)::bigint AS member_count
FROM project_members pm
JOIN teams t ON t.id = pm.team_id AND t.deleted_at IS NULL
WHERE pm.project_id = @project_id
ORDER BY t.name, t.id;

-- name: ListOrgManagers :many
-- Organization Owners and Admins, who inherit that role on every project.
SELECT u.id, u.email, u.display_name, om.role
FROM organization_members om
JOIN users u ON u.id = om.user_id AND u.deleted_at IS NULL
WHERE om.organization_id = @organization_id AND om.role IN ('owner', 'admin')
ORDER BY om.role, u.display_name, u.id;

-- name: DeleteProjectGrantsForUserInOrg :exec
-- Called when someone leaves or is removed from an organization.
DELETE FROM project_members pm
USING projects p
WHERE pm.project_id = p.id AND p.organization_id = @organization_id AND pm.user_id = @user_id::uuid;

-- name: DeleteProjectGrantsForTeam :exec
DELETE FROM project_members WHERE team_id = @team_id::uuid;

-- name: GetTeamInOrg :one
SELECT * FROM teams WHERE id = @id AND organization_id = @organization_id AND deleted_at IS NULL;
