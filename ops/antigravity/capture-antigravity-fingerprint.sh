#!/usr/bin/env bash
# Capture a real Antigravity IDE (Google cloudcode-pa) HTTP fingerprint via
# mitmproxy and diff it against TokenKey repo constants.
#
# Why mitmproxy (vs cc's collector-redirect and kiro's passive pcap): for
# Antigravity the load-bearing fingerprint is the HTTP layer (impersonated client
# UA *version*, body `userAgent`, ideType metadata, gl-node X-Goog-Api-Client),
# NOT the TLS JA3 — TokenKey and the real IDE share a native Go/Node TLS stack, so
# the ClientHello is same-origin and JA3 carries no signal. The cloudcode-pa
# endpoint is hard-coded (cannot be redirected like cc), so the on-wire HTTP is
# recovered by pointing the IDE through a mitmproxy whose CA it trusts. TLS is an
# OPTIONAL, non-gating add-on (--tls, passive pcap) kept only for completeness.
#
# Deterministic parse / diff is delegated to capture_antigravity_fingerprint.py;
# this shell only drives mitmdump (+ optional tcpdump/tshark) and shells out.
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
  capture-antigravity-fingerprint.sh check env
  capture-antigravity-fingerprint.sh show-baseline
  capture-antigravity-fingerprint.sh capture [--http] [--tls] [--proxy-port N] [--seconds N] [--out-dir DIR]
  capture-antigravity-fingerprint.sh diff --bundle PATH [--check]
  capture-antigravity-fingerprint.sh check --bundle PATH
  capture-antigravity-fingerprint.sh check-tls --bundle PATH      # informational only (JA3 non-load-bearing)

capture flow (--http is the default, load-bearing path):
  1. starts mitmdump on 127.0.0.1:<proxy-port> with the antigravity addon
  2. prompts you to trigger ONE request from the real Antigravity IDE
     (the IDE must egress through this proxy AND trust its CA — see below)
  3. capture_antigravity_fingerprint.py assembles the bundle from the mitm log,
     then diffs against the Go-constant baseline
  --tls (optional): also run sudo tcpdump + tshark to record the JA3 (NON-gating;
     antigravity JA3 is non-load-bearing).

Point the IDE at the proxy (one of):
  - IDE setting  http.proxy = http://127.0.0.1:<port>  + http.proxyStrictSSL=false
  - or launch with  HTTPS_PROXY=http://127.0.0.1:<port>  NODE_EXTRA_CA_CERTS=~/.mitmproxy/mitmproxy-ca-cert.pem
Trust the mitmproxy CA (~/.mitmproxy/mitmproxy-ca-cert.pem) in the OS/Node trust store.

Requires: python3, mitmdump (mitmproxy). --tls additionally needs tcpdump + tshark (sudo).
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "error: required command not found: $1" >&2; exit 1; }
}

cmd_check_env() {
  local ok=1
  if command -v mitmdump >/dev/null 2>&1; then
    echo "  ✓ mitmdump present ($(command -v mitmdump))"
  else
    echo "  ✗ mitmdump NOT found (install mitmproxy: pipx install mitmproxy)"; ok=0
  fi
  command -v python3 >/dev/null 2>&1 && echo "  ✓ python3 present" || { echo "  ✗ python3 NOT found"; ok=0; }
  if [[ -f "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]]; then
    echo "  ✓ mitmproxy CA exists (~/.mitmproxy/mitmproxy-ca-cert.pem) — ensure the IDE/OS trusts it"
  else
    echo "  · mitmproxy CA not generated yet (runs once on first mitmdump start)"
  fi
  if pgrep -i antigravity >/dev/null 2>&1; then
    echo "  ✓ an Antigravity process is running"
  else
    echo "  · no Antigravity process detected (install + log in to the IDE before capturing)"
  fi
  [[ "$ok" -eq 1 ]] || { echo "check env: missing prerequisites" >&2; exit 1; }
  echo "check env: ok"
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
  # Give mitmdump a moment to bind + generate its CA on first run.
  sleep 2
  echo
  echo ">>> NOW trigger ONE request from the real Antigravity IDE (e.g. ask it anything)."
  echo ">>> IDE must egress through http://127.0.0.1:${PROXY_PORT} and trust the mitmproxy CA."
  echo ">>> Waiting up to ${CAPTURE_SECONDS}s for v1internal requests ..."
  local waited=0
  while [[ "$waited" -lt "$CAPTURE_SECONDS" ]]; do
    sleep 3; waited=$((waited + 3))
    [[ -s "$http_log" ]] && { echo "  captured $(wc -l <"$http_log" | tr -d ' ') request line(s)"; break; }
  done
  kill "$mitm_pid" 2>/dev/null || true  # preflight-allow: swallow (mitmdump backgrounded; we stop it after the window)
  wait "$mitm_pid" 2>/dev/null || true  # preflight-allow: swallow (killed above; exit status irrelevant)
  if [[ ! -s "$http_log" ]]; then
    echo "  ! no v1internal request captured — the IDE is likely bypassing the proxy or rejecting the CA." >&2
    echo "    (bundle will still be written; diff will report 'no HTTP capture'. See --tls fallback.)" >&2
  fi
}

resolve_ips() {
  local host ip ips=""
  for host in $AG_HOSTS; do
    if command -v dig >/dev/null 2>&1; then
      ip="$(dig +short "$host" A | grep -E '^[0-9.]+$' || true)"  # preflight-allow: swallow (no A -> empty, handled by caller)
    else
      ip="$(host "$host" 2>/dev/null | awk '/has address/ {print $4}' || true)"  # preflight-allow: swallow (host(1) absent -> empty)
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
  echo ">>> trigger another Antigravity request now (TLS handshake, ${CAPTURE_SECONDS}s window) ..."
  wait "$tcpdump_pid" 2>/dev/null || true  # preflight-allow: swallow (tcpdump exits via -G/-W; pcap content checked next)
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
    > "$tsv" 2>/dev/null || true  # preflight-allow: swallow (TLS is optional/non-gating; absent tsv handled downstream)
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
      *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
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

  bundle_args=(--out "$bundle" --source "mitmproxy" \
    --captured-at "${stamp:0:4}-${stamp:4:2}-${stamp:6:2}T${stamp:9:2}:${stamp:11:2}:${stamp:13:2}Z")
  [[ -s "$http_log" ]] && bundle_args+=(--http-log "$http_log")
  [[ -s "$tsv" ]] && bundle_args+=(--tshark-tsv "$tsv")
  python3 "$PY" bundle-from-artifacts "${bundle_args[@]}"

  echo
  echo "bundle=$bundle"
  python3 "$PY" diff --bundle "$bundle"
}

main() {
  local cmd="${1:-}"
  shift || true  # preflight-allow: swallow (no-arg invocation -> nothing to shift)
  case "$cmd" in
    check)
      if [[ "${1:-}" == "env" ]]; then cmd_check_env; else require_cmd python3; exec python3 "$PY" check "$@"; fi ;;
    capture) cmd_capture "$@" ;;
    diff) require_cmd python3; exec python3 "$PY" diff "$@" ;;
    check-tls) require_cmd python3; exec python3 "$PY" check-tls "$@" ;;
    show-baseline) require_cmd python3; exec python3 "$PY" show-baseline "$@" ;;
    -h|--help|"") usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 1 ;;
  esac
}

main "$@"
