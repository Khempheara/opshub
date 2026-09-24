-- name: CreateTeam :one
INSERT INTO teams (organization_id, slug, name, description) VALUES (@organization_id, @slug, @name, @description)
RETURNING *;

-- name: ListTeams :many
SELECT t.*, (SELECT count(*) FROM team_members tm WHERE tm.team_id = t.id)::bigint AS member_count
FROM teams t
WHERE t.organization_id = @organization_id AND t.deleted_at IS NULL
  AND (sqlc.narg(cursor_name)::text IS NULL
       OR (t.name, t.id) > (sqlc.narg(cursor_name)::text, sqlc.narg(cursor_id)::uuid))
ORDER BY t.name, t.id
LIMIT @page_size;

-- name: GetTeam :one
SELECT * FROM teams WHERE id = @id AND deleted_at IS NULL;

-- name: UpdateTeam :one
UPDATE teams SET name = @name, description = @description, version = version + 1
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteTeam :exec
UPDATE teams SET deleted_at = now() WHERE id = @id AND deleted_at IS NULL;

-- name: ListTeamMembers :many
SELECT u.id AS user_id, u.email, u.display_name, tm.created_at AS added_at
FROM team_members tm
JOIN users u ON u.id = tm.user_id AND u.deleted_at IS NULL
WHERE tm.team_id = @team_id
ORDER BY u.display_name, u.id;

-- name: AddTeamMember :exec
INSERT INTO team_members (team_id, user_id) VALUES (@team_id, @user_id) ON CONFLICT DO NOTHING;

-- name: RemoveTeamMember :execrows
DELETE FROM team_members WHERE team_id = @team_id AND user_id = @user_id;
