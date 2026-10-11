#!/usr/bin/env python3
"""Rewrite an SSM RunShellScript params JSON for Hybrid/dash hosts.

AWS-RunShellScript on Hetzner/Ubuntu Hybrid managed instances runs under
``/bin/sh`` (dash), which rejects ``set -o pipefail`` and other bash-isms.
Wrap the existing ``commands`` array as a single:

  echo <base64> | base64 -d | bash -s

so the payload body still runs under bash. Idempotent if already wrapped.

Why wrap unconditionally, even for payloads that already pipe into `sudo bash`
(e.g. run-qa-maintenance-health-gate-via-ssm.sh)? The inner `sudo bash` exists
to ELEVATE; this wrapper exists to pick the SHELL. Keeping them separate means
no sender has to reason about which form it emits — one rule, same result on
EC2 and Hybrid. The cost is a second base64 layer when reading an ssm-params
dump by hand; `--unwrap-stdout` is the supported way to recover the real script
(it is also what the preflight host-parse guard lints, so wrapping cannot
silently neuter that check).
"""

from __future__ import annotations

import base64
import json
import pathlib
import re
import shlex
import sys

_WRAP_RE = re.compile(r"^echo ([A-Za-z0-9+/=]+) \| base64 -d \| bash -s$")
# Second dash-safe form already in the tree: sync_caddyfile_via_ssm.sh emits a
# single `bash -c '<script>'`. unwrap must recognise it too, or lint guards see
# only the wrapper line (the same blind spot, pre-dating this helper).
_BASH_C_PREFIX = "bash -c "


def wrap_commands(commands: list[str]) -> list[str]:
    if len(commands) == 1 and _WRAP_RE.fullmatch(commands[0]):
        return commands
    script = "\n".join(commands)
    b64 = base64.b64encode(script.encode()).decode()
    return [f"echo {b64} | base64 -d | bash -s"]


def unwrap_commands(commands: list[str]) -> list[str]:
    """Recover the real host script from either dash-safe wrapper form.

    Handles this helper's `echo <b64> | base64 -d | bash -s` and the
    pre-existing `bash -c '<script>'`. No-op on an unwrapped array.
    """
    if len(commands) != 1:
        return commands
    match = _WRAP_RE.fullmatch(commands[0])
    if match is not None:
        return base64.b64decode(match.group(1)).decode().split("\n")
    if commands[0].startswith(_BASH_C_PREFIX):
        parts = shlex.split(commands[0])
        if len(parts) >= 3 and parts[0] == "bash" and parts[1] == "-c":
            return parts[2].split("\n")
    return commands


def main(argv: list[str]) -> int:
    unwrap_stdout = False
    args = [a for a in argv[1:] if a != "--unwrap-stdout"]
    if len(args) != len(argv) - 1:
        unwrap_stdout = True
    if len(args) != 1:
        print(f"usage: {argv[0]} [--unwrap-stdout] <ssm-params.json>", file=sys.stderr)
        return 2
    path = pathlib.Path(args[0])
    data = json.loads(path.read_text())
    commands = data.get("commands")
    if not isinstance(commands, list) or not all(isinstance(c, str) for c in commands):
        print("ssm_wrap_bash_commands: commands must be a list of strings", file=sys.stderr)
        return 2
    if unwrap_stdout:
        # Read-only: print the REAL host script so callers (e.g. the preflight
        # host-parse guard) can lint what actually runs, wrapped or not.
        for line in unwrap_commands(commands):
            print(line)
        return 0
    data["commands"] = wrap_commands(commands)
    path.write_text(json.dumps(data))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
