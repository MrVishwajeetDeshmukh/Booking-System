#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"

echo "Running burst test against: $BASE_URL"
echo "Simulating 20,000 concurrent reservations fighting for 10 seats..."

exec go run ./burst -url="$BASE_URL" -c=20000 -s=10
