#!/usr/bin/env bash
# Umbrella orchestrator: run BOTH client-fingerprint capture engines (Claude Code
# and Kiro IDE) and print one combined drift report, so a single alignment pass
# covers every platform and feeds ONE PR.
#
# The engines stay independent BY NECESSITY — they capture differently:
#   - Claude Code (ops/anthropic/capture-cc-fingerprint.sh): active redirect of cc
#     to a self-hosted TLS collector + cc0 proxy MITM. Needs the cc0 stack up.
#   - Kiro IDE     (ops/kiro/capture-kiro-fingerprint.sh):    passive pcap, because
#     the CodeWhisperer endpoint is hard-coded and cannot be redirected. Needs sudo
#     tcpdump + a real Kiro IDE request triggered in the window.
#   - Antigravity (ops/antigravity/capture-antigravity-fingerprint.sh): mitmproxy of
#     the real IDE, because the load-bearing dimension is HTTP (UA version incl. the
#     /hub/ subclient segment / body userAgent / ideType metadata; X-Goog-Api-Client
#     gl-node is expected ABSENT post-#756), NOT TLS — cloudcode-pa is
#     hard-coded so it cannot be redirected; the IDE must trust the mitm CA. JA3 is
#     non-load-bearing and never gates here.
#   - Codex (ops/openai/capture-codex-fingerprint.sh): NO live capture at all — the
#     Codex CLI ships its fingerprint locally, so the engine reads the installed
#     binary (`codex --version` + native strings) and diffs against the TK pins. It
#     has no prerequisite stack and never needs an IDE/proxy window, so it always
#     runs. Mechanically it is the odd one out: the gate is `check`, not `capture`.
# This umbrella only SEQUENCES them and AGGREGATES the result; it does not merge
# their capture mechanics. It fails distinctly for drift, invalid evidence/error,
# and incomplete coverage so a wrapper cannot mistake skipped evidence for alignment.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CC="${TOKENKEY_CAPTURE_ALL_CC:-$REPO_ROOT/ops/anthropic/capture-cc-fingerprint.sh}"
KIRO="${TOKENKEY_CAPTURE_ALL_KIRO:-$REPO_ROOT/ops/kiro/capture-kiro-fingerprint.sh}"
ANTIGRAVITY="${TOKENKEY_CAPTURE_ALL_ANTIGRAVITY:-$REPO_ROOT/ops/antigravity/capture-antigravity-fingerprint.sh}"
CODEX="${TOKENKEY_CAPTURE_ALL_CODEX:-$REPO_ROOT/ops/openai/capture-codex-fingerprint.sh}"

SKIP_CC=0
SKIP_KIRO=0
SKIP_ANTIGRAVITY=0
SKIP_CODEX=0
CC_ARGS=()
KIRO_ARGS=()
ANTIGRAVITY_ARGS=()
CODEX_ARGS=()

usage() {
  cat <<'EOF'
Usage:
  capture-all-fingerprints.sh [--skip-cc] [--skip-kiro] [--skip-antigravity] [--skip-codex]
                              [--cc-arg ARG]... [--kiro-arg ARG]... [--antigravity-arg ARG]... [--codex-arg ARG]...

Runs each platform's capture+diff and prints a combined drift table. Common args:
  --kiro-arg --proxy-port --kiro-arg 7890        # Kiro IDE behind a system proxy (Clash)
  --cc-arg --http                                # cc: also capture HTTP headers
  --antigravity-arg --proxy-port --antigravity-arg 8080  # Antigravity IDE mitm proxy port
  --codex-arg ARG                                # forwarded to `codex check` (rarely needed)
  --skip-cc / --skip-kiro / --skip-antigravity / --skip-codex   # run only some engines

Per-engine prerequisites: cc0 stack for cc; sudo + a real Kiro IDE request for
kiro; mitmproxy + a real Antigravity IDE that trusts the mitm CA for antigravity.
Codex has NO prerequisite — it reads the installed Codex CLI binary, so it always
runs (skip it with --skip-codex if codex is not installed). On drift, refresh the
drifted platform(s)' artifacts and open ONE PR
(see .cursor/skills/tokenkey-fingerprint-alignment-all).

Exit: 0 = all required evidence aligned, 1 = drift, 2 = error/invalid evidence,
      3 = incomplete coverage (including explicitly skipped engines).
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-cc) SKIP_CC=1; shift ;;
    --skip-kiro) SKIP_KIRO=1; shift ;;
    --skip-antigravity) SKIP_ANTIGRAVITY=1; shift ;;
    --skip-codex) SKIP_CODEX=1; shift ;;
    --cc-arg) CC_ARGS+=("$2"); shift 2 ;;
    --kiro-arg) KIRO_ARGS+=("$2"); shift 2 ;;
    --antigravity-arg) ANTIGRAVITY_ARGS+=("$2"); shift 2 ;;
    --codex-arg) CODEX_ARGS+=("$2"); shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

