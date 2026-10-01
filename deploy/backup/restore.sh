#!/bin/sh
# Restores an OpsHub backup made by backup.sh into an EMPTY database, then checks it.
# See docs/backup.md (restore and the quarterly restore drill).
#
#   restore.sh <backup file (.dump.age)>
#
# Environment:
#   OPSHUB_RESTORE_DATABASE_URL   target database; the role must be able to create the schema
#                                 (the migration owner, e.g. opshub_migrator)
#   OPSHUB_BACKUP_AGE_IDENTITY    path to the age private key file (keep it offline; mount it
#                                 only for the restore)
#   OPSHUB_RESTORE_JOBS           parallel restore jobs (default 4)
#   OPSHUB_RESTORE_FORCE=1        restore into a database that already has OpsHub data (drops it first)
set -eu

file="${1:?usage: restore.sh <backup.dump.age>}"
: "${OPSHUB_RESTORE_DATABASE_URL:?set OPSHUB_RESTORE_DATABASE_URL}"
: "${OPSHUB_BACKUP_AGE_IDENTITY:?set OPSHUB_BACKUP_AGE_IDENTITY (path to the age private key)}"
JOBS="${OPSHUB_RESTORE_JOBS:-4}"

log() { printf '%s restore: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
psql_q() { psql --no-psqlrc --quiet --tuples-only --no-align --dbname="$OPSHUB_RESTORE_DATABASE_URL" -c "$1"; }

[ -f "$file" ] || { log "no such file: $file"; exit 2; }
if [ -f "$file.sha256" ]; then
	( cd "$(dirname "$file")" && sha256sum -c "$(basename "$file").sha256" >/dev/null ) || { log "checksum mismatch: $file"; exit 1; }
	log "checksum ok"
fi

tables="$(psql_q "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'")"
if [ "$tables" != 0 ] && [ "${OPSHUB_RESTORE_FORCE:-}" != 1 ]; then
	log "the target database has $tables tables; restore into an empty database or set OPSHUB_RESTORE_FORCE=1"
	exit 2
fi

if [ "$tables" != 0 ]; then
	# pg_restore --clean can't drop the partitions' inherited constraints, so empty the schema
	# first: tables (a partitioned table takes its partitions along), sequences, views,
	# functions and types, except what extensions own. The schema itself stays, and with it
	# the default privileges that give the application role its rights.
	log "OPSHUB_RESTORE_FORCE=1: dropping the $tables existing tables"
	psql --no-psqlrc --quiet -v ON_ERROR_STOP=1 --dbname="$OPSHUB_RESTORE_DATABASE_URL" <<'SQL'
DO $$
DECLARE r record;
BEGIN
  -- Names as text: an owned sequence is gone with its table by the time the loop reaches it.
  FOR r IN SELECT format('public.%I', c.relname) AS name, c.relkind FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
      WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'S') AND NOT c.relispartition
        AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype = 'e')
      ORDER BY c.relkind = 'S'
  LOOP
    EXECUTE format('DROP %s IF EXISTS %s CASCADE',
      CASE r.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'S' THEN 'SEQUENCE' ELSE 'TABLE' END, r.name);
  END LOOP;
  FOR r IN SELECT p.oid::regprocedure::text AS name FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
      WHERE n.nspname = 'public' AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
  LOOP
    EXECUTE format('DROP ROUTINE IF EXISTS %s CASCADE', r.name);
  END LOOP;
  FOR r IN SELECT t.oid::regtype::text AS name FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
      WHERE n.nspname = 'public' AND t.typtype IN ('e', 'd', 'c') AND (t.typrelid = 0 OR t.typtype <> 'c')
        AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = t.oid AND d.deptype = 'e')
  LOOP
    EXECUTE format('DROP TYPE IF EXISTS %s CASCADE', r.name);
  END LOOP;
END $$;
SQL
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
chmod 700 "$work"
log "decrypting $(basename "$file")"
age -d -i "$OPSHUB_BACKUP_AGE_IDENTITY" -o "$work/opshub.dump" "$file"

log "restoring with $JOBS jobs"
# Privileges: backups keep them (backup.sh), so they are restored exactly: the app role stays
# unable to change the audit log and only it may run the partition functions. The schema's
# default privileges would add rights to every table pg_restore creates, so they are off during
# the restore (the dump brings them back, and they are set again below). Backups made before
# privileges were kept have none: then the defaults stay on, and the restrictions the
# migrations add are applied again afterwards.
default_privileges() {
	psql --no-psqlrc --quiet -v ON_ERROR_STOP=1 --dbname="$OPSHUB_RESTORE_DATABASE_URL" <<SQL
DO \$\$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'opshub_app') THEN
    ALTER DEFAULT PRIVILEGES IN SCHEMA public $1 SELECT, INSERT, UPDATE, DELETE ON TABLES $2 opshub_app;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public $1 USAGE, SELECT ON SEQUENCES $2 opshub_app;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public $1 EXECUTE ON FUNCTIONS $2 opshub_app;
  END IF;
END \$\$;
SQL
}
if pg_restore --list "$work/opshub.dump" | grep -q ' ACL '; then
	default_privileges REVOKE FROM
	pg_restore --no-owner --exit-on-error --jobs="$JOBS" --dbname="$OPSHUB_RESTORE_DATABASE_URL" "$work/opshub.dump"
	default_privileges GRANT TO
else
	log "this backup has no privileges (made before they were kept): applying the defaults"
	default_privileges GRANT TO
	pg_restore --no-owner --no-privileges --exit-on-error --jobs="$JOBS" \
		--dbname="$OPSHUB_RESTORE_DATABASE_URL" "$work/opshub.dump"
	psql --no-psqlrc --quiet -v ON_ERROR_STOP=1 --dbname="$OPSHUB_RESTORE_DATABASE_URL" <<'SQL'
DO $$
DECLARE f text;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'opshub_app') THEN
    REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM opshub_app;
    FOR f IN SELECT p.oid::regprocedure::text FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = 'public' AND p.proname LIKE 'opshub\_maintain\_%' LOOP
      EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', f);
      EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO opshub_app', f);
    END LOOP;
  END IF;
END $$;
SQL
fi

# Checks: the schema version and that the core tables came back.
version="$(psql_q "SELECT version || CASE WHEN dirty THEN ' (dirty)' ELSE '' END FROM schema_migrations")"
log "schema version $version (compare with \`opshub-api migrate status\` of the version you run)"
for t in organizations users projects pipeline_runs deployments audit_log; do
	log "$t: $(psql_q "SELECT count(*) FROM $t") rows"
done
log "done — start OpsHub with the same OPSHUB_MASTER_KEYS as the source, or secrets can't be decrypted"
