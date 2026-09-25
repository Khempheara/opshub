-- 000004_pipelines: pipeline runs, jobs (one row per attempt), steps, log chunks, approvals
-- and cron schedules. A project has one pipeline, defined in `.opshub.yml`; every run keeps a
-- snapshot of the definition it was created from.

ALTER TABLE projects ADD COLUMN last_run_number integer NOT NULL DEFAULT 0;

CREATE TYPE run_status AS ENUM ('queued', 'running', 'waiting', 'succeeded', 'failed', 'canceled');
CREATE TYPE run_trigger AS ENUM ('push', 'pull_request', 'tag', 'manual', 'schedule');
CREATE TYPE job_status AS ENUM ('created', 'waiting_approval', 'queued', 'running', 'succeeded', 'failed', 'canceled', 'skipped');
CREATE TYPE step_status AS ENUM ('pending', 'running', 'succeeded', 'failed', 'skipped', 'canceled');
CREATE TYPE approval_decision AS ENUM ('approved', 'rejected');

CREATE TABLE pipeline_runs (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  number           integer NOT NULL,
  status           run_status NOT NULL DEFAULT 'queued',
  trigger          run_trigger NOT NULL,
  ref              text NOT NULL CHECK (length(ref) BETWEEN 1 AND 255),   -- refs/heads/main, refs/tags/v1
  commit_sha       text NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{7,64}$'),
  title            text NOT NULL DEFAULT '' CHECK (length(title) <= 200), -- commit message / PR title
  actor_name       text NOT NULL DEFAULT '' CHECK (length(actor_name) <= 100), -- Git user for webhook runs
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,          -- OpsHub user for manual runs
  rerun_of         uuid REFERENCES pipeline_runs (id) ON DELETE SET NULL,
  definition       jsonb,                                                  -- NULL when the file was invalid
  problems         jsonb,                                                  -- validation problems, if any
  variables        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(variables) = 'object'),
  started_at       timestamptz,
  finished_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, number)
);
CREATE INDEX pipeline_runs_project_created_idx ON pipeline_runs (project_id, created_at DESC, id DESC);
CREATE INDEX pipeline_runs_project_status_idx ON pipeline_runs (project_id, status);
CREATE TRIGGER pipeline_runs_updated_at BEFORE UPDATE ON pipeline_runs FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A retry adds a row with attempt + 1; the highest attempt per name is the job's current state.
CREATE TABLE pipeline_jobs (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  run_id           uuid NOT NULL REFERENCES pipeline_runs (id) ON DELETE CASCADE,
  project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  name             text NOT NULL,
  stage            text NOT NULL,
  stage_index      integer NOT NULL,
  needs            text[] NOT NULL DEFAULT '{}',
  condition        text NOT NULL CHECK (condition IN ('on_success', 'on_failure', 'always', 'manual')),
  environment      text,
  environment_id   uuid REFERENCES environments (id) ON DELETE SET NULL,
  runs_on          text[] NOT NULL DEFAULT '{}',
  spec             jsonb NOT NULL,          -- image, steps, variables, cache, artifacts, deploy
  status           job_status NOT NULL DEFAULT 'created',
  attempt          integer NOT NULL DEFAULT 1,
  runner_id        uuid,                    -- FK added with runners (Module 5)
  timeout_seconds  integer NOT NULL CHECK (timeout_seconds > 0),
  exit_code        integer,
  failure_reason   text,
  log_bytes        bigint NOT NULL DEFAULT 0,
  queued_at        timestamptz,
  started_at       timestamptz,
  finished_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, name, attempt)
);
CREATE INDEX pipeline_jobs_run_idx ON pipeline_jobs (run_id, name, attempt DESC);
-- Runner dispatch: oldest queued job of the organization (SELECT … FOR UPDATE SKIP LOCKED).
CREATE INDEX pipeline_jobs_queue_idx ON pipeline_jobs (organization_id, queued_at) WHERE status = 'queued';
CREATE INDEX pipeline_jobs_running_idx ON pipeline_jobs (started_at) WHERE status = 'running';
CREATE TRIGGER pipeline_jobs_updated_at BEFORE UPDATE ON pipeline_jobs FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE job_steps (
  job_id       uuid NOT NULL REFERENCES pipeline_jobs (id) ON DELETE CASCADE,
  index        integer NOT NULL,
  name         text NOT NULL,
  command      text NOT NULL,
  status       step_status NOT NULL DEFAULT 'pending',
  exit_code    integer,
  started_at   timestamptz,
  finished_at  timestamptz,
  PRIMARY KEY (job_id, index)
);

-- Append-only log chunks; seq is chosen by the runner so uploads are idempotent.
CREATE TABLE job_log_chunks (
  job_id      uuid NOT NULL REFERENCES pipeline_jobs (id) ON DELETE CASCADE,
  seq         integer NOT NULL CHECK (seq >= 0),
  content     text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (job_id, seq)
);

CREATE TABLE job_approvals (
  id          uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  job_id      uuid NOT NULL REFERENCES pipeline_jobs (id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  decision    approval_decision NOT NULL,
  comment     text NOT NULL DEFAULT '' CHECK (length(comment) <= 500),
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (job_id, user_id)
);

-- Cron triggers from the default branch's `.opshub.yml`, synced whenever it is read.
CREATE TABLE pipeline_schedules (
  id           uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  project_id   uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  cron         text NOT NULL,
  next_run_at  timestamptz NOT NULL,
  last_run_at  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, cron)
);
CREATE INDEX pipeline_schedules_due_idx ON pipeline_schedules (next_run_at);
CREATE TRIGGER pipeline_schedules_updated_at BEFORE UPDATE ON pipeline_schedules FOR EACH ROW EXECUTE FUNCTION set_updated_at();
