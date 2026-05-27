#!/usr/bin/env bash
# Cursor sessionStart: run daily cc TLS drift workflow in background (at most once per UTC day).
set -euo pipefail

REPO_ROOT="$(pwd)"
HOOK_SH="$REPO_ROOT/ops/anthropic/cc_fingerprint_daily_hook.sh"

if [[ ! -x "$HOOK_SH" ]]; then
  exit 0
fi

# Consume sessionStart stdin (required by hook protocol).
cat >/dev/null || true

nohup bash "$HOOK_SH" >>"${REPO_ROOT}/.tls_list/cc-fingerprint-daily-hook.log" 2>&1 &
exit 0
