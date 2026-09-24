DROP TABLE IF EXISTS audit_log;
DROP FUNCTION IF EXISTS audit_log_immutable();
DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;
DROP TABLE IF EXISTS organization_members;
DROP TABLE IF EXISTS organizations;
DROP TYPE IF EXISTS member_role;
DROP TABLE IF EXISTS user_recovery_codes;
DROP TABLE IF EXISTS email_tokens;
DROP TYPE IF EXISTS email_token_purpose;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS user_identities;
DROP TABLE IF EXISTS users;
DROP FUNCTION IF EXISTS set_updated_at();
DROP FUNCTION IF EXISTS uuid_generate_v7();
-- citext is left installed: other objects in the database may depend on it.
