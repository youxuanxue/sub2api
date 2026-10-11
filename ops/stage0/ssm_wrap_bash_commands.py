#!/usr/bin/env python3
"""Rewrite an SSM RunShellScript params JSON for Hybrid/dash hosts.

AWS-RunShellScript on Hetzner/Ubuntu Hybrid managed instances runs under
``/bin/sh`` (dash), which rejects ``set -o pipefail`` and other bash-isms.
Wrap the existing ``commands`` array as a single:

  echo <base64> | base64 -d | bash -s

so the payload body still runs under bash. Idempotent if already wrapped.
"""

from __future__ import annotations

import base64
import json
import pathlib
import re
import sys

_WRAP_RE = re.compile(r"^echo ([A-Za-z0-9+/=]+) \| base64 -d \| bash -s$")


def wrap_commands(commands: list[str]) -> list[str]:
    if len(commands) == 1 and _WRAP_RE.fullmatch(commands[0]):
        return commands
    script = "\n".join(commands)
    b64 = base64.b64encode(script.encode()).decode()
    return [f"echo {b64} | base64 -d | bash -s"]


def unwrap_commands(commands: list[str]) -> list[str]:
    """Inverse of wrap_commands for tests inspecting ssm-params.json."""
    if len(commands) != 1:
        return commands
    match = _WRAP_RE.fullmatch(commands[0])
    if match is None:
        return commands
    return base64.b64decode(match.group(1)).decode().split("\n")


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(f"usage: {argv[0]} <ssm-params.json>", file=sys.stderr)
        return 2
    path = pathlib.Path(argv[1])
    data = json.loads(path.read_text())
    commands = data.get("commands")
    if not isinstance(commands, list) or not all(isinstance(c, str) for c in commands):
        print("ssm_wrap_bash_commands: commands must be a list of strings", file=sys.stderr)
        return 2
    data["commands"] = wrap_commands(commands)
    path.write_text(json.dumps(data))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
