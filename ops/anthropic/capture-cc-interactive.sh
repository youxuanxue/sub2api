#!/usr/bin/env bash
# Capture interactive Claude Code REPL fingerprints (ingress cohort: external, cli).
# TLS: collector redirect (default) with passive-pcap fallback (Kiro-style tcpdump).
# HTTP: PTY REPL -> mitm -> user's configured API base (no gost by default).
set -euo pipefail

export PATH="/opt/homebrew/bin:/usr/local/bin:${PATH}"

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
CC_TLS_HOSTS_DEFAULT="api.anthropic.com"
CC_TLS_HOSTS="${TOKENKEY_CC_CAPTURE_TLS_HOSTS:-$CC_TLS_HOSTS_DEFAULT}"
PCAP_IFACE="${TOKENKEY_CC_CAPTURE_TLS_IFACE:-}"
PCAP_PROXY_PORT="${TOKENKEY_CC_CAPTURE_TLS_PROXY_PORT:-}"
PCAP_SECONDS="${TOKENKEY_CC_CAPTURE_TLS_SECONDS:-45}"

usage() {
  cat <<'EOF'
Usage:
  capture-cc-interactive.sh capture [options] [--out-dir DIR]
  capture-cc-interactive.sh check env [--relax-desktop]

Captures the prod-dominant interactive REPL cohort:
  User-Agent: claude-cli/<version> (external, cli)
  system[0]: You are Claude Code, Anthropic's official CLI for Claude.

TLS capture order (unless --http-only):
  1. tls.sub2api.org collector (claude --bare redirect)
  2. passive pcap fallback — tcpdump on loopback during interactive mitm (same REPL session)

HTTP path keeps the user's ANTHROPIC_BASE_URL and steers via HTTP(S)_PROXY mitm.
Set TOKENKEY_CC_CAPTURE_DIRECT_ANTHROPIC=1 to require gost upstream to Anthropic.

Options:
  --http-only              Skip TLS; seed bundle TLS from baseline (HTTP-only drift).
  --tls-pcap               Force passive pcap during interactive mitm (skip collector).
  --tls-pcap-standalone    Use direct/bare Claude TLS trigger instead of mitm loopback pcap.
  --no-tls-pcap-fallback   Do not auto-fallback to pcap when collector fails.
  --tls-pcap-iface IFACE   tcpdump interface (default: lo0 with --tls-pcap-proxy-port, else en0).
  --tls-pcap-proxy-port N  Capture cleartext ClientHello on loopback to local proxy port.
  --tls-pcap-seconds N     tcpdump window (default: 45).

Env:
  TOKENKEY_CC_CAPTURE_TLS_HOSTS   SNI hosts for tshark filter (default: api.anthropic.com)
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: required command not found: $1" >&2
    exit 1
  }
}

require_sudo_for_pcap() {
  # Non-interactive sessions cannot answer a sudo password prompt.
  if [[ ! -t 0 ]]; then
    if ! sudo -n true 2>/dev/null; then
      echo "error: passive pcap needs interactive sudo for tcpdump" >&2
      echo "  Run this capture in your terminal (not a non-interactive agent) so macOS can prompt for your password." >&2
      echo "  Or pre-authorize once: sudo -v" >&2
      echo "  Optional: brew install --cask wireshark-chmodbpf  # reduces capture friction on macOS" >&2
      return 1
    fi
  fi
  return 0
}

