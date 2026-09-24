DROP INDEX IF EXISTS organization_members_org_role_idx;
ALTER TABLE teams DROP COLUMN IF EXISTS version, DROP COLUMN IF EXISTS description;
DROP TABLE IF EXISTS invitations;
