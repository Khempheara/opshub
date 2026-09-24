-- name: CreateEnvironment :one
INSERT INTO environments (project_id, name, kind, variables)
VALUES (@project_id, @name, @kind, @variables)
RETURNING *;

-- name: CountEnvironments :one
SELECT count(*) FROM environments WHERE project_id = @project_id AND deleted_at IS NULL;

-- name: ListEnvironments :many
-- Ordered development, staging, production, then by name.
SELECT sqlc.embed(e),
       (pr.environment_id IS NOT NULL)::boolean AS protected,
       coalesce(pr.required_approvals, 0)::integer AS required_approvals,
       coalesce(pr.allowed_branches, '{}')::text[] AS allowed_branches,
       coalesce(pr.allowed_roles::text[], '{}')::text[] AS allowed_roles
FROM environments e
LEFT JOIN protection_rules pr ON pr.environment_id = e.id
WHERE e.project_id = @project_id AND e.deleted_at IS NULL
ORDER BY e.kind, e.name;

-- name: GetEnvironment :one
SELECT sqlc.embed(e),
       (pr.environment_id IS NOT NULL)::boolean AS protected,
       coalesce(pr.required_approvals, 0)::integer AS required_approvals,
       coalesce(pr.allowed_branches, '{}')::text[] AS allowed_branches,
       coalesce(pr.allowed_roles::text[], '{}')::text[] AS allowed_roles
FROM environments e
LEFT JOIN protection_rules pr ON pr.environment_id = e.id
WHERE e.id = @id AND e.deleted_at IS NULL;

-- name: UpdateEnvironment :one
UPDATE environments SET kind = @kind, variables = @variables, version = version + 1
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteEnvironment :exec
UPDATE environments SET deleted_at = now() WHERE id = @id AND deleted_at IS NULL;

-- name: UpsertProtectionRule :exec
INSERT INTO protection_rules (environment_id, required_approvals, allowed_branches, allowed_roles)
VALUES (@environment_id, @required_approvals, @allowed_branches::text[], CAST(sqlc.arg(allowed_roles)::text[] AS member_role[]))
ON CONFLICT (environment_id) DO UPDATE SET
  required_approvals = EXCLUDED.required_approvals,
  allowed_branches = EXCLUDED.allowed_branches,
  allowed_roles = EXCLUDED.allowed_roles;

-- name: DeleteProtectionRule :exec
DELETE FROM protection_rules WHERE environment_id = @environment_id;
