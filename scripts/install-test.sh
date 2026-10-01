#!/usr/bin/env bash
# End-to-end test of the Docker install (make install-test; CI). Installs from this checkout with
# no questions and Caddy's internal certificate authority on spare ports, then checks HTTPS, the
# security headers, the first admin (registered, confirmed with verify-email, platform admin),
# an encrypted backup that the generated key decrypts and that restores, and an upgrade that
# keeps the data.
# Everything it created is removed at the end, also when a check fails.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL="$ROOT/deploy/install"
cd "$INSTALL"
[[ ! -f .env ]] || { echo "install-test: $INSTALL/.env exists (a real install?); not touching it" >&2; exit 1; }

DOMAIN=opshub.localhost
URL="https://$DOMAIN:8443"
EMAIL=admin@example.com
PASSWORD=install-test-mekong-42

WORK="$(mktemp -d)"
cleanup() {
	status=$?
	if (( status != 0 )); then ./opshub status || true; docker compose logs --no-color --tail 80 || true; fi
	./opshub uninstall --delete-data < /dev/null || true
	rm -f backup-private-key.txt
	rm -rf "$WORK"
	exit "$status"
}
trap cleanup EXIT

https() { curl --silent --show-error --insecure --resolve "$DOMAIN:8443:127.0.0.1" "$@"; }
status_of() { https -o /dev/null -w '%{http_code}' "$@"; }
contains() { grep -qi -- "$2" <<<"$1"; }
lacks() { ! grep -qi -- "$2" <<<"$1"; }
# expect "description" command…: runs the command as a check.
expect() {
	local what="$1"
	shift
	if "$@"; then printf '  ✓ %s\n' "$what"; else printf '  ✗ %s\n' "$what" >&2; exit 1; fi
}
# expect_eq "description" actual expected
expect_eq() {
	if [[ "$2" == "$3" ]]; then printf '  ✓ %s\n' "$1"; else printf '  ✗ %s: got %q, expected %q\n' "$1" "$2" "$3" >&2; exit 1; fi
}

# Spare ports, so the test runs next to the development stack too.
echo "install-test: installing"
./opshub install --yes --tls internal --domain "$DOMAIN" --https-port 8443 --http-public-port 8088 \
	--http-port 18080 --prometheus-port 19090 --grafana-port 13001 --admin-email "$EMAIL" --network-prefix 172.31.5 < /dev/null

echo "install-test: checking"
expect_eq ".env is readable by its owner only" "$(stat -c %a .env 2>/dev/null || stat -f %Lp .env)" 600
expect_eq "HTTPS /readyz" "$(status_of "$URL/readyz")" 200
headers="$(https -D - -o /dev/null "$URL/")"
expect "HSTS header" contains "$headers" '^strict-transport-security:'
expect "CSP header" contains "$headers" '^content-security-policy:'
expect_eq "/metrics is not public" "$(status_of "$URL/metrics")" 404
expect_eq "http redirects to https" "$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $DOMAIN" http://127.0.0.1:8088/)" 308

register_body="{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\",\"display_name\":\"First Admin\",\"locale\":\"en\"}"
expect_eq "the admin registers" "$(status_of -X POST "$URL/api/v1/auth/register" -H 'Content-Type: application/json' -d "$register_body")" 202
login() {
	https -X POST "$URL/api/v1/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"${1:-$EMAIL}\",\"password\":\"$PASSWORD\"}"
}
expect "sign-in waits for a confirmed email" contains "$(login)" EMAIL_NOT_VERIFIED
./opshub verify-email "$EMAIL" < /dev/null
expect "verify-email lets the platform admin in" contains "$(login)" '"is_platform_admin":true'

./opshub backup < /dev/null > /dev/null
dump="$(docker compose exec -T backup sh -c 'ls /backups/daily | head -1')"
# As root: the dump belongs to the backup user, the key to this host's user (both mode 600).
tables="$(docker run --rm --user 0:0 -v opshub-server_backups:/backups:ro -v "$INSTALL/backup-private-key.txt:/key:ro" --entrypoint sh \
	"$(sed -n "s/^OPSHUB_IMAGE_PREFIX='\(.*\)'/\1/p" .env)-backup:$(sed -n "s/^OPSHUB_VERSION='\(.*\)'/\1/p" .env)" \
	-c "age -d -i /key /backups/daily/$dump | pg_restore --list | grep -c 'TABLE DATA'")"
expect "the generated key decrypts the backup ($tables tables)" test "$tables" -gt 50

# Sign-up is closed (the installer's default): only the admin address may register.
later_body="${register_body/$EMAIL/later@example.com}"
expect_eq "sign-up is closed to others" "$(status_of -X POST "$URL/api/v1/auth/register" -H 'Content-Type: application/json' -d "$later_body")" 403

# Restore: an organization created after the backup is gone again, the admin is still there.
docker compose cp "backup:/backups/daily/$dump" "$WORK/" > /dev/null
docker compose cp "backup:/backups/daily/$dump.sha256" "$WORK/" > /dev/null
token="$(login | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
expect_eq "the admin creates an organization" "$(status_of -X POST "$URL/api/v1/orgs" -H "Authorization: Bearer $token" \
	-H 'Content-Type: application/json' -d '{"name":"After the backup","slug":"after-backup"}')" 201
./opshub restore "$WORK/$dump" backup-private-key.txt --force < /dev/null > /dev/null
token="$(login | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
expect "after the restore the admin signs in" test -n "$token"
expect "after the restore that organization is gone" lacks "$(https "$URL/api/v1/orgs" -H "Authorization: Bearer $token")" after-backup
expect_eq "after the restore the app role still can't change the audit log" \
	"$(docker compose exec -T postgres psql -U opshub -d opshub -tAc "SELECT has_table_privilege('opshub_app', 'audit_log', 'UPDATE, DELETE, TRUNCATE')")" f
expect_eq "after the restore only the app role runs the partition functions" \
	"$(docker compose exec -T postgres psql -U opshub -d opshub -tAc "SELECT has_function_privilege('opshub_backup', 'opshub_maintain_log_partitions(integer)', 'EXECUTE')::text || has_function_privilege('opshub_app', 'opshub_maintain_log_partitions(integer)', 'EXECUTE')::text")" falsetrue

./opshub upgrade < /dev/null > /dev/null
expect "an upgrade keeps the data" contains "$(login)" '"is_platform_admin":true'
echo "install-test: passed"
