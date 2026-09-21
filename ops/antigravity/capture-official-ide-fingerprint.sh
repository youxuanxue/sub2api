#!/usr/bin/env bash
# Local-only official Antigravity IDE/language-server fingerprint workflow.
# It never forwards captured traffic and never stores OAuth credentials.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
APP_PATH="${ANTIGRAVITY_APP_PATH:-/Applications/Antigravity.app}"

usage() {
  cat <<'EOF'
Usage:
  capture-official-ide-fingerprint.sh check-version
  capture-official-ide-fingerprint.sh serve-tls [--out-dir DIR] [--port N] [--seconds N]
  capture-official-ide-fingerprint.sh report-tls --capture-dir DIR [--out PATH]
  capture-official-ide-fingerprint.sh serve-http --out PATH [--port N] [--seconds N]

The TLS sink is a non-forwarding CONNECT listener. After starting it, launch
the locally installed official language_server with endpoint overrides that
point to the sink, then run report-tls with --sni cloudcode-pa.googleapis.com.
The HTTP sink records only redacted headers and JSON metadata.
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "error: required command not found: $1" >&2; exit 2; }
}

ide_version() {
  require_cmd plutil
  plutil -extract CFBundleShortVersionString raw -o - "$APP_PATH/Contents/Info.plist"
}

pinned_ide_version() {
  sed -n 's/^[[:space:]]*DefaultIDEUserAgentVersion = "\([^"]*\)".*/\1/p' \
    "$REPO_ROOT/backend/internal/pkg/antigravity/oauth.go" | head -n 1
}

check_version() {
  local installed pinned
  installed="$(ide_version)"
  pinned="$(pinned_ide_version)"
  echo "installed=$installed"
  echo "pinned=$pinned"
  if [[ "$installed" != "$pinned" ]]; then
    echo "DRIFT: set ANTIGRAVITY_IDE_VERSION=$installed for a hot update, then review the profile evidence" >&2
    return 1
  fi
  echo "MATCH: official IDE package version agrees with TokenKey pin"
}

serve_tls() {
  require_cmd python3
  local out_dir="${TOKENKEY_AG_IDE_CAPTURE_DIR:-$REPO_ROOT/.antigravity_ide_fp}" port=18080 seconds=120
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --out-dir) out_dir="$2"; shift 2 ;;
      --port) port="$2"; shift 2 ;;
      --seconds) seconds="$2"; shift 2 ;;
      *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
    esac
  done
  exec python3 "$SCRIPT_DIR/capture_official_ls_clienthello.py" serve \
    --out-dir "$out_dir" --port "$port" --duration "$seconds"
}

report_tls() {
  require_cmd python3
  local capture_dir="" out="" sni="cloudcode-pa.googleapis.com"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --capture-dir) capture_dir="$2"; shift 2 ;;
      --out) out="$2"; shift 2 ;;
      --sni) sni="$2"; shift 2 ;;
      *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
    esac
  done
  [[ -n "$capture_dir" ]] || { echo "--capture-dir is required" >&2; exit 2; }
  local args=(report --capture-dir "$capture_dir" --sni "$sni")
  [[ -n "$out" ]] && args+=(--out "$out")
  exec python3 "$SCRIPT_DIR/capture_official_ls_clienthello.py" "${args[@]}"
}

serve_http() {
  require_cmd python3
  local out="" port=18081 seconds=120
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --out) out="$2"; shift 2 ;;
      --port) port="$2"; shift 2 ;;
      --seconds) seconds="$2"; shift 2 ;;
      *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
    esac
  done
  [[ -n "$out" ]] || { echo "--out is required" >&2; exit 2; }
  exec python3 "$SCRIPT_DIR/capture_official_ls_http.py" serve \
    --out "$out" --port "$port" --duration "$seconds"
}

case "${1:-}" in
  check-version) shift; check_version "$@" ;;
  serve-tls) shift; serve_tls "$@" ;;
  report-tls) shift; report_tls "$@" ;;
  serve-http) shift; serve_http "$@" ;;
  -h|--help|"") usage ;;
  *) echo "unknown command: $1" >&2; usage; exit 2 ;;
esac
