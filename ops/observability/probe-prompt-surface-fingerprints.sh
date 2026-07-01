#!/bin/bash
# probe-prompt-surface-fingerprints.sh — tail gateway.anthropic_prompt_fingerprint
# log lines from docker logs (read-only; runs on host via SSM).
#
# Env:
#   LIMIT       max rows (default 200)
#   SINCE       docker logs --since (default 24h)
#   CONTAINER   gateway container (default auto; same as probe-tail-gateway-logs.sh)
set -u

LIMIT="${LIMIT:-200}"
SINCE="${SINCE:-24h}"
CONTAINER="${CONTAINER:-auto}"
ACTIVE_COLOR_FILE="${ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"
MARKER="${MARKER:-gateway.anthropic_prompt_fingerprint}"

python3 - "$LIMIT" "$SINCE" "$CONTAINER" "$ACTIVE_COLOR_FILE" "$MARKER" <<'PY'
import json
import pathlib
import re
import subprocess
import sys

limit = int(sys.argv[1])
since = sys.argv[2]
container_arg = sys.argv[3]
active_color_file = sys.argv[4]
marker = sys.argv[5]

json_re = re.compile(r"\{.*\}\s*$")


def docker_inspect_exists(name):
    proc = subprocess.run(
        ["docker", "inspect", name, "--format", "{{.Name}}"],
        capture_output=True,
        text=True,
        check=False,
    )
    return proc.returncode == 0


def resolve_container(container):
    notes = []
    if container != "auto":
        return container, ["explicit"]
    path = pathlib.Path(active_color_file)
    if path.is_file():
        color = path.read_text(encoding="utf-8", errors="ignore").strip()
        notes.append(f"active-color={color or '<empty>'}")
        if color in ("blue", "green"):
            candidate = f"tokenkey-{color}"
            if docker_inspect_exists(candidate):
                return candidate, notes + ["active-color container exists"]
            notes.append(f"{candidate} missing")
    else:
        notes.append("active-color missing")
    for candidate in ("tokenkey", "tokenkey-blue", "tokenkey-green"):
        if docker_inspect_exists(candidate):
            return candidate, notes + [f"fallback={candidate}"]
    return "tokenkey", notes + ["fallback=tokenkey-unverified"]


container, resolution = resolve_container(container_arg)
proc = subprocess.run(
    ["docker", "logs", container, "--since", since],
    capture_output=True,
    text=True,
    check=False,
)
if proc.returncode != 0:
    print(json.dumps({
        "error": proc.stderr.strip() or "docker logs failed",
        "container": container,
        "fingerprints": [],
    }))
    raise SystemExit(0)

rows = []
for line in proc.stdout.splitlines():
    if marker not in line:
        continue
    m = json_re.search(line)
    if not m:
        continue
    try:
        payload = json.loads(m.group(0))
    except json.JSONDecodeError:
        continue
    rows.append(payload)
    if len(rows) >= limit:
        break

print(json.dumps({
    "meta": {
        "container": container,
        "container_resolution": resolution,
        "since": since,
        "marker": marker,
        "count": len(rows),
    },
    "fingerprints": rows,
}, ensure_ascii=False))
PY
