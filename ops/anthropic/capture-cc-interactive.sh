#!/usr/bin/env bash
# Capture interactive Claude Code REPL fingerprints (ingress cohort: external, cli).
# TLS via collector (-p/--bare) + HTTP via PTY REPL -> mitm -> user's configured API base.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PY="$SCRIPT_DIR/capture_cc_fingerprint.py"
MITM_ADDON="$SCRIPT_DIR/mitm_cc_http_headers.py"
EXPECT_SCRIPT="$SCRIPT_DIR/capture_interactive_repl.exp"

COLLECTOR_ORIGIN="${TOKENKEY_TLS_PROFILE_COLLECTOR_ORIGIN:-https://tls.sub2api.org}"
COLLECTOR_API_ORIGIN="${TOKENKEY_TLS_PROFILE_COLLECTOR_API_ORIGIN:-https://tls.sub2api.org}"
MODEL="${TOKENKEY_CC_CAPTURE_MODEL:-claude-haiku-4-5-20251001}"
SONNET_MODEL="${TOKENKEY_CC_CAPTURE_SONNET_MODEL:-claude-sonnet-4-6}"
OUT_DIR="${TOKENKEY_CC_CAPTURE_OUT_DIR:-$REPO_ROOT/.tls_list}"
MITM_PORT="${TOKENKEY_CC_CAPTURE_MITM_PORT:-11820}"
GOST_PORT="${CC0_GOST_HTTP_PORT:-11800}"
PROMPT="${TOKENKEY_CC_HTTP_CAPTURE_PROMPT:-Say only the word PONG}"

usage() {
  cat <<'EOF'
Usage:
  capture-cc-interactive.sh capture [--out-dir DIR]
  capture-cc-interactive.sh check env [--relax-desktop]

Captures the prod-dominant interactive REPL cohort:
  User-Agent: claude-cli/<version> (external, cli)
  system[0]: You are Claude Code, Anthropic's official CLI for Claude.

Default HTTP path keeps the user's ANTHROPIC_BASE_URL (often api.tokenkey.dev)
and only steers traffic through mitm via HTTP(S)_PROXY override. Set
TOKENKEY_CC_CAPTURE_DIRECT_ANTHROPIC=1 to require gost upstream to Anthropic.
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
  command -v claude
}

require_gost_for_http() {
  if [[ "${TOKENKEY_CC_CAPTURE_DIRECT_ANTHROPIC:-0}" != "1" ]]; then
    return 0
  fi
  python3 - "$GOST_PORT" <<'PY' || {
import socket, sys
port = int(sys.argv[1])
s = socket.socket()
s.settimeout(2)
try:
    s.connect(("127.0.0.1", port))
except OSError:
    raise SystemExit(1)
finally:
    s.close()
PY
    echo "error: gost not listening on 127.0.0.1:${GOST_PORT}" >&2
    exit 1
  }
}

oauth_logged_in() {
  local config_dir="$1"
  local claude_bin="$2"
  local status
  status="$(CLAUDE_CONFIG_DIR="$config_dir" "$claude_bin" auth status 2>/dev/null || true)"
  echo "$status" | grep -q '"loggedIn": true'
}

resolve_config_dir() {
  local claude_bin="$1"
  if [[ -n "${TOKENKEY_CC_CAPTURE_CONFIG_DIR:-}" ]]; then
    echo "$TOKENKEY_CC_CAPTURE_CONFIG_DIR"
    return
  fi
  local overlay="${CC0_USER_OVERLAY:-$HOME/.cache/cc0/claude-user-overlay}"
  if [[ -d "$overlay" ]] && oauth_logged_in "$overlay" "$claude_bin"; then
    echo "$overlay"
    return
  fi
  if oauth_logged_in "$HOME/.claude" "$claude_bin"; then
    echo "$HOME/.claude"
    return
  fi
  echo "error: no OAuth session — run: claude login (or CLAUDE_CONFIG_DIR=\$CC0_USER_OVERLAY claude login)" >&2
  exit 1
}

write_capture_settings() {
  local work="$1"
  local mitm_port="$2"
  local config_dir="$3"
  python3 <<PY
import json, pathlib
work = pathlib.Path("$work")
config_dir = pathlib.Path("$config_dir")
path = work / "settings-override.json"
user_settings = {}
user_path = config_dir / "settings.json"
if user_path.is_file():
    user_settings = json.loads(user_path.read_text(encoding="utf-8"))
env = dict(user_settings.get("env") or {})
env["HTTP_PROXY"] = f"http://127.0.0.1:{int('$mitm_port')}"
env["HTTPS_PROXY"] = f"http://127.0.0.1:{int('$mitm_port')}"
payload = {
    **user_settings,
    "env": env,
    "permissions": {"defaultMode": "bypassPermissions"},
    "skipWebFetchPreflight": True,
    "skipDangerousModePermissionPrompt": True,
}
path.write_text(json.dumps(payload, indent=2) + "\n")
print(path)
PY
}

run_tls_capture() {
  local work="$1"
  local token="$2"
  local claude_bin="$3"
  local overlay="${CC0_USER_OVERLAY:-$HOME/.cache/cc0/claude-user-overlay}"
  local settings="$work/settings-tls.json"

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
    >"$work/claude-tls.out" 2>"$work/claude-tls.err" || true

  curl -fsS --max-time 30 "$COLLECTOR_API_ORIGIN/api/latest?token=$token" >"$work/latest.json"
  local count
  count="$(jq -r '.count // 0' "$work/latest.json")"
  if [[ "${count:-0}" == "0" ]]; then
    echo "error: TLS collector recorded no fingerprint (token=$token)" >&2
    sed -n '1,8p' "$work/claude-tls.err" >&2 || true
    exit 1
  fi
  jq '.fingerprints[0]' "$work/latest.json" >"$work/tls-observed.json"
}

