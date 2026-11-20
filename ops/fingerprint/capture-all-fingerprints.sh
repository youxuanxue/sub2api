#!/usr/bin/env bash
# Run the independent Claude Code, Kiro CLI, Antigravity, Codex, Gemini CLI,
# and Grok CLI evidence
# engines, then aggregate their exit contracts without merging capture mechanics.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CC="${TOKENKEY_CAPTURE_ALL_CC:-$REPO_ROOT/ops/anthropic/capture-cc-fingerprint.sh}"
KIRO="${TOKENKEY_CAPTURE_ALL_KIRO:-$REPO_ROOT/ops/kiro/capture-kiro-fingerprint.sh}"
ANTIGRAVITY="${TOKENKEY_CAPTURE_ALL_ANTIGRAVITY:-$REPO_ROOT/ops/antigravity/capture-antigravity-fingerprint.sh}"
CODEX="${TOKENKEY_CAPTURE_ALL_CODEX:-$REPO_ROOT/ops/openai/capture-codex-fingerprint.sh}"
GEMINI="${TOKENKEY_CAPTURE_ALL_GEMINI:-$REPO_ROOT/ops/geminicli/capture-gemini-fingerprint.sh}"
GROK="${TOKENKEY_CAPTURE_ALL_GROK:-$REPO_ROOT/ops/xai/capture-grok-fingerprint.sh}"
IDENTITY_REGISTRY_CHECK="$REPO_ROOT/scripts/fingerprint/check_client_identity_registry.py"
IDENTITY_REGISTRY="${TOKENKEY_CAPTURE_ALL_REGISTRY:-$REPO_ROOT/scripts/fingerprint/client_identity_registry.json}"

SKIP_CC=0
SKIP_KIRO=0
SKIP_ANTIGRAVITY=0
SKIP_CODEX=0
SKIP_CODEX_TLS=0
SKIP_GEMINI=0
SKIP_GROK=0
CODEX_TLS_ARGS=()
GEMINI_ARGS=()
GROK_ARGS=()
CC_ARGS=()
KIRO_ARGS=()
ANTIGRAVITY_ARGS=()
CODEX_ARGS=()

usage() {
  cat <<'EOF'
Usage:
  capture-all-fingerprints.sh [--skip-cc] [--skip-kiro] [--skip-antigravity] [--skip-codex]
                              [--skip-gemini] [--skip-grok]
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
  --gemini-arg ARG                 forward an argument to Gemini CLI capture
  --grok-arg ARG                   forward an argument to Grok CLI capture

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
    --skip-gemini) SKIP_GEMINI=1; shift ;;
    --skip-grok) SKIP_GROK=1; shift ;;
    --codex-tls-arg) CODEX_TLS_ARGS+=("$2"); shift 2 ;;
    --cc-arg) CC_ARGS+=("$2"); shift 2 ;;
    --kiro-arg) KIRO_ARGS+=("$2"); shift 2 ;;
    --antigravity-arg) ANTIGRAVITY_ARGS+=("$2"); shift 2 ;;
    --codex-arg) CODEX_ARGS+=("$2"); shift 2 ;;
    --gemini-arg) GEMINI_ARGS+=("$2"); shift 2 ;;
    --grok-arg) GROK_ARGS+=("$2"); shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

CC_STATUS="skipped"
KIRO_STATUS="skipped"
ANTIGRAVITY_STATUS="skipped"
CODEX_STATUS="skipped"
CODEX_TLS_STATUS="skipped"
GEMINI_STATUS="skipped"
GROK_STATUS="skipped"

record_status() {
  case "$1" in
    0) printf '%s' aligned ;;
    1) printf '%s' drift ;;
    3) printf '%s' incomplete ;;
    *) printf '%s' error ;;
  esac
}

# Keep each invocation's engine output as the provenance source for static checks.
mkdir -p "$REPO_ROOT/.cache/fingerprint" || exit 2
CAPTURE_RUN_DIR="$(mktemp -d "$REPO_ROOT/.cache/fingerprint/all.XXXXXX")" || exit 2
run_engine() {
  local identity="$1" started_ns log
  shift
  log="$CAPTURE_RUN_DIR/$identity.log"
  started_ns="$(python3 -c 'import time; print(time.time_ns())')" || return 2
  bash "$@" 2>&1 | tee "$log"
  local codes=("${PIPESTATUS[@]}")
  [[ "${codes[1]}" == "0" ]] || return 2
  echo "engine_log=$log"
  python3 "$SCRIPT_DIR/capture_all_evidence.py" --identity "$identity" \
    --log "$log" --started-ns "$started_ns" --engine-code "${codes[0]}"
}

