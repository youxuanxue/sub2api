#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PY="$SCRIPT_DIR/capture_grok_fingerprint.py"
usage() {
  cat <<'EOF'
Usage:
  capture-grok-fingerprint.sh check env
  capture-grok-fingerprint.sh show-baseline
  capture-grok-fingerprint.sh diff
  capture-grok-fingerprint.sh check
  capture-grok-fingerprint.sh capture [--out-dir DIR]
EOF
}
require_py() { command -v python3 >/dev/null 2>&1 || { echo "error: python3 not found" >&2; exit 2; }; }
main() {
  local cmd="${1:-}"
  shift || true
  case "$cmd" in
    check)
      require_py
      if [[ "${1:-}" == "env" ]]; then exec python3 "$PY" check-env; else exec python3 "$PY" check "$@"; fi ;;
    show-baseline) require_py; exec python3 "$PY" show-baseline "$@" ;;
    diff)          require_py; exec python3 "$PY" diff "$@" ;;
    capture)       require_py; exec python3 "$PY" capture "$@" ;;
    -h|--help|"")  usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 2 ;;
  esac
}
main "$@"
