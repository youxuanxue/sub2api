#!/usr/bin/env bash
# Advisory first-output timeout env check for prod via run-probe.
# Drift → ::warning::; exit 0 always (except usage).
#
# Usage: ops/stage0/assert-first-output-timeouts.sh --target prod [--comment ...]
set -euo pipefail

TARGET=""
COMMENT="stage0 first-output timeout assert"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) TARGET="${2:-}"; shift 2 ;;
    --comment) COMMENT="${2:-}"; shift 2 ;;
    -h|--help)
      sed -n '2,6p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "usage: $0 --target prod [--comment ...]" >&2
      exit 2
      ;;
  esac
done

[[ -n "$TARGET" ]] || { echo "usage: $0 --target prod [--comment ...]" >&2; exit 2; }
case "$TARGET" in
  prod) ;;
  *) echo "unsupported --target $TARGET (expected prod)" >&2; exit 2 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
tmp_err="$(mktemp)"
trap 'rm -f "$tmp_err"' EXIT

set +e
probe_out="$(bash "$REPO_ROOT/ops/observability/run-probe.sh" \
  --target "$TARGET" \
  --script "$REPO_ROOT/ops/observability/probe-first-output-timeouts.sh" \
  --comment "$COMMENT" \
  --timeout-seconds "${SSM_TIMEOUT_SECONDS:-120}" \
  2>"$tmp_err")"
probe_rc=$?
set -e

if [[ ${probe_rc} -ne 0 ]]; then
  echo "::warning::first-output assert transport failed (rc=${probe_rc}) target=${TARGET}; skipping verdict"
  [[ -s "$tmp_err" ]] && sed 's/^/[assert-first-output] /' "$tmp_err" >&2 || true
  exit 0
fi

# --require-env "" skips prod-only QA keys; EXPECTED_ENV defaults to SSOT.
set +e
verdict_out="$(printf '%s\n' "${probe_out}" | python3 "$SCRIPT_DIR/live_host_state_verdict.py" --require-env "")"
verdict_rc=$?
set -e

echo "${verdict_out}"
if [[ ${verdict_rc} -ne 0 ]]; then
  while IFS= read -r line; do
    [[ "${line}" == "  - "* ]] && echo "::warning::first-output drift target=${TARGET}:${line#  -}"
  done <<< "${verdict_out}"
fi
exit 0