extract_tls_observed_from_pcap() {
  local work="$1"
  local pcap="$2"
  local tsv="$3"
  local cc_version="$4"
  local source_label="$5"
  shift 5
  local host_list=("$@")
  local sni_filter

  if [[ ! -s "$pcap" ]]; then
    echo "error: empty pcap — no Claude TLS handshake captured" >&2
    sed -n '1,12p' "$work/tcpdump.err" 2>/dev/null | sed 's/^/  /' || true
    return 1
  fi

  sni_filter="$(cc_sni_filter "${host_list[@]}")"
  echo "Extracting ClientHello via tshark ..." >&2
  echo "  tshark SNI filter: $sni_filter" >&2
  tshark -r "$pcap" \
    -Y "$sni_filter" \
    -T fields -E header=y -E separator='	' -E aggregator=, \
    -e tls.handshake.version \
    -e tls.handshake.ciphersuite \
    -e tls.handshake.extension.type \
    -e tls.handshake.extensions_supported_group \
    -e tls.handshake.extensions_ec_point_format \
    -e tls.handshake.sig_hash_alg \
    -e tls.handshake.extensions_alpn_str \
    -e tls.handshake.extensions.supported_version \
    -e tls.handshake.extensions_key_share_group \
    -e tls.extension.psk_ke_mode \
    -e tls.handshake.extensions_server_name \
    >"$tsv"

  if [[ "$(wc -l <"$tsv")" -lt 2 ]]; then
    echo "warn: strict SNI filter missed — retrying with any ClientHello" >&2
    tshark -r "$pcap" \
      -Y "tls.handshake.type==1" \
      -T fields -E header=y -E separator='	' -E aggregator=, \
      -e tls.handshake.version \
      -e tls.handshake.ciphersuite \
      -e tls.handshake.extension.type \
      -e tls.handshake.extensions_supported_group \
      -e tls.handshake.extensions_ec_point_format \
      -e tls.handshake.sig_hash_alg \
      -e tls.handshake.extensions_alpn_str \
      -e tls.handshake.extensions.supported_version \
      -e tls.handshake.extensions_key_share_group \
      -e tls.extension.psk_ke_mode \
      -e tls.handshake.extensions_server_name \
      >"$tsv"
  fi

  if [[ "$(wc -l <"$tsv")" -lt 2 ]]; then
    echo "error: tshark found no Claude ClientHello in $pcap" >&2
    return 1
  fi

  python3 "$PY" tls-observed-from-pcap \
    --tshark-tsv "$tsv" \
    --out "$work/tls-observed.json" \
    --cc-version "${cc_version:-}" \
    --source "$source_label" >&2
  echo "ok TLS pcap -> $work/tls-observed.json" >&2
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

resolve_tls_api_base() {
  local config_dir="$1"
  python3 - "$config_dir" <<'PY'
import json, sys, urllib.parse
from pathlib import Path
config_dir = Path(sys.argv[1])
base = "https://api.anthropic.com"
settings_path = config_dir / "settings.json"
if settings_path.is_file():
    env = (json.loads(settings_path.read_text(encoding="utf-8")).get("env") or {})
    for key in ("ANTHROPIC_BASE_URL", "CLAUDE_CODE_API_BASE_URL"):
        raw = str(env.get(key) or "").strip()
        if raw:
            parsed = urllib.parse.urlparse(raw)
            if parsed.scheme and parsed.netloc:
                base = f"{parsed.scheme}://{parsed.netloc}"
                break
print(base)
PY
}

resolve_tls_hosts() {
  local config_dir="$1"
  if [[ -n "${TOKENKEY_CC_CAPTURE_TLS_HOSTS:-}" ]]; then
    echo "$CC_TLS_HOSTS"
    return
  fi
  local base host
  base="$(resolve_tls_api_base "$config_dir")"
  host="$(python3 - "$base" <<'PY'
import sys, urllib.parse
print(urllib.parse.urlparse(sys.argv[1]).hostname or "api.anthropic.com")
PY
)"
  echo "$host"
}

cc_sni_filter() {
  local clause="" host
  for host in "$@"; do
    [[ -n "$clause" ]] && clause="$clause || "
    clause="${clause}tls.handshake.extensions_server_name==\"$host\""
  done
  echo "tls.handshake.type==1 && ( $clause )"
}

resolve_host_ips() {
  local host ip ips=""
  for host in "$@"; do
    if command -v dig >/dev/null 2>&1; then
      ip="$(dig +short "$host" A | grep -E '^[0-9.]+$' || true)"
    else
      ip="$(host "$host" 2>/dev/null | awk '/has address/ {print $4}' || true)"
    fi
    ips="$ips $ip"
  done
  echo "$ips" | tr ' ' '\n' | grep -E '^[0-9.]+$' | sort -u
}

