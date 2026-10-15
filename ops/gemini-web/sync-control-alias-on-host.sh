#!/usr/bin/env bash
# Ensure Docker DNS name `tokenkey` reaches the active blue/green app.
#
# Gemini Web Workers ship with GEMINI_WEB_CONTROL_URL=http://tokenkey:8080/api/v1.
# After blue/green cutover the app containers are tokenkey-blue / tokenkey-green only,
# so Workers lose the control plane (session_poll_failed, /readyz not_ready).
#
# This script keeps a tiny TCP proxy container literally named `tokenkey` that
# forwards :8080 to the active color. Legacy single-container hosts (real app
# still named tokenkey) are left alone.
#
# Usage (on host, or via run-probe):
#   bash ops/gemini-web/sync-control-alias-on-host.sh
#   TK_ACTIVE_COLOR=blue bash ops/gemini-web/sync-control-alias-on-host.sh
set -euo pipefail

NETWORK="${TK_NETWORK:-tokenkey_tokenkey-network}"
ACTIVE_FILE="${TK_ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"
PROXY_NAME="${TK_CONTROL_PROXY_NAME:-tokenkey}"
SOCAT_IMAGE="${TK_CONTROL_PROXY_IMAGE:-alpine/socat:1.8.0.0}"
DOCKER="${TK_DOCKER:-docker}"

log() { printf 'gemini-web-control-alias: %s\n' "$*"; }

resolve_color() {
  if [[ -n "${TK_ACTIVE_COLOR:-}" ]]; then
    printf '%s\n' "${TK_ACTIVE_COLOR}"
    return 0
  fi
  if [[ -r "${ACTIVE_FILE}" ]]; then
    sed -n '1p' "${ACTIVE_FILE}" | tr -d '[:space:]'
    return 0
  fi
  # Fall back to whichever color Caddy currently reverse_proxies.
  local caddy="${TK_CADDYFILE:-/var/lib/tokenkey/caddy/Caddyfile}"
  if [[ -r "${caddy}" ]]; then
    awk '
      $1 == "reverse_proxy" && $2 ~ /^tokenkey-(blue|green):8080$/ {
        split($2, a, /[:-]/); print a[2]; exit
      }
    ' "${caddy}"
    return 0
  fi
  return 1
}

if ! color="$(resolve_color)" || [[ ! "${color}" =~ ^(blue|green)$ ]]; then
  log "ERROR: could not resolve active color (set TK_ACTIVE_COLOR=blue|green)"
  exit 1
fi
target="tokenkey-${color}"

if ! "$DOCKER" inspect "${target}" >/dev/null 2>&1; then
  log "ERROR: active app container ${target} is not present"
  exit 1
fi

if "$DOCKER" inspect "${PROXY_NAME}" >/dev/null 2>&1; then
  image="$("$DOCKER" inspect "${PROXY_NAME}" --format '{{.Config.Image}}' 2>/dev/null || true)"
  # Real Stage0 app image — legacy single-container topology; do not replace.
  if [[ "${image}" == *sub2api* && "${image}" != *socat* && "${image}" != *gemini-web* ]]; then
    log "OK legacy app container already named ${PROXY_NAME} (image=${image})"
    exit 0
  fi
  desired="TCP:${target}:8080"
  current="$("$DOCKER" inspect "${PROXY_NAME}" --format '{{json .Config.Cmd}}' 2>/dev/null || true)"
  running="$("$DOCKER" inspect "${PROXY_NAME}" --format '{{.State.Running}}' 2>/dev/null || true)"
  if [[ "${running}" == "true" && "${current}" == *"${desired}"* ]]; then
    log "OK ${PROXY_NAME} already proxies ${desired}"
    exit 0
  fi
  log "recreating ${PROXY_NAME} -> ${desired}"
  "$DOCKER" rm -f "${PROXY_NAME}" >/dev/null
fi

if ! "$DOCKER" image inspect "${SOCAT_IMAGE}" >/dev/null 2>&1; then
  log "pulling ${SOCAT_IMAGE}"
  "$DOCKER" pull "${SOCAT_IMAGE}" >/dev/null
fi

log "starting ${PROXY_NAME} -> TCP:${target}:8080 on ${NETWORK}"
"$DOCKER" run -d \
  --name "${PROXY_NAME}" \
  --restart unless-stopped \
  --network "${NETWORK}" \
  --user 65534:65534 \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --memory 32m --memory-swap 32m --cpus 0.1 --pids-limit 32 \
  "${SOCAT_IMAGE}" \
  TCP-LISTEN:8080,fork,reuseaddr "TCP:${target}:8080" >/dev/null

# Prove Docker DNS name `tokenkey` answers on :8080 (Worker control URL).
if [[ "$("$DOCKER" inspect tokenkey-gemini-web --format '{{.State.Running}}' 2>/dev/null || true)" == true ]]; then
  "$DOCKER" exec tokenkey-gemini-web python3 -c "
import socket
s = socket.create_connection(('${PROXY_NAME}', 8080), 5)
s.close()
" >/dev/null
else
  # Fresh-host startup: the worker needs this alias to pass its startup check.
  # Verify the real HTTP path from the already healthy gateway first.
  "$DOCKER" exec "${target}" wget -q -T 5 -O /dev/null "http://${PROXY_NAME}:8080/health"
fi
log "OK ${PROXY_NAME} -> ${target}:8080"
echo "tk_gemini_web_control_alias: OK color=${color} target=${target}"
