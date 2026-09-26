#!/bin/sh
# Runs backup.sh every day at OPSHUB_BACKUP_AT (HH:MM, UTC; default 02:00) — the compose
# `backup` service. Kubernetes uses a CronJob instead (Helm chart). A failed backup is logged
# and retried at the next scheduled time; watch `last-success` in the backup directory.
set -eu

AT="${OPSHUB_BACKUP_AT:-02:00}"
case "$AT" in
[0-2][0-9]:[0-5][0-9]) ;;
*) echo "OPSHUB_BACKUP_AT must be HH:MM (UTC), got $AT" >&2; exit 2 ;;
esac
hh=$(printf '%s' "$AT" | cut -d: -f1 | sed 's/^0//'); mm=$(printf '%s' "$AT" | cut -d: -f2 | sed 's/^0//')
target=$(( ${hh:-0} * 3600 + ${mm:-0} * 60 ))

if [ "${OPSHUB_BACKUP_ON_START:-}" = 1 ]; then
	/usr/local/bin/opshub-backup || echo "backup failed; retrying at $AT UTC" >&2
fi
while :; do
	now=$(( $(date -u +%s) % 86400 ))
	wait=$(( (target - now + 86400) % 86400 ))
	[ "$wait" -eq 0 ] && wait=86400
	echo "next backup at $AT UTC (in ${wait}s)"
	sleep "$wait"
	/usr/local/bin/opshub-backup || echo "backup failed; retrying at $AT UTC tomorrow" >&2
done
