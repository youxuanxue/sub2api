#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/upstream-drift.sh
source "$SCRIPT_DIR/lib/upstream-drift.sh"

STATE_FILE="${STATE_FILE:-/tmp/upstream-merge-state.json}"

require_state_file() {
  if [ ! -f "$STATE_FILE" ]; then
    echo "STATE_FILE missing: $STATE_FILE" >&2
    return 2
  fi
}

update_state() {
  require_state_file
  jq "$@" "$STATE_FILE" > "${STATE_FILE}.tmp"
  mv "${STATE_FILE}.tmp" "$STATE_FILE"
}

set_state_meta() {
  local state="$1"
  local code="$2"
  update_state ".state=\"$state\" | .state_code=$code | .updated_at_utc=\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\""
}

set_reason_code() {
  local reason_code="$1"
  update_state ".reason_code=\"$reason_code\" | .updated_at_utc=\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\""
}

drift_check_json() {
  if ! fetch_and_load_upstream_drift_snapshot; then
    echo '{"status":"infra_error"}'
    return 0
  fi

  local status="need_merge"
  if [ "${TK_BEHIND}" -eq 0 ]; then
    status="up_to_date"
  fi

  jq -n \
    --arg status "$status" \
    --arg upstream_head "$UPSTREAM_HEAD" \
    --arg origin_head "$ORIGIN_HEAD" \
    --argjson behind "$TK_BEHIND" \
    --argjson ahead "$TK_AHEAD" \
    '{status:$status, behind:$behind, ahead:$ahead, upstream_head:$upstream_head, origin_head:$origin_head}'
}

apply_drift_checkpoint() {
  local drift_json="$1"
  local status
  status="$(jq -r '.status' <<<"$drift_json")"

  update_state \
    --arg status "$status" \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson behind "$(jq -r '.behind // 0' <<<"$drift_json")" \
    --argjson ahead "$(jq -r '.ahead // 0' <<<"$drift_json")" \
    --arg upstream_head "$(jq -r '.upstream_head // ""' <<<"$drift_json")" \
    --arg origin_head "$(jq -r '.origin_head // ""' <<<"$drift_json")" \
    '.state="DRIFT_CHECK" | .state_code=10 | .drift={status:$status, behind:$behind, ahead:$ahead, upstream_head:$upstream_head, origin_head:$origin_head} | .updated_at_utc=$ts'

  case "$status" in
    up_to_date)
      update_state '.state="DONE" | .state_code=90 | .status="completed" | .reason_code="ALREADY_SYNCED" | .updated_at_utc="'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'"'
      ;;
    infra_error)
      update_state '.state="FAILED" | .state_code=99 | .status="failed" | .reason_code="DRIFT_CHECK_INFRA" | .updated_at_utc="'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'"'
      ;;
  esac
}

prepare_branch_checkpoint() {
  local exists="$1"
  local number="$2"
  local url="$3"
  local branch="$4"

  update_state \
    --arg exists "$exists" \
    --arg number "$number" \
    --arg url "$url" \
    --arg branch "$branch" \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '.state="PREPARE_BRANCH" | .state_code=20 | .target_branch=$branch | .existing_pr.exists=($exists=="true") | .existing_pr.number=$number | .existing_pr.url=$url | .updated_at_utc=$ts'
}

update_agent_attempt_checkpoint() {
  local attempt="$1"
  local exit_code="$2"
  local key="attempt${attempt}_exit_code"

  update_state \
    --arg key "$key" \
    --arg exit_code "$exit_code" \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '.state="AGENT_RUN" | .state_code=30 | .agent[$key]=$exit_code | .updated_at_utc=$ts'
}