build_pcap_filter() {
  local ips="$1" expr="" ip
  for ip in $ips; do
    [[ -n "$expr" ]] && expr="$expr or "
    expr="${expr}host $ip"
  done
  echo "tcp port 443 and ( $expr )"
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

collector_preflight() {
  local host port="${TOKENKEY_TLS_PROFILE_COLLECTOR_PORT:-8090}"
  host="$(python3 - "$COLLECTOR_ORIGIN" <<'PY'
import sys, urllib.parse
print(urllib.parse.urlparse(sys.argv[1]).hostname or "tls.sub2api.org")
PY
)"
  echo "TLS collector preflight: ${host}:${port} ..."
  if ! python3 - "$host" "$port" <<'PY'
import socket, sys
host, port = sys.argv[1], int(sys.argv[2])
s = socket.socket()
s.settimeout(4)
try:
    s.connect((host, port))
except OSError:
    raise SystemExit(1)
finally:
    s.close()
PY
  then
    echo "  FAIL: collector port ${host}:${port} not reachable (will skip claude --bare)" >&2
    return 1
  fi
  echo "  OK: collector port reachable"
  return 0
}

run_tls_capture() {
  local work="$1"
  local token="$2"
  local claude_bin="$3"
  local overlay="${CC0_USER_OVERLAY:-$HOME/.cache/cc0/claude-user-overlay}"
  local settings="$work/settings-tls.json"
  local bare_timeout="${TOKENKEY_CC_TLS_BARE_TIMEOUT:-60}"

  if ! collector_preflight; then
    return 1
  fi

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

  echo "TLS collector: running claude --bare (timeout ${bare_timeout}s, no stdout until done) ..."
  (
    sleep "$bare_timeout"
    pkill -f "claude.*settings-tls.json" 2>/dev/null || true
  ) &
  local watchdog_pid=$!

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

  kill "$watchdog_pid" 2>/dev/null || true
  wait "$watchdog_pid" 2>/dev/null || true
  echo "TLS collector: claude --bare finished; fetching JA3 from API ..."

  if ! curl -fsS --max-time 15 "$COLLECTOR_API_ORIGIN/api/latest?token=$token" >"$work/latest.json"; then
    echo "error: TLS collector API unreachable ($COLLECTOR_API_ORIGIN)" >&2
    return 1
  fi
  local count
  count="$(jq -r '.count // 0' "$work/latest.json")"
  if [[ "${count:-0}" == "0" ]]; then
    echo "error: TLS collector recorded no fingerprint (token=$token)" >&2
    sed -n '1,8p' "$work/claude-tls.err" >&2 || true
    return 1
  fi
  jq '.fingerprints[0]' "$work/latest.json" >"$work/tls-observed.json"
}

run_tls_pcap_trigger() {
  local work="$1"
  local config_dir="$2"
  local claude_bin="$3"
  local api_base="$4"
  local settings="$work/settings-tls-pcap.json"

  jq -n \
    --arg api_base "$api_base" \
    '{
      env: {
        CLAUDE_CODE_API_BASE_URL: $api_base,
        ANTHROPIC_BASE_URL: $api_base
      },
      hooks: {SessionStart: []},
      permissions: {defaultMode: "bypassPermissions"},
      skipWebFetchPreflight: true,
      skipDangerousModePermissionPrompt: true
    }' >"$settings"

  CLAUDE_CONFIG_DIR="$config_dir" \
    "$claude_bin" --bare --setting-sources local,user --settings "$settings" \
    -p 'test' --model "$MODEL" --allowedTools '' --max-budget-usd 0.15 \
    >"$work/claude-pcap.out" 2>"$work/claude-pcap.err" || true
}

