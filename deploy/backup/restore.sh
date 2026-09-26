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
#   OPSHUB_RESTORE_FORCE=1        restore into a database that already has tables (drops them)
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

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
chmod 700 "$work"
log "decrypting $(basename "$file")"
age -d -i "$OPSHUB_BACKUP_AGE_IDENTITY" -o "$work/opshub.dump" "$file"

log "restoring with $JOBS jobs"
pg_restore --clean --if-exists --no-owner --no-privileges --exit-on-error --jobs="$JOBS" \
	--dbname="$OPSHUB_RESTORE_DATABASE_URL" "$work/opshub.dump"

# Checks: the schema version and that the core tables came back.
version="$(psql_q "SELECT version || CASE WHEN dirty THEN ' (dirty)' ELSE '' END FROM schema_migrations")"
log "schema version $version (compare with \`opshub-api migrate status\` of the version you run)"
for t in organizations users projects pipeline_runs deployments audit_log; do
	log "$t: $(psql_q "SELECT count(*) FROM $t") rows"
done
log "done — start OpsHub with the same OPSHUB_MASTER_KEYS as the source, or secrets can't be decrypted"
