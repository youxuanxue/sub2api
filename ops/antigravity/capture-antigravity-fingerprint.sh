#!/usr/bin/env bash
# Antigravity CLI (`agy`) fingerprint alignment for TokenKey.
#
# Default path (version owner): read locally installed `agy --version` and diff
# against `DefaultUserAgentVersion` — no IDE, no mitm (same class as Codex/Gemini).
#
# Optional wire path: mitmproxy + `agy --print` for HTTP body/ideType regression
# checks. Go CLI trusts login keychain for mitm CA (see skill).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PY="$SCRIPT_DIR/capture_antigravity_fingerprint.py"
MITM_ADDON="$SCRIPT_DIR/mitm_antigravity_http_headers.py"

OUT_DIR="${TOKENKEY_AG_CAPTURE_OUT_DIR:-$REPO_ROOT/.antigravity_fp}"
PROXY_PORT="${TOKENKEY_AG_CAPTURE_PROXY_PORT:-8080}"
CAPTURE_SECONDS="${TOKENKEY_AG_CAPTURE_SECONDS:-90}"
IFACE="${TOKENKEY_AG_CAPTURE_IFACE:-}"
AG_HOSTS_DEFAULT="cloudcode-pa.googleapis.com daily-cloudcode-pa.sandbox.googleapis.com"
AG_HOSTS="${TOKENKEY_AG_CAPTURE_HOSTS:-$AG_HOSTS_DEFAULT}"

usage() {
  cat <<'EOF'
Usage:
  capture-antigravity-fingerprint.sh check env          # agy CLI installed (static owner)
  capture-antigravity-fingerprint.sh check              # static version diff (agy vs pin)
  capture-antigravity-fingerprint.sh emit-edits         # print oauth.go version bump
  capture-antigravity-fingerprint.sh show-baseline
  capture-antigravity-fingerprint.sh capture [--http] [--tls] [--proxy-port N] [--seconds N] [--out-dir DIR]
  capture-antigravity-fingerprint.sh diff --bundle PATH [--check]
  capture-antigravity-fingerprint.sh check --bundle PATH
  capture-antigravity-fingerprint.sh check-tls --bundle PATH

Static path (default for release-watch bumps):
  bash ops/antigravity/capture-antigravity-fingerprint.sh check env
  bash ops/antigravity/capture-antigravity-fingerprint.sh check
  bash ops/antigravity/capture-antigravity-fingerprint.sh emit-edits

Optional wire capture (`agy` through mitmproxy; IDE not used):
  1. Trust mitm CA in login keychain (Go binary — see skill)
  2. mitmdump + agy --print in an empty directory with HTTP(S)_PROXY set
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "error: required command not found: $1" >&2; exit 2; }
}

cmd_check_env() {
  require_cmd python3
  python3 "$PY" check-env
}

run_http_capture() {
  require_cmd mitmdump
  local stamp="$1" http_log="$2"
  : > "$http_log"
  echo "Starting mitmdump on 127.0.0.1:${PROXY_PORT} (addon: $(basename "$MITM_ADDON")) ..."
  ANTIGRAVITY_CAPTURE_HTTP_LOG="$http_log" \
    mitmdump --listen-host 127.0.0.1 -p "$PROXY_PORT" -q -s "$MITM_ADDON" \
    >"$OUT_DIR/${stamp}-mitmdump.log" 2>&1 &
  local mitm_pid=$!
  sleep 2
  echo
  echo ">>> NOW run agy through the proxy (empty cwd; see skill for login-keychain CA trust):"
  echo ">>>   HTTP_PROXY=http://127.0.0.1:${PROXY_PORT} HTTPS_PROXY=http://127.0.0.1:${PROXY_PORT} \\"
  echo ">>>     agy --print \"Reply with one word: pong\" --print-timeout 60s </dev/null"
  echo ">>> Waiting up to ${CAPTURE_SECONDS}s for v1internal requests ..."
  local waited=0
  while [[ "$waited" -lt "$CAPTURE_SECONDS" ]]; do
    sleep 3; waited=$((waited + 3))
    if ! kill -0 "$mitm_pid" 2>/dev/null; then
      echo "  ! mitmdump exited prematurely. Log tail:" >&2
      tail -n 20 "$OUT_DIR/${stamp}-mitmdump.log" >&2 || true
      exit 2
    fi
    [[ -s "$http_log" ]] && { echo "  captured $(wc -l <"$http_log" | tr -d ' ') request line(s)"; break; }
  done
  kill "$mitm_pid" 2>/dev/null || true
  wait "$mitm_pid" 2>/dev/null || true
  if [[ ! -s "$http_log" ]]; then
    echo "  ! no v1internal request captured — confirm agy trusts mitm CA and uses the proxy." >&2
  fi
}