run_tls_pcap_capture() {
  local work="$1"
  local config_dir="$2"
  local claude_bin="$3"
  local cc_version="$4"
  local stamp="$5"
  local hosts host_list=() filter iface iface_arg=() pcap tsv sni_filter api_base

  require_cmd tcpdump
  require_cmd tshark
  require_sudo_for_pcap

  api_base="$(resolve_tls_api_base "$config_dir")"
  hosts="$(resolve_tls_hosts "$config_dir")"
  read -r -a host_list <<<"$hosts"

  pcap="$work/${stamp}-cc-interactive.pcap"
  tsv="$work/${stamp}-cc-interactive.tshark.tsv"

  if [[ -n "$PCAP_PROXY_PORT" ]]; then
    [[ -z "$PCAP_IFACE" ]] && PCAP_IFACE="lo0"
    filter="tcp port $PCAP_PROXY_PORT"
    echo "TLS pcap (proxied): iface=$PCAP_IFACE filter='$filter'"
  else
    echo "Resolving TLS host IPs for: $hosts"
    local ips
    ips="$(resolve_host_ips "${host_list[@]}")"
    if [[ -z "$ips" ]]; then
      echo "error: could not resolve any IP for: $hosts" >&2
      echo "  (if Claude egresses via a local proxy, pass --tls-pcap-proxy-port N)" >&2
      return 1
    fi
    echo "IPs:"; echo "$ips" | sed 's/^/  /'
    filter="$(build_pcap_filter "$ips")"
    [[ -z "$PCAP_IFACE" ]] && PCAP_IFACE="en0"
    echo "TLS pcap (direct): iface=$PCAP_IFACE"
  fi
  [[ -n "$PCAP_IFACE" ]] && iface_arg=(-i "$PCAP_IFACE")

  echo "  bpf filter: $filter"

  echo "Starting tcpdump for up to ${PCAP_SECONDS}s (sudo may prompt) ..."
  sudo tcpdump ${iface_arg[@]+"${iface_arg[@]}"} -s 0 -w "$pcap" -G "$PCAP_SECONDS" -W 1 "$filter" \
    >"$work/tcpdump.err" 2>&1 &
  local tcpdump_pid=$!
  sleep 1

  echo "Triggering Claude CLI TLS handshake to $api_base ..."
  run_tls_pcap_trigger "$work" "$config_dir" "$claude_bin" "$api_base"
  sleep 2
  wait "$tcpdump_pid" 2>/dev/null || true

  extract_tls_observed_from_pcap \
    "$work" "$pcap" "$tsv" "$cc_version" "passive-pcap:${api_base}" \
    "${host_list[@]}"
}

run_tls_pcap_via_mitm() {
  local work="$1"
  local config_dir="$2"
  local cc_version="$3"
  local stamp="$4"
  local hosts host_list=() pcap tsv api_base proxy_port

  require_cmd tcpdump
  require_cmd tshark
  require_sudo_for_pcap

  api_base="$(resolve_tls_api_base "$config_dir")"
  hosts="$(resolve_tls_hosts "$config_dir")"
  read -r -a host_list <<<"$hosts"
  proxy_port="${PCAP_PROXY_PORT:-$MITM_PORT}"
  PCAP_IFACE="${PCAP_IFACE:-lo0}"

  pcap="$work/${stamp}-cc-interactive-mitm.pcap"
  tsv="$work/${stamp}-cc-interactive-mitm.tshark.tsv"

  echo "TLS pcap (interactive mitm): iface=$PCAP_IFACE filter='tcp port $proxy_port'" >&2
  echo "Starting tcpdump for interactive REPL session (sudo may prompt) ..." >&2
  sudo tcpdump -i "$PCAP_IFACE" -s 0 -w "$pcap" -G "$PCAP_SECONDS" -W 1 "tcp port $proxy_port" \
    >"$work/tcpdump.err" 2>&1 &
  echo "$!" >"$work/tcpdump.pid"
  sleep 1
}

finish_tls_pcap_via_mitm() {
  local work="$1"
  local config_dir="$2"
  local cc_version="$3"
  local stamp="$4"
  local hosts host_list=() pcap tsv api_base tcpdump_pid

  if [[ ! -f "$work/tcpdump.pid" ]]; then
    return 0
  fi
  tcpdump_pid="$(cat "$work/tcpdump.pid")"
  kill "$tcpdump_pid" 2>/dev/null || true
  wait "$tcpdump_pid" 2>/dev/null || true
  rm -f "$work/tcpdump.pid"

  api_base="$(resolve_tls_api_base "$config_dir")"
  hosts="$(resolve_tls_hosts "$config_dir")"
  read -r -a host_list <<<"$hosts"
  pcap="$work/${stamp}-cc-interactive-mitm.pcap"
  tsv="$work/${stamp}-cc-interactive-mitm.tshark.tsv"

  extract_tls_observed_from_pcap \
    "$work" "$pcap" "$tsv" "$cc_version" "passive-pcap-mitm:${api_base}" \
    "${host_list[@]}"
}

