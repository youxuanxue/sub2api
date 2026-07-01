#!/usr/bin/env bash
# Back-compat wrapper — canonical entry is prompt_surface_align.sh
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
exec bash "$SCRIPT_DIR/prompt_surface_align.sh" "$@"
