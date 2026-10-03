#!/bin/sh
# Demo: snapshot the heap page, insert rows through the allow-listed action,
# snapshot again. Shows the page filling up with real pageinspect data.
#
# Prerequisites:
#   1. docker compose up -d        (in adapters/postgres)
#   2. inspector running with the postgres adapter, see README
set -eu

BASE="${GLASSHOUSE_INSPECTOR_URL:-http://127.0.0.1:8765}"
HDR='X-Glasshouse-Action: 1'

PY_SUMMARY="
import json, sys
env = json.load(sys.stdin)
p = env[\"snapshot\"][\"data\"]
h = p[\"header\"]
print(f\"  items={len(p['items'])}  lower={h['lower']}  upper={h['upper']}  free={p['free_space']} bytes  lsn={h['lsn']}\")
"

summarize() {
  python3 -c "$PY_SUMMARY"
}

echo "Health:"
curl -fsS "$BASE/health"
echo
echo
echo "Before:"
curl -fsS "$BASE/snapshot" | summarize

echo "Running action insert_rows (10 rows, fixed SQL)..."
curl -fsS -X POST -H "$HDR" "$BASE/actions/insert_rows"
echo
sleep 1

echo "After:"
curl -fsS "$BASE/snapshot" | summarize
