#!/bin/bash
# probe-tail-gateway-logs.sh — tail recent TokenKey gateway "http request completed"
# lines from docker logs, sanitize, emit JSON array. Read-only; runs on host via SSM.
#
# Env:
#   LIMIT       max rows (default 50)
#   SINCE       docker logs --since (default 24h)
#   CONTAINER   gateway container (default tokenkey)
set -u

LIMIT="${LIMIT:-50}"
SINCE="${SINCE:-24h}"
CONTAINER="${CONTAINER:-tokenkey}"

python3 - "$LIMIT" "$SINCE" "$CONTAINER" <<'PY'
import json
import re
import subprocess
import sys

limit = int(sys.argv[1])
since = sys.argv[2]
container = sys.argv[3]

marker = "http request completed"
json_re = re.compile(r"\{.*\}\s*$")

proc = subprocess.run(
    ["docker", "logs", container, "--since", since],
    capture_output=True,
    text=True,
    check=False,
)
if proc.returncode != 0:
    print(json.dumps({"error": proc.stderr.strip() or "docker logs failed"}))
    sys.exit(1)

rows = []
for line in proc.stdout.splitlines():
    if marker not in line:
        continue
    m = json_re.search(line)
    if not m:
        continue
    try:
        obj = json.loads(m.group(0))
    except json.JSONDecodeError:
        continue
    # Redact / trim fields that may carry secrets or huge payloads
    safe = {}
    for k in (
        "path",
        "model",
        "status_code",
        "latency_ms",
        "completed_at",
        "request_id",
        "client_request_id",
        "platform",
        "account_id",
        "group_id",
        "user_id",
        "api_key_id",
        "method",
        "upstream_status_code",
        "error_kind",
        "billing_platform",
    ):
        if k in obj and obj[k] is not None and obj[k] != "":
            safe[k] = obj[k]
    rows.append(safe)

tail = rows[-limit:] if len(rows) > limit else rows
out = {
    "meta": {
        "container": container,
        "since": since,
        "limit": limit,
        "matched_total": len(rows),
        "returned": len(tail),
    },
    "requests": tail,
}
print(json.dumps(out, indent=2, sort_keys=True))
PY
