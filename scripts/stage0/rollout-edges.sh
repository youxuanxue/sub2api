#!/usr/bin/env bash
# rollout-edges.sh — fail-stop Edge rollout for a released tag.
#
# Mechanizes the tokenkey-stage0-release-rollout skill step "其余 deployable
# Edge upgrade（infra only）": for each deployable edge (minus --skip, normally
# the canary already upgraded), dispatch an upgrade via dispatch-edge-deploy.sh,
# watch the run to completion, and verify the canonical acceptance marker
# `tk_edge_post_deploy_smoke: OK phase=infra` in the run log.
#
# Default is sequential. --parallel N dispatches bounded batches after the
# canary/prod gates have already passed, reducing all-rollout wall clock while
# preserving fail-stop between batches. If any edge in a batch fails, the script
# finishes verifying that already-dispatched batch, prints the failed edge(s),
# and does not dispatch the next batch.
#
# Usage:
#   bash scripts/stage0/rollout-edges.sh --tag 1.7.88 --skip uk1
#   bash scripts/stage0/rollout-edges.sh --tag 1.7.88 --skip uk1 --parallel 3
#   bash scripts/stage0/rollout-edges.sh --tag 1.7.88 --edges "uk2 uk3 us2"
#
# Flags:
#   --tag X.Y.Z      image tag (no leading v). Required.
#   --skip a[,b]     edges to exclude from the deployable matrix (e.g. canary).
#   --edges "a b c"  explicit edge list; overrides the matrix + --skip.
#   --parallel N     batch size (default 1). N>1 dispatches a batch before
#                    watching/verifying it; fail-stop applies before next batch.
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
PARALLEL=1
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) TAG="${2:-}"; shift 2 ;;
    --skip) SKIP="${2:-}"; shift 2 ;;
    --edges) EDGES="${2:-}"; shift 2 ;;
    --parallel) PARALLEL="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
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
case "$PARALLEL" in
  ''|*[!0-9]*) echo "rollout-edges: --parallel must be a positive integer" >&2; exit 1 ;;
esac
if [ "$PARALLEL" -lt 1 ]; then
  echo "rollout-edges: --parallel must be >= 1" >&2
  exit 1
fi

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
echo "rollout-edges: tag=$TAG edges=[$EDGES] parallel=$PARALLEL"

