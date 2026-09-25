-- 000008_secrets: write-only project secrets with versions and envelope encryption.
-- Each version's value is sealed with its own random data key (DEK, AES-256-GCM); the DEK is
-- wrapped by the master key ring (KEK). Rotating a secret adds a version and destroys the
-- previous values; only metadata (who, when) is kept.

CREATE TABLE secrets (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  -- NULL: the secret applies to every environment (and to jobs without one).
  environment_id   uuid REFERENCES environments (id) ON DELETE CASCADE,
  -- Injected as an environment variable, so the name must be a valid one.
  name             text NOT NULL CHECK (name ~ '^[A-Z_][A-Z0-9_]{0,127}$' AND name !~ '^OPSHUB_'),
  description      text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  current_version  integer NOT NULL DEFAULT 1 CHECK (current_version >= 1),
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  version          integer NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  deleted_at       timestamptz
);
-- One live secret per name and scope (a project-wide one and an environment one may share a
-- name: the environment's wins for its jobs).
CREATE UNIQUE INDEX secrets_scope_name_key ON secrets (project_id, environment_id, name)
  NULLS NOT DISTINCT WHERE deleted_at IS NULL;
CREATE INDEX secrets_environment_idx ON secrets (environment_id) WHERE environment_id IS NOT NULL;
CREATE TRIGGER secrets_updated_at BEFORE UPDATE ON secrets FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE secret_versions (
  secret_id     uuid NOT NULL REFERENCES secrets (id) ON DELETE CASCADE,
  version       integer NOT NULL CHECK (version >= 1),
  -- AES-256-GCM of the value under the DEK; aad = "secret:<secret id>:<version>".
  ciphertext    bytea,
  nonce         bytea,
  -- The DEK sealed by the key ring (same aad); kek_id is the master key that sealed it.
  dek_enc       bytea,
  kek_id        text,
  created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  -- Set when the value was destroyed (rotation or deletion); the row stays as history.
  destroyed_at  timestamptz,
  PRIMARY KEY (secret_id, version),
  CHECK ((destroyed_at IS NULL) = (ciphertext IS NOT NULL AND nonce IS NOT NULL AND dek_enc IS NOT NULL AND kek_id IS NOT NULL))
);

-- Values to mask in a running job's log, sealed by the key ring (aad = "job-masks:<job id>").
-- Lives as long as the job token, so the API can mask output it stores.
ALTER TABLE job_tokens ADD COLUMN masks_enc bytea;
