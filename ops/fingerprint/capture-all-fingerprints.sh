#!/usr/bin/env bash
# Umbrella orchestrator: run BOTH client-fingerprint capture engines (Claude Code
# and Kiro IDE) and print one combined drift report, so a single alignment pass
# covers every platform and feeds ONE PR.
#
# The two engines stay independent BY NECESSITY — they capture differently:
#   - Claude Code (ops/anthropic/capture-cc-fingerprint.sh): active redirect of cc
#     to a self-hosted TLS collector + cc0 proxy MITM. Needs the cc0 stack up.
#   - Kiro IDE     (ops/kiro/capture-kiro-fingerprint.sh):    passive pcap, because
#     the CodeWhisperer endpoint is hard-coded and cannot be redirected. Needs sudo
#     tcpdump + a real Kiro IDE request triggered in the window.
# This umbrella only SEQUENCES them and AGGREGATES the result; it does not merge
# their capture mechanics. Exit non-zero if any engine reports actionable drift,
# so CI / a wrapper skill can gate a single combined PR.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CC="$REPO_ROOT/ops/anthropic/capture-cc-fingerprint.sh"
KIRO="$REPO_ROOT/ops/kiro/capture-kiro-fingerprint.sh"

SKIP_CC=0
SKIP_KIRO=0
CC_ARGS=()
KIRO_ARGS=()

usage() {
  cat <<'EOF'
Usage:
  capture-all-fingerprints.sh [--skip-cc] [--skip-kiro]
                              [--cc-arg ARG]... [--kiro-arg ARG]...

Runs each platform's capture+diff and prints a combined drift table. Common args:
  --kiro-arg --proxy-port --kiro-arg 7890   # Kiro IDE behind a system proxy (Clash)
  --cc-arg --http                           # cc: also capture HTTP headers
  --skip-cc / --skip-kiro                   # run only one engine

Per-engine prerequisites are unchanged (cc0 stack for cc; sudo + a real Kiro IDE
request for kiro). On drift, refresh BOTH platforms' artifacts and open ONE PR
(see .cursor/skills/tokenkey-fingerprint-alignment-all).

Exit: 0 = all aligned/skipped, 1 = at least one engine drifted, 2 = a run errored.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-cc) SKIP_CC=1; shift ;;
    --skip-kiro) SKIP_KIRO=1; shift ;;
    --cc-arg) CC_ARGS+=("$2"); shift 2 ;;
    --kiro-arg) KIRO_ARGS+=("$2"); shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

# status codes per engine: aligned | drift | skipped | error
CC_STATUS="skipped"
KIRO_STATUS="skipped"

# Run cc.
if [[ "$SKIP_CC" == "0" ]]; then
  echo "############ Claude Code (anthropic) ############"
  # ${CC_ARGS[@]+...} keeps the empty-array expansion safe under `set -u` on
  # macOS's default bash 3.2 (a bare "${CC_ARGS[@]}" aborts with "unbound variable").
  bash "$CC" capture ${CC_ARGS[@]+"${CC_ARGS[@]}"}
  rc=$?
  case "$rc" in
    0) CC_STATUS="aligned" ;;
    1) CC_STATUS="drift" ;;
    *) CC_STATUS="error" ;;
  esac
fi

# Run kiro.
if [[ "$SKIP_KIRO" == "0" ]]; then
  echo "############ Kiro IDE (kiro) ############"
  bash "$KIRO" capture ${KIRO_ARGS[@]+"${KIRO_ARGS[@]}"}
  rc=$?
  case "$rc" in
    0) KIRO_STATUS="aligned" ;;
    1) KIRO_STATUS="drift" ;;
    *) KIRO_STATUS="error" ;;
  esac
fi

echo ""
echo "================ combined fingerprint drift report ================"
printf "  %-14s %s\n" "claude-code" "$CC_STATUS"
printf "  %-14s %s\n" "kiro"        "$KIRO_STATUS"
echo "==================================================================="

overall=0
for st in "$CC_STATUS" "$KIRO_STATUS"; do
  case "$st" in
    drift) overall=1 ;;
    error) [[ "$overall" -eq 0 ]] && overall=2 ;;
  esac
done

if [[ "$overall" -eq 1 ]]; then
  echo "→ drift detected. Refresh the drifted platform(s)' artifacts and open ONE PR"
  echo "  covering both (see tokenkey-fingerprint-alignment-all skill). cc drift edits"
  echo "  constants.go / *-mimicry-baselines.json / tk_canonical_cc_oauth.json; kiro"
  echo "  drift re-runs ops/kiro emit-profile -> tk_canonical_kiro_ide.json."
elif [[ "$overall" -eq 0 ]]; then
  echo "→ all engines aligned (or skipped). Nothing to commit."
fi
exit "$overall"