if [[ "$SKIP_CC" == "0" ]]; then
  echo "############ Claude Code (anthropic) ############"
  run_engine claude-code "$CC" capture ${CC_ARGS[@]+"${CC_ARGS[@]}"}
  CC_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_KIRO" == "0" ]]; then
  echo "############ Kiro CLI (kiro-cli) ############"
  run_engine kiro-cli "$KIRO" capture ${KIRO_ARGS[@]+"${KIRO_ARGS[@]}"}
  KIRO_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_ANTIGRAVITY" == "0" ]]; then
  echo "############ Antigravity CLI (antigravity) ############"
  run_engine antigravity "$ANTIGRAVITY" capture ${ANTIGRAVITY_ARGS[@]+"${ANTIGRAVITY_ARGS[@]}"}
  ANTIGRAVITY_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_CODEX" == "0" ]]; then
  echo "############ Codex CLI (openai) ############"
  run_engine codex "$CODEX" check ${CODEX_ARGS[@]+"${CODEX_ARGS[@]}"}
  CODEX_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_CODEX" == "0" && "$SKIP_CODEX_TLS" == "0" ]]; then
  echo "############ Codex CLI TLS (openai, local capture) ############"
  run_engine codex-tls "$CODEX" tls-capture ${CODEX_TLS_ARGS[@]+"${CODEX_TLS_ARGS[@]}"}
  CODEX_TLS_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_GEMINI" == "0" ]]; then
  echo "############ Gemini CLI (gemini-cli) ############"
  run_engine gemini-cli "$GEMINI" capture ${GEMINI_ARGS[@]+"${GEMINI_ARGS[@]}"}
  GEMINI_STATUS="$(record_status "$?")"
fi

if [[ "$SKIP_GROK" == "0" ]]; then
  echo "############ Grok CLI (grok-cli) ############"
  run_engine grok-cli "$GROK" capture ${GROK_ARGS[@]+"${GROK_ARGS[@]}"}
  GROK_STATUS="$(record_status "$?")"
fi

echo ""
echo "================ combined fingerprint drift report ================"
printf "  %-14s %s\n" "claude-code" "$CC_STATUS"
printf "  %-14s %s\n" "kiro-cli" "$KIRO_STATUS"
printf "  %-14s %s\n" "antigravity" "$ANTIGRAVITY_STATUS"
printf "  %-14s %s\n" "codex" "$CODEX_STATUS"
printf "  %-14s %s\n" "codex-tls" "$CODEX_TLS_STATUS"
printf "  %-14s %s\n" "gemini-cli" "$GEMINI_STATUS"
printf "  %-14s %s\n" "grok-cli" "$GROK_STATUS"
echo "==================================================================="
echo ""
echo "================ identity evidence coverage ======================"
python3 "$IDENTITY_REGISTRY_CHECK" --registry "$IDENTITY_REGISTRY" --coverage || {
  echo "→ identity registry invalid; coverage cannot be trusted." >&2
  exit 2
}
python3 - "$IDENTITY_REGISTRY" \
  "ops/anthropic/capture-cc-fingerprint.sh=$CC_STATUS" \
  "ops/kiro/capture-kiro-fingerprint.sh=$KIRO_STATUS" \
  "ops/antigravity/capture-antigravity-fingerprint.sh=$ANTIGRAVITY_STATUS" \
  "ops/openai/capture-codex-fingerprint.sh=$CODEX_STATUS" \
  "ops/geminicli/capture-gemini-fingerprint.sh=$GEMINI_STATUS" \
  "ops/xai/capture-grok-fingerprint.sh=$GROK_STATUS" <<'PYTHON'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    registry = json.load(source)
results = dict(item.rsplit("=", 1) for item in sys.argv[2:])
identities = {row["id"]: row for row in registry["identities"]}
incomplete = False
for row in identities.values():
    # Companions share their parent's evidence; advisory rows remain visible.
    owner = identities.get(row.get("companion_identity"), row)
    tool = owner.get("capture_tool")
    status = results.get(tool, "incomplete")
    required = row.get("actionable", True)
    if required and status in {"incomplete", "skipped"}:
        incomplete = True
    policy = "required" if required else "suppressed/advisory"
    companion = row.get("companion_identity")
    if companion:
        policy += f", companion of {companion}"
    print(f"  {row['id']}: {status} ({policy}); evidence engine={tool or 'missing'}")
raise SystemExit(3 if incomplete else 0)
PYTHON
IDENTITY_COVERAGE_STATUS="$(record_status "$?")"
echo "==================================================================="

has_drift=0
has_error=0
has_incomplete=0
for st in "$CC_STATUS" "$KIRO_STATUS" "$ANTIGRAVITY_STATUS" "$CODEX_STATUS" "$CODEX_TLS_STATUS" "$GEMINI_STATUS" "$GROK_STATUS" "$IDENTITY_COVERAGE_STATUS"; do
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
