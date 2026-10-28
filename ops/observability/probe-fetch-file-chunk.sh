#!/usr/bin/env bash
# Read one slice of a single-account ladder request log. Stdout is one JSON
# object: offset, n, b64. The path is restricted so this cannot read secrets.
set -euo pipefail

FILE="${FILE:?FILE required}"
OFFSET="${OFFSET:-0}"
MAX="${MAX:-9000}"
case "$FILE" in
  /tmp/tk-single-acct-ladder-*.jsonl) ;;
  *)
    echo '{"error":"refused path"}' >&2
    exit 2
    ;;
esac
case "$OFFSET" in
  ''|*[!0-9]*) echo '{"error":"bad OFFSET"}' >&2; exit 2 ;;
esac
case "$MAX" in
  ''|*[!0-9]*) echo '{"error":"bad MAX"}' >&2; exit 2 ;;
esac
if [[ "$MAX" -lt 1 || "$MAX" -gt 9000 ]]; then
  echo '{"error":"MAX must be 1..9000"}' >&2
  exit 2
fi
export FILE OFFSET MAX
python3 - <<'PY'
import base64
import json
import os

path = os.environ["FILE"]
off = int(os.environ["OFFSET"])
n = int(os.environ["MAX"])
with open(path, "rb") as handle:
    handle.seek(off)
    blob = handle.read(n)
print(json.dumps({"offset": off, "n": len(blob), "b64": base64.b64encode(blob).decode("ascii")}))
PY