run_interactive_http_capture() {
  local work="$1"
  local config_dir="$2"
  local claude_bin="$3"
  local with_tls_pcap="${4:-0}"
  local pcap_stamp="${5:-}"
  local pcap_cc_version="${6:-}"
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
  echo "mitmdump listening on 127.0.0.1:$MITM_PORT (logs: $work/mitm.err)" >&2

  if [[ "$with_tls_pcap" == "1" ]]; then
    run_tls_pcap_via_mitm "$work" "$config_dir" "$pcap_cc_version" "$pcap_stamp"
  fi

  local expect_timeout="${TK_CAPTURE_TIMEOUT:-240}"
  echo "Automated Claude REPL via expect (silent, up to ${expect_timeout}s) — do not type in this shell; wait for completion ..." >&2
  run_one() {
    local model="$1"
    export TK_CAPTURE_WORK="$work"
    export TK_CAPTURE_MODEL="$model"
    export TK_CAPTURE_PROMPT="$PROMPT"
    export TK_CAPTURE_CONFIG_DIR="$config_dir"
    export TK_CAPTURE_CLAUDE_BIN="$claude_bin"
    echo "  expect model=$model ..." >&2
    expect "$EXPECT_SCRIPT" >"$work/expect-${model##*-}.out" 2>"$work/expect-${model##*-}.err" || true
    echo "  expect model=$model done (see $work/expect-${model##*-}.err on failure)" >&2
    sleep 2
  }

  run_one "$MODEL"
  if [[ "${TOKENKEY_CC_CAPTURE_INTERACTIVE_SONNET:-0}" == "1" ]]; then
    run_one "$SONNET_MODEL"
  fi
  sleep 2
  kill "$mitm_pid" 2>/dev/null || true
  wait "$mitm_pid" 2>/dev/null || true

  if [[ "$with_tls_pcap" == "1" ]]; then
    finish_tls_pcap_via_mitm "$work" "$config_dir" "$pcap_cc_version" "$pcap_stamp"
  fi

  if ! grep -q '"user_agent"' "$http_log" 2>/dev/null; then
    echo "error: interactive HTTP mitm log empty — check OAuth, mitm, expect output in $work" >&2
    sed -n '1,20p' "$work"/expect-*.err 2>/dev/null | sed 's/^/  /' || true
    exit 1
  fi
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

write_baseline_tls_stub() {
  local work="$1"
  local _claude_bin="$2"
  local cc_version="$3"
  python3 - "$PY" "$work/tls-observed.json" "$cc_version" <<'PY'
import importlib.util, json, sys
from pathlib import Path

mod_path = Path(sys.argv[1])
out = Path(sys.argv[2])
cc_version = sys.argv[3]
spec = importlib.util.spec_from_file_location("capture_cc_fingerprint", mod_path)
mod = importlib.util.module_from_spec(spec)
assert spec and spec.loader
sys.modules[spec.name] = mod
spec.loader.exec_module(mod)
baseline = mod.load_tokenkey_baseline()
tls = baseline["tls"]
payload = {
    "ja3_hash": tls.get("ja3_hash", ""),
    "ja3_raw": tls.get("ja3_raw", ""),
    "user_agent": f"claude-cli/{cc_version} (external, cli)",
    "stainless_package_version": baseline["canonical_http"]["stainless_package_version"],
    "source": "baseline_stub_http_only",
}
out.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
print(f"tls stub from baseline -> {out}")
PY
}

run_tls_capture_with_fallback() {
  local work="$1"
  local token="$2"
  local config_dir="$3"
  local claude_bin="$4"
  local cc_version="$5"
  local stamp="$6"
  local tls_mode="$7"
  local pcap_fallback="$8"
  local pcap_standalone="$9"

  case "$tls_mode" in
    stub)
      write_baseline_tls_stub "$work" "$claude_bin" "${cc_version:-unknown}"
      return 0
      ;;
    pcap)
      if [[ "$pcap_standalone" == "1" ]]; then
        run_tls_pcap_capture "$work" "$config_dir" "$claude_bin" "$cc_version" "$stamp"
        return 0
      fi
      echo "TLS pcap deferred to interactive mitm session (lo0:$MITM_PORT)"
      echo "1" >"$work/pcap-deferred"
      return 0
      ;;
    collector)
      echo "TLS mode: collector ($COLLECTOR_ORIGIN:8090)"
      if run_tls_capture "$work" "$token" "$claude_bin"; then
        return 0
      fi
      if [[ "$pcap_fallback" != "1" ]]; then
        return 1
      fi
      echo "warn: TLS collector failed; falling back to passive pcap during interactive mitm" >&2
      echo "1" >"$work/pcap-deferred"
      return 0
      ;;
    *)
      echo "error: unknown tls_mode=$tls_mode" >&2
      return 1
      ;;
  esac
}