# status codes per engine: aligned | drift | incomplete | skipped | error
CC_STATUS="skipped"
KIRO_STATUS="skipped"
ANTIGRAVITY_STATUS="skipped"
CODEX_STATUS="skipped"

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
    3) CC_STATUS="incomplete" ;;
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
    3) KIRO_STATUS="incomplete" ;;
    *) KIRO_STATUS="error" ;;
  esac
fi

# Run antigravity.
if [[ "$SKIP_ANTIGRAVITY" == "0" ]]; then
  echo "############ Antigravity IDE (antigravity) ############"
  bash "$ANTIGRAVITY" capture ${ANTIGRAVITY_ARGS[@]+"${ANTIGRAVITY_ARGS[@]}"}
  rc=$?
  case "$rc" in
    0) ANTIGRAVITY_STATUS="aligned" ;;
    1) ANTIGRAVITY_STATUS="drift" ;;
    3) ANTIGRAVITY_STATUS="incomplete" ;;
    *) ANTIGRAVITY_STATUS="error" ;;
  esac
fi

# Run codex. Unlike the other three, codex has no capture window — the gate is
# `check` (reads the installed Codex CLI binary): 0 aligned / 1 drift / 2 env error.
if [[ "$SKIP_CODEX" == "0" ]]; then
  echo "############ Codex CLI (openai) ############"
  bash "$CODEX" check ${CODEX_ARGS[@]+"${CODEX_ARGS[@]}"}
  rc=$?
  case "$rc" in
    0) CODEX_STATUS="aligned" ;;
    1) CODEX_STATUS="drift" ;;
    3) CODEX_STATUS="incomplete" ;;
    *) CODEX_STATUS="error" ;;
  esac
fi

echo ""
echo "================ combined fingerprint drift report ================"
printf "  %-14s %s\n" "claude-code" "$CC_STATUS"
printf "  %-14s %s\n" "kiro"        "$KIRO_STATUS"
printf "  %-14s %s\n" "antigravity" "$ANTIGRAVITY_STATUS"
printf "  %-14s %s\n" "codex"       "$CODEX_STATUS"
echo "==================================================================="

has_drift=0
has_error=0
has_incomplete=0
for st in "$CC_STATUS" "$KIRO_STATUS" "$ANTIGRAVITY_STATUS" "$CODEX_STATUS"; do
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

if [[ "$overall" -eq 1 ]]; then
  echo "→ drift detected. Refresh the drifted platform(s)' artifacts and open ONE PR"
  echo "  (see tokenkey-fingerprint-alignment-all skill). cc drift edits constants.go /"
  echo "  *-mimicry-baselines.json / tk_canonical_cc_oauth.json; kiro drift re-runs"
  echo "  ops/kiro emit-profile -> tk_canonical_kiro_ide.json; antigravity drift bumps"
  echo "  DefaultUserAgentVersion in internal/pkg/antigravity/oauth.go (+ oauth_test.go);"
  echo "  codex drift runs ops/openai emit-edits to bump the 5 codex version pins."
elif [[ "$overall" -eq 2 ]]; then
  echo "→ an engine ERRORED (rc=2): capture/env failure, NOT fingerprint drift."
  echo "  Common causes: cc0 stack down; kiro got no traffic (sudo + a real Kiro"
  echo "  request needed; Kiro must egress on the captured iface/proxy); Antigravity"
  echo "  not routed through mitm :8080 or not trusting the CA. Fix and re-run."
  echo "  Do NOT refresh any fingerprint artifact on an rc=2 — there is no drift evidence."
elif [[ "$overall" -eq 3 ]]; then
  echo "→ coverage incomplete (rc=3): at least one engine was skipped or lacked valid evidence."
  echo "  Do not claim all fingerprints aligned; collect the missing dimensions and re-run."
else
  echo "→ all required engine evidence is observed and aligned. Nothing to commit."
fi
exit "$overall"
