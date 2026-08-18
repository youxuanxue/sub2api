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
# glance instead of being masked by prod failover.
#
# External HTTPS /health (edge_https_health.py) runs BEFORE SSM: a guest hang
# (2026-08-03 us6: SSM ConnectionLost + NetworkOut=0) must page as unreachable
# without waiting on Undeliverable SSM. Read-only: HTTPS GET + run-probe.sh
# (docker logs + psql SELECT). No writes, no AWS mutations.
#
# Usage:
#   bash ops/observability/scan-edge-health.sh                 # all deployable edges
#   bash ops/observability/scan-edge-health.sh --with-prod     # + prod
#   bash ops/observability/scan-edge-health.sh --since 15h     # widen traffic window
#   bash ops/observability/scan-edge-health.sh --edges us3,us6 # subset
#
# Exit codes: 0 when every intended target produced a valid verdict. Remote probe
# failures are represented as verdict=unreachable; local helper/resolver failures or
# an incomplete result set return nonzero so automation cannot advance dedup state.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
RUN_PROBE="${EDGE_HEALTH_RUN_PROBE:-$HERE/run-probe.sh}"
PROBE="$HERE/probe-edge-health.sh"
VERDICT="${EDGE_HEALTH_VERDICT:-$HERE/edge_health_verdict.py}"
HTTPS_PROBE="${EDGE_HEALTH_HTTPS_PROBE:-$HERE/edge_https_health.py}"
RESOLVE="${EDGE_HEALTH_RESOLVE:-$REPO_ROOT/deploy/aws/stage0/resolve-edge-target.py}"

SINCE="2h"
WITH_PROD=0
EDGES_CSV=""
JSON=0
ALERT_JSON=0
PROBE_TIMEOUT=150 # per-edge SSM timeout (s); the watch lowers it to bound total wall-clock
HTTPS_TIMEOUT=8
SKIP_HTTPS=0
while [ $# -gt 0 ]; do
  case "$1" in
    --since) SINCE="$2"; shift 2 ;;
    --with-prod) WITH_PROD=1; shift ;;
    --edges) EDGES_CSV="$2"; shift 2 ;;
    --json) JSON=1; shift ;;
    --alert-json) ALERT_JSON=1; shift ;;
    --timeout-seconds) PROBE_TIMEOUT="$2"; shift 2 ;;
    --https-timeout-seconds) HTTPS_TIMEOUT="$2"; shift 2 ;;
    --skip-https) SKIP_HTTPS=1; shift ;;
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
  if ! resolved_edges="$(python3 "$RESOLVE" --list-deployable)"; then
    echo "scan-edge-health: failed to resolve deployable targets" >&2
    exit 1
  fi
  while IFS= read -r _edge; do
    [ -n "$_edge" ] && EDGES+=("$_edge")
  done <<< "$resolved_edges"
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
ORCHESTRATION_ERRORS=0

for tgt in "${TARGETS[@]}"; do
  label="${tgt#edge:}"
  echo "  probing $tgt ..." >&2
  # Cheap external probe first: host hang / blackhole must not wait on SSM.
  # Skip SSM ONLY on transport failure (reachable=false / http_code=0).
  # Non-200 (deploy 502/503) still runs SSM for a truth-telling verdict.
  if [ "$SKIP_HTTPS" != "1" ]; then
    https_json=""
    if ! https_json="$(python3 "$HTTPS_PROBE" --target "$tgt" --probe --timeout-seconds "$HTTPS_TIMEOUT")"; then
      echo "    https probe helper failed — collecting SSM evidence, scan will fail" >&2
      ORCHESTRATION_ERRORS=$((ORCHESTRATION_ERRORS + 1))
    elif ! https_transport_ok="$(printf '%s' "$https_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); r=d["reachable"]; c=d["http_code"]; assert isinstance(r,bool) and (r or c == 0); print("1" if r else "0")' 2>/dev/null)"; then
      echo "    https probe returned invalid JSON — collecting SSM evidence, scan will fail" >&2
      ORCHESTRATION_ERRORS=$((ORCHESTRATION_ERRORS + 1))
    elif [ "$https_transport_ok" != "1" ]; then
      echo "    https transport failure — marking unreachable (skip SSM)" >&2
      if [ "$ALERT_JSON" = "1" ]; then
        printf '{"edge":"%s","reachable":false,"reason":"https_unreachable","schema_version":1}\n' "$label" >> "$RESULTS"
      else
        printf '{"edge":"%s","verdict":"unreachable","reason":"https_unreachable"}\n' "$label" >> "$RESULTS"
      fi
      continue
    fi
  fi
  if out="$(bash "$RUN_PROBE" --target "$tgt" --script "$PROBE" \
              --env "PLATFORM=anthropic" --env "SINCE=$SINCE" \
              --env "TERMINAL_ONLY=$ALERT_JSON" \
              --timeout-seconds "$PROBE_TIMEOUT" 2>/dev/null)"; then
    if [ "$ALERT_JSON" = "1" ]; then
      terminal_json=""
      if ! terminal_json="$(printf '%s\n' "$out" | python3 "$HERE/edge_terminal_probe.py" --label "$label" 2>/dev/null)"; then
        echo "    terminal parser failed — scan will fail" >&2
        ORCHESTRATION_ERRORS=$((ORCHESTRATION_ERRORS + 1))
        printf '{"edge":"%s","reachable":false,"reason":"parse_error","schema_version":1}\n' "$label" >> "$RESULTS"
      else
        printf '%s\n' "$terminal_json" >> "$RESULTS"
      fi
      continue
    fi
    verdict_json=""
    if ! verdict_json="$(printf '%s\n' "$out" | python3 "$VERDICT" --label "$label" 2>/dev/null)"; then
      echo "    verdict helper failed — scan will fail" >&2
      ORCHESTRATION_ERRORS=$((ORCHESTRATION_ERRORS + 1))
      printf '{"edge":"%s","verdict":"parse-error"}\n' "$label" >> "$RESULTS"
    elif [ -n "$verdict_json" ]; then
      printf '%s\n' "$verdict_json" >> "$RESULTS"
    else
      printf '{"edge":"%s","verdict":"parse-error"}\n' "$label" >> "$RESULTS"
    fi
  else
    if [ "$ALERT_JSON" = "1" ]; then
      printf '{"edge":"%s","reachable":true,"reason":"ssm_unreachable","schema_version":1,"telemetry_status":"unavailable","buckets":[]}\n' "$label" >> "$RESULTS"
    else
      printf '{"edge":"%s","verdict":"unreachable","reason":"ssm_unreachable"}\n' "$label" >> "$RESULTS"
    fi
  fi