resolve_ips() {
  local host ip ips=""
  for host in $AG_HOSTS; do
    if command -v dig >/dev/null 2>&1; then
      ip="$(dig +short "$host" A | grep -E '^[0-9.]+$' || true)"
    else
      ip="$(host "$host" 2>/dev/null | awk '/has address/ {print $4}' || true)"
    fi
    ips="$ips $ip"
  done
  echo "$ips" | tr ' ' '\n' | grep -E '^[0-9.]+$' | sort -u
}

run_tls_capture() {
  require_cmd tcpdump; require_cmd tshark
  local stamp="$1" tsv="$2" pcap="$OUT_DIR/${stamp}-antigravity.pcap"
  local ips filter iface_arg=()
  echo "Resolving Antigravity host IPs (direct egress) ..."
  ips="$(resolve_ips)"
  if [[ -z "$ips" ]]; then
    echo "  ! could not resolve antigravity hosts; skipping TLS capture (non-gating)." >&2
    return 0
  fi
  local expr="" ip
  for ip in $ips; do [[ -n "$expr" ]] && expr="$expr or "; expr="${expr}host $ip"; done
  filter="tcp port 443 and ( $expr )"
  [[ -n "$IFACE" ]] && iface_arg=(-i "$IFACE")
  echo "  tcpdump filter: $filter"
  sudo tcpdump ${iface_arg[@]+"${iface_arg[@]}"} -s 0 -w "$pcap" -G "$CAPTURE_SECONDS" -W 1 "$filter" \
    >/dev/null 2>"$OUT_DIR/${stamp}-tcpdump.err" &
  local tcpdump_pid=$!
  echo ">>> trigger another agy request now (TLS handshake, ${CAPTURE_SECONDS}s window) ..."
  wait "$tcpdump_pid" 2>/dev/null || true
  if [[ ! -s "$pcap" ]]; then echo "  ! empty pcap; skipping TLS (non-gating)." >&2; return 0; fi
  tshark -r "$pcap" \
    -Y "tls.handshake.type==1" \
    -T fields -E header=y -E separator='	' -E aggregator=, \
    -e tls.handshake.version \
    -e tls.handshake.ciphersuite \
    -e tls.handshake.extension.type \
    -e tls.handshake.extensions_supported_group \
    -e tls.handshake.extensions_ec_point_format \
    -e tls.handshake.extensions_server_name \
    > "$tsv" 2>/dev/null || true
}

cmd_capture() {
  local do_http=1 do_tls=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --http) do_http=1; shift ;;
      --tls) do_tls=1; shift ;;
      --proxy-port) PROXY_PORT="$2"; shift 2 ;;
      --seconds) CAPTURE_SECONDS="$2"; shift 2 ;;
      --out-dir) OUT_DIR="$2"; shift 2 ;;
      *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
    esac
  done
  require_cmd python3
  mkdir -p "$OUT_DIR"
  local stamp http_log tsv bundle bundle_args=()
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  http_log="$OUT_DIR/${stamp}-antigravity.http-log.jsonl"
  tsv="$OUT_DIR/${stamp}-antigravity.tshark.tsv"
  bundle="$OUT_DIR/${stamp}-antigravity-capture.bundle.json"

  [[ "$do_http" -eq 1 ]] && run_http_capture "$stamp" "$http_log"
  [[ "$do_tls" -eq 1 ]] && run_tls_capture "$stamp" "$tsv"

  bundle_args=(--out "$bundle" --source "mitmproxy-agy" \
    --captured-at "${stamp:0:4}-${stamp:4:2}-${stamp:6:2}T${stamp:9:2}:${stamp:11:2}:${stamp:13:2}Z")
  [[ -s "$http_log" ]] && bundle_args+=(--http-log "$http_log")
  [[ -s "$tsv" ]] && bundle_args+=(--tshark-tsv "$tsv")
  python3 "$PY" bundle-from-artifacts "${bundle_args[@]}"

  echo
  echo "bundle=$bundle"
  python3 "$PY" diff --bundle "$bundle" --check
}

main() {
  local cmd="${1:-}"
  shift || true
  case "$cmd" in
    check)
      if [[ "${1:-}" == "env" ]]; then
        cmd_check_env
      elif [[ "${1:-}" == "--bundle" ]]; then
        shift
        require_cmd python3; exec python3 "$PY" check --bundle "$@"
      else
        require_cmd python3; exec python3 "$PY" check-static "$@"
      fi ;;
    capture) cmd_capture "$@" ;;
    diff) require_cmd python3; exec python3 "$PY" diff "$@" ;;
    check-tls) require_cmd python3; exec python3 "$PY" check-tls "$@" ;;
    show-baseline) require_cmd python3; exec python3 "$PY" show-baseline "$@" ;;
    emit-edits) require_cmd python3; exec python3 "$PY" emit-edits "$@" ;;
    -h|--help|"") usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 2 ;;
  esac
}

main "$@"
