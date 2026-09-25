#!/usr/bin/env bash
# Fails when statement coverage of the service packages is below the threshold (default 70%).
# Service packages hold business rules; see docs/architecture.md §5.
set -euo pipefail
THRESHOLD="${COVERAGE_THRESHOLD:-70}"
PACKAGES=(./internal/auth/... ./internal/org/... ./internal/project/... ./internal/authz/... ./internal/gitprovider/... ./internal/idempotency/... ./internal/safehttp/... ./internal/pipeline/... ./internal/events/... ./internal/runners/... ./internal/blob/... ./internal/deploy/... ./internal/dockerapi/... ./internal/infra/... ./internal/secret/... ./internal/keyrotate/...)
OUT="${COVERAGE_OUT:-coverage-services.out}"

go test -count=1 -coverprofile="$OUT" "${PACKAGES[@]}" >/dev/null
total=$(go tool cover -func="$OUT" | awk '/^total:/ { sub("%", "", $3); print $3 }')
echo "service coverage: ${total}% (threshold ${THRESHOLD}%)"
awk -v t="$total" -v min="$THRESHOLD" 'BEGIN { exit (t + 0 < min + 0) ? 1 : 0 }' || {
  echo "coverage below ${THRESHOLD}%" >&2
  exit 1
}
