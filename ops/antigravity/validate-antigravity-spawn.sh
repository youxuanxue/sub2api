#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PY="$SCRIPT_DIR/validate_antigravity_spawn.py"
usage() {
  cat <<'EOF'
Usage:
  validate-antigravity-spawn.sh check env   # IDE + language_server running
  validate-antigravity-spawn.sh check       # spawn args vs TK constants (no mitm)
  validate-antigravity-spawn.sh capture     # check + write .cache/fingerprint/antigravity-spawn/*.json

Use when mitmproxy cannot intercept the IDE (language_server direct-dials Google).
See docs/antigravity-fingerprint-changelog.md (2026-06-12 ide-validate).
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
    capture) require_py; exec python3 "$PY" capture "$@" ;;
    -h|--help|"") usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 2 ;;
  esac
}
main "$@"
