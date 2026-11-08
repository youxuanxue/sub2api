#!/usr/bin/env bash
# Run the independent Claude Code, Kiro CLI, Antigravity, and Codex evidence
# engines, then aggregate their exit contracts without merging capture mechanics.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CC="${TOKENKEY_CAPTURE_ALL_CC:-$REPO_ROOT/ops/anthropic/capture-cc-fingerprint.sh}"
KIRO="${TOKENKEY_CAPTURE_ALL_KIRO:-$REPO_ROOT/ops/kiro/capture-kiro-fingerprint.sh}"
ANTIGRAVITY="${TOKENKEY_CAPTURE_ALL_ANTIGRAVITY:-$REPO_ROOT/ops/antigravity/capture-antigravity-fingerprint.sh}"
CODEX="${TOKENKEY_CAPTURE_ALL_CODEX:-$REPO_ROOT/ops/openai/capture-codex-fingerprint.sh}"
IDENTITY_REGISTRY_CHECK="$REPO_ROOT/scripts/fingerprint/check_client_identity_registry.py"

SKIP_CC=0
SKIP_KIRO=0
SKIP_ANTIGRAVITY=0
SKIP_CODEX=0
SKIP_CODEX_TLS=0
CODEX_TLS_ARGS=()
CC_ARGS=()
KIRO_ARGS=()
ANTIGRAVITY_ARGS=()
CODEX_ARGS=()

usage() {
  cat <<'EOF'
Usage:
  capture-all-fingerprints.sh [--skip-cc] [--skip-kiro] [--skip-antigravity] [--skip-codex]
                              [--cc-arg ARG]... [--kiro-arg ARG]... [--antigravity-arg ARG]... [--codex-arg ARG]...

Runs each platform's evidence engine and prints a combined status table:
  --cc-arg --http                  capture Claude Code HTTP in addition to TLS
  --kiro-arg --samples --kiro-arg 5
                                    run five real kiro-cli samples
  --antigravity-arg --proxy-port --antigravity-arg 8080
                                    route Antigravity through its MITM collector
  --codex-arg ARG                  forward an argument to Codex `check`
  --codex-tls-arg ARG              forward an argument to Codex `tls-capture`
  --skip-codex-tls                 skip the Codex TLS lane (coverage incomplete)

Claude Code needs its cc0 stack; Kiro needs a logged-in real kiro-cli plus
mitmdump and its CA; Antigravity needs its real client routed through mitmproxy;
Codex reads the installed CLI binary, and its TLS lane runs the real codex
against a local capture endpoint (openssl only; no login, nothing leaves the
machine). Explicit skips make coverage incomplete.

Exit: 0 all required evidence aligned, 1 drift, 2 invalid/error, 3 incomplete.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-cc) SKIP_CC=1; shift ;;
    --skip-kiro) SKIP_KIRO=1; shift ;;
    --skip-antigravity) SKIP_ANTIGRAVITY=1; shift ;;
    --skip-codex) SKIP_CODEX=1; shift ;;
    --skip-codex-tls) SKIP_CODEX_TLS=1; shift ;;
    --codex-tls-arg) CODEX_TLS_ARGS+=("$2"); shift 2 ;;
    --cc-arg) CC_ARGS+=("$2"); shift 2 ;;
    --kiro-arg) KIRO_ARGS+=("$2"); shift 2 ;;
    --antigravity-arg) ANTIGRAVITY_ARGS+=("$2"); shift 2 ;;
    --codex-arg) CODEX_ARGS+=("$2"); shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

CC_STATUS="skipped"
KIRO_STATUS="skipped"
ANTIGRAVITY_STATUS="skipped"
CODEX_STATUS="skipped"
CODEX_TLS_STATUS="skipped"

record_status() {
  case "$1" in
    0) printf '%s' aligned ;;
    1) printf '%s' drift ;;
    3) printf '%s' incomplete ;;
    *) printf '%s' error ;;
  esac
}

if [[ "$SKIP_CC" == "0" ]]; then
  echo "############ Claude Code (anthropic) ############"
  bash "$CC" capture ${CC_ARGS[@]+"${CC_ARGS[@]}"}
  CC_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_KIRO" == "0" ]]; then
  echo "############ Kiro CLI (kiro-cli) ############"
  bash "$KIRO" capture ${KIRO_ARGS[@]+"${KIRO_ARGS[@]}"}
  KIRO_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_ANTIGRAVITY" == "0" ]]; then
  echo "############ Antigravity CLI (antigravity) ############"
  bash "$ANTIGRAVITY" capture ${ANTIGRAVITY_ARGS[@]+"${ANTIGRAVITY_ARGS[@]}"}
  ANTIGRAVITY_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_CODEX" == "0" ]]; then
  echo "############ Codex CLI (openai) ############"
  bash "$CODEX" check ${CODEX_ARGS[@]+"${CODEX_ARGS[@]}"}
  CODEX_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_CODEX" == "0" && "$SKIP_CODEX_TLS" == "0" ]]; then
  echo "############ Codex CLI TLS (openai, local capture) ############"
  bash "$CODEX" tls-capture ${CODEX_TLS_ARGS[@]+"${CODEX_TLS_ARGS[@]}"}
  CODEX_TLS_STATUS="$(record_status "$?")"
fi

echo ""
echo "================ combined fingerprint drift report ================"
printf "  %-14s %s\n" "claude-code" "$CC_STATUS"
printf "  %-14s %s\n" "kiro-cli" "$KIRO_STATUS"
printf "  %-14s %s\n" "antigravity" "$ANTIGRAVITY_STATUS"
printf "  %-14s %s\n" "codex" "$CODEX_STATUS"
printf "  %-14s %s\n" "codex-tls" "$CODEX_TLS_STATUS"
echo "==================================================================="
echo ""
echo "================ identity evidence coverage ======================"
python3 "$IDENTITY_REGISTRY_CHECK" --coverage || {
  echo "→ identity registry invalid; coverage cannot be trusted." >&2
  exit 2
}
echo "==================================================================="

has_drift=0
has_error=0
has_incomplete=0
for st in "$CC_STATUS" "$KIRO_STATUS" "$ANTIGRAVITY_STATUS" "$CODEX_STATUS" "$CODEX_TLS_STATUS"; do
  case "$st" in
    drift) has_drift=1 ;;
    error) has_error=1 ;;
    incomplete|skipped) has_incomplete=1 ;;
  esac
done

if [[ "$has_error" -eq 1 ]]; then
  overall=2
elif [[ "$has_drift" -eq 1 ]]; then
  overall=1
elif [[ "$has_incomplete" -eq 1 ]]; then
  overall=3
else
  overall=0
fi

case "$overall" in
  1)
    echo "→ drift detected. Refresh only from the drifted platform's real evidence."
    echo "  Kiro changes come from capture-kiro-fingerprint.sh and update"
    echo "  tk_canonical_kiro_cli.json plus the single CLI runtime owner."
    echo "  Codex TLS drift follows its tls-check verdict: version_only needs no"
    echo "  migration; a runtime digest change needs a new forward migration."
    ;;
  2)
    echo "→ an evidence engine failed. Fix its environment and re-run; do not update"
    echo "  a fingerprint artifact without valid observed evidence."
    ;;
  3)
    echo "→ coverage incomplete. Do not claim all fingerprints aligned."
    ;;
  0)
    echo "→ all required engine evidence is observed and aligned. Nothing to commit."
    ;;
esac
exit "$overall"
