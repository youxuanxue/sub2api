#!/usr/bin/env bash
# Remote wrapper for the monthly Kiro recovery workflow.
set -euo pipefail

PYTHON_SCRIPT="${KIRO_MONTHLY_RECOVERY_PY:-/tmp/monthly_recovery.py}"
if [[ ! -f "$PYTHON_SCRIPT" ]]; then
  echo "monthly recovery helper missing: $PYTHON_SCRIPT" >&2
  exit 1
fi

exec python3 "$PYTHON_SCRIPT" "$@"
