-- name: CreateDeployTargetWithID :one
-- The id is chosen by the caller: it is the associated data of the credentials' encryption.
INSERT INTO deploy_targets (id, organization_id, name, kind, description, config, credentials_enc, created_by)
VALUES (@id, @organization_id, @name, @kind, @description, @config, @credentials_enc, @created_by)
RETURNING *;

-- name: GetDeployTarget :one
SELECT * FROM deploy_targets WHERE id = @id;

-- name: GetDeployTargetByName :one
SELECT * FROM deploy_targets WHERE organization_id = @organization_id AND name = @name;

-- name: ListDeployTargets :many
SELECT * FROM deploy_targets WHERE organization_id = @organization_id ORDER BY name;

-- name: UpdateDeployTarget :one
UPDATE deploy_targets SET description = @description, config = @config, credentials_enc = @credentials_enc,
  version = version + 1
WHERE id = @id AND version = @version
RETURNING *;

-- name: DeleteDeployTarget :execrows
DELETE FROM deploy_targets WHERE id = @id;

-- name: RecordTargetTest :exec
UPDATE deploy_targets SET last_test_at = now(), last_test_ok = @ok WHERE id = @id;

-- name: CountActiveTargetDeployments :one
SELECT count(*) FROM deployments WHERE target_id = @target_id AND status IN ('pending', 'running');

-- name: NextDeploymentNumber :one
UPDATE projects SET last_deployment_number = last_deployment_number + 1 WHERE id = @id RETURNING last_deployment_number;

-- name: CreateDeployment :one
INSERT INTO deployments (organization_id, project_id, environment_id, number, target_id, target_name, target_kind,
  version, previous_version, strategy, run_id, job_id, rollback_of_id, created_by)
VALUES (@organization_id, @project_id, @environment_id, @number, @target_id, @target_name, @target_kind,
  @version, @previous_version, @strategy, @run_id, @job_id, @rollback_of_id, @created_by)
RETURNING *;

-- name: GetDeployment :one
SELECT * FROM deployments WHERE id = @id;

-- name: GetDeploymentDetail :one
SELECT sqlc.embed(d), e.name AS environment_name, coalesce(u.display_name, '')::text AS created_by_name,
       coalesce(r.number, 0)::integer AS run_number, coalesce(j.name, '')::text AS job_name,
       (e.current_deployment_id IS NOT DISTINCT FROM d.id)::boolean AS is_current
FROM deployments d
JOIN environments e ON e.id = d.environment_id
LEFT JOIN users u ON u.id = d.created_by
LEFT JOIN pipeline_runs r ON r.id = d.run_id
LEFT JOIN pipeline_jobs j ON j.id = d.job_id
WHERE d.id = @id;

-- name: ListDeployments :many
-- Newest first; keyset pagination on (created_at, id).
SELECT sqlc.embed(d), e.name AS environment_name, coalesce(u.display_name, '')::text AS created_by_name,
       coalesce(r.number, 0)::integer AS run_number, coalesce(j.name, '')::text AS job_name,
       (e.current_deployment_id IS NOT DISTINCT FROM d.id)::boolean AS is_current
FROM deployments d
JOIN environments e ON e.id = d.environment_id
LEFT JOIN users u ON u.id = d.created_by
LEFT JOIN pipeline_runs r ON r.id = d.run_id
LEFT JOIN pipeline_jobs j ON j.id = d.job_id
WHERE d.project_id = @project_id
  AND (sqlc.narg(environment_id)::uuid IS NULL OR d.environment_id = sqlc.narg(environment_id))
  AND (sqlc.narg(status)::text IS NULL OR d.status::text = sqlc.narg(status))
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR d.created_at >= sqlc.narg(from_time))
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR d.created_at < sqlc.narg(to_time))
  AND (NOT @only_current::boolean OR e.current_deployment_id = d.id)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL OR (d.created_at, d.id) < (sqlc.narg(cursor_time), sqlc.narg(cursor_id)::uuid))
ORDER BY d.created_at DESC, d.id DESC
LIMIT @page_size;

-- name: StartDeployment :one
UPDATE deployments SET status = 'running', started_at = now() WHERE id = @id AND status = 'pending' RETURNING *;

-- name: FinishDeployment :one
UPDATE deployments SET status = @status, failure_reason = sqlc.narg(failure_reason), reverted = @reverted,
  health = @health, finished_at = now(),
  previous_version = CASE WHEN previous_version = '' THEN @observed_previous::text ELSE previous_version END
WHERE id = @id AND status IN ('pending', 'running')
RETURNING *;

-- name: SetCurrentDeployment :exec
UPDATE environments SET current_deployment_id = @deployment_id WHERE id = @id;

-- name: GetCurrentDeployment :one
SELECT d.* FROM environments e JOIN deployments d ON d.id = e.current_deployment_id WHERE e.id = @environment_id;

-- name: PreviousSuccessfulDeployment :one
-- The latest successful deployment of the environment before the given time.
SELECT * FROM deployments
WHERE environment_id = @environment_id AND status = 'succeeded' AND created_at < @before AND id <> @exclude_id
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: DeploymentByJob :one
SELECT * FROM deployments WHERE job_id = @job_id;

-- name: InsertDeploymentLog :execrows
INSERT INTO deployment_log_chunks (deployment_id, seq, content) VALUES (@deployment_id, @seq, @content)
ON CONFLICT (deployment_id, seq) DO NOTHING;

-- name: AddDeploymentLogBytes :exec
UPDATE deployments SET log_bytes = log_bytes + @bytes WHERE id = @id;

-- name: ListDeploymentLogs :many
SELECT seq, content FROM deployment_log_chunks
WHERE deployment_id = @deployment_id AND seq > @after_seq
ORDER BY seq
LIMIT @page_size;

-- name: NextJobLogSeq :one
SELECT (coalesce(max(seq), -1) + 1)::integer FROM job_log_chunks WHERE job_id = @job_id;
