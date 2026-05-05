#!/usr/bin/env bash

UPSTREAM_URL="${UPSTREAM_URL:-https://github.com/Wei-Shaw/sub2api.git}"

ensure_upstream_remote() {
  if ! git remote get-url upstream >/dev/null 2>&1; then
    if declare -F log >/dev/null 2>&1; then
      log "Adding upstream remote: $UPSTREAM_URL"
    fi
    git remote add upstream "$UPSTREAM_URL"
  fi
}

fetch_upstream_drift_refs() {
  ensure_upstream_remote
  if ! git fetch upstream main --quiet 2>/dev/null; then
    echo "ERROR: failed to fetch upstream/main" >&2
    return 2
  fi
  if ! git fetch origin main --quiet 2>/dev/null; then
    echo "ERROR: failed to fetch origin/main" >&2
    return 2
  fi
}

load_upstream_drift_snapshot() {
  TK_BEHIND=$(git rev-list --count origin/main..upstream/main)
  TK_AHEAD=$(git rev-list --count upstream/main..origin/main)
  UPSTREAM_HEAD=$(git rev-parse --short upstream/main)
  ORIGIN_HEAD=$(git rev-parse --short origin/main)
  export TK_BEHIND TK_AHEAD UPSTREAM_HEAD ORIGIN_HEAD
}

fetch_and_load_upstream_drift_snapshot() {
  fetch_upstream_drift_refs || return $?
  load_upstream_drift_snapshot
}