cmd_capture() {
  local http_only=0 tls_pcap=0 pcap_standalone=0 pcap_fallback=1 tls_mode="collector"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --http-only) http_only=1; shift ;;
      --tls-pcap) tls_pcap=1; shift ;;
      --tls-pcap-standalone) pcap_standalone=1; shift ;;
      --no-tls-pcap-fallback) pcap_fallback=0; shift ;;
      --tls-pcap-iface) PCAP_IFACE="$2"; shift 2 ;;
      --tls-pcap-proxy-port) PCAP_PROXY_PORT="$2"; shift 2 ;;
      --tls-pcap-seconds) PCAP_SECONDS="$2"; shift 2 ;;
      --out-dir)
        OUT_DIR="$2"
        shift 2
        ;;
      *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
    esac
  done

  require_cmd python3
  require_cmd jq
  require_cmd curl
  local claude_bin
  claude_bin="$(resolve_claude_bin)"
  local config_dir
  config_dir="$(resolve_config_dir "$claude_bin")"

  mkdir -p "$OUT_DIR"
  local stamp cc_version token work http_log tls_capture bundle_path collector_label
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  cc_version="$("$claude_bin" --version 2>/dev/null | awk '{print $1}' || true)"
  token="tk-cc-interactive-${stamp}-$$"
  work="$(mktemp -d "${TMPDIR:-/tmp}/tk-cc-interactive.XXXXXX")"
  http_log="$work/http-interactive.log"
  cleanup_work() { rm -rf "$work"; }
  trap cleanup_work EXIT

  if [[ "$http_only" == "1" ]]; then
    tls_mode="stub"
  elif [[ "$tls_pcap" == "1" ]]; then
    tls_mode="pcap"
  fi

  echo "cc_version=${cc_version:-unknown} claude_bin=$claude_bin config_dir=$config_dir tls_mode=$tls_mode"
  run_tls_capture_with_fallback "$work" "$token" "$config_dir" "$claude_bin" "$cc_version" "$stamp" "$tls_mode" "$pcap_fallback" "$pcap_standalone"

  if [[ -f "$work/pcap-deferred" ]]; then
    echo "Starting interactive HTTP capture + mitm loopback TLS pcap ..."
    run_interactive_http_capture "$work" "$config_dir" "$claude_bin" 1 "$stamp" "$cc_version"
  else
    echo "Starting interactive HTTP capture ..."
    run_interactive_http_capture "$work" "$config_dir" "$claude_bin"
  fi
  validate_interactive_log "$http_log"

  collector_label="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("source","collector"))' "$work/tls-observed.json" 2>/dev/null || echo "$COLLECTOR_ORIGIN:8090")"

  tls_capture="$OUT_DIR/${stamp}-cc-interactive.tls-observed.json"
  cp "$work/tls-observed.json" "$tls_capture"

  bundle_path="$OUT_DIR/${stamp}-cc-interactive.bundle.json"
  bundle_args=(
    --tls-json "$tls_capture"
    --http-log "$http_log"
    --out "$bundle_path"
    --collector "$collector_label"
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