contract_eval_pure() {
  # Pure assembler: given facts, produce the contract JSON. Unit-testable
  # without git/gh — used directly by tests. The I/O wrapper is contract_eval_json.
  local pr_exists="$1"            # "true"|"false"
  local had_existing_pr="$2"      # "true"|"false"
  local any_open_pr_count="$3"    # integer
  local matching_pr_count="$4"    # integer
  local upstream_in_main="$5"     # "true"|"false"
  local preflight_ok="${6:-skip}"      # "true"|"false"|"skip"
  local pr_body_audit_ok="${7:-skip}"  # "true"|"false"|"skip"

  local contract_ok="false"
  local reason="contract_unmet"
  local reason_code="CONTRACT_FAIL"

  # First gate: PR existence (or already-synced fallthrough).
  local pr_status="missing"
  if [ "$pr_exists" = "true" ]; then
    pr_status="present_matching"
  elif [ "$had_existing_pr" = "true" ] && [ "$any_open_pr_count" -gt 0 ]; then
    pr_status="present_existing"
  elif [ "$upstream_in_main" = "true" ] && [ "$any_open_pr_count" -eq 0 ]; then
    pr_status="up_to_date"
  fi

  case "$pr_status" in
    up_to_date)
      contract_ok="true"
      reason="already-synced path: origin/main already contains upstream/main and no upstream merge PR is open"
      reason_code="ALREADY_SYNCED"
      ;;
    present_matching|present_existing)
      # PR exists; evaluate secondary gates. Order matters — most actionable first.
      if [ "$preflight_ok" = "false" ]; then
        contract_ok="false"
        reason="preflight failed on target branch"
        reason_code="PREFLIGHT_FAIL"
      elif [ "$pr_body_audit_ok" = "false" ]; then
        contract_ok="false"
        reason="PR body missing required upstream/main..HEAD audit cadence"
        reason_code="PR_BODY_INCOMPLETE"
      else
        contract_ok="true"
        if [ "$pr_status" = "present_matching" ]; then
          reason="open PR exists for target branch"
        else
          reason="existing upstream PR still open after update path"
        fi
        reason_code=""
      fi
      ;;
    missing)
      contract_ok="false"
      reason="contract_unmet: no upstream merge PR open and origin/main lags upstream"
      reason_code="CONTRACT_FAIL"
      ;;
  esac

  jq -n \
    --arg contract_ok "$contract_ok" \
    --arg reason "$reason" \
    --arg reason_code "$reason_code" \
    --arg preflight_ok "$preflight_ok" \
    --arg pr_body_audit_ok "$pr_body_audit_ok" \
    --argjson matching_pr_count "$matching_pr_count" \
    --argjson any_open_upstream_pr_count "$any_open_pr_count" \
    '{
      contract_ok:$contract_ok,
      reason:$reason,
      reason_code:$reason_code,
      preflight_ok:$preflight_ok,
      pr_body_audit_ok:$pr_body_audit_ok,
      matching_pr_count:$matching_pr_count,
      any_open_upstream_pr_count:$any_open_upstream_pr_count
    }'
}

contract_eval_json() {
  local target_branch="$1"
  local had_existing_pr="$2"
  local preflight_ok="${3:-skip}"
  local pr_body_audit_ok="${4:-skip}"

  git fetch origin main >/dev/null
  ensure_upstream_remote
  git fetch upstream main >/dev/null

  local open_upstream_pr_json
  open_upstream_pr_json="$(gh pr list --state open --base main --json number,headRefName,url --jq '[.[] | select(.headRefName | startswith("merge/upstream-"))]')"

  local matching_pr_count any_open_upstream_pr_count
  matching_pr_count="$(jq --arg branch "$target_branch" '[.[] | select(.headRefName == $branch)] | length' <<<"$open_upstream_pr_json")"
  any_open_upstream_pr_count="$(jq 'length' <<<"$open_upstream_pr_json")"

  local pr_exists="false"
  [ "$matching_pr_count" -gt 0 ] && pr_exists="true"

  local upstream_in_main="false"
  if git merge-base --is-ancestor upstream/main origin/main 2>/dev/null; then
    upstream_in_main="true"
  fi

  contract_eval_pure \
    "$pr_exists" \
    "$had_existing_pr" \
    "$any_open_upstream_pr_count" \
    "$matching_pr_count" \
    "$upstream_in_main" \
    "$preflight_ok" \
    "$pr_body_audit_ok"
}

