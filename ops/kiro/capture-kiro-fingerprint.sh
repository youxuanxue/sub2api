#!/usr/bin/env bash
# Capture a real Kiro IDE (AWS CodeWhisperer) TLS ClientHello by passive pcap and
# diff its JA3 + User-Agent against TokenKey repo constants.
#
# Why passive pcap (vs the cc collector-redirect path): the real Kiro IDE
# hard-codes codewhisperer.us-east-1.amazonaws.com / q.us-east-1.amazonaws.com and
# cannot be pointed at a self-hosted collector. The TLS ClientHello is sent in the
# clear before the handshake completes, so tcpdump + tshark recover the JA3 with no
# MITM. HTTP headers (UA) live inside TLS and need the optional mitm path.
#
# Deterministic parse / JA3 / diff is delegated to capture_kiro_fingerprint.py;
# this shell only drives tcpdump + tshark and shells out.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PY="$SCRIPT_DIR/capture_kiro_fingerprint.py"

KIRO_HOSTS_DEFAULT="codewhisperer.us-east-1.amazonaws.com q.us-east-1.amazonaws.com"
KIRO_HOSTS="${TOKENKEY_KIRO_CAPTURE_HOSTS:-$KIRO_HOSTS_DEFAULT}"
OUT_DIR="${TOKENKEY_KIRO_CAPTURE_OUT_DIR:-$REPO_ROOT/.kiro_tls}"
IFACE="${TOKENKEY_KIRO_CAPTURE_IFACE:-}"
CAPTURE_SECONDS="${TOKENKEY_KIRO_CAPTURE_SECONDS:-60}"

usage() {
  cat <<'EOF'
Usage:
  capture-kiro-fingerprint.sh capture [--iface IF] [--seconds N] [--out-dir DIR] [--http-log FILE]
  capture-kiro-fingerprint.sh diff --bundle PATH [--check]
  capture-kiro-fingerprint.sh check --bundle PATH
  capture-kiro-fingerprint.sh check-tls --bundle PATH
  capture-kiro-fingerprint.sh show-baseline
  capture-kiro-fingerprint.sh emit-profile --bundle PATH      # write tk_canonical_kiro_ide.json

capture flow:
  1. resolves the Kiro CodeWhisperer host IPs
  2. starts tcpdump on the TLS handshake to those IPs
  3. prompts you to trigger ONE request from the real Kiro IDE
  4. tshark extracts the ClientHello -> tshark TSV
  5. capture_kiro_fingerprint.py computes ja3 + assembles the bundle, then diffs

Requires: python3, tcpdump, tshark, dig (or host). tcpdump needs sudo/root on macOS.
The optional --http-log FILE is a line-JSON log produced by mitm_kiro_http_headers.py
(only usable if the Kiro IDE honors HTTP_PROXY + a trusted MITM CA).
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "error: required command not found: $1" >&2; exit 1; }
}

resolve_ips() {
  local host ip ips=""
  for host in $KIRO_HOSTS; do
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
  # tcp port 443 and (host A or host B ...)
  local ips="$1" expr="" ip
  for ip in $ips; do
    [[ -n "$expr" ]] && expr="$expr or "
    expr="${expr}host $ip"
  done
  echo "tcp port 443 and ( $expr )"
}

