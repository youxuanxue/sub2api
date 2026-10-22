#!/bin/bash
# TokenKey SOCKS proxy host watch — systemd unit + listen port.
# Feishu via TOKENKEY_HOST_ALERT_ENV_FILE (TOKENKEY_FEISHU_*).
#
# Owner: deploy/tencent/ — consumed by install-proxy-host-alerts.sh.
# Edit ONLY this file for proxy process/port alert logic on egress hosts.
set -euo pipefail

if [ "${1:-}" = "--selftest" ]; then
  fail=0
  # Port match: trailing :1080 on local address column
  if ! printf '%s\n' 'LISTEN 0 4096 0.0.0.0:1080 0.0.0.0:*' \
    | awk -v p=':1080' '$4 ~ p"$" {found=1} END{exit found?0:1}'; then
    echo "FAIL port awk should match :1080" >&2
    fail=1
  fi
  if printf '%s\n' 'LISTEN 0 4096 0.0.0.0:10800 0.0.0.0:*' \
    | awk -v p=':1080' '$4 ~ p"$" {found=1} END{exit found?0:1}'; then
    echo "FAIL port awk must not match :10800" >&2
    fail=1
  fi
  command -v curl >/dev/null || { echo "FAIL curl missing" >&2; fail=1; }
  command -v openssl >/dev/null || { echo "FAIL openssl missing" >&2; fail=1; }
  command -v python3 >/dev/null || { echo "FAIL python3 missing" >&2; fail=1; }
  [ "$fail" = "0" ] || exit 1
  echo "tokenkey-proxy-host-watch selftest: ok" >&2
  exit 0
fi

NODE="${TOKENKEY_PROXY_NODE_NAME:-$(hostname)}"
ALERT_ENV_FILE="${TOKENKEY_HOST_ALERT_ENV_FILE:-/opt/tokenkey/host-alerts.env}"
COOLDOWN="${TOKENKEY_PROXY_ALERT_COOLDOWN_SEC:-300}"
STAMP_DIR=/run/tokenkey-proxy-watch
mkdir -p "$STAMP_DIR"
WEBHOOK=""; SECRET=""
if [ -r "${ALERT_ENV_FILE}" ]; then
  WEBHOOK="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_URL=//p' "${ALERT_ENV_FILE}" | head -1)"
  SECRET="$(sed -n 's/^TOKENKEY_FEISHU_WEBHOOK_SECRET=//p' "${ALERT_ENV_FILE}" | head -1)"
fi

tk_feishu_alert() {
  local stamp="$1" text="$2" now last sign payload resp esc
  [ -n "${WEBHOOK}" ] || return 1
  now="$(date +%s)"; last=0
  [ -r "${stamp}" ] && last="$(cat "${stamp}" 2>/dev/null || echo 0)"
  [ "$((now - last))" -ge "${COOLDOWN}" ] || return 0
  esc="$(printf '%s' "$text" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read())[1:-1])')"
  if [ -n "${SECRET}" ]; then
    sign="$(printf '' | openssl dgst -sha256 -hmac "${now}"$'\n'"${SECRET}" -binary 2>/dev/null | base64)"
    payload="$(printf '{"timestamp":"%s","sign":"%s","msg_type":"text","content":{"text":"%s"}}' "${now}" "${sign}" "${esc}")"
  else
    payload="$(printf '{"msg_type":"text","content":{"text":"%s"}}' "${esc}")"
  fi
  resp="$(curl -sS -m 10 -X POST "${WEBHOOK}" -H 'Content-Type: application/json' -d "${payload}" 2>/dev/null || true)"
  case "${resp}" in
    *'"code":0'*) echo "${now}" > "${stamp}" || true; return 0 ;;
    *) return 1 ;;
  esac
}

tk_feishu_recover() {
  local active="$1" cooldown="$2" text="$3"
  if [ -r "${active}" ] || [ -r "${cooldown}" ]; then
    local now sign payload resp esc
    [ -n "${WEBHOOK}" ] || return 1
    now="$(date +%s)"
    esc="$(printf '%s' "$text" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read())[1:-1])')"
    if [ -n "${SECRET}" ]; then
      sign="$(printf '' | openssl dgst -sha256 -hmac "${now}"$'\n'"${SECRET}" -binary 2>/dev/null | base64)"
      payload="$(printf '{"timestamp":"%s","sign":"%s","msg_type":"text","content":{"text":"%s"}}' "${now}" "${sign}" "${esc}")"
    else
      payload="$(printf '{"msg_type":"text","content":{"text":"%s"}}' "${esc}")"
    fi
    resp="$(curl -sS -m 10 -X POST "${WEBHOOK}" -H 'Content-Type: application/json' -d "${payload}" 2>/dev/null || true)"
    case "${resp}" in
      *'"code":0'*) rm -f "${active}" "${cooldown}" 2>/dev/null || true; return 0 ;;
      *) return 1 ;;
    esac
  fi
  return 0
}

SVC="${TOKENKEY_PROXY_SERVICE:-tokenkey-proxy}"
PORT="${TOKENKEY_PROXY_PORT:-1080}"
ACTIVE="$STAMP_DIR/proxy-down-active"
COOL="$STAMP_DIR/proxy-down.stamp"

svc_ok=0
systemctl is-active --quiet "$SVC" 2>/dev/null && svc_ok=1
listen_ok=0
if ss -lnt 2>/dev/null | awk -v p=":$PORT" '$4 ~ p"$" {found=1} END{exit found?0:1}'; then
  listen_ok=1
fi

if [ "$svc_ok" -eq 1 ] && [ "$listen_ok" -eq 1 ]; then
  tk_feishu_recover "$ACTIVE" "$COOL" "✅ SOCKS 代理已恢复 ${NODE} — ${SVC} active 且 :${PORT} 在听。node=${NODE}"
  echo "proxy_watch ok svc=$SVC port=$PORT node=$NODE"
  exit 0
fi

reason=""
[ "$svc_ok" -eq 0 ] && reason="systemd ${SVC} not-active"
[ "$listen_ok" -eq 0 ] && reason="${reason:+$reason; }:${PORT} not-listening"
echo 1 >"$ACTIVE" 2>/dev/null || true
tk_feishu_alert "$COOL" "🔴 P0 SOCKS 代理异常 ${NODE} — ${reason}。Volc/DeepSeek 出口可能 dial timeout。node=${NODE} service=${SVC}"
echo "proxy_watch FAIL svc_ok=$svc_ok listen_ok=$listen_ok node=$NODE" >&2
exit 1
