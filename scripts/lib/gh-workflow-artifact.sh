#!/usr/bin/env bash

# Shared GitHub Actions workflow dispatch/poll/download helpers for TokenKey ops scripts.
# Caller must define err() and log() before sourcing this file.

require_tool() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    err "$tool is not installed (required). On Cursor Cloud Agent, install via dev-rules/templates/cloud-agent-bootstrap.sh."
    return 1
  fi
}

validate_gh_workflow_env() {
  local repo="$1"
  local ok=0
  require_tool gh || ok=1
  require_tool jq || ok=1

  if [ -z "${GH_TOKEN:-}" ]; then
    err "GH_TOKEN is not set. Add it in Cursor Dashboard → Cloud Agents → Secrets."
    err "  Required scopes on $repo: actions:write, actions:read, contents:read."
    ok=1
  fi

  return $ok
}

dispatch_workflow_and_download_artifact() {
  local repo="$1"
  local workflow="$2"
  local poll_timeout_s="$3"
  local artifact_name_template="$4"
  local out_dir="$5"
  shift 5

  mkdir -p "$out_dir"

  log "snapshotting last run id on $repo/$workflow"
  local prev_id
  prev_id=$(gh run list --workflow="$workflow" --repo "$repo" --limit 1 \
    --json databaseId --jq '.[0].databaseId // 0')
  log "previous run id: $prev_id"

  gh workflow run "$workflow" --repo "$repo" "$@"

  log "polling for new run id (timeout ${poll_timeout_s}s)"
  local deadline run_id
  deadline=$(( $(date +%s) + poll_timeout_s ))
  run_id="$prev_id"
  while [ "$run_id" = "$prev_id" ] || [ "$run_id" = "0" ]; do
    sleep 4
    run_id=$(gh run list --workflow="$workflow" --repo "$repo" --limit 1 \
      --json databaseId --jq '.[0].databaseId // 0')
    if [ "$(date +%s)" -ge "$deadline" ]; then
      err "timed out waiting for workflow to start (still seeing previous run id $prev_id)"
      return 2
    fi
  done
  log "new run id: $run_id"

  local watch_rc=0
  gh run watch "$run_id" --repo "$repo" --exit-status || watch_rc=$?

  local artifact_name
  artifact_name="${artifact_name_template//\{run_id\}/$run_id}"
  log "downloading artifact $artifact_name → $out_dir"
  if ! gh run download "$run_id" --repo "$repo" --name "$artifact_name" --dir "$out_dir"; then
    err "artifact download failed (run conclusion exit=$watch_rc). Check 'gh run view $run_id --repo $repo --log'."
    return 1
  fi

  GH_WORKFLOW_RUN_ID="$run_id"
  GH_WORKFLOW_WATCH_RC="$watch_rc"
  export GH_WORKFLOW_RUN_ID GH_WORKFLOW_WATCH_RC
  return 0
}