run_interactive_http_capture() {
  local work="$1"
  local config_dir="$2"
  local claude_bin="$3"
  local http_log="$work/http-interactive.log"
  local ca="${HOME}/.mitmproxy/mitmproxy-ca-cert.pem"
  local mitm_pid=""
  local mitm_mode="regular"

  require_cmd mitmdump
  require_cmd expect
  require_gost_for_http
  if [[ ! -f "$ca" ]]; then
    echo "error: mitm CA missing at $ca — run mitmdump once to generate" >&2
    exit 1
  fi
  if [[ ! -x "$EXPECT_SCRIPT" ]]; then
    echo "error: missing expect script: $EXPECT_SCRIPT" >&2
    exit 1
  fi
  if [[ "${TOKENKEY_CC_CAPTURE_DIRECT_ANTHROPIC:-0}" == "1" ]]; then
    mitm_mode="upstream:http://127.0.0.1:${GOST_PORT}"
  fi

  write_capture_settings "$work" "$MITM_PORT" "$config_dir" >/dev/null

  pkill -f "mitmdump.*${MITM_PORT}" 2>/dev/null || true
  sleep 1
  : >"$http_log"
  CC_CAPTURE_HTTP_LOG="$http_log" \
    mitmdump --mode "$mitm_mode" \
    -s "$MITM_ADDON" --listen-port "$MITM_PORT" \
    >"$work/mitm.out" 2>"$work/mitm.err" &
  mitm_pid=$!
  sleep 2

  run_one() {
    local model="$1"
    export TK_CAPTURE_WORK="$work"
    export TK_CAPTURE_MODEL="$model"
    export TK_CAPTURE_PROMPT="$PROMPT"
    export TK_CAPTURE_CONFIG_DIR="$config_dir"
    export TK_CAPTURE_CLAUDE_BIN="$claude_bin"
    expect "$EXPECT_SCRIPT" >"$work/expect-${model##*-}.out" 2>"$work/expect-${model##*-}.err" || true
    sleep 2
  }

  run_one "$MODEL"
  if [[ "${TOKENKEY_CC_CAPTURE_INTERACTIVE_SONNET:-0}" == "1" ]]; then
    run_one "$SONNET_MODEL"
  fi
  sleep 2
  kill "$mitm_pid" 2>/dev/null || true
  wait "$mitm_pid" 2>/dev/null || true

  if ! grep -q '"user_agent"' "$http_log" 2>/dev/null; then
    echo "error: interactive HTTP mitm log empty — check OAuth, mitm, expect output in $work" >&2
    sed -n '1,20p' "$work"/expect-*.err 2>/dev/null | sed 's/^/  /' || true
    exit 1
  fi
  echo "$http_log"
}

validate_interactive_log() {
  local http_log="$1"
  python3 - "$PY" "$http_log" <<'PY'
import importlib.util, sys
from pathlib import Path
mod_path = Path(sys.argv[1])
http_log = Path(sys.argv[2])
spec = importlib.util.spec_from_file_location("capture_cc_fingerprint", mod_path)
mod = importlib.util.module_from_spec(spec)
assert spec and spec.loader
sys.modules[spec.name] = mod
spec.loader.exec_module(mod)
summary = mod.validate_interactive_http_log(http_log)
print(f"ok interactive cohort: {summary['request_count']} requests, UA={summary['user_agent']}")
PY
}

cmd_capture() {
  require_cmd python3
  require_cmd jq
  require_cmd curl
  local claude_bin
  claude_bin="$(resolve_claude_bin)"
  local config_dir
  config_dir="$(resolve_config_dir "$claude_bin")"

  mkdir -p "$OUT_DIR"
  local stamp cc_version token work tls_capture http_log bundle_path
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  cc_version="$("$claude_bin" --version 2>/dev/null | awk '{print $1}' || true)"
  token="tk-cc-interactive-${stamp}-$$"
  work="$(mktemp -d "${TMPDIR:-/tmp}/tk-cc-interactive.XXXXXX")"
  cleanup_work() { rm -rf "$work"; }
  trap cleanup_work EXIT

  echo "cc_version=${cc_version:-unknown} claude_bin=$claude_bin config_dir=$config_dir"
  run_tls_capture "$work" "$token" "$claude_bin"
  http_log="$(run_interactive_http_capture "$work" "$config_dir" "$claude_bin")"
  validate_interactive_log "$http_log"

  tls_capture="$OUT_DIR/${stamp}-cc-interactive.tls-observed.json"
  cp "$work/tls-observed.json" "$tls_capture"

  bundle_path="$OUT_DIR/${stamp}-cc-interactive.bundle.json"
  bundle_args=(
    --tls-json "$tls_capture"
    --http-log "$http_log"
    --out "$bundle_path"
    --collector "$COLLECTOR_ORIGIN:8090"
  )
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

cmd_check_env() {
  bash "$SCRIPT_DIR/capture-cc-fingerprint.sh" check env "$@"
  require_cmd expect
}

main() {
  local cmd="${1:-}"
  shift || true
  case "$cmd" in
    capture) cmd_capture "$@" ;;
    check)
      sub="${1:-}"
      shift || true
      if [[ "$sub" == "env" ]]; then
        cmd_check_env "$@"
      else
        usage >&2
        exit 1
      fi
      ;;
    -h|--help|"") usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 1 ;;
  esac
}

main "$@"
