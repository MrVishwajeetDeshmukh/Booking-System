#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"

echo "Running burst test against: $BASE_URL"
echo "Running a 20,000-request hot-seat wave plus idempotency and per-user limit checks..."

ARGS=(-url="$BASE_URL" -c=20000 -u=5000 -s=12)
if [[ -n "${BURST_WORKERS:-}" ]]; then
  ARGS+=("-workers=$BURST_WORKERS")
fi

exec go run ./burst "${ARGS[@]}"
