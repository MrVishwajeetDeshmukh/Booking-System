#!/bin/bash
# Usage: ./burst.sh <BASE_URL>

if [ -z "$1" ]; then
  BASE_URL="http://localhost:8080"
else
  BASE_URL=$1
fi

echo "Running burst test against: $BASE_URL"
echo "Simulating 20,000 concurrent reservations fighting for 10 seats..."

go run burst/burst.go -url="$BASE_URL" -c=20000 -s=10