done

# One valid, unique verdict per intended target is required before a caller may
# compare the actionable set with prior state. Partial scans can otherwise look
# like recoveries and incorrectly advance the dedup key.
if ! python3 - "$RESULTS" "$ALERT_JSON" "${TARGETS[@]}" <<'PY'
import json
import sys

result_path, alert_json, *targets = sys.argv[1:]
expected = [target[5:] if target.startswith("edge:") else target for target in targets]
valid_verdicts = {
    "down", "unreachable", "parse-error", "degraded", "thin", "no-accounts",
    "idle-thin", "idle", "healthy",
}
rows = []
with open(result_path, encoding="utf-8") as fh:
    for line in fh:
        rows.append(json.loads(line))
if alert_json == "1":
    for row in rows:
        if not isinstance(row, dict) or row.get("schema_version") != 1 or not isinstance(row.get("reachable"), bool):
            raise SystemExit(f"invalid terminal row: {row!r}")
        if row["reachable"] and (not isinstance(row.get("buckets"), list) or row.get("telemetry_status") not in {"fresh", "stale", "unavailable"}):
            raise SystemExit(f"invalid reachable terminal row: {row!r}")
else:
    if any(not isinstance(row, dict) or row.get("verdict") not in valid_verdicts for row in rows):
        raise SystemExit(f"invalid verdict row: {rows!r}")
actual = [row.get("edge") for row in rows]
if len(actual) != len(expected) or len(set(actual)) != len(actual) or set(actual) != set(expected):
    raise SystemExit(f"incomplete verdict set: expected={expected!r} actual={actual!r}")
PY
then
  echo "scan-edge-health: incomplete or invalid verdict set" >&2
  ORCHESTRATION_ERRORS=$((ORCHESTRATION_ERRORS + 1))
fi

# --json: emit the per-edge verdict JSON lines verbatim (machine-readable, for
# edge-health-alert.py manual triage) and skip the human table.
if [ "$JSON" = "1" ] || [ "$ALERT_JSON" = "1" ]; then
  cat "$RESULTS"
  [ "$ORCHESTRATION_ERRORS" -eq 0 ] && exit 0
  exit 1
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
         "thin": 2, "no-accounts": 3, "idle-thin": 4, "idle": 5, "healthy": 6}
rows.sort(key=lambda r: (order.get(r.get("verdict"), 9), r.get("edge") or ""))

hdr = ("EDGE", "VERDICT", "SCHED", "SERVED_200", "NO_AVAIL_429", "RATIO", "WAIT_TO", "SPOF")
w = (6, 11, 6, 11, 13, 7, 8, 5)
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
spof = [r for r in rows if r.get("single_account_risk") and r.get("verdict") not in ("down","unreachable","no-accounts")]
noacct = [r for r in rows if r.get("verdict") == "no-accounts"]
if bad:
    print("ACTION — down/degraded/unreachable:", ", ".join(r.get("edge","?") for r in bad))
if spof:
    print("RISK   — single-account (SPOF):    ", ", ".join(r.get("edge","?") for r in spof))
if noacct:
    print("PLAN   — no accounts provisioned:  ", ", ".join(r.get("edge","?") for r in noacct), "(add accounts or decommission)")
if not bad and not spof and not noacct:
    print("all edges healthy with >=2 schedulable accounts")
PY

[ "$ORCHESTRATION_ERRORS" -eq 0 ] && exit 0
exit 1
