#!/usr/bin/env bash
# scripts/edge-ip-status.sh — Render edge IP tables for docs.
#
# Sources:
#   deploy/aws/stage0/edge-polluted-ips.json
#   deploy/aws/lightsail/edge-targets-lightsail.json (deployable current IPs)
# Target: docs/deploy/tokenkey-edge-ip-history.md
#
# Usage:
#   scripts/edge-ip-status.sh             # print polluted markdown block
#   scripts/edge-ip-status.sh --json      # raw JSON
#   scripts/edge-ip-status.sh --check     # exit non-zero if doc blocks drifted

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POLLUTED="${REPO_ROOT}/deploy/aws/stage0/edge-polluted-ips.json"
MATRIX="${REPO_ROOT}/deploy/aws/lightsail/edge-targets-lightsail.json"
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

emit_current_block() {
  if [ ! -f "$MATRIX" ]; then
    echo "missing $MATRIX" >&2
    exit 1
  fi
  echo "<!-- BEGIN edge-ip-status:current (generated from deploy/aws/lightsail/edge-targets-lightsail.json) -->"
  echo "| Edge | Region | Domain | Static IP name | IPv4 |"
  echo "| --- | --- | --- | --- | --- |"
  jq -r '
    .targets
    | to_entries
    | sort_by(.key)[]
    | select(.value.deployable == true)
    | "| `\(.key)` | \(.value.lightsail_region) | `\(.value.domain)` | `\(.value.static_ip_name)` | `\(.value.porkbun_a_ipv4 // "")` |"
  ' "$MATRIX"
  echo "<!-- END edge-ip-status:current -->"
}

extract_block() {
  local begin_prefix="$1"
  local end_line="$2"
  awk -v begin="$begin_prefix" -v end="$end_line" '
    index($0, begin) == 1 { inside = 1 }
    inside { print }
    $0 == end { inside = 0 }
  ' "$DOC"
}

check_block() {
  local name="$1"
  local expected="$2"
  local actual="$3"
  if [ -z "$actual" ]; then
    echo "edge-ip-status: ${name} block missing from $DOC" >&2
    return 1
  fi
  if [ "$expected" != "$actual" ]; then
    echo "edge-ip-status: ${name} block in $DOC drifted" >&2
    diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") >&2 || true
    return 1
  fi
  echo "edge-ip-status: ${name} table in sync"
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
    failed=0
    check_block "polluted" "$(emit_polluted_block)" "$(extract_block '<!-- BEGIN edge-ip-status:polluted ' '<!-- END edge-ip-status:polluted -->')" || failed=1
    check_block "current" "$(emit_current_block)" "$(extract_block '<!-- BEGIN edge-ip-status:current ' '<!-- END edge-ip-status:current -->')" || failed=1
    exit "$failed"
    ;;
  *)
    echo "usage: $0 [--markdown|--json|--check]" >&2
    exit 2
    ;;
esac
