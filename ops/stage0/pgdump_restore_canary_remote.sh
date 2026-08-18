#!/usr/bin/env bash
set -euo pipefail

TARGET="${CANARY_TARGET:-}"
[ -n "${TARGET}" ] || { echo "pgdump restore canary: CANARY_TARGET is required" >&2; exit 2; }

exec python3 /tmp/pgdump_restore_canary.py --target "${TARGET}"
