#!/usr/bin/env bash
# Model release discovery — compare upstream public model IDs to TokenKey
# multi-channel SSOT, then open GitHub issues for missing/unpriced/narrow.
#
# Layer 1 (this script): discovery + classification only — no activate/mapping writes.
# Layer 2 (skills): tokenkey-modelops-planner → onboard / catalog refresh.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PY="$REPO_ROOT/ops/pricing/model_release_watch.py"
ISSUES_PY="$REPO_ROOT/ops/pricing/open_model_release_watch_issues.py"
REPORT_JSON="$REPO_ROOT/.cache/model-release-watch/report.json"

usage() {
  cat <<'EOF'
Usage:
  model-release-watch.sh [scan] [--fixture PATH] [--vendors a,b]
                         [--bootstrap-state] [--alert-all] [--quiet]
  model-release-watch.sh issues [--dry-run] [--umbrella]
  model-release-watch.sh selftest

Notes:
  - Empty state auto-bootstraps (seeds seen ids, no issues).
  - Actionable issues require: in-scope generation + newly seen + missing/unpriced/narrow.
  - Google scope: text Flash >= gemini-3.8-flash; Pro >= gemini-3.2-pro
    (3.1-pro* frozen); image >= gemini-3.1-flash-image non-lite
    (+ nano-banana-2 / nano-2 / nano-banana-pro aliases).
  - GitHub issue sync is opt-in (workflow open_issues=true).

Exit: 0 = no actionable findings, 1 = actionable gaps, 2 = usage error.
EOF
}

cmd="${1:-scan}"
shift || true

case "$cmd" in
  scan)
    python3 "$PY" "$@"
    ;;
  issues)
    python3 "$ISSUES_PY" --report-json "$REPORT_JSON" "$@"
    ;;
  selftest)
    cd "$REPO_ROOT"
    python3 "$PY" --selftest
    python3 -m unittest ops.pricing.test_model_release_watch \
      ops.pricing.test_open_model_release_watch_issues -v
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
