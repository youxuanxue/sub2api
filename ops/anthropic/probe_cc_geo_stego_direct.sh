#!/usr/bin/env bash
# Dynamic CC geo-stego probe: plain `claude` CLI + mitmdump (no cc0, no gost).
#
# Captures system[], messages[] <system-reminder>, and date_change attachments
# under a scenario matrix (TZ × ANTHROPIC_BASE_URL host). Use findings to extend
# gateway_request_tk_cc_geo_stego.go and tokenkey-cc-fingerprint-alignment skill §2.6.
#
# Optional mode probes:
#   TOKENKEY_CC_GEO_PERMISSION_MODE=plan|acceptEdits|auto|dontAsk|bypassPermissions|default
#   TOKENKEY_CC_GEO_SAFE_MODE=1
#   TOKENKEY_CC_GEO_BARE=1
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MITM_ADDON="$SCRIPT_DIR/probe_cc_geo_stego_mitm.py"
ANALYZE="$SCRIPT_DIR/probe_cc_geo_stego.py"
CLAUDE_BIN="${CLAUDE_BIN:-$HOME/.local/bin/claude}"
CA="${HOME}/.mitmproxy/mitmproxy-ca-cert.pem"

MODEL="${TOKENKEY_CC_GEO_MODEL:-claude-haiku-4-5-20251001}"
MITM_PORT="${TOKENKEY_CC_GEO_MITM_PORT:-11804}"
OUT_DIR="${TOKENKEY_CC_GEO_PROBE_OUT:-$REPO_ROOT/.tls_list/geo-stego-direct-$(date -u +%Y%m%dT%H%M%SZ)}"
PROMPT="${TOKENKEY_CC_HTTP_CAPTURE_PROMPT:-Say only the word PONG}"
PERMISSION_MODE="${TOKENKEY_CC_GEO_PERMISSION_MODE:-}"
SAFE_MODE="${TOKENKEY_CC_GEO_SAFE_MODE:-0}"
BARE_MODE="${TOKENKEY_CC_GEO_BARE:-0}"

case "$PERMISSION_MODE" in
  ""|"acceptEdits"|"auto"|"bypassPermissions"|"default"|"dontAsk"|"plan") ;;
  *)
    echo "invalid TOKENKEY_CC_GEO_PERMISSION_MODE=$PERMISSION_MODE" >&2
    exit 1
    ;;
esac
case "$SAFE_MODE" in
  0|1) ;;
  *) echo "invalid TOKENKEY_CC_GEO_SAFE_MODE=$SAFE_MODE" >&2; exit 1 ;;
esac
case "$BARE_MODE" in
  0|1) ;;
  *) echo "invalid TOKENKEY_CC_GEO_BARE=$BARE_MODE" >&2; exit 1 ;;
esac

require_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "need $1" >&2; exit 1; }; }
require_cmd mitmdump
require_cmd python3
[[ -x "$CLAUDE_BIN" ]] || { echo "need CLAUDE_BIN=$CLAUDE_BIN" >&2; exit 1; }
[[ -f "$CA" ]] || { echo "need mitm CA at $CA (mitmproxy --version once)" >&2; exit 1; }

mkdir -p "$OUT_DIR"
LOG="$OUT_DIR/capture.jsonl"
: >"$LOG"

# Neutral config dir: do not inherit ~/.claude/settings.json ANTHROPIC_BASE_URL (tokenkey.dev).
GEO_CONFIG_DIR="$(mktemp -d "${TMPDIR:-/tmp}/tk-cc-geo-config.XXXXXX")"
echo '{}' >"$GEO_CONFIG_DIR/settings.json"
cleanup_geo_config() { rm -rf "$GEO_CONFIG_DIR"; }
trap cleanup_geo_config EXIT

TOKENKEY_AUTH_TOKEN=""
if [[ -f "${HOME}/.claude/settings.json" ]]; then
  TOKENKEY_AUTH_TOKEN="$(python3 - <<'PY'
import json, os
p = os.path.expanduser("~/.claude/settings.json")
try:
    print(json.load(open(p)).get("env", {}).get("ANTHROPIC_AUTH_TOKEN", "") or "")
except Exception:
    print("")
PY
)"
fi
CC0_OVERLAY="${CC0_USER_OVERLAY:-$HOME/.cache/cc0/claude-user-overlay}"

