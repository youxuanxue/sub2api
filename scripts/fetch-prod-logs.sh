#!/usr/bin/env bash
#
# scripts/fetch-prod-logs.sh — Cloud-Agent-friendly wrapper that triggers
# the `prod-log-dump.yml` workflow on GitHub and downloads the resulting
# `logs.txt` artifact.
#
# Companion to scripts/fetch-prod-error-clusters.sh. Same architecture
# (gh workflow run → poll → download), different question:
#   - fetch-prod-error-clusters.sh → "show me aggregate trends"
#   - fetch-prod-logs.sh           → "show me raw log lines for incident X"
#
# Both scripts share the same GH_TOKEN secret and the same OIDC role on
# the AWS side; no new credentials are required to enable this one.
#
# Required env (Cursor Cloud Agents → Secrets — already configured for
# fetch-prod-error-clusters.sh):
#   GH_TOKEN           GitHub PAT scoped to youxuanxue/sub2api with
#                      actions:read/write + contents:read.
#
# Optional env:
#   GH_REPO            default: youxuanxue/sub2api
#   SINCE              default: 10m              docker logs --since value
#                                                (must match ^[0-9]+[smhd]$)
#   CONTAINER          default: tokenkey         one of tokenkey,
#                                                tokenkey-postgres,
#                                                tokenkey-caddy,
#                                                tokenkey-redis
#   GREP_PATTERN       default: ""               ERE; empty = no filter.
#                                                Transported to EC2 via
#                                                base64 + grep -E -f file,
#                                                so any byte is preserved
#                                                (\d, \(, ', ", $, ...).
#   TAIL_LINES         default: 1000             positive int <= 10000
#   OUT_DIR            default: ./.prod-logs
#   POLL_TIMEOUT_S     default: 600
#
# Modes:
#   bash scripts/fetch-prod-logs.sh           # dispatch + wait + download
#   bash scripts/fetch-prod-logs.sh --check   # validate env + tools, no dispatch
#
# Exit codes (same shape as fetch-prod-error-clusters.sh):
#   0  logs downloaded (including the case where no lines matched)
#   1  bad input / missing token / missing tool / workflow run failed
#      (most common P1: SSM 24KB stdout cap was hit — workflow detects
#       this and fails with "Tighten GREP_PATTERN, lower TAIL_LINES, or
#       shorten SINCE". The script propagates that as exit 1.)
#   2  workflow dispatched but did not start within POLL_TIMEOUT_S
set -euo pipefail

GH_REPO="${GH_REPO:-youxuanxue/sub2api}"
SINCE="${SINCE:-10m}"
CONTAINER="${CONTAINER:-tokenkey}"
GREP_PATTERN="${GREP_PATTERN:-}"
TAIL_LINES="${TAIL_LINES:-1000}"
OUT_DIR="${OUT_DIR:-./.prod-logs}"
POLL_TIMEOUT_S="${POLL_TIMEOUT_S:-600}"
WORKFLOW="prod-log-dump.yml"

MODE="run"
if [ "${1:-}" = "--check" ]; then
  MODE="check"
fi

err() { echo "[fetch-prod-logs] error: $*" >&2; }
log() { echo "[fetch-prod-logs] $*"; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/gh-workflow-artifact.sh
source "$SCRIPT_DIR/lib/gh-workflow-artifact.sh"

validate_env() {
  local ok=0
  validate_gh_workflow_env "$GH_REPO" || ok=1

  if ! printf '%s' "$SINCE" | grep -Eq '^[0-9]+[smhd]$'; then
    err "SINCE must match ^[0-9]+[smhd]\$ (e.g. 30m, 2h, 1d), got: '$SINCE'"
    ok=1
  fi

  case "$CONTAINER" in
    tokenkey|tokenkey-postgres|tokenkey-caddy|tokenkey-redis) ;;
    *)
      err "CONTAINER must be one of tokenkey|tokenkey-postgres|tokenkey-caddy|tokenkey-redis, got: '$CONTAINER'"
      ok=1
      ;;
  esac

  if ! printf '%s' "$TAIL_LINES" | grep -Eq '^[1-9][0-9]*$'; then
    err "TAIL_LINES must be a positive integer, got: '$TAIL_LINES'"
    ok=1
  elif [ "$TAIL_LINES" -gt 10000 ]; then
    err "TAIL_LINES capped at 10000 (workflow enforces same), got: $TAIL_LINES"
    ok=1
  fi

  if [ ${#GREP_PATTERN} -gt 512 ]; then
    err "GREP_PATTERN too long (>512 chars)"
    ok=1
  fi

  return $ok
}

if ! validate_env; then
  exit 1
fi

if [ "$MODE" = "check" ]; then
  log "env + tools OK (repo=$GH_REPO, container=$CONTAINER, since=$SINCE, tail=$TAIL_LINES, grep=${GREP_PATTERN:-(none)})"
  exit 0
fi

log "dispatching $WORKFLOW (container=$CONTAINER since=$SINCE tail=$TAIL_LINES grep=${GREP_PATTERN:-(none)})"
dispatch_workflow_and_download_artifact \
  "$GH_REPO" \
  "$WORKFLOW" \
  "$POLL_TIMEOUT_S" \
  'prod-logs-{run_id}' \
  "$OUT_DIR" \
  -f "since=$SINCE" \
  -f "container=$CONTAINER" \
  -f "grep_pattern=$GREP_PATTERN" \
  -f "tail_lines=$TAIL_LINES"

if [ -s "$OUT_DIR/logs.txt" ]; then
  LINES=$(wc -l < "$OUT_DIR/logs.txt")
  BYTES=$(wc -c < "$OUT_DIR/logs.txt")
  log "captured $LINES lines / $BYTES bytes → $OUT_DIR/logs.txt"
else
  log "no log entries returned (empty artifact). Try widening SINCE or removing GREP_PATTERN."
fi

if [ "$GH_WORKFLOW_WATCH_RC" -ne 0 ]; then
  err "workflow run $GH_WORKFLOW_RUN_ID did not succeed (exit=$GH_WORKFLOW_WATCH_RC), but artifact was downloaded if available."
  exit 1
fi
