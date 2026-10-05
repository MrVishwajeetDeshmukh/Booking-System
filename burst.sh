#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"

echo "Running burst test against: $BASE_URL"
echo "Running 20,000 concurrent hot-seat requests plus idempotency and per-user limit checks..."

exec go run ./burst -url="$BASE_URL" -c=20000 -u=5000 -s=12