update_preflight_checkpoint() {
  local result="$1"        # "true"|"false"|"skip"
  local error_count="$2"   # integer or "-1" when unknown
  local reason="${3:-}"

  update_state \
    --arg result "$result" \
    --arg reason "$reason" \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson error_count "${error_count:--1}" \
    '.preflight = {ok:$result, error_count:$error_count, reason:$reason} | .updated_at_utc=$ts'
}

update_contract_checkpoint() {
  local phase="$1"
  local eval_json="$2"

  local ok reason reason_code
  ok="$(jq -r '.contract_ok' <<<"$eval_json")"
  reason="$(jq -r '.reason' <<<"$eval_json")"
  reason_code="$(jq -r '.reason_code' <<<"$eval_json")"

  if [ "$phase" = "after_attempt1" ]; then
    update_state \
      --arg ok "$ok" \
      --arg reason "$reason" \
      --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      '.state="CONTRACT_VERIFY" | .state_code=40 | .contract.after_attempt1_ok=$ok | .contract.after_attempt1_reason=$reason | .updated_at_utc=$ts'
  else
    if [ -n "$reason_code" ]; then
      update_state \
        --arg ok "$ok" \
        --arg reason "$reason" \
        --arg reason_code "$reason_code" \
        --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        '.state="CONTRACT_VERIFY" | .state_code=40 | .contract.final_ok=$ok | .contract.final_reason=$reason | .reason_code=$reason_code | .updated_at_utc=$ts'
    else
      update_state \
        --arg ok "$ok" \
        --arg reason "$reason" \
        --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        '.state="CONTRACT_VERIFY" | .state_code=40 | .contract.final_ok=$ok | .contract.final_reason=$reason | .updated_at_utc=$ts'
    fi

    if [ "$ok" = "true" ]; then
      update_state '.state="DONE" | .state_code=90 | .status="completed" | .updated_at_utc="'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'"'
    else
      update_state '.state="FAILED" | .state_code=99 | .status="failed" | .updated_at_utc="'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'"'
    fi
  fi
}

restore_state_from_artifact() {
  local run_id="$1"
  if [ -z "$run_id" ]; then
    return 0
  fi

  local artifact_name="upstream-merge-state-${run_id}"
  if gh run download "$run_id" -n "$artifact_name" -D /tmp/upstream-merge-restore >/dev/null 2>&1; then
    if [ -f /tmp/upstream-merge-restore/upstream-merge-state.json ]; then
      cp /tmp/upstream-merge-restore/upstream-merge-state.json "$STATE_FILE"
      return 0
    fi
  fi
  return 1
}

cmd="${1:-}"
case "$cmd" in
  drift-check-json)
    drift_check_json
    ;;
  apply-drift-checkpoint)
    apply_drift_checkpoint "$2"
    ;;
  prepare-branch-checkpoint)
    prepare_branch_checkpoint "$2" "$3" "$4" "$5"
    ;;
  update-agent-attempt)
    update_agent_attempt_checkpoint "$2" "$3"
    ;;
  contract-eval-json)
    contract_eval_json "$2" "$3" "${4:-skip}" "${5:-skip}"
    ;;
  contract-eval-pure)
    # Direct pure-assembler entrypoint: facts → JSON, no git/gh I/O. Used by
    # unit tests in scripts/upstream-merge-state_test.sh.
    contract_eval_pure "$2" "$3" "$4" "$5" "$6" "${7:-skip}" "${8:-skip}"
    ;;
  update-contract-checkpoint)
    update_contract_checkpoint "$2" "$3"
    ;;
  update-preflight-checkpoint)
    update_preflight_checkpoint "$2" "$3" "${4:-}"
    ;;
  restore-state)
    restore_state_from_artifact "${2:-}"
    ;;
  set-state-meta)
    set_state_meta "$2" "$3"
    ;;
  set-reason-code)
    set_reason_code "$2"
    ;;
  *)
    echo "unknown command: $cmd" >&2
    exit 2
    ;;
esac
