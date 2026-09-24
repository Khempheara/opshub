-- 000002_rbac: organization invitations; team descriptions and optimistic locking.

CREATE TABLE invitations (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  email            citext NOT NULL,
  role             member_role NOT NULL,
  token_hash       bytea NOT NULL UNIQUE,          -- SHA-256 of the emailed token
  invited_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  expires_at       timestamptz NOT NULL,
  accepted_at      timestamptz,
  accepted_by      uuid REFERENCES users (id) ON DELETE SET NULL,
  revoked_at       timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now()
);
-- At most one open invitation per address and organization.
CREATE UNIQUE INDEX invitations_open_key ON invitations (organization_id, email)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX invitations_org_created_idx ON invitations (organization_id, created_at DESC);

ALTER TABLE teams
  ADD COLUMN description text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  ADD COLUMN version integer NOT NULL DEFAULT 1;

CREATE INDEX organization_members_org_role_idx ON organization_members (organization_id, role);
