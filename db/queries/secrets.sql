-- name: CreateSecret :one
INSERT INTO secrets (project_id, environment_id, name, description, created_by)
VALUES (@project_id, sqlc.narg(environment_id), @name, @description, sqlc.narg(created_by))
RETURNING *;

-- name: GetSecret :one
-- A live secret with its scope; environments deleted since hide their secrets.
SELECT sqlc.embed(s),
       e.name AS environment_name,
       (pr.environment_id IS NOT NULL)::boolean AS protected,
       v.created_at AS rotated_at
FROM secrets s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN protection_rules pr ON pr.environment_id = s.environment_id
JOIN secret_versions v ON v.secret_id = s.id AND v.version = s.current_version
WHERE s.id = @id AND s.deleted_at IS NULL AND (s.environment_id IS NULL OR e.deleted_at IS NULL);

-- name: ListSecrets :many
-- Project-wide first, then by environment and name. project_wide = true lists only
-- project-wide secrets; environment_id lists only that environment's.
SELECT sqlc.embed(s),
       e.name AS environment_name,
       (pr.environment_id IS NOT NULL)::boolean AS protected,
       v.created_at AS rotated_at
FROM secrets s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN protection_rules pr ON pr.environment_id = s.environment_id
JOIN secret_versions v ON v.secret_id = s.id AND v.version = s.current_version
WHERE s.project_id = @project_id AND s.deleted_at IS NULL
  AND (s.environment_id IS NULL OR e.deleted_at IS NULL)
  AND (sqlc.narg(environment_id)::uuid IS NULL OR s.environment_id = sqlc.narg(environment_id))
  AND (NOT @project_wide::boolean OR s.environment_id IS NULL)
ORDER BY s.environment_id IS NOT NULL, e.name NULLS FIRST, s.name;

-- name: CountSecrets :one
SELECT count(*) FROM secrets WHERE project_id = @project_id AND deleted_at IS NULL;

-- name: UpdateSecretDescription :one
UPDATE secrets SET description = @description, version = version + 1
WHERE id = @id AND version = @expected_version AND deleted_at IS NULL
RETURNING *;

-- name: BumpSecretVersion :one
-- Rotation: the next value version, also a new ETag version.
UPDATE secrets SET current_version = current_version + 1, version = version + 1
WHERE id = @id AND version = @expected_version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteSecret :execrows
UPDATE secrets SET deleted_at = now(), version = version + 1
WHERE id = @id AND version = @expected_version AND deleted_at IS NULL;

-- name: InsertSecretVersion :exec
INSERT INTO secret_versions (secret_id, version, ciphertext, nonce, dek_enc, kek_id, created_by)
VALUES (@secret_id, @version, @ciphertext, @nonce, @dek_enc, @kek_id, sqlc.narg(created_by));

-- name: DestroySecretValues :execrows
-- Destroys the values of every version below before_version (all of them when it is 0).
UPDATE secret_versions SET ciphertext = NULL, nonce = NULL, dek_enc = NULL, kek_id = NULL, destroyed_at = now()
WHERE secret_id = @secret_id AND destroyed_at IS NULL AND (@before_version::integer = 0 OR version < @before_version);

-- name: ListSecretVersions :many
SELECT v.version, v.created_at, v.destroyed_at, v.kek_id, v.created_by,
       u.display_name AS created_by_name
FROM secret_versions v
LEFT JOIN users u ON u.id = v.created_by
WHERE v.secret_id = @secret_id
ORDER BY v.version DESC;

-- name: ResolveJobSecrets :many
-- The live secrets a job may receive, by name: the job's environment's secret wins over a
-- project-wide one of the same name. environment_id NULL = a job without an environment.
SELECT DISTINCT ON (s.name)
       s.id, s.name, s.environment_id, v.version, v.ciphertext, v.nonce, v.dek_enc
FROM secrets s
JOIN secret_versions v ON v.secret_id = s.id AND v.version = s.current_version
WHERE s.project_id = @project_id AND s.deleted_at IS NULL AND v.destroyed_at IS NULL
  AND s.name = ANY (@names::text[])
  AND (s.environment_id IS NULL OR s.environment_id = sqlc.narg(environment_id)::uuid)
ORDER BY s.name, s.environment_id NULLS LAST;

-- name: JobSecretNames :many
-- The subset of names a job can be given (no decryption: used when the job becomes ready).
SELECT DISTINCT s.name
FROM secrets s
WHERE s.project_id = @project_id AND s.deleted_at IS NULL
  AND s.name = ANY (@names::text[])
  AND (s.environment_id IS NULL OR s.environment_id = sqlc.narg(environment_id)::uuid);

-- name: GetJobMasks :one
SELECT masks_enc FROM job_tokens WHERE job_id = @job_id;
