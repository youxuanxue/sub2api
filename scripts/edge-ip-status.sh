#!/usr/bin/env bash
# scripts/edge-ip-status.sh — Render polluted EIP exclusion table for docs.
#
# Source: deploy/aws/stage0/edge-polluted-ips.json
# Target: docs/deploy/tokenkey-edge-ip-history.md (polluted block only)
#
# Usage:
#   scripts/edge-ip-status.sh             # print polluted markdown block
#   scripts/edge-ip-status.sh --json      # raw JSON
#   scripts/edge-ip-status.sh --check     # exit non-zero if doc polluted block drifted

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POLLUTED="${REPO_ROOT}/deploy/aws/stage0/edge-polluted-ips.json"
DOC="${REPO_ROOT}/docs/deploy/tokenkey-edge-ip-history.md"
MODE="${1:---markdown}"

if [ ! -f "$POLLUTED" ]; then
  echo "missing $POLLUTED" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

polluted_json=$(jq '.polluted' "$POLLUTED")

emit_polluted_block() {
  echo "<!-- BEGIN edge-ip-status:polluted (generated from deploy/aws/stage0/edge-polluted-ips.json) -->"
  echo "| IP | Region | Notes |"
  echo "| --- | --- | --- |"
  jq -r '.[] | "| `\(.ip)` | \(.region) | \(.notes // "") |"' <<<"$polluted_json"
  echo "<!-- END edge-ip-status:polluted -->"
}

extract_block() {
  awk '
    index($0, "<!-- BEGIN edge-ip-status:polluted ") == 1 { inside = 1 }
    inside { print }
    index($0, "<!-- END edge-ip-status:polluted -->") == 1 { inside = 0 }
  ' "$DOC"
}

case "$MODE" in
  --markdown)
    emit_polluted_block
    ;;
  --json)
    jq -nc --argjson polluted "$polluted_json" '{polluted:$polluted}'
    ;;
  --check)
    if [ ! -f "$DOC" ]; then
      echo "missing $DOC" >&2
      exit 1
    fi
    expected=$(emit_polluted_block)
    actual=$(extract_block)
    if [ -z "$actual" ]; then
      echo "edge-ip-status: polluted block missing from $DOC" >&2
      exit 1
    fi
    if [ "$expected" = "$actual" ]; then
      echo "edge-ip-status: polluted table in sync"
      exit 0
    fi
    echo "edge-ip-status: polluted block in $DOC drifted" >&2
    diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") >&2 || true
    exit 1
    ;;
  *)
    echo "usage: $0 [--markdown|--json|--check]" >&2
    exit 2
    ;;
esac
