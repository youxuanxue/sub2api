#!/usr/bin/env bash
# run-post-release-check.sh — +5min post-release check owned by one script.
#
# Builds a check plan from the commits/PRs between the tag that was serving
# (live) and the tag just deployed (new), probes prod, and evaluates whether
# those changes look like the expected live behavior. Do not invent
# HOOK_PATTERNS in prompt prose — this script derives them from the plan.
#
# Usage:
#   bash ops/observability/run-post-release-check.sh \
#       --live 1.8.169 --new 1.8.170 \
#       [--target prod] \
#       [--repo DIR] \
#       [--skip-probe] \
#       [--tick-file PATH] \
#       [--control-plane-ok true|false] \
#       [--phase immediate|delayed] \
#       [--since DURATION_OR_TIMESTAMP] \
#       [--plan-file PATH] \
#       [--out-dir DIR]
#
# Exit:
#   0 — verdict green (or skip: no product commits in range)
#   1 — verdict red (regression) or plan/probe usage failure
#   2 — AWS/SSM transport failure
set -euo pipefail

usage() {
  sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
}

LIVE=""
NEW=""
TARGET="prod"
REPO="."
SKIP_PROBE=0
TICK_FILE=""
CONTROL_PLANE_OK=""
PLAN_FILE_ARG=""
OUT_DIR=""
PHASE="delayed"
SINCE="6m"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --live) LIVE="${2:-}"; shift 2 ;;
    --new) NEW="${2:-}"; shift 2 ;;
    --target) TARGET="${2:-}"; shift 2 ;;
    --repo) REPO="${2:-}"; shift 2 ;;
    --skip-probe) SKIP_PROBE=1; shift ;;
    --tick-file) TICK_FILE="${2:-}"; shift 2 ;;
    --control-plane-ok) CONTROL_PLANE_OK="${2:-}"; shift 2 ;;
    --phase) PHASE="${2:-}"; shift 2 ;;
    --since) SINCE="${2:-}"; shift 2 ;;
    --plan-file) PLAN_FILE_ARG="${2:-}"; shift 2 ;;
    --out-dir) OUT_DIR="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "[run-post-release-check] unknown arg: $1" >&2; usage >&2; exit 1 ;;
  esac
done

if [ -z "$LIVE" ] || [ -z "$NEW" ]; then
  echo "[run-post-release-check] --live and --new are required" >&2
  usage >&2
  exit 1
fi
case "$PHASE" in
  immediate|delayed) ;;
  *) echo "[run-post-release-check] --phase must be immediate|delayed" >&2; exit 1 ;;
esac

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PLANNER="$ROOT/scripts/release_post_check.py"
if [ ! -f "$PLANNER" ]; then
  echo "[run-post-release-check] missing $PLANNER" >&2
  exit 1
fi

if [ -z "$OUT_DIR" ]; then
  OUT_DIR="$(mktemp -d /tmp/tk-post-release-check.XXXXXX)"
fi
mkdir -p "$OUT_DIR"
if [ -n "$PLAN_FILE_ARG" ]; then
  PLAN_FILE="$PLAN_FILE_ARG"
  if [ ! -f "$PLAN_FILE" ]; then
    echo "[run-post-release-check] --plan-file not found: $PLAN_FILE" >&2
    exit 1
  fi
else
  PLAN_FILE="$OUT_DIR/plan.json"
fi
TICK_OUT="$OUT_DIR/tick.txt"
EVAL_FILE="$OUT_DIR/evaluate.json"

if [ -f "$PLAN_FILE" ]; then
  echo "[run-post-release-check] reusing existing plan $PLAN_FILE" >&2
else
  python3 "$PLANNER" plan --live "$LIVE" --new "$NEW" --repo "$REPO" > "$PLAN_FILE"
fi
CHANGE_COUNT="$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1])).get("changes") or []))' "$PLAN_FILE")"
if [ "$CHANGE_COUNT" = "0" ]; then
  echo "[run-post-release-check] skip: no product commits in $LIVE..$NEW" >&2
  printf '%s\n' '{"verdict":"skip","reason":"no product commits","range":{"live":"'"$LIVE"'","new":"'"$NEW"'"}}' | tee "$EVAL_FILE"
  exit 0
fi

HOOKS="$(python3 "$PLANNER" hook-patterns --plan-file "$PLAN_FILE")"
TRAFFIC_PATHS="$(python3 "$PLANNER" traffic-paths --plan-file "$PLAN_FILE")"
echo "[run-post-release-check] phase=$PHASE since=$SINCE plan=$PLAN_FILE changes=$CHANGE_COUNT hooks=$HOOKS traffic_paths=$TRAFFIC_PATHS" >&2

CP_OK="${CONTROL_PLANE_OK:-true}"
if [ "$SKIP_PROBE" -eq 0 ]; then
  if [ "$TARGET" = "prod" ]; then
    export EDGE_IDS="${EDGE_IDS:-none}"
  fi
  if ! bash "$ROOT/ops/observability/probe-release-control-plane.sh" | tee "$OUT_DIR/control-plane.jsonl"; then
    CP_OK="false"
  else
    if python3 -c 'import json,sys; rows=[json.loads(l) for l in open(sys.argv[1]) if l.startswith("{")];
print("ok" if any(r.get("summary")=="control_plane" and r.get("status")=="ok" for r in rows) else "bad")' \
      "$OUT_DIR/control-plane.jsonl" | grep -qx ok; then
      CP_OK="true"
    else
      CP_OK="false"
    fi
  fi

  set +e
  bash "$ROOT/ops/observability/run-probe.sh" --target "$TARGET" \
    --script "$ROOT/ops/observability/probe-post-release-tick.sh" \
    --env "SINCE=${SINCE}" \
    --env "HOOK_PATTERNS=${HOOKS}" \
    --env "TRAFFIC_PATHS=${TRAFFIC_PATHS}" \
    --timeout-seconds 120 \
    | tee "$TICK_OUT"
  PROBE_RC=${PIPESTATUS[0]}
  set -e
  if [ "$PROBE_RC" -eq 2 ] || [ "$PROBE_RC" -eq 3 ]; then
    echo "[run-post-release-check] probe transport/remote failed rc=$PROBE_RC" >&2
    exit 2
  fi
  if [ "$PROBE_RC" -ne 0 ]; then
    echo "[run-post-release-check] probe failed rc=$PROBE_RC" >&2
    exit 1
  fi
else
  if [ -z "$TICK_FILE" ]; then
    echo "[run-post-release-check] --skip-probe requires --tick-file" >&2
    exit 1
  fi
  cp "$TICK_FILE" "$TICK_OUT"
  CP_OK="${CONTROL_PLANE_OK:-true}"
fi

set +e
python3 "$PLANNER" evaluate \
  --plan-file "$PLAN_FILE" \
  --tick-file "$TICK_OUT" \
  --control-plane-ok "$CP_OK" \
  --phase "$PHASE" \
  | tee "$EVAL_FILE"
EVAL_RC=${PIPESTATUS[0]}
set -e
echo "[run-post-release-check] evaluate=$EVAL_FILE rc=$EVAL_RC" >&2
exit "$EVAL_RC"
