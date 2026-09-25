-- 000006_deployments: deploy targets (SSH hosts, Docker hosts, Kubernetes clusters) and
-- deployments with their logs. A deployment puts one container image version on one target
-- for one environment; a rollback is a new deployment pointing at the one it replaces.

CREATE TYPE deploy_target_kind AS ENUM ('ssh', 'docker', 'kubernetes');
CREATE TYPE deploy_strategy AS ENUM ('rolling', 'blue_green');
CREATE TYPE deployment_status AS ENUM ('pending', 'running', 'succeeded', 'failed');

CREATE TABLE deploy_targets (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  -- Pipelines refer to targets by name (`deploy.target`).
  name             text NOT NULL CHECK (name ~ '^[a-z0-9](?:[a-z0-9_-]{0,38}[a-z0-9])?$'),
  kind             deploy_target_kind NOT NULL,
  description      text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  config           jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(config) = 'object'),
  -- AES-GCM (master key ring) of the JSON credentials; never returned by the API.
  credentials_enc  bytea NOT NULL,
  last_test_at     timestamptz,
  last_test_ok     boolean,
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  version          integer NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, name)
);
CREATE TRIGGER deploy_targets_updated_at BEFORE UPDATE ON deploy_targets FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE projects ADD COLUMN last_deployment_number integer NOT NULL DEFAULT 0;

CREATE TABLE deployments (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  environment_id   uuid NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
  number           integer NOT NULL,
  -- History outlives targets: the name is kept when a target is deleted.
  target_id        uuid REFERENCES deploy_targets (id) ON DELETE SET NULL,
  target_name      text NOT NULL,
  target_kind      deploy_target_kind NOT NULL,
  version          text NOT NULL CHECK (length(version) BETWEEN 1 AND 255),
  previous_version text NOT NULL DEFAULT '',
  strategy         deploy_strategy NOT NULL,
  status           deployment_status NOT NULL DEFAULT 'pending',
  failure_reason   text,
  reverted         boolean NOT NULL DEFAULT false, -- a failed deployment put the previous release back
  health           jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(health) = 'array'),
  run_id           uuid REFERENCES pipeline_runs (id) ON DELETE SET NULL,
  job_id           uuid REFERENCES pipeline_jobs (id) ON DELETE SET NULL,
  rollback_of_id   uuid REFERENCES deployments (id) ON DELETE SET NULL,
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  log_bytes        bigint NOT NULL DEFAULT 0,
  started_at       timestamptz,
  finished_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, number)
);
CREATE INDEX deployments_project_created_idx ON deployments (project_id, created_at DESC, id DESC);
CREATE INDEX deployments_environment_idx ON deployments (environment_id, created_at DESC);
CREATE INDEX deployments_target_idx ON deployments (target_id) WHERE status IN ('pending', 'running');
-- One deployment at a time per environment.
CREATE UNIQUE INDEX deployments_one_active_per_env ON deployments (environment_id) WHERE status IN ('pending', 'running');
CREATE UNIQUE INDEX deployments_job_key ON deployments (job_id) WHERE job_id IS NOT NULL;
CREATE TRIGGER deployments_updated_at BEFORE UPDATE ON deployments FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE environments ADD COLUMN current_deployment_id uuid REFERENCES deployments (id) ON DELETE SET NULL;

CREATE TABLE deployment_log_chunks (
  deployment_id  uuid NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
  seq            integer NOT NULL CHECK (seq >= 0),
  content        text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, seq)
);