invoke_claude() {
  local tz="$1" base_url="$2"
  local out err config_dir auth_token="" api_key_token=""
  local -a claude_args=("$CLAUDE_BIN")
  local -a env_args=()
  out="$(mktemp "${TMPDIR:-/tmp}/geo-claude.out.XXXXXX")"
  err="$(mktemp "${TMPDIR:-/tmp}/geo-claude.err.XXXXXX")"
  config_dir="$GEO_CONFIG_DIR"
  if [[ "$base_url" == *"api.anthropic.com"* ]]; then
    config_dir="$CC0_OVERLAY"
  elif [[ "$base_url" == *"tokenkey.dev"* && -n "$TOKENKEY_AUTH_TOKEN" ]]; then
    auth_token="$TOKENKEY_AUTH_TOKEN"
  fi
  if [[ -n "$PERMISSION_MODE" ]]; then
    claude_args+=(--permission-mode "$PERMISSION_MODE")
  fi
  if [[ "$SAFE_MODE" == "1" ]]; then
    claude_args+=(--safe-mode)
  fi
  if [[ "$BARE_MODE" == "1" ]]; then
    claude_args+=(--bare)
    api_key_token="$auth_token"
  fi
  env_args=(
    "HOME=$HOME"
    "USER=${USER:-$(id -un)}"
    "LOGNAME=${LOGNAME:-$(id -un)}"
    "PATH=$PATH"
    "SHELL=${SHELL:-/bin/sh}"
    "TERM=${TERM:-xterm-256color}"
    "LANG=${LANG:-en_US.UTF-8}"
    "TZ=$tz"
    "CLAUDE_CONFIG_DIR=$config_dir"
    "HTTP_PROXY=http://127.0.0.1:${MITM_PORT}"
    "HTTPS_PROXY=http://127.0.0.1:${MITM_PORT}"
    "http_proxy=http://127.0.0.1:${MITM_PORT}"
    "https_proxy=http://127.0.0.1:${MITM_PORT}"
    "NO_PROXY="
    "no_proxy="
    "ANTHROPIC_BASE_URL=$base_url"
    "ANTHROPIC_AUTH_TOKEN=$auth_token"
    "NODE_EXTRA_CA_CERTS=$CA"
    "NODE_TLS_REJECT_UNAUTHORIZED=0"
    "CLAUDE_CODE_REMOTE_SEND_KEEPALIVES=true"
  )
  if [[ -n "$api_key_token" ]]; then
    env_args+=("ANTHROPIC_API_KEY=$api_key_token")
  fi
  (
    cd /tmp || exit 1
    # Neutral cwd: avoid sub2api SessionStart short-circuit.
    # HTTPS via mitm; ANTHROPIC_BASE_URL hostname drives CC geo-stego branch.
    env -i "${env_args[@]}" \
      "${claude_args[@]}" \
      -p "$PROMPT" \
      --model "$MODEL" \
      --max-budget-usd 0.15 \
      --output-format text \
      </dev/null >"$out" 2>"$err"
  ) || true
  echo "--- tz=$tz base_url=$base_url ---" >&2
  sed -n '1,12p' "$err" >&2 || true
  sed -n '1,3p' "$out" >&2 || true
  rm -f "$out" "$err"
}

run_scenario() {
  local name="$1" tz="$2" base_url="$3"
  local mitm_pid=""

  echo "[probe-direct] scenario=$name tz=$tz base_url=$base_url" >&2
  pkill -f "mitmdump.*${MITM_PORT}" 2>/dev/null || true
  sleep 1

  CC_GEO_PROBE_LOG="$LOG" \
  CC_GEO_PROBE_SCENARIO="$name" \
  CC_GEO_PROBE_TZ="$tz" \
  CC_GEO_PROBE_PROXY="mitm" \
  CC_GEO_PROBE_BASE_URL="$base_url" \
  CC_GEO_PROBE_HOSTS="tokenkey.dev,anthropic.com,aicodemirror.com" \
    mitmdump --listen-port "$MITM_PORT" \
      -s "$MITM_ADDON" \
      >"$OUT_DIR/mitm-${name}.out" 2>"$OUT_DIR/mitm-${name}.err" &
  mitm_pid=$!
  sleep 2

  invoke_claude "$tz" "$base_url"
  sleep 2
  kill "$mitm_pid" 2>/dev/null || true
  wait "$mitm_pid" 2>/dev/null || true
}

echo "claude=$("$CLAUDE_BIN" --version 2>/dev/null || true)" >&2
echo "model=$MODEL permission_mode=${PERMISSION_MODE:-<default>} safe_mode=$SAFE_MODE bare=$BARE_MODE out_dir=$OUT_DIR" >&2

# Default matrix — override with TOKENKEY_CC_GEO_SCENARIOS="name|tz|base_url,..."
if [[ -n "${TOKENKEY_CC_GEO_SCENARIOS:-}" ]]; then
  IFS=',' read -r -a _scenarios <<<"$TOKENKEY_CC_GEO_SCENARIOS"
  for spec in "${_scenarios[@]}"; do
    IFS='|' read -r name tz base_url <<<"$spec"
    run_scenario "$name" "$tz" "$base_url"
  done
else
  run_scenario "shanghai_tokenkey" "Asia/Shanghai" "https://api.tokenkey.dev"
  run_scenario "utc_tokenkey" "UTC" "https://api.tokenkey.dev"
  run_scenario "shanghai_official" "Asia/Shanghai" "https://api.anthropic.com"
  run_scenario "shanghai_mirror" "Asia/Shanghai" "https://api.aicodemirror.com"
fi

python3 "$ANALYZE" "$LOG" | tee "$OUT_DIR/report.txt"
python3 "$ANALYZE" "$LOG" --json >"$OUT_DIR/report.json"

echo "out_dir=$OUT_DIR"
echo "TokenKey normalize target: ASCII U+0027 + YYYY-MM-DD in gateway_request_tk_cc_geo_stego.go" >&2
