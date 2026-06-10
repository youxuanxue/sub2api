#!/bin/bash
# probe-post-release-tick.sh — post-release follow-up tick probe (read-only).
#
# Ships to prod/edge via run-probe.sh on each follow-up tick of the
# tokenkey-stage0-release-rollout skill. The generic signals (traffic volume,
# per-path mix, 5xx, panic) are fixed here; the release-specific "new-code
# hook" greps are supplied per release via HOOK_PATTERNS — the model names the
# hooks (judgment), this script counts them (mechanical).
#
# Env (consumed inside the remote shell):
#   SINCE          docker logs --since window (default 6m)
#   CONTAINER      gateway container name (default tokenkey)
#   HOOK_PATTERNS  comma-separated FIXED strings (grep -F semantics), one per
#                  release hook, e.g.:
#                  HOOK_PATTERNS='stripped explicit thinking.type=disabled,pricing_missing'
#                  Empty → hooks section reports none configured.
#
# Output: stable `=== section ===` markers; the traffic section is JSON
# (row_to_json-style) so downstream parsing never relies on column position.
# Request lines are deduplicated by request_id (docker stdout/stderr replay
# previously double-counted paths when streams were naively merged).
set -u

SINCE="${SINCE:-6m}"
CONTAINER="${CONTAINER:-tokenkey}"
HOOK_PATTERNS="${HOOK_PATTERNS:-}"

python3 - "$SINCE" "$CONTAINER" "$HOOK_PATTERNS" <<'PY'
import json
import re
import subprocess
import sys

since, container, hook_patterns_raw = sys.argv[1], sys.argv[2], sys.argv[3]

proc = subprocess.run(
    ["docker", "logs", container, "--since", since],
    capture_output=True,
    text=True,
    check=False,
)
if proc.returncode != 0:
    print(json.dumps({"error": proc.stderr.strip() or "docker logs failed"}))
    sys.exit(1)

# The gateway writes structured logs to one stream, but ops shells have merged
# both in the past; scan both and dedupe exact duplicate lines defensively.
lines = list(dict.fromkeys(proc.stdout.splitlines() + proc.stderr.splitlines()))

print("=== meta ===")
print(json.dumps({"container": container, "since": since, "log_lines": len(lines)}))

print("=== hooks ===")
patterns = [p.strip() for p in hook_patterns_raw.split(",") if p.strip()]
if not patterns:
    print(json.dumps({"note": "no HOOK_PATTERNS configured for this release"}))
for pat in patterns:
    matched = [ln for ln in lines if pat in ln]
    print(json.dumps({"pattern": pat, "count": len(matched)}))

print("=== panic ===")
print(json.dumps({"count": sum(1 for ln in lines if "panic" in ln)}))

marker = "http request completed"
json_re = re.compile(r"\{.*\}\s*$")
seen_request_ids = set()
total = 0
by_path = {}
status_5xx = {}
for ln in lines:
    if marker not in ln:
        continue
    m = json_re.search(ln)
    if not m:
        continue
    try:
        obj = json.loads(m.group(0))
    except json.JSONDecodeError:
        continue
    rid = obj.get("request_id")
    if rid:
        if rid in seen_request_ids:
            continue
        seen_request_ids.add(rid)
    total += 1
    path = str(obj.get("path", "<none>"))
    by_path[path] = by_path.get(path, 0) + 1
    status = obj.get("status_code")
    if isinstance(status, int) and status >= 500:
        status_5xx[str(status)] = status_5xx.get(str(status), 0) + 1

print("=== traffic ===")
top = sorted(by_path.items(), key=lambda kv: (-kv[1], kv[0]))[:10]
print(json.dumps({
    "completed_total": total,
    "top_paths": [{"path": p, "n": n} for p, n in top],
    "status_5xx": status_5xx,
}))
PY
