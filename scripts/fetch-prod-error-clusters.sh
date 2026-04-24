#!/usr/bin/env bash
#
# scripts/fetch-prod-error-clusters.sh — Cloud-Agent-friendly wrapper that
# triggers the `error-clustering-daily.yml` workflow on GitHub and downloads
# its artifact (`report.json` + `report.md`) to a local directory.
#
# Why this exists (path 2 of "let Cloud Agent read prod logs"):
#   Cursor Cloud Agent has no AWS credentials and no GitHub Actions OIDC
#   token, so it cannot replay the GHA → STS → SSM → EC2 chain directly.
#   Instead it asks GitHub Actions to do that chain on its behalf via
#   `gh workflow run`, then pulls the resulting artifact.
#
# Net effect:
#   - No new long-lived AWS credentials in Cloud Agent Secrets.
#   - No new IAM trust surface — the OIDC role still only trusts
#     `token.actions.githubusercontent.com` for the configured repo+ref.
#   - Cloud Agent only needs ONE secret: a GitHub PAT scoped to this repo.
#
# Required env (Cursor Cloud Agents → Secrets):
#   GH_TOKEN           GitHub PAT or fine-grained token with these scopes
#                      on youxuanxue/sub2api:
#                        - actions: write   (dispatch the workflow)
#                        - actions: read    (poll status, download artifact)
#                        - contents: read   (gh run download requires this)
#                      Recommended: fine-grained token, single repo, no other
#                      permissions. This token never reaches AWS — it only
#                      authenticates against api.github.com.
#
# Optional env:
#   GH_REPO            default: youxuanxue/sub2api
#   SINCE_HOURS        default: 24      (workflow input, must be a positive int)
#   OUT_DIR            default: ./.error-clusters
#   POLL_TIMEOUT_S     default: 600     (max seconds to wait for the run to finish)
#
# Modes:
#   bash scripts/fetch-prod-error-clusters.sh           # dispatch + wait + download
#   bash scripts/fetch-prod-error-clusters.sh --check   # validate env + tools, no dispatch
#
# Exit codes:
#   0  report downloaded (or run finished gracefully — including the
#      `skip:` path when AWS_OIDC_ROLE_ARN is unset on the repo)
#   1  bad input / missing token / missing tool / workflow run failed
#   2  workflow dispatched but did not start within POLL_TIMEOUT_S
set -euo pipefail

GH_REPO="${GH_REPO:-youxuanxue/sub2api}"
SINCE_HOURS="${SINCE_HOURS:-24}"
OUT_DIR="${OUT_DIR:-./.error-clusters}"
POLL_TIMEOUT_S="${POLL_TIMEOUT_S:-600}"
WORKFLOW="error-clustering-daily.yml"

MODE="run"
if [ "${1:-}" = "--check" ]; then
  MODE="check"
fi

err() { echo "[fetch-clusters] error: $*" >&2; }
log() { echo "[fetch-clusters] $*"; }

require_tool() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    err "$tool is not installed (required). On Cursor Cloud Agent, install via dev-rules/templates/cloud-agent-bootstrap.sh."
    return 1
  fi
}

validate_env() {
  local ok=0
  require_tool gh || ok=1
  require_tool jq || ok=1

  if [ -z "${GH_TOKEN:-}" ]; then
    err "GH_TOKEN is not set. Add it in Cursor Dashboard → Cloud Agents → Secrets."
    err "  Required scopes on $GH_REPO: actions:write, actions:read, contents:read."
    ok=1
  fi

  if ! printf '%s' "$SINCE_HOURS" | grep -Eq '^[1-9][0-9]*$'; then
    err "SINCE_HOURS must be a positive integer, got: '$SINCE_HOURS'"
    ok=1
  fi

  return $ok
}

if ! validate_env; then
  exit 1
fi

if [ "$MODE" = "check" ]; then
  log "env + tools OK (repo=$GH_REPO, since_hours=$SINCE_HOURS)"
  exit 0
fi

mkdir -p "$OUT_DIR"

log "snapshotting last run id on $GH_REPO/$WORKFLOW"
PREV_ID=$(gh run list --workflow="$WORKFLOW" --repo "$GH_REPO" --limit 1 \
  --json databaseId --jq '.[0].databaseId // 0')
log "previous run id: $PREV_ID"

log "dispatching $WORKFLOW (since_hours=$SINCE_HOURS)"
gh workflow run "$WORKFLOW" --repo "$GH_REPO" -f "since_hours=$SINCE_HOURS"

log "polling for new run id (timeout ${POLL_TIMEOUT_S}s)"
DEADLINE=$(( $(date +%s) + POLL_TIMEOUT_S ))
RUN_ID="$PREV_ID"
while [ "$RUN_ID" = "$PREV_ID" ] || [ "$RUN_ID" = "0" ]; do
  sleep 4
  RUN_ID=$(gh run list --workflow="$WORKFLOW" --repo "$GH_REPO" --limit 1 \
    --json databaseId --jq '.[0].databaseId // 0')
  if [ "$(date +%s)" -ge "$DEADLINE" ]; then
    err "timed out waiting for workflow to start (still seeing previous run id $PREV_ID)"
    exit 2
  fi
done
log "new run id: $RUN_ID"

# `gh run watch --exit-status` returns the run's conclusion as exit code.
# We capture it but still try to download the artifact — the precheck
# step may have produced a 'skip:' report.json that's useful even on
# non-success conclusions.
WATCH_RC=0
gh run watch "$RUN_ID" --repo "$GH_REPO" --exit-status || WATCH_RC=$?

ART_NAME="error-clustering-$RUN_ID"
log "downloading artifact $ART_NAME → $OUT_DIR"
if ! gh run download "$RUN_ID" --repo "$GH_REPO" --name "$ART_NAME" --dir "$OUT_DIR"; then
  err "artifact download failed (run conclusion exit=$WATCH_RC). Check 'gh run view $RUN_ID --repo $GH_REPO --log'."
  exit 1
fi

if [ -s "$OUT_DIR/report.json" ]; then
  SUMMARY=$(jq -r '.summary // "(no summary field)"' "$OUT_DIR/report.json" 2>/dev/null || echo "(report.json not parseable)")
  log "summary: $SUMMARY"
fi

if [ "$WATCH_RC" -ne 0 ]; then
  err "workflow run $RUN_ID did not succeed (exit=$WATCH_RC), but report files were downloaded."
  exit 1
fi

log "done. files: $OUT_DIR/report.json $OUT_DIR/report.md"
