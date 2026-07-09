#!/usr/bin/env bash
# scan-oauth-mimic-chain.sh — discover edges with schedulable Anthropic OAuth, then fan out
# probe-oauth-mimicry-chain.sh on each eligible edge. Emits one JSON object per line.
#
# Read-only via run-probe.sh (SSM). Used by client-fidelity-watch edge-oauth-mimic job.
#
# Usage:
#   bash ops/observability/scan-oauth-mimic-chain.sh
#   bash ops/observability/scan-oauth-mimic-chain.sh --since 24h --window-minutes 1440
#   bash ops/observability/scan-oauth-mimic-chain.sh --edges us3,uk
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
RUN_PROBE="$HERE/run-probe.sh"
PROBE_MIMIC="$HERE/probe-oauth-mimicry-chain.sh"
POOL_PROBE="$REPO_ROOT/ops/stage0/edge_anthropic_oauth_schedulable_probe.sh"
RESOLVE="$REPO_ROOT/deploy/aws/stage0/resolve-edge-target.py"

SINCE="${SINCE:-24h}"
WINDOW_MINUTES="${WINDOW_MINUTES:-1440}"
LIMIT="${LIMIT:-800}"
PROBE_TIMEOUT="${PROBE_TIMEOUT:-120}"
EDGES_CSV=""

while [ $# -gt 0 ]; do
  case "$1" in
    --since) SINCE="$2"; shift 2 ;;
    --window-minutes) WINDOW_MINUTES="$2"; shift 2 ;;
    --limit) LIMIT="$2"; shift 2 ;;
    --timeout-seconds) PROBE_TIMEOUT="$2"; shift 2 ;;
    --edges) EDGES_CSV="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "scan-oauth-mimic-chain: unknown arg '$1'" >&2; exit 1 ;;
  esac
done

EDGES=()
if [ -n "$EDGES_CSV" ]; then
  IFS=',' read -r -a EDGES <<< "$EDGES_CSV"
else
  while IFS= read -r _edge; do
    [ -n "$_edge" ] && EDGES+=("$_edge")
  done < <(python3 "$RESOLVE" --list-deployable)
fi

if [ "${#EDGES[@]}" -eq 0 ]; then
  echo "scan-oauth-mimic-chain: no deployable edges resolved" >&2
  exit 1
fi

echo "scan-oauth-mimic-chain: discovering schedulable anthropic oauth on ${#EDGES[@]} edges" >&2

ELIGIBLE=()
for edge in "${EDGES[@]}"; do
  [ -n "$edge" ] || continue
  echo "  pool-probe edge:$edge ..." >&2
  count="$(bash "$RUN_PROBE" --target "edge:$edge" --script "$POOL_PROBE" \
    --timeout-seconds 60 2>/dev/null | tr -d '[:space:]' || true)"
  if [[ "$count" =~ ^[0-9]+$ ]] && [ "$count" -gt 0 ]; then
    ELIGIBLE+=("$edge")
    echo "    eligible schedulable_anthropic_oauth=$count" >&2
  else
    echo "    skip (schedulable_anthropic_oauth=${count:-unreachable})" >&2
  fi
done

if [ "${#ELIGIBLE[@]}" -eq 0 ]; then
  python3 -c 'import json; print(json.dumps({"edge_id":"_fleet","probe_error":"no_schedulable_anthropic_oauth_edges","eligible_edges":[]}))'
  exit 0
fi

for edge in "${ELIGIBLE[@]}"; do
  echo "  mimic-probe edge:$edge ..." >&2
  if out="$(bash "$RUN_PROBE" --target "edge:$edge" --script "$PROBE_MIMIC" \
      --env "SINCE=$SINCE" --env "WINDOW_MINUTES=$WINDOW_MINUTES" --env "LIMIT=$LIMIT" \
      --env "PLATFORM=anthropic" \
      --timeout-seconds "$PROBE_TIMEOUT" 2>/dev/null)"; then
    printf '%s\n' "$out" | python3 -c "
import json, sys
edge = sys.argv[1]
raw = sys.stdin.read().strip()
try:
    payload = json.loads(raw)
except json.JSONDecodeError:
    payload = {'probe_error': 'parse-error', 'raw_tail': raw[-400:]}
payload['edge_id'] = edge
payload['schedulable_anthropic_oauth_edge'] = True
print(json.dumps(payload, separators=(',', ':'), sort_keys=True))
" "$edge"
  else
    python3 -c "import json; print(json.dumps({'edge_id':sys.argv[1],'probe_error':'unreachable','schedulable_anthropic_oauth_edge':True}))" "$edge"
  fi
done
