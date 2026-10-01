#!/usr/bin/env bash
# End-to-end test of the all-in-one image (make aio-test; CI). Builds it (or uses $IMAGE), then:
# try-out mode (first admin, verify-email, data kept across restarts), an encrypted backup and a
# restore from /data/restore, a crashed process stopping the container, a clean shutdown,
# HTTPS mode (Caddy's internal CA) and that nothing runs as root. Removes what it created.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${IMAGE:-opshub:aio-test}"
C=opshub-aio-test
CS=opshub-aio-test-https
URL=http://localhost:18190
EMAIL=admin@example.com
PASSWORD=aio-test-mekong-sunrise-42

cleanup() {
	status=$?
	if (( status != 0 )); then docker logs --tail 60 "$C" 2>&1 | grep -v '^{"time"' || true; fi
	docker rm -f "$C" "$C-new" "$CS" > /dev/null 2>&1 || true
	docker volume rm -f opshub-aio-test opshub-aio-test-new opshub-aio-test-https > /dev/null 2>&1 || true
	exit "$status"
}
trap cleanup EXIT

expect() {
	local what="$1"
	shift
	if "$@"; then printf '  ✓ %s\n' "$what"; else printf '  ✗ %s\n' "$what" >&2; exit 1; fi
}
expect_eq() {
	if [[ "$2" == "$3" ]]; then printf '  ✓ %s\n' "$1"; else printf '  ✗ %s: got %q, expected %q\n' "$1" "$2" "$3" >&2; exit 1; fi
}
contains() { grep -q -- "$2" <<<"$1" || { printf '    (got: %.300s)\n' "$1" >&2; return 1; }; }
lacks() { ! grep -q -- "$2" <<<"$1"; }
status_of() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
wait_healthy() {
	local name="$1"
	for _ in $(seq 90); do
		case "$(docker inspect -f '{{.State.Health.Status}}' "$name" 2> /dev/null)" in
		healthy) return 0 ;;
		esac
		[[ "$(docker inspect -f '{{.State.Running}}' "$name")" == true ]] || return 1
		sleep 2
	done
	return 1
}
login() {
	curl -s -X POST "$URL/api/v1/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}"
}
token() { login | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'; }

if [[ "$IMAGE" == opshub:aio-test ]]; then
	echo "aio-test: building the image"
	docker build -q -f "$ROOT/deploy/docker/opshub.Dockerfile" --build-arg VERSION=aio-test -t "$IMAGE" "$ROOT" > /dev/null
fi

echo "aio-test: try-out mode"
docker run -d --name "$C" -p 18190:8080 -v opshub-aio-test:/data \
	-e OPSHUB_PUBLIC_URL="$URL" -e OPSHUB_ADMIN_EMAIL="$EMAIL" "$IMAGE" > /dev/null
expect "starts and becomes healthy" wait_healthy "$C"
expect_eq "the UI answers" "$(status_of "$URL/")" 200
# Request bodies go into variables first: macOS's bash 3.2 mis-parses a quoted $(…) split over lines.
register_body="{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\",\"display_name\":\"First Admin\",\"locale\":\"en\"}"
expect_eq "the admin registers" "$(status_of -X POST "$URL/api/v1/auth/register" -H 'Content-Type: application/json' -d "$register_body")" 202
expect "sign-in waits for a confirmed email" contains "$(login)" EMAIL_NOT_VERIFIED
docker exec "$C" opshub verify-email "$EMAIL" > /dev/null
expect "verify-email lets the platform admin in" contains "$(login)" '"is_platform_admin":true'
expect_eq "nothing runs as root" "$(docker exec "$C" ps -o user= | sort -u | tr -d ' ')" postgres
expect_eq "secrets in /data are private" "$(docker exec "$C" stat -c %a /data/secrets.env)" 600

docker restart "$C" > /dev/null
expect "healthy again after a restart" wait_healthy "$C"
expect "the data survives the restart" contains "$(login)" '"is_platform_admin":true'

echo "aio-test: backup and restore"
docker exec "$C" opshub backup > /dev/null
tables="$(docker exec "$C" sh -c 'f=$(find /data/backups/daily -name "*.dump.age" | head -n 1); age -d -i /data/backup-private-key.txt "$f" | pg_restore --list | grep -c "TABLE DATA"')"
expect "the generated key decrypts the backup ($tables tables)" test "$tables" -gt 50
auth="Authorization: Bearer $(token)"
org_body='{"name":"After the backup","slug":"after-backup"}'
expect_eq "an organization created after the backup" "$(status_of -X POST "$URL/api/v1/orgs" -H "$auth" -H 'Content-Type: application/json' -d "$org_body")" 201
docker exec "$C" sh -c 'f=$(find /data/backups/daily -name "*.dump.age" | head -n 1); cp "$f" "$f.sha256" /data/restore/ && cp /data/backup-private-key.txt /data/restore/key.txt'
docker restart "$C" > /dev/null
expect "healthy after restoring on start" wait_healthy "$C"
expect "the organization is gone again" lacks "$(curl -s "$URL/api/v1/orgs" -H "Authorization: Bearer $(token)")" after-backup
expect_eq "the restore removed the key from /data/restore" "$(docker exec "$C" sh -c 'ls /data/restore/key.txt 2>/dev/null | wc -l' | tr -d ' ')" 0

echo "aio-test: restore on a new server (the old keys given on its first start)"
docker exec "$C" sh -c 'f=$(find /data/backups/daily -name "*.dump.age" | head -n 1); mkdir -p /tmp/move && cp "$f" /tmp/move/backup.dump.age && cp /data/backup-private-key.txt /tmp/move/key.txt'
master="$(docker exec "$C" sed -n 's/^OPSHUB_MASTER_KEYS=//p' /data/secrets.env)"
jwt="$(docker exec "$C" sed -n 's/^OPSHUB_JWT_KEYS=//p' /data/secrets.env)"
docker stop "$C" > /dev/null
docker run -d --name "$C-new" -p 18190:8080 -v opshub-aio-test-new:/data -e OPSHUB_PUBLIC_URL="$URL" \
	-e OPSHUB_MASTER_KEYS="$master" -e OPSHUB_JWT_KEYS="$jwt" "$IMAGE" > /dev/null
expect "the new server starts" wait_healthy "$C-new"
expect_eq "it kept the old keys" "$(docker exec "$C-new" sed -n 's/^OPSHUB_MASTER_KEYS=//p' /data/secrets.env)" "$master"
docker cp "$C:/tmp/move/backup.dump.age" - | docker cp - "$C-new:/data/restore/"
docker cp "$C:/tmp/move/key.txt" - | docker cp - "$C-new:/data/restore/"
docker restart "$C-new" > /dev/null
expect "it restores the old server's backup" wait_healthy "$C-new"
expect "the admin signs in on the new server" contains "$(login)" '"is_platform_admin":true'
docker rm -f "$C-new" > /dev/null
docker start "$C" > /dev/null
expect "the old server starts again" wait_healthy "$C"

echo "aio-test: supervision"
docker exec "$C" pkill -f 'opshub-api serve'
for _ in $(seq 20); do [[ "$(docker inspect -f '{{.State.Running}}' "$C")" == false ]] && break; sleep 1; done
expect_eq "a crashed API stops the container" "$(docker inspect -f '{{.State.Running}}' "$C")" false
docker start "$C" > /dev/null
expect "it starts again" wait_healthy "$C"
start=$SECONDS
docker stop "$C" > /dev/null
expect "docker stop is quick ($((SECONDS - start)) s) and clean" test "$((SECONDS - start))" -lt 15 -a \
	"$(docker inspect -f '{{.State.ExitCode}}' "$C")" = 0
expect "PostgreSQL shut down cleanly" contains "$(docker logs "$C" 2>&1 | tail -n 20)" "database system is shut down"

echo "aio-test: HTTPS mode"
docker run -d --name "$CS" -p 18191:8080 -p 18192:8443 -v opshub-aio-test-https:/data \
	-e OPSHUB_DOMAIN=opshub.localhost -e OPSHUB_TLS=internal -e OPSHUB_PUBLIC_URL=https://opshub.localhost:18192 \
	-e OPSHUB_ADMIN_EMAIL="$EMAIL" -e OPSHUB_DEMO=true "$IMAGE" > /dev/null
expect "starts and becomes healthy" wait_healthy "$CS"
https() { curl -s --insecure --resolve opshub.localhost:18192:127.0.0.1 "$@"; }
expect_eq "HTTPS /readyz" "$(https -o /dev/null -w '%{http_code}' https://opshub.localhost:18192/readyz)" 200
expect "HSTS header" contains "$(https -D - -o /dev/null https://opshub.localhost:18192/)" "strict-transport-security"
expect_eq "/metrics is not public" "$(https -o /dev/null -w '%{http_code}' https://opshub.localhost:18192/metrics)" 404
expect_eq "http redirects to https" "$(status_of -H 'Host: opshub.localhost' http://127.0.0.1:18191/)" 308
expect "production mode refuses demo data" contains "$(docker logs "$CS" 2>&1)" "OPSHUB_DEMO is ignored"
echo "aio-test: passed"
