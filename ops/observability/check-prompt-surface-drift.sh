#!/usr/bin/env bash
# check-prompt-surface-drift.sh — exit 1 when prod fingerprint aggregate shows drift.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "${SCRIPT_DIR}/prompt_surface_aggregate.py" "$@"
