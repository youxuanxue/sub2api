#!/usr/bin/env bash
# approve-github-run-env.sh — Poll and approve a GitHub Actions Environment gate.
#
# Usage:
#   bash scripts/stage0/approve-github-run-env.sh --run-id <id> [--comment TEXT] [--timeout-seconds N]
#
# Exit codes:
#   0 — approved (or run already past waiting with no pending deployment)
#   1 — timeout / run failed before approval / no pending deployment when required
#   2 — gh/network misconfiguration
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID=""
COMMENT="approved via approve-github-run-env.sh"
TIMEOUT=120
POLL=3

while [ "$#" -gt 0 ]; do
  case "$1" in
    --run-id) RUN_ID="$2"; shift 2 ;;
    --comment) COMMENT="$2"; shift 2 ;;
    --timeout-seconds) TIMEOUT="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "approve-github-run-env: unknown arg: $1" >&2; exit 1 ;;
  esac
done

if [ -z "$RUN_ID" ]; then
  echo "approve-github-run-env: --run-id is required" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "approve-github-run-env: gh not on PATH" >&2
  exit 2
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "approve-github-run-env: jq not on PATH" >&2
  exit 2
fi

REPO="$(bash "$REPO_ROOT/scripts/lib/resolve-gh-repo.sh" "$REPO_ROOT" 2>/dev/null)" || {
  echo "approve-github-run-env: failed to resolve GitHub repo" >&2
  exit 2
}

deadline=$(( $(date +%s) + TIMEOUT ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  ENV_ID="$(gh api "repos/$REPO/actions/runs/$RUN_ID/pending_deployments" --jq '.[0].environment.id // empty' 2>/dev/null || true)"
  if [ -n "$ENV_ID" ]; then
    PAYLOAD="$(jq -n --argjson env_id "$ENV_ID" --arg comment "$COMMENT" \
      '{environment_ids: [$env_id], state: "approved", comment: $comment}')"
    gh api -X POST "repos/$REPO/actions/runs/$RUN_ID/pending_deployments" --input - <<<"$PAYLOAD"
    echo "approve-github-run-env: run=$RUN_ID env_id=$ENV_ID approved"
    gh run view "$RUN_ID" --json status --jq '.status' 2>/dev/null || true
    exit 0
  fi

  STATUS="$(gh run view "$RUN_ID" --json status --jq .status 2>/dev/null || echo unknown)"
  case "$STATUS" in
    waiting|queued|in_progress|pending) ;;
    completed)
      CONCLUSION="$(gh run view "$RUN_ID" --json conclusion --jq .conclusion 2>/dev/null || echo "")"
      if [ "$CONCLUSION" = "success" ]; then
        echo "approve-github-run-env: run=$RUN_ID already completed success (no pending deployment)"
        exit 0
      fi
      echo "approve-github-run-env: run=$RUN_ID completed with conclusion=$CONCLUSION" >&2
      exit 1
      ;;
    *)
      echo "approve-github-run-env: run=$RUN_ID status=$STATUS (no pending deployment yet)" >&2
      ;;
  esac
  sleep "$POLL"
done

echo "approve-github-run-env: timeout after ${TIMEOUT}s waiting for pending deployment on run=$RUN_ID" >&2
exit 1
