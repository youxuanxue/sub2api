#!/usr/bin/env bash
# Client release discovery — compare upstream semver to TokenKey pins, then route
# drift to the matching fingerprint-alignment skill (capture/diff/PR stay in skills).
#
# Layer 1 (this script): version watch only — no capture, no pin bump.
# Layer 2 (skills): tokenkey-*-fingerprint-alignment / capture-all-fingerprints.sh.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PY="$REPO_ROOT/scripts/fingerprint/client_release_watch.py"

usage() {
  cat <<'EOF'
Usage:
  client-release-watch.sh [scan] [--plan] [--quiet]   # poll upstream + report (default)
  client-release-watch.sh plan [--report-json PATH]    # skill routing from last/new scan
  client-release-watch.sh selftest                      # engine self-test

Workflow:
  1. Run scan (optionally with --plan) to see upstream-ahead platforms.
  2. In Cursor, load the printed skill (e.g. tokenkey-kiro-fingerprint-alignment).
  3. Run that skill's capture commands — never bump pins from release metadata alone.

Exit: 0 = aligned, 1 = upstream ahead of pin (load skill), 2 = usage/env error.
EOF
}

cmd="${1:-scan}"
shift || true

case "$cmd" in
  scan)
    plan=0
    args=()
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --plan) plan=1; shift ;;
        *) args+=("$1"); shift ;;
      esac
    done
    if ((plan)); then
      args+=(--plan)
    fi
    python3 "$PY" "${args[@]}"
    ;;
  plan)
    report="$REPO_ROOT/.cache/fingerprint/client-release-watch/report.json"
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --report-json) report="$2"; shift 2 ;;
        *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
      esac
    done
    if [[ ! -f "$report" ]]; then
      echo "missing report: $report (run scan first)" >&2
      exit 2
    fi
    python3 "$PY" --plan-only "$report"
    ;;
  selftest)
    python3 "$PY" --selftest "$@"
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    echo "unknown command: $cmd" >&2
    usage
    exit 2
    ;;
esac
