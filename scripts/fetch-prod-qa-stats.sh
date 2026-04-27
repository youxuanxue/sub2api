#!/usr/bin/env bash
#
# scripts/fetch-prod-qa-stats.sh — dispatch prod-qa-stats.yml and download qa-stats.json
#
# Uses the same GH_TOKEN pattern as fetch-prod-error-clusters.sh (no AWS keys on the agent).
#
# Required env:
#   GH_TOKEN   — same scopes as fetch-prod-error-clusters.sh (actions:write/read, contents:read)
#
# Optional:
#   GH_REPO           default: youxuanxue/sub2api
#   OUT_DIR           default: ./.prod-qa-stats
#   POLL_TIMEOUT_S    default: 600
#   WORKFLOW_REF      git ref to run workflow from (default: empty = repo default branch).
#                     Use 'cursor/.../branch-name' before the workflow is merged to main.
#
set -euo pipefail

GH_REPO="${GH_REPO:-youxuanxue/sub2api}"
OUT_DIR="${OUT_DIR:-./.prod-qa-stats}"
POLL_TIMEOUT_S="${POLL_TIMEOUT_S:-600}"
WORKFLOW="prod-qa-stats.yml"

MODE="run"
if [ "${1:-}" = "--check" ]; then
  MODE="check"
fi

err() { echo "[fetch-prod-qa-stats] error: $*" >&2; }
log() { echo "[fetch-prod-qa-stats] $*"; }

require_tool() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    err "$tool is not installed (required)."
    return 1
  fi
}

validate_env() {
  local ok=0
  require_tool gh || ok=1
  require_tool jq || ok=1
  if [ -z "${GH_TOKEN:-}" ]; then
    err "GH_TOKEN is not set."
    ok=1
  fi
  return $ok
}

if ! validate_env; then
  exit 1
fi

if [ "$MODE" = "check" ]; then
  log "env + tools OK (repo=$GH_REPO)"
  exit 0
fi

mkdir -p "$OUT_DIR"

log "snapshotting last run id on $GH_REPO/$WORKFLOW"
PREV_ID=$(gh run list --workflow="$WORKFLOW" --repo "$GH_REPO" --limit 1 \
  --json databaseId --jq '.[0].databaseId // 0')
log "previous run id: $PREV_ID"

DISPATCH_ARGS=(workflow run "$WORKFLOW" --repo "$GH_REPO")
if [ -n "${WORKFLOW_REF:-}" ]; then
  DISPATCH_ARGS+=(--ref "$WORKFLOW_REF")
fi
log "dispatching $WORKFLOW${WORKFLOW_REF:+ (ref=$WORKFLOW_REF)}"
gh "${DISPATCH_ARGS[@]}"

log "polling for new run id (timeout ${POLL_TIMEOUT_S}s)"
DEADLINE=$(( $(date +%s) + POLL_TIMEOUT_S ))
RUN_ID="$PREV_ID"
while [ "$RUN_ID" = "$PREV_ID" ] || [ "$RUN_ID" = "0" ]; do
  sleep 4
  RUN_ID=$(gh run list --workflow="$WORKFLOW" --repo "$GH_REPO" --limit 1 \
    --json databaseId --jq '.[0].databaseId // 0')
  if [ "$(date +%s)" -ge "$DEADLINE" ]; then
    err "timed out waiting for workflow to start"
    exit 2
  fi
done
log "new run id: $RUN_ID"

WATCH_RC=0
gh run watch "$RUN_ID" --repo "$GH_REPO" --exit-status || WATCH_RC=$?

ART_NAME="prod-qa-stats-$RUN_ID"
log "downloading artifact $ART_NAME → $OUT_DIR"
if ! gh run download "$RUN_ID" --repo "$GH_REPO" --name "$ART_NAME" --dir "$OUT_DIR"; then
  err "artifact download failed (run exit=$WATCH_RC). Try: gh run view $RUN_ID --repo $GH_REPO --log"
  exit 1
fi

if [ -s "$OUT_DIR/qa-stats.json" ]; then
  jq . "$OUT_DIR/qa-stats.json"
fi

if [ "$WATCH_RC" -ne 0 ]; then
  err "workflow run $RUN_ID did not succeed (exit=$WATCH_RC)"
  exit 1
fi

log "done: $OUT_DIR/qa-stats.json"
