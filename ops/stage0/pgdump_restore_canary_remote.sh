#!/usr/bin/env bash
set -euo pipefail

TARGET="${CANARY_TARGET:-}"
[ -n "${TARGET}" ] || { echo "pgdump restore canary: CANARY_TARGET is required" >&2; exit 2; }

args=()
if [[ "${CANARY_CREATE_DUMP:-0}" == 1 ]]; then
  args+=(--create-dump)
fi

exec python3 /tmp/pgdump_restore_canary.py --target "${TARGET}" "${args[@]}"
