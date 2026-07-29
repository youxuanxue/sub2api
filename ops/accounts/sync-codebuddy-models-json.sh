#!/usr/bin/env bash
# Sync CodeBuddy / WorkBuddy models.json from TokenKey universal Gateway Key (TK_FULLTEST_KEY).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$SCRIPT_DIR/sync-codebuddy-models-json.py" "$@"
