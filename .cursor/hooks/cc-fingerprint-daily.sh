#!/usr/bin/env bash
# Cursor sessionStart: run daily cc TLS drift workflow in background (at most once per UTC day).
set -euo pipefail

REPO_ROOT="$(pwd)"
HOOK_SH="$REPO_ROOT/ops/anthropic/cc_fingerprint_daily_hook.sh"

if [[ ! -x "$HOOK_SH" ]]; then
  exit 0
fi

# Consume sessionStart stdin (required by hook protocol).
cat >/dev/null || true  # preflight-allow: swallow

# Create the log dir before redirecting into it: on a fresh checkout .tls_list/
# is gitignored and absent, and the only other creator is the daily hook itself
# (which this redirect launches) — so without this the append fails, the
# backgrounded hook never starts, and the automation can never bootstrap.
mkdir -p "${REPO_ROOT}/.tls_list"

nohup bash "$HOOK_SH" >>"${REPO_ROOT}/.tls_list/cc-fingerprint-daily-hook.log" 2>&1 &
exit 0
