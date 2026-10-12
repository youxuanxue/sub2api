#!/usr/bin/env bash
# Read-only: emit live_host_state_verdict ENV lines for first-output timeout keys.
# Numbers live in first_output_timeout_ssot.py; missing keys → verdict drift.
# run-probe auto-ships /tmp/resolve-app-container.sh.
set -euo pipefail

# shellcheck disable=SC1091
. /tmp/resolve-app-container.sh

app_container="$(tk_resolve_app_container "${APP_CONTAINER:-auto}")" || {
  echo "app container unresolved" >&2
  exit 1
}

printf 'APPCONTAINER {"name":"%s"}\n' "$app_container"
img="$(${TK_DOCKER:-docker} inspect "$app_container" --format '{{.Config.Image}}' 2>/dev/null || true)"
printf 'RUNIMAGE {"image":"%s"}\n' "$img"

${TK_DOCKER:-docker} exec "$app_container" printenv 2>/dev/null \
  | grep -E '^(GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS|GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS|GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS|GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS)=' \
  | while IFS='=' read -r k v; do
      printf 'ENV {"key":"%s","value":"%s"}\n' "$k" "$v"
    done
