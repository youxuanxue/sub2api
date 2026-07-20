#!/usr/bin/env bash
set -euo pipefail

# Remote read-only entrypoint. run-probe.sh uploads edge_capacity_probe.py to /tmp.
EDGE_ID="${EDGE_ID:?EDGE_ID is required}"
DAYS="${DAYS:-60}"
MIN_SECONDS="${MIN_SECONDS:-60}"
SNAPSHOT_AT="${SNAPSHOT_AT:?SNAPSHOT_AT is required}"
ANALYZER="${ANALYZER:-/tmp/edge_capacity_probe.py}"

if [ ! -f "$ANALYZER" ]; then
  echo "probe-edge-capacity: missing $ANALYZER" >&2
  exit 2
fi

exec python3 "$ANALYZER" analyze \
  --edge "$EDGE_ID" \
  --days "$DAYS" \
  --min-seconds "$MIN_SECONDS" \
  --snapshot-at "$SNAPSHOT_AT"
