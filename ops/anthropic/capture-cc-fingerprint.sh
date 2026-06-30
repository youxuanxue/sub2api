#!/usr/bin/env bash
# Capture real Claude Code (cc0-here) TLS + optional HTTP fingerprints and diff
# against TokenKey repo constants. Deterministic diff via capture_cc_fingerprint.py.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PY="$SCRIPT_DIR/capture_cc_fingerprint.py"
MITM_ADDON="$SCRIPT_DIR/mitm_cc_http_headers.py"
HTTP_INVOKE="$SCRIPT_DIR/http_capture_invoke.sh"

COLLECTOR_ORIGIN="${TOKENKEY_TLS_PROFILE_COLLECTOR_ORIGIN:-https://tls.sub2api.org}"
COLLECTOR_API_ORIGIN="${TOKENKEY_TLS_PROFILE_COLLECTOR_API_ORIGIN:-https://tls.sub2api.org}"
MODEL="${TOKENKEY_CC_CAPTURE_MODEL:-claude-haiku-4-5-20251001}"
SONNET_MODEL="${TOKENKEY_CC_CAPTURE_SONNET_MODEL:-claude-sonnet-4-20250514}"
OUT_DIR="${TOKENKEY_CC_CAPTURE_OUT_DIR:-$REPO_ROOT/.tls_list}"
HTTP_CAPTURE="${TOKENKEY_CC_CAPTURE_HTTP:-0}"
MITM_PORT="${TOKENKEY_CC_CAPTURE_MITM_PORT:-11803}"
GOST_PORT="${CC0_GOST_HTTP_PORT:-11800}"

usage() {
  cat <<'EOF'
Usage:
  capture-cc-fingerprint.sh capture [--http] [--out-dir DIR]
  capture-cc-fingerprint.sh check env [--relax-desktop] [--skip-egress] [--json]
  capture-cc-fingerprint.sh check --bundle PATH
  capture-cc-fingerprint.sh check-tls --bundle PATH [--json]
  capture-cc-fingerprint.sh diff --bundle PATH [--check]
  capture-cc-fingerprint.sh show-baseline
  capture-cc-fingerprint.sh daily-hook   # sessionStart: TLS capture + drift PR (internal)
  capture-cc-fingerprint.sh geo-stego [--out-dir DIR] [--fix]  # plain claude+mitm body probe

Environment (capture):
  CC0_USER_OVERLAY          cc0 overlay (default: ~/.cache/cc0/claude-user-overlay)
  CC0_GOST_HTTP_PORT        gost HTTP listen port (default: 11800)
  TOKENKEY_CC_CAPTURE_HTTP  1 = also run mitm HTTP capture (or pass --http)
  CLAUDE_BIN                TLS 采集用 claude（默认 ~/.local/bin/claude）；HTTP 用 cc0-here
  CC0_HTTP_CLAUDE_BIN       覆盖 HTTP mitm 路径的 launcher（默认 cc0-here）
  TOKENKEY_CC_CAPTURE_MODEL / TOKENKEY_CC_CAPTURE_SONNET_MODEL
  TOKENKEY_CC_HTTP_CAPTURE_PROMPT  HTTP probe prompt (default: Say only the word PONG)

Requires: python3, jq, curl, claude CLI. HTTP capture also needs mitmdump + gost on CC0_GOST_HTTP_PORT
  (mitm upstream -> gost -> SOCKS). Uses http_capture_invoke.sh (plain claude + overlay OAuth).
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: required command not found: $1" >&2
    exit 1
  }
}

resolve_claude_bin() {
  if [[ -n "${CLAUDE_BIN:-}" ]]; then
    echo "$CLAUDE_BIN"
    return
  fi
  if [[ -x "${HOME}/.local/bin/claude" ]]; then
    echo "${HOME}/.local/bin/claude"
    return
  fi
  if command -v claude >/dev/null 2>&1; then
    echo "claude"
    return
  fi
  echo "error: set CLAUDE_BIN or install ~/.local/bin/claude" >&2
  exit 1
}

resolve_http_invoke() {
  if [[ -n "${CC0_HTTP_CLAUDE_BIN:-}" ]]; then
    echo "$CC0_HTTP_CLAUDE_BIN"
    return
  fi
  if [[ -x "$HTTP_INVOKE" ]]; then
    echo "$HTTP_INVOKE"
    return
  fi
  echo "error: HTTP capture invoke script missing: $HTTP_INVOKE" >&2
  exit 1
}

_cc0_port_open() {
  local host="$1" port="$2"
  python3 - "$host" "$port" <<'PY' 2>/dev/null
import socket, sys
host, port = sys.argv[1], int(sys.argv[2])
s = socket.socket()
s.settimeout(2)
try:
    s.connect((host, port))
except OSError:
    raise SystemExit(1)
finally:
    s.close()
PY
}

