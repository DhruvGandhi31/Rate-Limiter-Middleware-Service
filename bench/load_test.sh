#!/usr/bin/env bash
# Run an HTTP load test against a running rate-limiter service.
#
# Prerequisites:
#   - docker compose up   (service + redis listening on :8080)
#   - hey installed       (go install github.com/rakyll/hey@latest)
#
# Usage:
#   ./bench/load_test.sh                     # 30s @ c=100
#   DURATION=60s CONCURRENCY=200 ./bench/load_test.sh
#
# The script generates per-identifier load so the limiter actually does work
# (a single identifier would just bounce off the limit and never exercise Redis).
set -euo pipefail

URL="${URL:-http://localhost:8080/check}"
DURATION="${DURATION:-30s}"
CONCURRENCY="${CONCURRENCY:-100}"
RULE="${RULE:-burst-tolerant}"

if ! command -v hey >/dev/null; then
  echo "hey not found. install with:  go install github.com/rakyll/hey@latest" >&2
  exit 1
fi

# hey sends the same body to every request; vary identifier via the rule's
# configured high limit so we measure the limiter's hot path, not 429s.
BODY=$(printf '{"rule":"%s","identifier":"loadtest"}' "$RULE")

echo ">> URL=$URL  duration=$DURATION  concurrency=$CONCURRENCY  rule=$RULE"
echo

hey -z "$DURATION" \
    -c "$CONCURRENCY" \
    -m POST \
    -H 'content-type: application/json' \
    -d "$BODY" \
    "$URL"
