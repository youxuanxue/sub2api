#!/usr/bin/env bash
# scan-edge-health.sh — fan out probe-edge-health.sh across every deployable Stage0
# edge (and optionally prod), compute each one's verdict, and print one truth-telling
# table: schedulable accounts + served_200 : no_available_429 + verdict.
#
# This is the LOCAL orchestrator half of the edge-health triad:
#   probe-edge-health.sh    (remote read-only probe, run via run-probe.sh)
#   edge_health_verdict.py  (pure verdict logic + --selftest)
#   scan-edge-health.sh     (this — fan-out over the deployable-edge matrix)
#
# WHY: prod's "upstream-429 by account" reads ~1300 across ALL mirror edges whether an
# edge is fully healthy (us5) or 100% dead for hours (us3, 2026-06-06). This scan reads
# each edge's OWN access log + roster so a silently-dead edge shows verdict=down at a
# glance instead of being masked by prod failover. Read-only: only runs run-probe.sh
# (docker logs + psql SELECT). No writes, no AWS mutations.
#
# Usage:
#   bash ops/observability/scan-edge-health.sh                 # all deployable edges
#   bash ops/observability/scan-edge-health.sh --with-prod     # + prod
#   bash ops/observability/scan-edge-health.sh --since 15h     # widen traffic window
#   bash ops/observability/scan-edge-health.sh --edges us3,us6 # subset
#
# Exit codes: 0 always (verdicts are in the table). An edge whose probe fails shows
# verdict=unreachable rather than aborting the whole scan.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
RUN_PROBE="$HERE/run-probe.sh"
PROBE="$HERE/probe-edge-health.sh"
VERDICT="$HERE/edge_health_verdict.py"
RESOLVE="$REPO_ROOT/deploy/aws/stage0/resolve-edge-target.py"

SINCE="2h"
WITH_PROD=0
EDGES_CSV=""
JSON=0
while [ $# -gt 0 ]; do
  case "$1" in
    --since) SINCE="$2"; shift 2 ;;
    --with-prod) WITH_PROD=1; shift ;;
    --edges) EDGES_CSV="$2"; shift 2 ;;
    --json) JSON=1; shift ;;
    -h|--help) sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "scan-edge-health: unknown arg '$1'" >&2; exit 1 ;;
  esac
done

# Resolve the target list. Default = the deployable-edge matrix (EC2 ∪ Lightsail,
# same source the deploy workflows use). Subset via --edges.
EDGES=()
if [ -n "$EDGES_CSV" ]; then
  IFS=',' read -r -a EDGES <<< "$EDGES_CSV"
else
  # macOS default bash is 3.2 and has no `mapfile`; read line-by-line so the
  # default (no --edges) invocation works for operators on a Mac, not just the
  # --edges path.
  while IFS= read -r _edge; do
    [ -n "$_edge" ] && EDGES+=("$_edge")
  done < <(python3 "$RESOLVE" --list-deployable)
fi

TARGETS=()
# "${EDGES[@]+...}" guards expanding an empty array under `set -u` on bash 3.2
# (newer bash treats empty-array expansion as unset; the +-form is portable).
for e in "${EDGES[@]+"${EDGES[@]}"}"; do
  [ -n "$e" ] && TARGETS+=("edge:$e")
done
[ "$WITH_PROD" = "1" ] && TARGETS+=("prod")

if [ "${#TARGETS[@]}" -eq 0 ]; then
  echo "scan-edge-health: no targets resolved (check --edges / resolve-edge-target.py --list-deployable)" >&2
  exit 1
fi

echo "scan-edge-health: ${#TARGETS[@]} targets, traffic window since=$SINCE" >&2

RESULTS="$(mktemp /tmp/eh_scan.XXXXXX)"
trap 'rm -f "$RESULTS"' EXIT

for tgt in "${TARGETS[@]}"; do
  label="${tgt#edge:}"
  echo "  probing $tgt ..." >&2
  if out="$(bash "$RUN_PROBE" --target "$tgt" --script "$PROBE" \
              --env "PLATFORM=anthropic" --env "SINCE=$SINCE" \
              --timeout-seconds 150 2>/dev/null)"; then
    verdict_json="$(printf '%s\n' "$out" | python3 "$VERDICT" --label "$label" 2>/dev/null)"
    if [ -n "$verdict_json" ]; then
      printf '%s\n' "$verdict_json" >> "$RESULTS"
    else
      printf '{"edge":"%s","verdict":"parse-error"}\n' "$label" >> "$RESULTS"
    fi
  else
    printf '{"edge":"%s","verdict":"unreachable"}\n' "$label" >> "$RESULTS"
  fi
done

# --json: emit the per-edge verdict JSON lines verbatim (machine-readable, for the
# edge-health-watch workflow / edge-health-alert.py) and skip the human table.
if [ "$JSON" = "1" ]; then
  cat "$RESULTS"
  exit 0
fi

echo
echo "=== edge health (truth from each edge's own access log, NOT prod upstream-429) ==="
python3 - "$RESULTS" <<'PY'
import json, sys
rows = []
for line in open(sys.argv[1], encoding="utf-8"):
    line = line.strip()
    if line:
        try: rows.append(json.loads(line))
        except json.JSONDecodeError: pass  # preflight-allow: swallow — a non-JSON verdict line (unreachable edge note) is skipped, not fatal

# sort: most severe first
order = {"down": 0, "unreachable": 0, "parse-error": 0, "degraded": 1,
         "thin": 2, "idle-thin": 3, "idle": 4, "healthy": 5}
rows.sort(key=lambda r: (order.get(r.get("verdict"), 9), r.get("edge") or ""))

hdr = ("EDGE", "VERDICT", "SCHED", "SERVED_200", "NO_AVAIL_429", "RATIO", "WAIT_TO", "SPOF")
w = (6, 10, 6, 11, 13, 7, 8, 5)
def fmt(vals): return "  ".join(str(v).ljust(width) for v, width in zip(vals, w))
print(fmt(hdr))
for r in rows:
    ratio = r.get("served_ratio")
    print(fmt((
        r.get("edge", "?"),
        r.get("verdict", "?"),
        r.get("schedulable_accounts", "-"),
        r.get("served_200", "-"),
        r.get("no_available_429", "-"),
        "-" if ratio is None else f"{ratio:.3f}",
        r.get("wait_timeout", "-"),
        "YES" if r.get("single_account_risk") else "no",
    )))
print()
# loud summary of the edges that need action
bad = [r for r in rows if r.get("verdict") in ("down", "degraded", "unreachable")]
spof = [r for r in rows if r.get("single_account_risk") and r.get("verdict") not in ("down","unreachable")]
if bad:
    print("ACTION — down/degraded/unreachable:", ", ".join(r.get("edge","?") for r in bad))
if spof:
    print("RISK   — single-account (SPOF):    ", ", ".join(r.get("edge","?") for r in spof))
if not bad and not spof:
    print("all edges healthy with >=2 schedulable accounts")
PY
