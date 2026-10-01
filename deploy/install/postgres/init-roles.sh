#!/bin/sh
# Creates OpsHub's PostgreSQL roles with the passwords from .env. Runs once, when the database
# volume is first created (docker-entrypoint-initdb.d); see docs/database.md, "PostgreSQL roles".
#   opshub_migrator  owns the database/schema; runs migrations (DDL)
#   opshub_app       what the API connects as: DML only; audit_log is insert-only
#   opshub_backup    reads everything, writes nothing (pg_dump for backups)
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
	-v migrator="$OPSHUB_DB_MIGRATOR_PASSWORD" -v app="$OPSHUB_DB_APP_PASSWORD" -v backup="$OPSHUB_DB_BACKUP_PASSWORD" <<'SQL'
CREATE ROLE opshub_migrator LOGIN PASSWORD :'migrator';
CREATE ROLE opshub_app LOGIN PASSWORD :'app';
CREATE ROLE opshub_backup LOGIN PASSWORD :'backup' IN ROLE pg_read_all_data;

ALTER DATABASE opshub OWNER TO opshub_migrator;
ALTER SCHEMA public OWNER TO opshub_migrator;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE opshub TO opshub_app;
GRANT CONNECT ON DATABASE opshub TO opshub_backup;
GRANT USAGE ON SCHEMA public TO opshub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE opshub_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO opshub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE opshub_migrator IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO opshub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE opshub_migrator IN SCHEMA public
  GRANT EXECUTE ON FUNCTIONS TO opshub_app;
SQL
