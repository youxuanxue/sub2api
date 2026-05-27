#!/usr/bin/env bash
# Capture real Claude Code (cc0-here) TLS + optional HTTP fingerprints and diff
# against TokenKey repo constants. Deterministic diff via capture_cc_fingerprint.py.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PY="$SCRIPT_DIR/capture_cc_fingerprint.py"
MITM_ADDON="$SCRIPT_DIR/mitm_cc_http_headers.py"

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
  capture-cc-fingerprint.sh diff --bundle PATH [--check]
  capture-cc-fingerprint.sh check --bundle PATH
  capture-cc-fingerprint.sh show-baseline

Environment (capture):
  CC0_USER_OVERLAY          cc0 overlay (default: ~/.cache/cc0/claude-user-overlay)
  CC0_GOST_HTTP_PORT        gost HTTP listen port (default: 11800)
  TOKENKEY_CC_CAPTURE_HTTP  1 = also run mitm HTTP capture (or pass --http)
  CLAUDE_BIN                TLS 采集用 claude（默认 ~/.local/bin/claude）；HTTP 用 cc0-here
  CC0_HTTP_CLAUDE_BIN       覆盖 HTTP mitm 路径的 launcher（默认 cc0-here）
  TOKENKEY_CC_CAPTURE_MODEL / TOKENKEY_CC_CAPTURE_SONNET_MODEL

Requires: python3, jq, curl, claude CLI. HTTP capture also needs mitmdump + cc0 gost chain.
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

resolve_http_claude_bin() {
  if [[ -n "${CC0_HTTP_CLAUDE_BIN:-}" ]]; then
    echo "$CC0_HTTP_CLAUDE_BIN"
    return
  fi
  if command -v cc0-here >/dev/null 2>&1; then
    echo "cc0-here"
    return
  fi
  resolve_claude_bin
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
  local claude_bin
  claude_bin="$(resolve_http_claude_bin)"

  require_cmd mitmdump
  if [[ ! -f "$ca" ]]; then
    echo "error: mitm CA missing at $ca — run mitmdump once to generate" >&2
    exit 1
  fi

  pkill -f "mitmdump.*${MITM_PORT}" 2>/dev/null || true  # preflight-allow: swallow
  sleep 1
  : >"$http_log"
  CC_CAPTURE_HTTP_LOG="$http_log" \
    mitmdump --mode "upstream:http://127.0.0.1:${GOST_PORT}" \
    -s "$MITM_ADDON" --listen-port "$MITM_PORT" \
    >"$work/mitm.out" 2>"$work/mitm.err" &
  mitm_pid=$!
  sleep 2

  local proxy="http://127.0.0.1:${MITM_PORT}"
  local overlay="${CC0_USER_OVERLAY:-$HOME/.cache/cc0/claude-user-overlay}"

  run_one_http() {
    local model="$1"
    env -i \
      PATH="$PATH" HOME="$HOME" TERM="${TERM:-xterm}" SHELL="${SHELL:-/bin/sh}" \
      CLAUDE_CONFIG_DIR="$overlay" \
      HTTP_PROXY="$proxy" HTTPS_PROXY="$proxy" \
      http_proxy="$proxy" https_proxy="$proxy" \
      NO_PROXY="127.0.0.1,localhost" no_proxy="127.0.0.1,localhost" \
      NODE_EXTRA_CA_CERTS="$ca" \
      "$claude_bin" -p 'Reply OK' --model "$model" --max-budget-usd 0.15 --output-format text \
      </dev/null >"$work/claude-${model##*-}.out" 2>"$work/claude-${model##*-}.err" || true  # preflight-allow: swallow
  }

  run_one_http "$MODEL"
  run_one_http "$SONNET_MODEL"
  sleep 2
  kill "$mitm_pid" 2>/dev/null || true  # preflight-allow: swallow
  wait "$mitm_pid" 2>/dev/null || true  # preflight-allow: swallow

  if ! grep -q '"anthropic_beta"' "$http_log" 2>/dev/null; then
    echo "error: HTTP mitm log empty — check gost on port $GOST_PORT and cc0-here OAuth" >&2
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
    http_log="$(run_http_capture "$work")"
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
  trap - EXIT
  cleanup_work
}

main() {
  local cmd="${1:-}"
  shift || true
  case "$cmd" in
    capture) cmd_capture "$@" ;;
    diff)
      require_cmd python3
      exec python3 "$PY" diff "$@"
      ;;
    check)
      require_cmd python3
      exec python3 "$PY" check "$@"
      ;;
    show-baseline)
      require_cmd python3
      exec python3 "$PY" show-baseline "$@"
      ;;
    -h|--help|"") usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 1 ;;
  esac
}

main "$@"
