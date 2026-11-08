#!/usr/bin/env bash
# Capture the locally-installed Codex CLI fingerprint and diff it against the
# TokenKey OpenAI-platform constants.
#
# Two evidence lanes, each with its own Python owner:
#   * HTTP identity (check/diff/emit-edits): read off the installed binary
#     (`codex --version` + native-binary strings): the codex VERSION in the UA /
#     `version` header plus the non-version pins (originator=codex-tui,
#     OpenAI-Beta: responses=experimental). Owner: capture_codex_fingerprint.py.
#   * TLS / HTTP-2 wire (tls-*): the real codex talks to a LOCAL CONNECT+TLS
#     capture endpoint (throwaway CODEX_HOME, dummy key, per-run local CA; CONNECT
#     is answered locally, nothing leaves the machine) and the ClientHello /
#     SETTINGS are diffed against deploy/aws/stage0/tk_canonical_codex_cli.json.
#     Owner: codex_tls_fingerprint.py (reuses the ops/kiro ClientHello engine).
#
# This shell is only a thin dispatcher.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PY="$SCRIPT_DIR/capture_codex_fingerprint.py"
TLS_PY="$SCRIPT_DIR/codex_tls_fingerprint.py"

usage() {
  cat <<'EOF'
Usage:
  capture-codex-fingerprint.sh check env          # codex CLI present + binary locatable
  capture-codex-fingerprint.sh show-baseline      # TK owner/aliases + installed codex version
  capture-codex-fingerprint.sh diff               # installed vs owner/aliases (human)
  capture-codex-fingerprint.sh check              # diff + exit 1 on drift / 2 on env error
  capture-codex-fingerprint.sh check-consistency  # version owner/aliases agree (no CLI needed)
  capture-codex-fingerprint.sh emit-edits [--version X.Y.Z] [--json]
  capture-codex-fingerprint.sh tls-capture [--samples N] [--out-dir DIR]
                                  # real codex -> local TLS endpoint -> bundle + check
  capture-codex-fingerprint.sh tls-check --bundle PATH [--json]
                                  # aligned | version_only | tls_drift | invalid
  capture-codex-fingerprint.sh tls-emit-profile --bundle PATH [--out PATH]

tls-check verdicts: aligned (0); version_only = codex version moved, TLS runtime
digest and wire unchanged (1, no migration); tls_drift = runtime digest or
HTTP/2 / WSS wire changed (1; a digest change needs a NEW forward migration +
changelog row); invalid (2). Bundles go to .codex_tls/ (git-ignored).

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
    tls-capture)       require_py; exec python3 "$TLS_PY" capture "$@" ;;
    tls-check)         require_py; exec python3 "$TLS_PY" check "$@" ;;
    tls-emit-profile)  require_py; exec python3 "$TLS_PY" emit-profile "$@" ;;
    -h|--help|"")      usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 2 ;;
  esac
}

main "$@"
