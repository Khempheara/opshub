#!/bin/sh
# Starts OpsHub in one container (image ghcr.io/khempheara/opshub; docs/install-docker.md):
# PostgreSQL, the API, the web UI (nginx), Caddy when OPSHUB_DOMAIN is set, and nightly
# backups. Everything runs as the unprivileged postgres user (uid 70); all data lives in /data.
#
#   no OPSHUB_DOMAIN           try-out: http://localhost:8080 (development mode)
#   OPSHUB_DOMAIN=ops.ex.com   production: Caddy gets a Let's Encrypt certificate (ports 8080/8443)
#   OPSHUB_PUBLIC_URL=https:…  production behind your own HTTPS proxy, on port 8080
#
# If any process stops, the others are stopped too and the container exits (Docker restarts it).
set -eu
umask 077

DATA=/data
RUN=/tmp/opshub
PGDATA="$DATA/postgres"
export PGHOST="$RUN"

log() { printf '%s opshub: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "$*"; exit 1; }
rand() { tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 32 || true; }

[ -w "$DATA" ] || die "/data isn't writable by uid $(id -u): use a named volume (-v opshub-data:/data) or chown a host folder to 70:70"
mkdir -p "$DATA/blobs" "$DATA/backups" "$DATA/caddy" "$DATA/restore" "$RUN/nginx"

# ── Secrets: made on the first start, kept in /data (mode 600) ──────────────
if [ ! -s "$DATA/secrets.env" ]; then
	log "first start: generating the database passwords and encryption keys"
	{
		echo "OPSHUB_DB_ADMIN_PASSWORD=$(rand)"
		echo "OPSHUB_DB_MIGRATOR_PASSWORD=$(rand)"
		echo "OPSHUB_DB_APP_PASSWORD=$(rand)"
		echo "OPSHUB_DB_BACKUP_PASSWORD=$(rand)"
		# A new server restoring an old one's backup passes that server's keys (-e) on the first start.
		if [ -n "${OPSHUB_MASTER_KEYS:-}" ] && [ -n "${OPSHUB_JWT_KEYS:-}" ]; then
			log "using the OPSHUB_MASTER_KEYS and OPSHUB_JWT_KEYS given" >&2
			echo "OPSHUB_MASTER_KEYS=$OPSHUB_MASTER_KEYS"
			echo "OPSHUB_JWT_KEYS=$OPSHUB_JWT_KEYS"
		else
			opshub-api keys generate
		fi
	} > "$DATA/secrets.env.tmp"
	mv "$DATA/secrets.env.tmp" "$DATA/secrets.env"
fi
set -a
# shellcheck disable=SC1091
. "$DATA/secrets.env"
set +a

# ── Mode ────────────────────────────────────────────────────────────────────
if [ -n "${OPSHUB_DOMAIN:-}" ]; then
	MODE=https
	OPSHUB_PUBLIC_URL="${OPSHUB_PUBLIC_URL:-https://$OPSHUB_DOMAIN}"
	OPSHUB_CADDY_TLS="${OPSHUB_TLS:-${OPSHUB_ACME_EMAIL:-${OPSHUB_ADMIN_EMAIL:-}}}"
	[ -n "$OPSHUB_CADDY_TLS" ] || die "with OPSHUB_DOMAIN, set OPSHUB_ADMIN_EMAIL (also used for Let's Encrypt)"
	WEB_LISTEN=127.0.0.1:8081
elif case "${OPSHUB_PUBLIC_URL:-}" in https://*) true ;; *) false ;; esac; then
	MODE=proxy
	WEB_LISTEN=8080
else
	MODE=try
	OPSHUB_PUBLIC_URL="${OPSHUB_PUBLIC_URL:-http://localhost:8080}"
	WEB_LISTEN=8080
fi
if [ "$MODE" = try ]; then OPSHUB_ENV=development; else OPSHUB_ENV=production; fi
DB="host=$RUN"
cat > "$RUN/env" <<EOF
export OPSHUB_ENV='$OPSHUB_ENV'
export OPSHUB_PUBLIC_URL='$OPSHUB_PUBLIC_URL'
export OPSHUB_HTTP_ADDR='127.0.0.1:9000'
export OPSHUB_LOG_FORMAT='json'
export OPSHUB_DATABASE_URL='postgres://opshub_app:$OPSHUB_DB_APP_PASSWORD@/opshub?$DB'
export OPSHUB_MIGRATE_DATABASE_URL='postgres://opshub_migrator:$OPSHUB_DB_MIGRATOR_PASSWORD@/opshub?$DB'
export OPSHUB_TRUSTED_PROXIES='127.0.0.1/32'
export OPSHUB_BLOB_DIR='$DATA/blobs'
export OPSHUB_BOOTSTRAP_ADMIN_EMAIL='${OPSHUB_ADMIN_EMAIL:-}'
export OPSHUB_ALLOW_SIGNUP='${OPSHUB_ALLOW_SIGNUP:-$([ "$MODE" = try ] && echo true || echo false)}'
export OPSHUB_SMTP_HOST='${OPSHUB_SMTP_HOST:-localhost}'
export OPSHUB_SMTP_FROM='${OPSHUB_SMTP_FROM:-OpsHub <noreply@${OPSHUB_DOMAIN:-localhost}>}'
export OPSHUB_BACKUP_DATABASE_URL='postgres://opshub_backup:$OPSHUB_DB_BACKUP_PASSWORD@/opshub?$DB'
export OPSHUB_BACKUP_DIR='$DATA/backups'
export OPSHUB_WEB_LISTEN='$WEB_LISTEN'
EOF
# shellcheck disable=SC1091
. "$RUN/env"
log "mode: $MODE ($OPSHUB_PUBLIC_URL)"

# ── PostgreSQL ──────────────────────────────────────────────────────────────
if [ ! -s "$PGDATA/PG_VERSION" ]; then
	log "first start: creating the database"
	printf '%s\n' "$OPSHUB_DB_ADMIN_PASSWORD" > "$RUN/pw"
	# PostgreSQL's built-in UTF-8 locale: the image (musl) has no system locales.
	initdb -D "$PGDATA" -U postgres --auth=scram-sha-256 --pwfile="$RUN/pw" -E UTF8 \
		--locale-provider=builtin --builtin-locale=C.UTF-8 --no-instructions > "$RUN/initdb.log" 2>&1 ||
		{ cat "$RUN/initdb.log"; die "initdb failed"; }
	rm -f "$RUN/pw"
	pg_ctl -D "$PGDATA" -o "-c listen_addresses='' -c unix_socket_directories='$RUN'" -w start > /dev/null
	PGPASSWORD="$OPSHUB_DB_ADMIN_PASSWORD" createdb -U postgres opshub
	PGPASSWORD="$OPSHUB_DB_ADMIN_PASSWORD" POSTGRES_USER=postgres POSTGRES_DB=opshub /usr/local/lib/opshub/init-roles.sh > /dev/null
	pg_ctl -D "$PGDATA" -m fast -w stop > /dev/null
fi
# Only the local socket: nothing outside the container can reach the database.
postgres -D "$PGDATA" -c listen_addresses='' -c unix_socket_directories="$RUN" &
PG_PID=$!
i=0
until pg_isready -q; do
	i=$((i + 1)); [ "$i" -lt 120 ] || die "PostgreSQL didn't start"
	sleep 0.5
done

# ── Restore (files dropped into /data/restore, then the container restarted) ─
dump="$(find "$DATA/restore" -maxdepth 1 -name '*.dump.age' | head -n 1)"
if [ -n "$dump" ]; then
	[ -s "$DATA/restore/key.txt" ] || die "found $(basename "$dump") in /data/restore but no key.txt (the age private key) next to it"
	log "restoring $(basename "$dump")"
	OPSHUB_RESTORE_DATABASE_URL="$OPSHUB_MIGRATE_DATABASE_URL" OPSHUB_BACKUP_AGE_IDENTITY="$DATA/restore/key.txt" \
		OPSHUB_RESTORE_FORCE=1 opshub-restore "$dump" || die "the restore failed; the files stay in /data/restore"
	done_dir="$DATA/restore/done-$(date -u +%Y%m%dT%H%M%SZ)"
	mkdir -p "$done_dir"
	mv "$dump" "$done_dir/"
	[ ! -f "$dump.sha256" ] || mv "$dump.sha256" "$done_dir/"
	rm -f "$DATA/restore/key.txt"
	log "restored; the private key was removed from /data/restore"
fi

# ── Backups: always encrypted; a key pair is generated if none was given ──
if [ -z "${OPSHUB_BACKUP_AGE_RECIPIENTS:-}" ]; then
	if [ ! -s "$DATA/backup-public-key.txt" ]; then
		age-keygen -o "$DATA/backup-private-key.txt" 2> /dev/null
		age-keygen -y "$DATA/backup-private-key.txt" > "$DATA/backup-public-key.txt"
	fi
	OPSHUB_BACKUP_AGE_RECIPIENTS="$(cat "$DATA/backup-public-key.txt")"
fi
export OPSHUB_BACKUP_AGE_RECIPIENTS
echo "export OPSHUB_BACKUP_AGE_RECIPIENTS='$OPSHUB_BACKUP_AGE_RECIPIENTS'" >> "$RUN/env"
if [ -f "$DATA/backup-private-key.txt" ]; then
	log "WARNING: the backup private key is in /data/backup-private-key.txt. Copy it somewhere safe off this server"
	log "         (docker cp opshub:/data/backup-private-key.txt .), then delete it there: docker exec opshub rm /data/backup-private-key.txt"
fi
opshub-backup-schedule &
BACKUP_PID=$!

# ── API, web, HTTPS ─────────────────────────────────────────────────────────
opshub-api serve &
API_PID=$!
# The shared template's ${…} placeholders are literal text here (SC2016).
# shellcheck disable=SC2016
sed -e "s|listen 8080;|listen $WEB_LISTEN;|" \
	-e 's|${OPSHUB_API_UPSTREAM}|127.0.0.1:9000|' \
	-e 's|${OPSHUB_REAL_IP_FROM}|127.0.0.1/32|' \
	-e 's|root /usr/share/nginx/html;|root /usr/share/opshub/web;|' \
	/etc/opshub/site.conf.template > "$RUN/nginx/opshub.conf"
nginx -e stderr -c /etc/opshub/nginx.conf -g 'daemon off;' &
WEB_PID=$!
PIDS="$PG_PID $BACKUP_PID $API_PID $WEB_PID"
if [ "$MODE" = https ]; then
	export OPSHUB_DOMAIN OPSHUB_CADDY_TLS XDG_DATA_HOME="$DATA/caddy" XDG_CONFIG_HOME="$DATA/caddy/config"
	caddy run --config /etc/opshub/Caddyfile --adapter caddyfile &
	PIDS="$PIDS $!"
fi

# ── Demo data (try-out mode only, once) ─────────────────────────────────────
if [ "${OPSHUB_DEMO:-}" = true ]; then
	if [ "$MODE" != try ]; then
		log "OPSHUB_DEMO is ignored with a domain or https URL: demo data is for trying OpsHub"
	elif [ ! -f "$DATA/.demo-seeded" ]; then
		(
			i=0
			until opshub-api healthcheck > /dev/null 2>&1; do
				i=$((i + 1)); [ "$i" -lt 120 ] || exit 1
				sleep 1
			done
			opshub-api seed && touch "$DATA/.demo-seeded"
		) || log "loading the demo data failed (see above)"
	fi
fi

# ── Watch ───────────────────────────────────────────────────────────────────
stop() {
	trap - TERM INT
	log "stopping"
	for p in $PIDS; do
		[ "$p" = "$PG_PID" ] || kill "$p" 2> /dev/null || true
	done
	wait "$API_PID" 2> /dev/null || true
	pg_ctl -D "$PGDATA" -m fast -w stop > /dev/null 2>&1 || true
	exit "$1"
}
trap 'stop 0' TERM INT
log "OpsHub is starting at $OPSHUB_PUBLIC_URL"
while :; do
	for p in $PIDS; do
		if ! kill -0 "$p" 2> /dev/null; then
			log "a process stopped (pid $p); stopping the container"
			stop 1
		fi
	done
	sleep 5 &
	wait $! || true
done
