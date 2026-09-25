-- 000005_runners: runners, their one-time registration tokens, per-job tokens, artifacts and
-- the pipeline cache. Tokens are stored as SHA-256 hashes only.

CREATE TABLE runners (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  labels           text[] NOT NULL DEFAULT '{}' CHECK (cardinality(labels) <= 20),
  token_hash       bytea NOT NULL UNIQUE,
  token_prefix     text NOT NULL,
  version          text NOT NULL DEFAULT '' CHECK (length(version) <= 50),
  os               text NOT NULL DEFAULT '' CHECK (length(os) <= 50),
  arch             text NOT NULL DEFAULT '' CHECK (length(arch) <= 50),
  max_concurrency  integer NOT NULL DEFAULT 1 CHECK (max_concurrency BETWEEN 1 AND 64),
  last_seen_at     timestamptz,
  disabled_at      timestamptz,
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  row_version      integer NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runners_org_idx ON runners (organization_id, name, id);
CREATE TRIGGER runners_updated_at BEFORE UPDATE ON runners FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE runner_registration_tokens (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  token_hash       bytea NOT NULL UNIQUE,
  labels           text[] NOT NULL DEFAULT '{}',
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  expires_at       timestamptz NOT NULL,
  used_at          timestamptz,
  runner_id        uuid REFERENCES runners (id) ON DELETE SET NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);

-- Before runners existed (Module 4) jobs could name any runner id; drop those references.
UPDATE pipeline_jobs SET runner_id = NULL WHERE runner_id IS NOT NULL;
ALTER TABLE pipeline_jobs
  ADD CONSTRAINT pipeline_jobs_runner_fk FOREIGN KEY (runner_id) REFERENCES runners (id) ON DELETE SET NULL;
CREATE INDEX pipeline_jobs_runner_running_idx ON pipeline_jobs (runner_id) WHERE status = 'running';

-- One token per job attempt, valid while the job can run (timeout + 10 minutes).
CREATE TABLE job_tokens (
  job_id      uuid PRIMARY KEY REFERENCES pipeline_jobs (id) ON DELETE CASCADE,
  token_hash  bytea NOT NULL UNIQUE,
  expires_at  timestamptz NOT NULL
);

-- A job's artifacts are one gzip-compressed tar archive of its declared paths.
CREATE TABLE artifacts (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  job_id           uuid NOT NULL REFERENCES pipeline_jobs (id) ON DELETE CASCADE,
  project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
  size_bytes       bigint NOT NULL CHECK (size_bytes >= 0),
  sha256           bytea NOT NULL,
  storage_key      text NOT NULL UNIQUE,
  expires_at       timestamptz NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (job_id)
);
CREATE INDEX artifacts_expires_idx ON artifacts (expires_at);

CREATE TABLE cache_entries (
  id            uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  project_id    uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  key           text NOT NULL CHECK (key ~ '^[A-Za-z0-9._-]{1,200}$'),
  storage_key   text NOT NULL UNIQUE,
  size_bytes    bigint NOT NULL CHECK (size_bytes >= 0),
  sha256        bytea NOT NULL,
  last_used_at  timestamptz NOT NULL DEFAULT now(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, key)
);
CREATE INDEX cache_entries_lru_idx ON cache_entries (project_id, last_used_at);
