-- Local mirror of the production role layout (docs/database.md, "PostgreSQL roles").
-- Runs once, when the postgres volume is first initialized.
--   opshub_migrator  owns the database/schema; runs migrations (DDL)
--   opshub_app       what the API connects as: DML only; audit_log is insert-only
--   opshub_backup    reads everything, writes nothing (pg_dump for backups)
-- Passwords here are for local development only.
CREATE ROLE opshub_migrator LOGIN PASSWORD 'opshub_migrator';
CREATE ROLE opshub_app LOGIN PASSWORD 'opshub_app';
CREATE ROLE opshub_backup LOGIN PASSWORD 'opshub_backup' IN ROLE pg_read_all_data;

ALTER DATABASE opshub OWNER TO opshub_migrator;
GRANT CONNECT ON DATABASE opshub TO opshub_app;
GRANT CONNECT ON DATABASE opshub TO opshub_backup;

\connect opshub
GRANT USAGE ON SCHEMA public TO opshub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE opshub_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO opshub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE opshub_migrator IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO opshub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE opshub_migrator IN SCHEMA public
  GRANT EXECUTE ON FUNCTIONS TO opshub_app;