# find_run — resolve the run id created by the dispatch we just issued.
# Prefer the run URL the dispatch prints (if gh ever starts printing one);
# otherwise poll the run list of the workflow the dispatch itself reported
# (`workflow=…` on its status line — single source of truth, dispatch routes
# per edge platform). All edges share that workflow file, so a bare
# createdAt>=t0 match can be hijacked by a concurrent dispatch for another
# edge — match the run-name identity (`edge=<id> `, set by the workflow's
# run-name) first, and only fall back to the newest run after t0 (with a
# warning) for runs dispatched from refs that predate the run-name.
find_run() {
  local dispatch_out="$1" t0="$2" edge="$3" run_id="" workflow=""
  run_id="$(printf '%s' "$dispatch_out" | grep -oE 'https://github.com/[^ ]*/actions/runs/[0-9]+' | head -1 | grep -oE '[0-9]+$' || true)"
  if [ -n "$run_id" ]; then printf '%s' "$run_id"; return 0; fi
  workflow="$(printf '%s' "$dispatch_out" | grep -oE 'workflow=[^ ]+' | head -1 | cut -d= -f2 || true)"
  if [ -z "$workflow" ]; then
    echo "rollout-edges: cannot resolve workflow from dispatch output" >&2
    return 1
  fi
  local i
  for i in $(seq 1 12); do
    run_id="$(gh run list --workflow="$workflow" --limit 10 \
      --json databaseId,createdAt,displayTitle --jq \
      "[.[] | select(.createdAt >= \"$t0\") | select(.displayTitle | startswith(\"edge=$edge \"))] | sort_by(.createdAt) | first | .databaseId // empty" || true)"
    if [ -n "$run_id" ]; then printf '%s' "$run_id"; return 0; fi
    sleep 5
  done
  # Legacy fallback: run-name only takes effect once the dispatched ref carries
  # it; identity is then NOT verified, so warn loudly.
  run_id="$(gh run list --workflow="$workflow" --limit 5 \
    --json databaseId,createdAt --jq \
    "[.[] | select(.createdAt >= \"$t0\")] | sort_by(.createdAt) | first | .databaseId // empty" || true)"
  if [ -n "$run_id" ]; then
    echo "rollout-edges: WARN: no run-name match for edge=$edge; falling back to newest run after t0 (identity unverified)" >&2
    printf '%s' "$run_id"; return 0
  fi
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

# verify_smoke_marker — confirm the canonical infra-smoke marker in the run log,
# RETRYING when the log is reachable but the marker is absent. A run that
# watch_run already confirmed completed/success can still expose a PARTIAL log
# for a few seconds: `gh run view --log` returns the earlier steps before the
# final "Edge smoke" step's lines flush to the log API, so a fetch-once-then-grep
# races and false-negatives (observed on us3 during the v1.8.14 rollout,
# 2026-06-18 — run was green and the marker was present moments later, yet the
# immediate fetch missed it and fail-stopped the rollout). Combining fetch+grep
# in one retry loop closes that window. Three outcomes, by exit code:
#   0 — marker found
#   1 — log fetched (non-empty) on at least one attempt but marker never appeared
#       (a genuine smoke failure — fail-stop)
#   2 — log never fetchable across all attempts (transient gh/proxy — infra error)
# Distinguishing 1 from 2 keeps a network blip from masquerading as a real
# regression, and vice versa.
verify_smoke_marker() {
  local run_id="$1" attempt log got_log=0
  for attempt in 1 2 3 4 5 6; do
    if log="$(gh run view "$run_id" --log 2>/dev/null)" && [ -n "$log" ]; then
      got_log=1
      if printf '%s' "$log" | grep -q 'tk_edge_post_deploy_smoke: OK phase=infra'; then
        return 0
      fi
    fi
    sleep 5
  done
  [ "$got_log" -eq 1 ] && return 1 || return 2
}

dispatch_edge() {
  local EDGE="$1" T0 DISPATCH_OUT RUN_ID
  echo "rollout-edges: dispatching edge=$EDGE" >&2
  T0="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  if ! DISPATCH_OUT="$(bash scripts/stage0/dispatch-edge-deploy.sh \
        --edge-id "$EDGE" --operation upgrade --tag "$TAG" 2>&1)"; then
    printf '%s\n' "$DISPATCH_OUT" >&2
    echo "rollout-edges: edge=$EDGE run_id=unknown result=fail (dispatch)" >&2
    return 2
  fi
  if ! RUN_ID="$(find_run "$DISPATCH_OUT" "$T0" "$EDGE")"; then
    echo "rollout-edges: edge=$EDGE run_id=unknown result=fail (run not found after dispatch)" >&2
    return 2
  fi
  printf '%s|%s\n' "$EDGE" "$RUN_ID"
}

verify_edge_run() {
  local EDGE="$1" RUN_ID="$2" MARKER_RC=0
  if ! watch_run "$RUN_ID"; then
    echo "rollout-edges: edge=$EDGE run_id=$RUN_ID result=fail (run failed)" >&2
    return 1
  fi
  verify_smoke_marker "$RUN_ID" || MARKER_RC=$?
  if [ "$MARKER_RC" -eq 2 ]; then
    echo "rollout-edges: edge=$EDGE run_id=$RUN_ID result=fail (could not fetch run log)" >&2
    return 2
  elif [ "$MARKER_RC" -ne 0 ]; then
    echo "rollout-edges: edge=$EDGE run_id=$RUN_ID result=fail (smoke marker missing)" >&2
    return 1
  fi
  echo "rollout-edges: edge=$EDGE run_id=$RUN_ID result=ok"
  return 0
}

COUNT=0
FAILURES=""
declare -a BATCH_EDGES=()
declare -a BATCH_RUNS=()

flush_batch() {
  local i edge run_id rc batch_failed=0 transport_failed=0
  for i in "${!BATCH_EDGES[@]}"; do
    edge="${BATCH_EDGES[$i]}"
    run_id="${BATCH_RUNS[$i]}"
    rc=0
    verify_edge_run "$edge" "$run_id" || rc=$?
    if [ "$rc" -eq 0 ]; then
      COUNT=$((COUNT + 1))
    else
      batch_failed=1
      [ "$rc" -eq 2 ] && transport_failed=1
      FAILURES="${FAILURES} ${edge}:${run_id}"
    fi
  done
  BATCH_EDGES=()
  BATCH_RUNS=()
  if [ "$batch_failed" -ne 0 ]; then
    echo "rollout-edges: FAILURES${FAILURES}" >&2
    [ "$transport_failed" -ne 0 ] && return 2 || return 1
  fi
  return 0
}

for EDGE in $EDGES; do
  DISPATCH_RC=0
  DISPATCH_ROW="$(dispatch_edge "$EDGE")" || DISPATCH_RC=$?
  if [ "$DISPATCH_RC" -ne 0 ]; then
    [ "${#BATCH_EDGES[@]}" -gt 0 ] && flush_batch || true
    exit "$DISPATCH_RC"
  fi
  RUN_ID="$(printf '%s\n' "$DISPATCH_ROW" | awk -F '|' 'NF >= 2 {print $2; exit}')"
  if [ -z "$RUN_ID" ]; then
    echo "rollout-edges: edge=$EDGE run_id=unknown result=fail (dispatch row malformed)" >&2
    [ "${#BATCH_EDGES[@]}" -gt 0 ] && flush_batch || true
    exit 2
  fi
  BATCH_EDGES+=("$EDGE")
  BATCH_RUNS+=("$RUN_ID")
  if [ "${#BATCH_EDGES[@]}" -ge "$PARALLEL" ]; then
    flush_batch || exit $?
  fi
done

[ "${#BATCH_EDGES[@]}" -eq 0 ] || flush_batch || exit $?
echo "rollout-edges: ALL_OK n=$COUNT"
