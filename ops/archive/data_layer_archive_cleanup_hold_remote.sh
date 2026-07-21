#!/usr/bin/env bash
set -euo pipefail

REMOTE_COMMAND="${REMOTE_COMMAND:?REMOTE_COMMAND is required}"
REMOTE_ARGS_JSON="${REMOTE_ARGS_JSON:-[]}"
exec python3 - "$REMOTE_COMMAND" "$REMOTE_ARGS_JSON" <<'PY'
import json
import os
import sys

command = sys.argv[1]
value = json.loads(sys.argv[2])
if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
    raise SystemExit("REMOTE_ARGS_JSON must be a JSON string array")
os.execv(
    sys.executable,
    [sys.executable, "/tmp/data_layer_archive_cleanup_hold_remote.py", command, *value],
)
PY
