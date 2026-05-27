#!/usr/bin/env bash
# Daily cc TLS drift check (sessionStart hook). TLS-only capture; opens PR on ja3 mismatch.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CAPTURE_SH="$SCRIPT_DIR/capture-cc-fingerprint.sh"
PY="$SCRIPT_DIR/capture_cc_fingerprint.py"
PR_SH="$SCRIPT_DIR/cc_fingerprint_open_tls_drift_pr.sh"
OUT_DIR="${TOKENKEY_CC_CAPTURE_OUT_DIR:-$REPO_ROOT/.tls_list}"
# STATE_FILE lives outside any single worktree so the once-per-UTC-day lock
# is shared across multiple sub2api checkouts / worktrees on the same host.
STATE_DIR="${TOKENKEY_CC_DAILY_STATE_DIR:-$HOME/.cache/tokenkey}"
STATE_FILE="$STATE_DIR/cc-fingerprint-daily-last"
LOG_FILE="$OUT_DIR/cc-fingerprint-daily-hook.log"
ALERT_FILE="$OUT_DIR/cc-fingerprint-drift-alert.json"

log() {
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*" | tee -a "$LOG_FILE"
}

mkdir -p "$OUT_DIR" "$STATE_DIR"
: >>"$LOG_FILE"

if [[ "$(uname -s)" != "Darwin" ]]; then
  log "skip: macOS-only (cc0-here / Claude Desktop)"
  exit 0
fi

if [[ ! -f "$REPO_ROOT/scripts/preflight.sh" && ! -f "$REPO_ROOT/dev-rules/templates/preflight.sh" ]]; then
  log "skip: not sub2api repo root ($REPO_ROOT)"
  exit 0
fi

today="$(date -u +%Y-%m-%d)"
if [[ "${TOKENKEY_CC_DAILY_FORCE:-}" != "1" ]] && [[ -f "$STATE_FILE" ]] && [[ "$(cat "$STATE_FILE")" == "$today" ]]; then
  log "skip: already ran today ($today); set TOKENKEY_CC_DAILY_FORCE=1 to override"
  exit 0
fi

[[ -f "${HOME}/.config/cc0/env" ]] && # shellcheck disable=SC1091
  source "${HOME}/.config/cc0/env"

relax="${TOKENKEY_CC_DAILY_RELAX_DESKTOP:-1}"
skip_egress="${TOKENKEY_CC_DAILY_SKIP_EGRESS:-0}"
env_flags=()
[[ "$relax" == "1" ]] && env_flags+=(--relax-desktop)
[[ "$skip_egress" == "1" ]] && env_flags+=(--skip-egress)

log "check env (${env_flags[*]})"
if ! bash "$CAPTURE_SH" check env "${env_flags[@]}" >>"$LOG_FILE" 2>&1; then
  log "FAIL: check-env — start cc0-here / claude0-here stack before daily capture"
  printf '%s\n' "$today" >"$STATE_FILE"
  exit 0
fi

log "capture (TLS only)"
if ! bash "$CAPTURE_SH" capture >>"$LOG_FILE" 2>&1; then
  log "FAIL: TLS capture"
  printf '%s\n' "$today" >"$STATE_FILE"
  exit 0
fi

bundle="$(ls -t "$OUT_DIR"/*-cc-capture.bundle.json 2>/dev/null | head -1 || true)"  # preflight-allow: swallow
if [[ -z "$bundle" ]]; then
  log "FAIL: no bundle under $OUT_DIR"
  printf '%s\n' "$today" >"$STATE_FILE"
  exit 0
fi

log "check-tls bundle=$bundle"
if python3 "$PY" check-tls --bundle "$bundle" --json >"$ALERT_FILE" 2>>"$LOG_FILE"; then
  log "OK: TLS matches TokenKey baseline"
  rm -f "$ALERT_FILE"
  printf '%s\n' "$today" >"$STATE_FILE"
  exit 0
fi

log "TLS drift detected — opening PR"
if bash "$PR_SH" "$bundle" >>"$LOG_FILE" 2>&1; then
  log "PR workflow finished"
else
  log "WARN: PR workflow failed (see $LOG_FILE)"
fi

printf '%s\n' "$today" >"$STATE_FILE"
exit 0
