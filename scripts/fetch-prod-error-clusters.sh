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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/gh-workflow-artifact.sh
source "$SCRIPT_DIR/lib/gh-workflow-artifact.sh"

validate_env() {
  local ok=0
  validate_gh_workflow_env "$GH_REPO" || ok=1

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

log "dispatching $WORKFLOW (since_hours=$SINCE_HOURS)"
dispatch_workflow_and_download_artifact \
  "$GH_REPO" \
  "$WORKFLOW" \
  "$POLL_TIMEOUT_S" \
  'error-clustering-{run_id}' \
  "$OUT_DIR" \
  -f "since_hours=$SINCE_HOURS" || exit $?

if [ -s "$OUT_DIR/report.json" ]; then
  SUMMARY=$(jq -r '.summary // "(no summary field)"' "$OUT_DIR/report.json" 2>/dev/null || echo "(report.json not parseable)")
  log "summary: $SUMMARY"
fi

if [ "$GH_WORKFLOW_WATCH_RC" -ne 0 ]; then
  err "workflow run $GH_WORKFLOW_RUN_ID did not succeed (exit=$GH_WORKFLOW_WATCH_RC), but report files were downloaded."
  exit 1
fi

log "done. files: $OUT_DIR/report.json $OUT_DIR/report.md"
