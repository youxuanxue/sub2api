#!/usr/bin/env bash
# Read-only: emit live_host_state_verdict ENV lines for first-output timeout keys.
# Numbers live in first_output_timeout_ssot.py; missing keys → verdict drift.
# run-probe auto-ships /tmp/resolve-app-container.sh.
set -euo pipefail

# run-probe ships the canonical resolver to /tmp; tests may override the path.
# shellcheck disable=SC1091
. "${TK_RESOLVE_APP_CONTAINER_SH:-/tmp/resolve-app-container.sh}"

app_container="$(tk_resolve_app_container "${APP_CONTAINER:-auto}")" || {
  echo "app container unresolved" >&2
  exit 1
}

printf 'APPCONTAINER {"name":"%s"}\n' "$app_container"
img="$(${TK_DOCKER:-docker} inspect "$app_container" --format '{{.Config.Image}}' 2>/dev/null || true)"
printf 'RUNIMAGE {"image":"%s"}\n' "$img"

# Empty match must not fail the probe under pipefail — missing keys are
# reported as drift by live_host_state_verdict, not as transport failure.
# Broad GATEWAY_*FIRST_OUTPUT* keeps probe keys aligned with SSOT without a
# second hard-coded list.
${TK_DOCKER:-docker} exec "$app_container" printenv 2>/dev/null \
  | { grep -E '^GATEWAY_[^=]*FIRST_OUTPUT[^=]*=' || true; } \
  | while IFS='=' read -r k v; do
      [[ -n "${k:-}" ]] || continue
      printf 'ENV {"key":"%s","value":"%s"}\n' "$k" "$v"
    done
