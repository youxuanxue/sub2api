#!/bin/bash
# probe-post-release-tick.sh — post-release follow-up tick probe (read-only).
#
# Ships to prod via run-post-release-check.sh (the +5min live→new PR check).
# Generic signals (traffic volume, per-path mix, 5xx, panic) are fixed here;
# HOOK_PATTERNS come from scripts/release_post_check.py plan — do not invent
# them in prompt prose. This script only counts the supplied fixed strings.
#
# Env (consumed inside the remote shell):
#   SINCE          docker logs --since window (default 6m)
#   CONTAINER      gateway container name (default auto). auto resolves
#                  /var/lib/tokenkey/active-color to tokenkey-blue/green and
#                  falls back to the legacy tokenkey container. This keeps
#                  post-release ticks working across the prod blue/green cutover.
#   ACTIVE_COLOR_FILE
#                  active-color file path for CONTAINER=auto
#                  (default /var/lib/tokenkey/active-color; test seam).
#   HOOK_PATTERNS  comma-separated FIXED strings from release_post_check.py
#                  hook-patterns (grep -F). Example: Status=424,WEEKLY_LIMIT_EXCEEDED
#                  Empty → hooks section reports none configured.
#
# Output: stable `=== section ===` markers; the traffic section is JSON
# (row_to_json-style) so downstream parsing never relies on column position.
# Request lines are deduplicated by request_id (docker stdout/stderr replay
# previously double-counted paths when streams were naively merged).
set -u

SINCE="${SINCE:-6m}"
CONTAINER="${CONTAINER:-auto}"
ACTIVE_COLOR_FILE="${ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"
HOOK_PATTERNS="${HOOK_PATTERNS:-}"

# Where the canonical resolver lives. run-probe.sh uploads
# resolve_app_container.py next to this probe (/tmp); a repo checkout has it
# under ops/lib. Exported so the python heredoc below resolves the same owner.
TK_LIB_DIR="${TK_LIB_DIR:-$(cd "$(dirname "$0")" && pwd)}"
export TK_LIB_DIR

TK_PROBE_DIR="$(cd "$(dirname "$0")" && pwd)"
export TK_PROBE_DIR
python3 - "$SINCE" "$CONTAINER" "$HOOK_PATTERNS" "$ACTIVE_COLOR_FILE" <<'PY'
import json
import pathlib
import re
import subprocess
import sys

since, container_arg, hook_patterns_raw, active_color_file = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]




# Canonical resolver (ops/lib/resolve_app_container.py). run-probe.sh uploads it
# next to the probe; the repo path covers local runs. Importing beats re-deriving:
# the copies this replaced had drifted into existence-only checks that happily
# selected a STOPPED container during a blue/green window.
import importlib.util as _ilu
import os as _os

# TK_PROBE_DIR is exported by this script so the heredoc knows where it lives:
# python reading a heredoc has no __file__. run-probe.sh drops the resolver next
# to the probe on the host; the ../lib hop covers running from the repo.
_tk_probe_dir = _os.environ.get("TK_PROBE_DIR", "")
_tk_resolver_candidates = [
    pathlib.Path(_tk_probe_dir) / "resolve_app_container.py",
    pathlib.Path(_tk_probe_dir) / ".." / "lib" / "resolve_app_container.py",
    pathlib.Path("/tmp/resolve_app_container.py"),
    pathlib.Path("ops/lib/resolve_app_container.py"),
]
_spec = None
for _cand in _tk_resolver_candidates:
    if _cand.is_file():
        _spec = _ilu.spec_from_file_location("tk_resolve_app_container", str(_cand))
        break
if _spec is None:
    raise SystemExit("canonical app-container resolver not found (ops/lib/resolve_app_container.py)")
_tk = _ilu.module_from_spec(_spec)
_spec.loader.exec_module(_tk)


def resolve_container(container):
    """Thin adapter over the canonical resolver, preserving this probe's
    (name, notes) output contract. Unresolved yields (None, notes) so callers
    report unknown instead of guessing a container that may not be running."""
    return _tk.resolve(container, active_color_file=active_color_file)


container, resolution = resolve_container(container_arg)
# Unresolved is an explicit unknown. Falling through would hand None/"" to
# `docker logs`, whose empty output is indistinguishable from a quiet window.
if container is None:
    print(json.dumps({
        "error": "app container unresolved (no single running candidate)",
        "container_input": container_arg,
        "container": None,
        "container_resolution": resolution,
    }))
    sys.exit(1)


proc = subprocess.run(
    ["docker", "logs", container, "--since", since],
    capture_output=True,
    text=True,
    check=False,
)
if proc.returncode != 0:
    print(json.dumps({
        "error": proc.stderr.strip() or "docker logs failed",
        "container_input": container_arg,
        "container": container,
        "container_resolution": resolution,
    }))
    sys.exit(1)

# The gateway writes structured logs to one stream, but ops shells have merged
# both in the past; scan both and dedupe exact duplicate lines defensively.
lines = list(dict.fromkeys(proc.stdout.splitlines() + proc.stderr.splitlines()))

print("=== meta ===")
print(json.dumps({
    "container_input": container_arg,
    "container": container,
    "container_resolution": resolution,
    "since": since,
    "log_lines": len(lines),
}))

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
