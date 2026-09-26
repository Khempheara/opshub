#!/bin/sh
# Restore drill against the compose stack (`make backup-drill`; also run in CI):
# makes a throwaway age key, backs up the running database with the backup image, restores
# it into a scratch database and compares row counts with the source. Nothing is kept.
set -eu

compose="docker compose"
# The drill's containers run as the caller, so they can share files in a host directory on
# Linux too (the scripts don't depend on the uid).
as_me="--user $(id -u):$(id -g)"
key_dir="$(mktemp -d)"
drill_db="opshub_drill_$(date +%s)"
cleanup() {
	rm -rf "$key_dir"
	$compose exec -T postgres psql -U opshub -d postgres -qc "DROP DATABASE IF EXISTS $drill_db" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "drill: building the backup image"
$compose --profile backup build -q backup

echo "drill: making a throwaway key"
# shellcheck disable=SC2086 # $as_me is two words on purpose
$compose --profile backup run --rm --no-deps -T -v "$key_dir:/keys" $as_me backup \
	age-keygen -o /keys/drill.key 2>/dev/null
recipient="$(grep -o 'age1[0-9a-z]*' "$key_dir/drill.key")"

echo "drill: backing up"
# shellcheck disable=SC2016 # the script runs inside the container
# shellcheck disable=SC2086
$compose --profile backup run --rm -T $as_me -e OPSHUB_BACKUP_AGE_RECIPIENTS="$recipient" -e OPSHUB_BACKUP_DIR=/tmp/drill \
	-v "$key_dir:/keys" --entrypoint sh backup -c '
	set -eu
	opshub-backup
	file=$(ls /tmp/drill/daily/*.dump.age)
	if age -d -i /dev/null "$file" >/dev/null 2>&1; then echo "drill: backup is not encrypted" >&2; exit 1; fi
	cp "$file" "$file.sha256" /keys/'

echo "drill: restoring into $drill_db"
$compose exec -T postgres psql -U opshub -d postgres -qc "CREATE DATABASE $drill_db OWNER opshub_migrator"
file="$(basename "$(ls "$key_dir"/*.dump.age)")"
# shellcheck disable=SC2086
$compose --profile backup run --rm --no-deps -T $as_me -v "$key_dir:/keys" \
	-e OPSHUB_RESTORE_DATABASE_URL="postgres://opshub_migrator:opshub_migrator@postgres:5432/$drill_db?sslmode=disable" \
	-e OPSHUB_BACKUP_AGE_IDENTITY=/keys/drill.key backup opshub-restore "/keys/$file"

echo "drill: comparing row counts"
query="SELECT string_agg(t || '=' || n, ' ' ORDER BY t) FROM (
  SELECT 'organizations' t, count(*) n FROM organizations UNION ALL SELECT 'users', count(*) FROM users
  UNION ALL SELECT 'projects', count(*) FROM projects UNION ALL SELECT 'pipeline_runs', count(*) FROM pipeline_runs
  UNION ALL SELECT 'deployments', count(*) FROM deployments UNION ALL SELECT 'audit_log', count(*) FROM audit_log
  UNION ALL SELECT 'secrets', count(*) FROM secrets UNION ALL SELECT 'log_entries', count(*) FROM log_entries) c"
src="$($compose exec -T postgres psql -U opshub -d opshub -tAc "$query")"
dst="$($compose exec -T postgres psql -U opshub -d "$drill_db" -tAc "$query")"
echo "  source:   $src"
echo "  restored: $dst"
[ "$src" = "$dst" ] || { echo "drill: FAILED — the restored data differs" >&2; exit 1; }
echo "drill: OK"