require_gost_for_http() {
  local host="${CC0_GOST_HTTP_HOST:-127.0.0.1}"
  local port="${CC0_GOST_HTTP_PORT:-11800}"
  if _cc0_port_open "$host" "$port"; then
    return 0
  fi
  echo "error: gost not listening on http://${host}:${port} (start cc0-here or cc0-gost)" >&2
  exit 1
}

run_tls_capture() {
  local work="$1"
  local token="$2"
  local claude_bin="$3"
  local overlay="${CC0_USER_OVERLAY:-$HOME/.cache/cc0/claude-user-overlay}"
  local settings="$work/settings.json"

  jq -n \
    --arg api_base "$COLLECTOR_ORIGIN:8090" \
    --arg token "$token" \
    '{
      env: {
        CLAUDE_CODE_API_BASE_URL: $api_base,
        ANTHROPIC_BASE_URL: $api_base,
        ANTHROPIC_API_KEY: $token,
        ANTHROPIC_AUTH_TOKEN: $token,
        TOKENKEY_TLS_PROFILE_CAPTURE_ACTIVE: "1",
        NODE_TLS_REJECT_UNAUTHORIZED: "0"
      },
      hooks: {SessionStart: []},
      permissions: {allow: [], deny: []}
    }' >"$settings"

  env -i \
    PATH="$PATH" \
    HOME="$work/home" \
    TERM="${TERM:-xterm}" \
    SHELL="${SHELL:-/bin/sh}" \
    CLAUDE_CONFIG_DIR="$overlay" \
    ANTHROPIC_CONFIG_DIR="$work/anthropic" \
    CLAUDE_CODE_API_BASE_URL="$COLLECTOR_ORIGIN:8090" \
    ANTHROPIC_BASE_URL="$COLLECTOR_ORIGIN:8090" \
    ANTHROPIC_API_KEY="$token" \
    ANTHROPIC_AUTH_TOKEN="$token" \
    TOKENKEY_TLS_PROFILE_CAPTURE_ACTIVE=1 \
    NODE_TLS_REJECT_UNAUTHORIZED=0 \
    "$claude_bin" --bare --setting-sources local --settings "$settings" \
    -p 'test' --model "$MODEL" --allowedTools '' --max-budget-usd 0.15 \
    >"$work/claude-tls.out" 2>"$work/claude-tls.err" || true  # preflight-allow: swallow

  curl -fsS --max-time 30 "$COLLECTOR_API_ORIGIN/api/latest?token=$token" >"$work/latest.json"
  local count
  count="$(jq -r '.count // 0' "$work/latest.json")"
  if [[ "${count:-0}" == "0" ]]; then
    echo "error: TLS collector recorded no fingerprint (token=$token)" >&2
    sed -n '1,5p' "$work/claude-tls.err" >&2 || true  # preflight-allow: swallow
    exit 1
  fi
  jq '.fingerprints[0]' "$work/latest.json" >"$work/tls-observed.json"
}

run_http_capture() {
  local work="$1"
  local http_log="$work/http.log"
  local ca="${HOME}/.mitmproxy/mitmproxy-ca-cert.pem"
  local mitm_pid=""
  local http_invoke
  http_invoke="$(resolve_http_invoke)"

  require_cmd mitmdump
  require_gost_for_http
  if [[ ! -f "$ca" ]]; then
    echo "error: mitm CA missing at $ca — run mitmdump once to generate" >&2
    exit 1
  fi
  if [[ ! -x "$http_invoke" ]]; then
    echo "error: HTTP invoke not executable: $http_invoke" >&2
    exit 1
  fi

  pkill -f "mitmdump.*${MITM_PORT}" 2>/dev/null || true  # preflight-allow: swallow
  sleep 1
  : >"$http_log"
  # Chain: claude -> mitm (log headers) -> gost (CC0_GOST_HTTP_PORT) -> SOCKS -> egress
  CC_CAPTURE_HTTP_LOG="$http_log" \
    mitmdump --mode "upstream:http://127.0.0.1:${GOST_PORT}" \
    -s "$MITM_ADDON" --listen-port "$MITM_PORT" \
    >"$work/mitm.out" 2>"$work/mitm.err" &
  mitm_pid=$!
  sleep 2

  run_one_http() {
    local model="$1"
    "$http_invoke" --mitm-port "$MITM_PORT" --model "$model" --work-dir "$work" || true  # preflight-allow: swallow
  }

  run_one_http "$MODEL"
  run_one_http "$SONNET_MODEL"
  sleep 2
  kill "$mitm_pid" 2>/dev/null || true  # preflight-allow: swallow
  wait "$mitm_pid" 2>/dev/null || true  # preflight-allow: swallow

  if ! grep -q '"anthropic_beta"' "$http_log" 2>/dev/null; then
    echo "error: HTTP mitm log empty — run 'check env', ensure gost+OAuth overlay, retry from neutral cwd" >&2
    sed -n '1,6p' "$work/claude-"*.err 2>/dev/null | sed 's/^/  /' >&2 || true  # preflight-allow: swallow
    exit 1
  fi
  echo "$http_log"
}

