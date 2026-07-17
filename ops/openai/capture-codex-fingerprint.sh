#!/usr/bin/env bash
# Capture the locally-installed Codex CLI fingerprint and diff it against the
# TokenKey OpenAI-platform constants.
#
# Why no mitmproxy/pcap (vs cc / kiro / antigravity): the Codex CLI ships its
# fingerprint locally, so the on-wire identity is read straight off the installed
# binary (`codex --version` + native-binary strings). There is no client traffic
# to intercept — the load-bearing signal is the codex VERSION embedded in the UA /
# `version` header / probe header, plus the stable non-version pins
# (originator=codex_cli_rs, OpenAI-Beta: responses=experimental).
#
# Deterministic parse / diff / gate is delegated to capture_codex_fingerprint.py;
# this shell is only a thin dispatcher.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PY="$SCRIPT_DIR/capture_codex_fingerprint.py"

usage() {
  cat <<'EOF'
Usage:
  capture-codex-fingerprint.sh check env          # codex CLI present + binary locatable
  capture-codex-fingerprint.sh show-baseline      # TK pins + installed codex version
  capture-codex-fingerprint.sh diff               # installed vs each TK pin (human)
  capture-codex-fingerprint.sh check              # diff + exit 1 on drift / 2 on env error
  capture-codex-fingerprint.sh check-consistency  # 5 TK pins agree among themselves (no CLI needed)
  capture-codex-fingerprint.sh emit-edits [--version X.Y.Z] [--json]

Exit codes: 0 = aligned, 1 = drift / inconsistency, 2 = usage/env error.
EOF
}

require_py() { command -v python3 >/dev/null 2>&1 || { echo "error: python3 not found" >&2; exit 2; }; }

main() {
  local cmd="${1:-}"
  shift || true  # preflight-allow: swallow (no-arg invocation -> nothing to shift)
  case "$cmd" in
    check)
      require_py
      if [[ "${1:-}" == "env" ]]; then exec python3 "$PY" check-env; else exec python3 "$PY" check "$@"; fi ;;
    show-baseline)     require_py; exec python3 "$PY" show-baseline "$@" ;;
    diff)              require_py; exec python3 "$PY" diff "$@" ;;
    check-consistency) require_py; exec python3 "$PY" check-consistency "$@" ;;
    emit-edits)        require_py; exec python3 "$PY" emit-edits "$@" ;;
    -h|--help|"")      usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 2 ;;
  esac
}

main "$@"
