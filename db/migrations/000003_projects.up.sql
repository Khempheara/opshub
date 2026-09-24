-- 000003_projects: projects, project access grants, repositories + webhook deliveries,
-- environments with protection rules, and idempotency keys.

CREATE TABLE projects (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  slug             text NOT NULL CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$'),
  name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  description      text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  default_branch   text NOT NULL DEFAULT 'main' CHECK (length(default_branch) BETWEEN 1 AND 255),
  version          integer NOT NULL DEFAULT 1,
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  deleted_at       timestamptz
);
CREATE UNIQUE INDEX projects_org_slug_key ON projects (organization_id, slug) WHERE deleted_at IS NULL;
CREATE INDEX projects_org_name_idx ON projects (organization_id, name, id) WHERE deleted_at IS NULL;
CREATE TRIGGER projects_updated_at BEFORE UPDATE ON projects FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A grant gives one user or one team a role on a project (docs/rbac.md). The Owner role is
-- never granted per project: project Owners are the organization's Owners.
CREATE TABLE project_members (
  id          uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  user_id     uuid REFERENCES users (id) ON DELETE CASCADE,
  team_id     uuid REFERENCES teams (id) ON DELETE CASCADE,
  role        member_role NOT NULL CHECK (role <> 'owner'),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (num_nonnulls(user_id, team_id) = 1)
);
CREATE UNIQUE INDEX project_members_user_key ON project_members (project_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX project_members_team_key ON project_members (project_id, team_id) WHERE team_id IS NOT NULL;
CREATE INDEX project_members_user_idx ON project_members (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX project_members_team_idx ON project_members (team_id) WHERE team_id IS NOT NULL;
CREATE TRIGGER project_members_updated_at BEFORE UPDATE ON project_members FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TYPE git_provider AS ENUM ('github', 'gitlab');
CREATE TYPE webhook_mode AS ENUM ('automatic', 'manual');

-- At most one repository per project. The access token and the webhook HMAC secret are
-- encrypted with the master key ring (AES-256-GCM, AAD = repository id).
CREATE TABLE repositories (
  id                  uuid PRIMARY KEY,
  project_id          uuid NOT NULL UNIQUE REFERENCES projects (id) ON DELETE CASCADE,
  provider            git_provider NOT NULL,
  base_url            text CHECK (base_url ~ '^https://'),  -- self-hosted instance; NULL = github.com / gitlab.com
  full_name           text NOT NULL CHECK (length(full_name) BETWEEN 1 AND 255),
  external_id         text NOT NULL,
  web_url             text NOT NULL,
  clone_url           text NOT NULL,
  default_branch      text NOT NULL,
  access_token_enc    bytea NOT NULL,
  webhook_secret_enc  bytea NOT NULL,
  webhook_mode        webhook_mode NOT NULL,
  webhook_id          text,                                   -- set when OpsHub created the hook
  connected_by        uuid REFERENCES users (id) ON DELETE SET NULL,
  last_delivery_at    timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  CHECK ((webhook_mode = 'automatic') = (webhook_id IS NOT NULL))
);
CREATE TRIGGER repositories_updated_at BEFORE UPDATE ON repositories FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Every webhook request, kept 30 days for debugging. Deliveries with a bad signature are
-- recorded without their payload. Valid deliveries are unique per provider delivery id, so
-- redeliveries are recognized.
CREATE TABLE webhook_deliveries (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  repository_id    uuid NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  delivery_id      text NOT NULL CHECK (length(delivery_id) <= 200),
  event            text NOT NULL CHECK (length(event) <= 100),
  ref              text NOT NULL DEFAULT '' CHECK (length(ref) <= 255),
  commit_sha       text NOT NULL DEFAULT '' CHECK (length(commit_sha) <= 64),
  signature_valid  boolean NOT NULL,
  payload          jsonb,
  received_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX webhook_deliveries_valid_key ON webhook_deliveries (repository_id, delivery_id) WHERE signature_valid;
CREATE INDEX webhook_deliveries_repo_received_idx ON webhook_deliveries (repository_id, received_at DESC, id DESC);
CREATE INDEX webhook_deliveries_received_idx ON webhook_deliveries (received_at);

CREATE TYPE environment_kind AS ENUM ('development', 'staging', 'production');

-- Environment names are identifiers (referenced from .opshub.yml), so they are slug-like.
-- `variables` holds non-secret configuration only; secrets arrive with Module 8.
CREATE TABLE environments (
  id          uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  name        text NOT NULL CHECK (name ~ '^[a-z0-9](?:[a-z0-9_-]{0,38}[a-z0-9])?$'),
  kind        environment_kind NOT NULL,
  variables   jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(variables) = 'object'),
  version     integer NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at  timestamptz
);
CREATE UNIQUE INDEX environments_project_name_key ON environments (project_id, name) WHERE deleted_at IS NULL;
CREATE TRIGGER environments_updated_at BEFORE UPDATE ON environments FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- An environment is protected when it has a rule. Empty allowed_branches = any branch.
CREATE TABLE protection_rules (
  environment_id      uuid PRIMARY KEY REFERENCES environments (id) ON DELETE CASCADE,
  required_approvals  integer NOT NULL DEFAULT 1 CHECK (required_approvals BETWEEN 0 AND 10),
  allowed_branches    text[] NOT NULL DEFAULT '{}' CHECK (cardinality(allowed_branches) <= 20),
  allowed_roles       member_role[] NOT NULL DEFAULT '{owner,admin}' CHECK (cardinality(allowed_roles) BETWEEN 1 AND 4),
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER protection_rules_updated_at BEFORE UPDATE ON protection_rules FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Idempotency-Key records (docs/architecture.md). Keys are scoped to the user who sent them
-- and replayed for 24 hours; the request hash detects a key reused with a different request.
CREATE TABLE idempotency_keys (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  user_id          uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  key              text NOT NULL CHECK (length(key) BETWEEN 1 AND 255),
  request_hash     bytea NOT NULL,
  response_status  integer,                    -- NULL while the first request is in flight
  response_body    bytea,
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL
);
CREATE UNIQUE INDEX idempotency_keys_user_key ON idempotency_keys (user_id, key);
CREATE INDEX idempotency_keys_expires_idx ON idempotency_keys (expires_at);
