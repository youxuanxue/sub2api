#!/usr/bin/env bash
# rollout-edges.sh — Sequential fail-stop Edge rollout for a released tag.
#
# Mechanizes the tokenkey-stage0-release-rollout skill step "其余 deployable
# Edge 顺序 upgrade（infra only）": for each deployable edge (minus --skip,
# normally the canary already upgraded), dispatch an upgrade via
# dispatch-edge-deploy.sh, watch the run to completion, and verify the
# canonical acceptance marker `tk_edge_post_deploy_smoke: OK phase=infra`
# in the run log. The first failure stops the rollout (fail-stop), leaving
# remaining edges on the previous tag.
#
# Usage:
#   bash scripts/stage0/rollout-edges.sh --tag 1.7.88 --skip uk1
#   bash scripts/stage0/rollout-edges.sh --tag 1.7.88 --edges "uk2 uk3 us2"
#
# Flags:
#   --tag X.Y.Z      image tag (no leading v). Required.
#   --skip a[,b]     edges to exclude from the deployable matrix (e.g. canary).
#   --edges "a b c"  explicit edge list; overrides the matrix + --skip.
#
# Output contract (stable, line-oriented):
#   rollout-edges: edge=<id> run_id=<id> result=ok|fail
#   rollout-edges: ALL_OK n=<count>          # only when every edge passed
#
# Exit codes: 0 — all edges upgraded + smoke-verified; 1 — fail-stop (the
# failing edge is the last `result=fail` line); 2 — dispatch/gh failure.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TAG=""
SKIP=""
EDGES=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) TAG="${2:-}"; shift 2 ;;
    --skip) SKIP="${2:-}"; shift 2 ;;
    --edges) EDGES="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,27p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "rollout-edges: unknown arg: $1" >&2; exit 1 ;;
  esac
done

if [ -z "$TAG" ]; then
  echo "rollout-edges: --tag is required (e.g. --tag 1.7.88, no leading v)" >&2
  exit 1
fi
case "$TAG" in
  v*) echo "rollout-edges: --tag must not carry the leading 'v' (workflows take the bare version)" >&2; exit 1 ;;
esac

if [ -z "$EDGES" ]; then
  EDGES="$(python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable)"
  if [ -n "$SKIP" ]; then
    SKIP_NORM="$(printf '%s' "$SKIP" | tr ',' ' ')"
    FILTERED=""
    for e in $EDGES; do
      keep=1
      for s in $SKIP_NORM; do [ "$e" = "$s" ] && keep=0; done
      [ "$keep" -eq 1 ] && FILTERED="$FILTERED $e"
    done
    EDGES="$FILTERED"
  fi
fi
EDGES="$(printf '%s' "$EDGES" | xargs || true)"
if [ -z "$EDGES" ]; then
  echo "rollout-edges: edge list is empty after filtering" >&2
  exit 1
fi
echo "rollout-edges: tag=$TAG edges=[$EDGES]"

# find_run — resolve the run id created by the dispatch we just issued.
# Prefer the run URL the dispatch prints (newer gh); fall back to polling the
# workflow's run list for a run created at/after our dispatch timestamp.
find_run() {
  local dispatch_out="$1" t0="$2" run_id=""
  run_id="$(printf '%s' "$dispatch_out" | grep -oE 'https://github.com/[^ ]*/actions/runs/[0-9]+' | head -1 | grep -oE '[0-9]+$' || true)"
  if [ -n "$run_id" ]; then printf '%s' "$run_id"; return 0; fi
  local i
  for i in $(seq 1 12); do
    run_id="$(gh run list --workflow=deploy-edge-lightsail-stage0.yml --limit 5 \
      --json databaseId,createdAt --jq \
      "[.[] | select(.createdAt >= \"$t0\")] | sort_by(.createdAt) | last | .databaseId // empty" || true)"
    if [ -n "$run_id" ]; then printf '%s' "$run_id"; return 0; fi
    sleep 5
  done
  return 1
}

# watch_run — follow a run to its terminal state, surviving transient gh/proxy
# failures: a failed `gh run watch` is only trusted after `gh run view`
# confirms the run actually completed unsuccessfully.
watch_run() {
  local run_id="$1" attempt status
  for attempt in 1 2 3 4 5; do
    if gh run watch "$run_id" --exit-status >/dev/null 2>&1; then
      return 0
    fi
    status="$(gh run view "$run_id" --json status,conclusion --jq '"\(.status)/\(.conclusion)"' 2>/dev/null || true)"
    case "$status" in
      completed/success) return 0 ;;
      completed/*) return 1 ;;
      *) sleep 10 ;;  # transient watch/network failure: re-attach
    esac
  done
  return 1
}

COUNT=0
for EDGE in $EDGES; do
  echo "rollout-edges: dispatching edge=$EDGE"
  T0="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  if ! DISPATCH_OUT="$(bash scripts/stage0/dispatch-edge-deploy.sh \
        --edge-id "$EDGE" --operation upgrade --tag "$TAG" 2>&1)"; then
    printf '%s\n' "$DISPATCH_OUT" >&2
    echo "rollout-edges: edge=$EDGE run_id=unknown result=fail (dispatch)" >&2
    exit 2
  fi
  if ! RUN_ID="$(find_run "$DISPATCH_OUT" "$T0")"; then
    echo "rollout-edges: edge=$EDGE run_id=unknown result=fail (run not found after dispatch)" >&2
    exit 2
  fi
  if ! watch_run "$RUN_ID"; then
    echo "rollout-edges: edge=$EDGE run_id=$RUN_ID result=fail (run failed)" >&2
    exit 1
  fi
  if ! gh run view "$RUN_ID" --log 2>/dev/null | grep -q 'tk_edge_post_deploy_smoke: OK phase=infra'; then
    echo "rollout-edges: edge=$EDGE run_id=$RUN_ID result=fail (smoke marker missing)" >&2
    exit 1
  fi
  echo "rollout-edges: edge=$EDGE run_id=$RUN_ID result=ok"
  COUNT=$((COUNT + 1))
done

echo "rollout-edges: ALL_OK n=$COUNT"
