-- name: NextRunNumber :one
-- Serializes run creation per project (row lock on the project).
UPDATE projects SET last_run_number = last_run_number + 1 WHERE id = @id RETURNING last_run_number;

-- name: InsertRun :one
INSERT INTO pipeline_runs (
  organization_id, project_id, number, status, trigger, ref, commit_sha, title, actor_name,
  created_by, rerun_of, definition, problems, variables, finished_at, committed_at
) VALUES (
  @organization_id, @project_id, @number, @status, @trigger, @ref, @commit_sha, @title, @actor_name,
  @created_by, @rerun_of, @definition, @problems, @variables, @finished_at, sqlc.narg(committed_at)
)
RETURNING *;

-- name: GetRun :one
SELECT * FROM pipeline_runs WHERE id = @id;

-- name: LockRun :one
SELECT * FROM pipeline_runs WHERE id = @id FOR UPDATE;

-- name: UpdateRunStatus :exec
UPDATE pipeline_runs SET status = @status, started_at = @started_at, finished_at = @finished_at WHERE id = @id;

-- name: ListRuns :many
SELECT r.*, coalesce(u.display_name, '')::text AS created_by_name
FROM pipeline_runs r
LEFT JOIN users u ON u.id = r.created_by
WHERE r.project_id = @project_id
  AND (sqlc.narg(status)::run_status IS NULL OR r.status = sqlc.narg(status)::run_status)
  AND (sqlc.narg(trigger)::run_trigger IS NULL OR r.trigger = sqlc.narg(trigger)::run_trigger)
  AND (sqlc.narg(ref)::text IS NULL OR r.ref = sqlc.narg(ref)::text)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (r.created_at, r.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY r.created_at DESC, r.id DESC
LIMIT @page_size;

-- name: InsertJob :one
INSERT INTO pipeline_jobs (
  run_id, project_id, organization_id, name, stage, stage_index, needs, condition, environment,
  environment_id, runs_on, spec, status, attempt, timeout_seconds
) VALUES (
  @run_id, @project_id, @organization_id, @name, @stage, @stage_index, @needs, @condition, @environment,
  @environment_id, @runs_on, @spec, @status, @attempt, @timeout_seconds
)
RETURNING *;

-- name: InsertStep :exec
INSERT INTO job_steps (job_id, index, name, command) VALUES (@job_id, @index, @name, @command);

-- name: CurrentJobs :many
-- The latest attempt of every job in a run.
SELECT DISTINCT ON (name) * FROM pipeline_jobs WHERE run_id = @run_id ORDER BY name, attempt DESC;

-- name: ListJobAttempts :many
SELECT * FROM pipeline_jobs WHERE run_id = @run_id AND name = @name ORDER BY attempt DESC;

-- name: GetJob :one
SELECT * FROM pipeline_jobs WHERE id = @id;

-- name: SetJobStatus :exec
UPDATE pipeline_jobs SET
  status = @status,
  failure_reason = sqlc.narg(failure_reason),
  exit_code = sqlc.narg(exit_code),
  queued_at = CASE WHEN @status = 'queued'::job_status THEN now() ELSE queued_at END,
  started_at = CASE WHEN @status = 'running'::job_status THEN now() ELSE started_at END,
  finished_at = CASE WHEN @status IN ('succeeded', 'failed', 'canceled', 'skipped') THEN now() ELSE finished_at END
WHERE id = @id;

-- name: ListSteps :many
SELECT * FROM job_steps WHERE job_id = @job_id ORDER BY index;

-- name: ListStepsForJobs :many
SELECT * FROM job_steps WHERE job_id = ANY(@job_ids::uuid[]) ORDER BY job_id, index;

-- name: UpdateStep :execrows
UPDATE job_steps SET
  status = @status,
  exit_code = sqlc.narg(exit_code),
  started_at = CASE WHEN @status = 'running'::step_status THEN coalesce(started_at, now()) ELSE started_at END,
  finished_at = CASE WHEN @status IN ('succeeded', 'failed', 'skipped', 'canceled') THEN now() ELSE finished_at END
WHERE job_id = @job_id AND index = @index;

-- name: FinishOpenSteps :exec
-- Steps still pending or running when a job ends.
UPDATE job_steps SET status = @status, finished_at = now()
WHERE job_id = @job_id AND status IN ('pending', 'running');

-- name: ClaimJob :one
-- Oldest queued runner job of the organization whose required labels the runner has.
SELECT * FROM pipeline_jobs
WHERE organization_id = @organization_id AND status = 'queued' AND runs_on <@ @labels::text[]
  AND spec->'deploy' IS NULL -- deploy jobs are performed by OpsHub (Module 6), not runners
ORDER BY queued_at, id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: StartJob :exec
UPDATE pipeline_jobs SET status = 'running', runner_id = @runner_id, started_at = now() WHERE id = @id;

-- name: InsertLogChunk :execrows
INSERT INTO job_log_chunks (job_id, seq, content) VALUES (@job_id, @seq, @content)
ON CONFLICT (job_id, seq) DO NOTHING;

-- name: AddLogBytes :one
UPDATE pipeline_jobs SET log_bytes = log_bytes + @bytes WHERE id = @id RETURNING log_bytes;

-- name: ListLogChunks :many
SELECT seq, content FROM job_log_chunks WHERE job_id = @job_id AND seq > @after_seq ORDER BY seq LIMIT @page_size;

-- name: ListApprovals :many
SELECT a.*, u.display_name, u.email
FROM job_approvals a JOIN users u ON u.id = a.user_id
WHERE a.job_id = @job_id ORDER BY a.created_at;

-- name: InsertApproval :one
INSERT INTO job_approvals (job_id, user_id, decision, comment) VALUES (@job_id, @user_id, @decision, @comment)
ON CONFLICT (job_id, user_id) DO NOTHING
RETURNING id;

-- name: CountApprovals :one
SELECT count(*) FROM job_approvals WHERE job_id = @job_id AND decision = 'approved';

-- name: GetEnvironmentByName :one
SELECT sqlc.embed(e),
       (pr.environment_id IS NOT NULL)::boolean AS protected,
       coalesce(pr.required_approvals, 0)::integer AS required_approvals,
       coalesce(pr.allowed_branches, '{}')::text[] AS allowed_branches,
       coalesce(pr.allowed_roles::text[], '{}')::text[] AS allowed_roles
FROM environments e
LEFT JOIN protection_rules pr ON pr.environment_id = e.id
WHERE e.project_id = @project_id AND e.name = @name AND e.deleted_at IS NULL;

-- name: ExpiredRunningJobs :many
SELECT id, run_id FROM pipeline_jobs
WHERE status = 'running' AND started_at + make_interval(secs => timeout_seconds) < now()
LIMIT 100;

-- name: StaleQueuedJobs :many
SELECT id, run_id FROM pipeline_jobs
WHERE status = 'queued' AND queued_at < now() - interval '24 hours'
LIMIT 100;

-- name: ListSchedules :many
SELECT * FROM pipeline_schedules WHERE project_id = @project_id ORDER BY cron;

-- name: UpsertSchedule :exec
INSERT INTO pipeline_schedules (project_id, cron, next_run_at) VALUES (@project_id, @cron, @next_run_at)
ON CONFLICT (project_id, cron) DO NOTHING;

-- name: DeleteSchedulesExcept :exec
DELETE FROM pipeline_schedules WHERE project_id = @project_id AND NOT (cron = ANY(@keep::text[]));

-- name: DueSchedules :many
SELECT * FROM pipeline_schedules WHERE next_run_at <= now() ORDER BY next_run_at LIMIT 50 FOR UPDATE SKIP LOCKED;

-- name: AdvanceSchedule :exec
UPDATE pipeline_schedules SET next_run_at = @next_run_at, last_run_at = now() WHERE id = @id;

-- name: GetSchedule :one
SELECT * FROM pipeline_schedules WHERE id = @id;

-- name: GetProjectForPipeline :one
SELECT p.*, r.id AS repository_id
FROM projects p
LEFT JOIN repositories r ON r.project_id = p.id
WHERE p.id = @id AND p.deleted_at IS NULL;

-- name: RecentRunExists :one
-- Guards against creating a second run for the same event when a worker is retried.
SELECT EXISTS (
  SELECT 1 FROM pipeline_runs
  WHERE project_id = @project_id AND trigger = @trigger AND ref = @ref AND commit_sha = @commit_sha
    AND rerun_of IS NULL AND created_at > now() - interval '1 hour'
);
