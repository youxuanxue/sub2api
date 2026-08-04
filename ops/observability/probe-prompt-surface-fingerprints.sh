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

# Where the canonical resolver lives. run-probe.sh uploads
# resolve_app_container.py next to this probe (/tmp); a repo checkout has it
# under ops/lib. Exported so the python heredoc below resolves the same owner.
TK_LIB_DIR="${TK_LIB_DIR:-$(cd "$(dirname "$0")" && pwd)}"
export TK_LIB_DIR

TK_PROBE_DIR="$(cd "$(dirname "$0")" && pwd)"
export TK_PROBE_DIR
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