cmd_capture() {
  local http_log=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --iface) IFACE="$2"; shift 2 ;;
      --seconds) CAPTURE_SECONDS="$2"; shift 2 ;;
      --out-dir) OUT_DIR="$2"; shift 2 ;;
      --http-log) http_log="$2"; shift 2 ;;
      *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
    esac
  done

  require_cmd python3
  require_cmd tcpdump
  require_cmd tshark

  mkdir -p "$OUT_DIR"
  local stamp ips filter pcap tsv bundle iface_arg=()
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  pcap="$OUT_DIR/${stamp}-kiro.pcap"
  tsv="$OUT_DIR/${stamp}-kiro.tshark.tsv"
  bundle="$OUT_DIR/${stamp}-kiro-capture.bundle.json"

  echo "Resolving Kiro CodeWhisperer host IPs ..."
  ips="$(resolve_ips)"
  if [[ -z "$ips" ]]; then
    echo "error: could not resolve any IP for: $KIRO_HOSTS" >&2
    exit 1
  fi
  echo "IPs:"; echo "$ips" | sed 's/^/  /'
  filter="$(build_pcap_filter "$ips")"
  [[ -n "$IFACE" ]] && iface_arg=(-i "$IFACE")

  echo
  echo "Starting tcpdump for up to ${CAPTURE_SECONDS}s (sudo may prompt) ..."
  echo "  filter: $filter"
  # -G + -W 1 stops after one CAPTURE_SECONDS window; capture only handshake bytes.
  sudo tcpdump "${iface_arg[@]}" -s 0 -w "$pcap" -G "$CAPTURE_SECONDS" -W 1 "$filter" \
    >/dev/null 2>"$OUT_DIR/${stamp}-tcpdump.err" &
  local tcpdump_pid=$!
  sleep 1

  echo
  echo ">>> NOW trigger ONE request from the real Kiro IDE (e.g. ask it anything)."
  echo ">>> Waiting for tcpdump to finish (or Ctrl-C tcpdump window after the request)."
  wait "$tcpdump_pid" 2>/dev/null || true  # tcpdump exits at window end

  if [[ ! -s "$pcap" ]]; then
    echo "error: empty pcap — no handshake captured. Check --iface and that Kiro made a request." >&2
    exit 1
  fi

  echo "Extracting ClientHello via tshark ..."
  # First ClientHello whose SNI is an amazonaws host. Fixed field order MUST match
  # TSHARK_FIELDS in capture_kiro_fingerprint.py.
  tshark -r "$pcap" \
    -Y 'tls.handshake.type==1 && tls.handshake.extensions_server_name contains "amazonaws"' \
    -T fields -E header=y -E separator='	' -E aggregator=, \
    -e tls.handshake.version \
    -e tls.handshake.ciphersuite \
    -e tls.handshake.extension.type \
    -e tls.handshake.extensions_supported_group \
    -e tls.handshake.extensions_ec_point_format \
    -e tls.handshake.sig_hash_alg \
    -e tls.handshake.extensions_alpn_str \
    -e tls.handshake.extensions_supported_version \
    -e tls.handshake.extensions_key_share_group \
    -e tls.handshake.extensions_psk_ke_modes \
    -e tls.handshake.extensions_server_name \
    > "$tsv"

  if [[ "$(wc -l <"$tsv")" -lt 2 ]]; then
    echo "error: tshark found no Kiro ClientHello in $pcap" >&2
    echo "  (try a wider --seconds, confirm --iface, or that Kiro egresses on this host)" >&2
    exit 1
  fi

  local bundle_args=(--tshark-tsv "$tsv" --out "$bundle" --source "passive-pcap" --captured-at "${stamp:0:4}-${stamp:4:2}-${stamp:6:2}T${stamp:9:2}:${stamp:11:2}:${stamp:13:2}Z")
  [[ -n "$http_log" && -f "$http_log" ]] && bundle_args+=(--http-log "$http_log")
  python3 "$PY" bundle-from-pcap "${bundle_args[@]}"

  echo
  echo "bundle=$bundle"
  python3 "$PY" diff --bundle "$bundle"
  echo
  echo "To commit the canonical profile (first capture):"
  echo "  python3 $PY emit-profile --bundle $bundle"
}

main() {
  local cmd="${1:-}"
  shift || true
  case "$cmd" in
    capture) cmd_capture "$@" ;;
    diff)         require_cmd python3; exec python3 "$PY" diff "$@" ;;
    check)        require_cmd python3; exec python3 "$PY" check "$@" ;;
    check-tls)    require_cmd python3; exec python3 "$PY" check-tls "$@" ;;
    show-baseline) require_cmd python3; exec python3 "$PY" show-baseline "$@" ;;
    emit-profile) require_cmd python3; exec python3 "$PY" emit-profile "$@" ;;
    -h|--help|"") usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 1 ;;
  esac
}

main "$@"