cmd_capture() {
  local with_http="$HTTP_CAPTURE"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --http) with_http=1; shift ;;
      --out-dir) OUT_DIR="$2"; shift 2 ;;
      *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
    esac
  done

  require_cmd python3
  require_cmd jq
  require_cmd curl
  local claude_bin
  claude_bin="$(resolve_claude_bin)"

  mkdir -p "$OUT_DIR"
  local stamp cc_version token work tls_capture http_log bundle_path
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  cc_version="$("$claude_bin" --version 2>/dev/null | awk '{print $1}' || true)"
  token="tk-cc-capture-${stamp}-$$"
  work="$(mktemp -d "${TMPDIR:-/tmp}/tk-cc-capture.XXXXXX")"
  cleanup_work() { rm -rf "$work"; }
  trap cleanup_work EXIT

  echo "cc_version=${cc_version:-unknown} claude_bin=$claude_bin"
  run_tls_capture "$work" "$token" "$claude_bin"

  http_log=""
  if [[ "$with_http" == "1" ]]; then
    http_log="$(run_http_capture "$work")" || exit 1
  fi

  tls_capture="$OUT_DIR/${stamp}-cc-capture.tls-observed.json"
  cp "$work/tls-observed.json" "$tls_capture"

  bundle_path="$OUT_DIR/${stamp}-cc-capture.bundle.json"
  bundle_args=(
    --tls-json "$tls_capture"
    --out "$bundle_path"
    --collector "$COLLECTOR_ORIGIN:8090"
  )
  if [[ -n "$http_log" ]]; then
    bundle_args+=(--http-log "$http_log")
  fi
  if [[ -n "${cc_version:-}" ]]; then
    bundle_args+=(--cc-version "$cc_version")
  fi
  python3 "$PY" bundle-from-artifacts "${bundle_args[@]}"

  echo "bundle=$bundle_path"
  python3 "$PY" diff --bundle "$bundle_path"
  python3 "$PY" check --bundle "$bundle_path"

  if [[ "${TOKENKEY_CC_CAPTURE_GEO:-1}" == "1" ]]; then
    local geo_fix=1
    [[ "${TOKENKEY_CC_CAPTURE_GEO_FIX:-1}" == "0" ]] && geo_fix=0
    echo "=== cc geo-stego align (auto after capture) ==="
    align_args=(run --out-dir "$OUT_DIR" --stamp "$stamp")
    [[ "$geo_fix" == "1" ]] && align_args+=(--fix)
    bash "$SCRIPT_DIR/cc_geo_stego_align.sh" "${align_args[@]}"
  fi

  trap - EXIT
  cleanup_work
}

cmd_check_env() {
  require_cmd python3
  # Honor the operator's egress/proxy config (CC0_EXPECT_EGRESS_IP, CC0_SOCKS5,
  # CC0_GOST_HTTP_*) instead of the python fallbacks. Matches capture-http-
  # comprehensive.sh; without this the check reports a stale default egress IP.
  if [[ -f "${HOME}/.config/cc0/env" ]]; then
    # shellcheck disable=SC1091
    source "${HOME}/.config/cc0/env"
  fi
  local args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --relax-desktop|--skip-egress|--json) args+=("$1"); shift ;;
      *) echo "unknown check env arg: $1" >&2; usage; exit 1 ;;
    esac
  done
  if ((${#args[@]})); then
    exec python3 "$PY" check-env "${args[@]}"
  fi
  exec python3 "$PY" check-env
}

main() {
  local cmd="${1:-}"
  shift || true
  case "$cmd" in
    capture) cmd_capture "$@" ;;
    check)
      if [[ "${1:-}" == "env" ]]; then
        shift
        cmd_check_env "$@"
        exit $?
      fi
      require_cmd python3
      exec python3 "$PY" check "$@"
      ;;
    check-tls)
      require_cmd python3
      exec python3 "$PY" check-tls "$@"
      ;;
    diff)
      require_cmd python3
      exec python3 "$PY" diff "$@"
      ;;
    show-baseline)
      require_cmd python3
      exec python3 "$PY" show-baseline "$@"
      ;;
    daily-hook)
      exec bash "$SCRIPT_DIR/cc_fingerprint_daily_hook.sh"
      ;;
    geo-stego)
      exec bash "$SCRIPT_DIR/cc_geo_stego_align.sh" run "$@"
      ;;
    -h|--help|"") usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 1 ;;
  esac
}

main "$@"
