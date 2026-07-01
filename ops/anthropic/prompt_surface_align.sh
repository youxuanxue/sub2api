#!/usr/bin/env bash
# Prompt-surface alignment: geo-stego probe + system anchors + gateway coverage.
# Superset of cc_geo_stego_align.sh; wired into capture-cc-fingerprint.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROBE="$SCRIPT_DIR/probe_cc_geo_stego_direct.sh"
ANALYZE="$SCRIPT_DIR/probe_prompt_surfaces.py"

usage() {
  cat <<'EOF'
Usage:
  prompt_surface_align.sh run [--out-dir DIR] [--stamp STAMP] [--fix] [--json]
  prompt_surface_align.sh check-gateway --jsonl PATH [--fix]
  prompt_surface_align.sh check-registry
  prompt_surface_align.sh check-fixture-gateway

Environment:
  TOKENKEY_CC_CAPTURE_GEO=0          skip when invoked from capture parent
  TOKENKEY_CC_CAPTURE_GEO_FIX=1      attempt mechanical --fix (default 1)
  TOKENKEY_PROMPT_SURFACE_ALIGN_SKIP=1  exit 0 without running
EOF
}

require_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "need $1" >&2; exit 1; }; }

resolve_claude_bin() {
  if [[ -n "${CLAUDE_BIN:-}" && -x "${CLAUDE_BIN}" ]]; then
    echo "$CLAUDE_BIN"
    return
  fi
  if [[ -x "${HOME}/.local/bin/claude" ]]; then
    echo "${HOME}/.local/bin/claude"
    return
  fi
  if command -v claude >/dev/null 2>&1; then
    echo "claude"
    return
  fi
  return 1
}

cmd_run() {
  local out_dir="${REPO_ROOT}/.tls_list"
  local stamp=""
  local do_fix=0
  local json_out=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --out-dir) out_dir="$2"; shift 2 ;;
      --stamp) stamp="$2"; shift 2 ;;
      --fix) do_fix=1; shift ;;
      --json) json_out=1; shift ;;
      *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
    esac
  done

  if [[ "${TOKENKEY_PROMPT_SURFACE_ALIGN_SKIP:-0}" == "1" || "${TOKENKEY_CC_GEO_ALIGN_SKIP:-0}" == "1" ]]; then
    echo "SKIP: prompt surface align disabled"
    exit 0
  fi

  if ! resolve_claude_bin >/dev/null 2>&1; then
    echo "SKIP: claude CLI not found"
    exit 0
  fi
  require_cmd mitmdump
  require_cmd python3
  if [[ ! -f "${HOME}/.mitmproxy/mitmproxy-ca-cert.pem" ]]; then
    echo "SKIP: mitm CA missing"
    exit 0
  fi

  if [[ -z "$stamp" ]]; then
    stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  fi
  mkdir -p "$out_dir"
  export TOKENKEY_CC_GEO_PROBE_OUT="$out_dir/prompt-surface-${stamp}"

  echo "=== prompt surface align: probe (claude CLI + mitm) ===" >&2
  bash "$PROBE"

  local log="$TOKENKEY_CC_GEO_PROBE_OUT/capture.jsonl"
  if [[ ! -s "$log" ]]; then
    echo "FAIL: probe produced empty capture.jsonl" >&2
    exit 1
  fi

  local analyze_args=("$log" "--check-gateway")
  [[ "$do_fix" == "1" || "${TOKENKEY_CC_CAPTURE_GEO_FIX:-1}" == "1" ]] && analyze_args+=("--fix")
  [[ "$json_out" == "1" ]] && analyze_args+=("--json")

  echo "=== prompt surface align: registry ===" >&2
  python3 "$ANALYZE" --check-registry

  echo "=== prompt surface align: gateway coverage ===" >&2
  python3 "$ANALYZE" "${analyze_args[@]}"
}

cmd_check_gateway() {
  local jsonl=""
  local do_fix=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --jsonl) jsonl="$2"; shift 2 ;;
      --fix) do_fix=1; shift ;;
      *) echo "unknown arg: $1" >&2; exit 1 ;;
    esac
  done
  [[ -n "$jsonl" && -f "$jsonl" ]] || { echo "need --jsonl PATH" >&2; exit 1; }
  local args=("$jsonl" "--check-gateway")
  [[ "$do_fix" == "1" ]] && args+=("--fix")
  exec python3 "$ANALYZE" "${args[@]}"
}

cmd_check_registry() {
  exec python3 "$ANALYZE" --check-registry
}

cmd_check_fixture_gateway() {
  exec python3 "$ANALYZE" --check-fixture-gateway
}

main() {
  local cmd="${1:-run}"
  shift || true  # preflight-allow: swallow
  case "$cmd" in
    run) cmd_run "$@" ;;
    check-gateway) cmd_check_gateway "$@" ;;
    check-registry) cmd_check_registry "$@" ;;
    check-fixture-gateway) cmd_check_fixture_gateway "$@" ;;
    -h|--help|"") usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 1 ;;
  esac
}

main "$@"
