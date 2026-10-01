#!/bin/sh
# OpsHub database backup: pg_dump (custom format) encrypted with age, kept as 7 daily,
# 4 weekly (Sundays) and 3 monthly (1st of the month) copies, optionally copied to
# S3-compatible storage with rclone. See docs/backup.md.
#
# Environment:
#   OPSHUB_BACKUP_DATABASE_URL      database to dump; use the read-only opshub_backup role
#   OPSHUB_BACKUP_AGE_RECIPIENTS    age public keys (age1…), space or comma separated; the
#                                   private key never needs to be on this machine
#   OPSHUB_BACKUP_DIR               where backups are kept (default /backups)
#   OPSHUB_BACKUP_KEEP_DAILY/WEEKLY/MONTHLY   retention counts (default 7 / 4 / 3)
#   OPSHUB_BACKUP_RCLONE_REMOTE     optional rclone destination, e.g. "s3:my-bucket/opshub";
#                                   configure the remote with RCLONE_CONFIG_* variables
#
# Exit status is non-zero when any step fails; nothing partial is left in the backup
# directories (files are written under a temporary name and renamed when complete).
set -eu

: "${OPSHUB_BACKUP_DATABASE_URL:?set OPSHUB_BACKUP_DATABASE_URL}"
: "${OPSHUB_BACKUP_AGE_RECIPIENTS:?set OPSHUB_BACKUP_AGE_RECIPIENTS (age public keys): backups are always encrypted}"
DIR="${OPSHUB_BACKUP_DIR:-/backups}"
KEEP_DAILY="${OPSHUB_BACKUP_KEEP_DAILY:-7}"
KEEP_WEEKLY="${OPSHUB_BACKUP_KEEP_WEEKLY:-4}"
KEEP_MONTHLY="${OPSHUB_BACKUP_KEEP_MONTHLY:-3}"

log() { printf '%s backup: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

# Recipients: every age1… key becomes one -r flag.
set --
for r in $(printf '%s' "$OPSHUB_BACKUP_AGE_RECIPIENTS" | tr ',' ' '); do
	case "$r" in
	age1*) set -- "$@" -r "$r" ;;
	*) log "not an age public key: $r"; exit 2 ;;
	esac
done
[ "$#" -gt 0 ] || { log "no age recipients"; exit 2; }

mkdir -p "$DIR/daily" "$DIR/weekly" "$DIR/monthly"
umask 077
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
name="opshub-$stamp.dump.age"
tmp="$DIR/daily/.$name.partial"
trap 'rm -f "$tmp"' EXIT

log "dumping to daily/$name"
# pipefail isn't POSIX: check pg_dump's status through a marker file instead.
status_file="$(mktemp)"
# Privileges are kept (restore.sh reproduces them, e.g. the insert-only audit log); ownership
# isn't (the restoring role owns everything).
{ pg_dump --format=custom --compress=6 --no-owner --dbname="$OPSHUB_BACKUP_DATABASE_URL" \
	|| echo "$?" >"$status_file"; } | age "$@" -o "$tmp"
if [ -s "$status_file" ]; then
	log "pg_dump failed (exit $(cat "$status_file"))"
	rm -f "$status_file"
	exit 1
fi
rm -f "$status_file"
[ -s "$tmp" ] || { log "empty backup"; exit 1; }
mv "$tmp" "$DIR/daily/$name"
( cd "$DIR/daily" && sha256sum "$name" >"$name.sha256" )
log "wrote daily/$name ($(wc -c <"$DIR/daily/$name" | tr -d ' ') bytes)"

# Weekly and monthly copies are hard links to the same file (no extra space).
if [ "$(date -u +%u)" = 7 ]; then
	ln -f "$DIR/daily/$name" "$DIR/weekly/$name" && ln -f "$DIR/daily/$name.sha256" "$DIR/weekly/$name.sha256"
	log "kept as weekly"
fi
if [ "$(date -u +%d)" = 01 ]; then
	ln -f "$DIR/daily/$name" "$DIR/monthly/$name" && ln -f "$DIR/daily/$name.sha256" "$DIR/monthly/$name.sha256"
	log "kept as monthly"
fi

# prune DIR KEEP: remove all but the newest KEEP backups. Names hold a UTC timestamp, so the
# glob lists them oldest first.
prune() {
	total=0
	for f in "$1"/opshub-*.dump.age; do
		[ -e "$f" ] && total=$((total + 1))
	done
	extra=$((total - $2))
	for f in "$1"/opshub-*.dump.age; do
		[ "$extra" -gt 0 ] || break
		[ -e "$f" ] || continue
		rm -f "$f" "$f.sha256"
		log "removed $(basename "$1")/$(basename "$f")"
		extra=$((extra - 1))
	done
}
prune "$DIR/daily" "$KEEP_DAILY"
prune "$DIR/weekly" "$KEEP_WEEKLY"
prune "$DIR/monthly" "$KEEP_MONTHLY"

if [ -n "${OPSHUB_BACKUP_RCLONE_REMOTE:-}" ]; then
	log "copying to $OPSHUB_BACKUP_RCLONE_REMOTE"
	# sync mirrors the retention above, so the remote keeps the same set of backups.
	rclone sync --checksum "$DIR" "$OPSHUB_BACKUP_RCLONE_REMOTE" --exclude ".*.partial"
fi

date -u +%Y-%m-%dT%H:%M:%SZ >"$DIR/last-success"
log "done"
