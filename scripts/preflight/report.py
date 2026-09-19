#!/usr/bin/env python3
"""Compact preflight output; retain full diagnostics and the child's exit status."""
from __future__ import annotations

import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

HEADER = re.compile(r'^=== sub2api: (.+) ===$')
FAILURE = re.compile(r'(^|\s)(FAIL(?:ED)?|ERROR)(?:[:\s(]|$)')


def render(name: str, lines: list[str], elapsed: float) -> list[str]:
    failed = any(FAILURE.search(line) for line in lines)
    skipped = any(line.strip().startswith('skip:') for line in lines)
    status = 'FAIL' if failed else ('SKIP' if skipped else 'PASS')
    prefix = 'FAIL:' if failed else status
    result = [f'{prefix} {name} ({elapsed:.1f}s)']
    if failed:
        result.extend(lines[-60:])
    return result


def main() -> int:
    root = Path(__file__).resolve().parents[2]
    env = dict(os.environ, PREFLIGHT_RAW_OUTPUT='1')
    command = ['bash', str(root / 'scripts/preflight.sh'), *sys.argv[1:]]
    if '--plan' in sys.argv or '--help' in sys.argv or '-h' in sys.argv:
        return subprocess.call(command, env=env)
    fd, log_name = tempfile.mkstemp(prefix='preflight-', suffix='.log')
    started = time.monotonic()
    with os.fdopen(fd, 'w') as log:
        proc = subprocess.Popen(command, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        name = None
        section_started = started
        lines = []
        def flush():
            if name is not None:
                for rendered in render(name, lines, time.monotonic() - section_started):
                    print(rendered, flush=True)
        try:
            for raw in proc.stdout:
                log.write(raw)
                log.flush()
                line = raw.rstrip('\n')
                header = HEADER.match(line)
                if header or line.startswith('=== preflight (with sub2api checks):'):
                    flush()
                    name = header.group(1) if header else None
                    section_started = time.monotonic()
                    lines = []
                    if name is None:
                        print(line, flush=True)
                elif name is None:
                    if line:
                        print(line, flush=True)
                else:
                    lines.append(line)
            code = proc.wait()
            if code and name is not None:
                lines.append(f"FAIL: preflight process exited {code}")
            flush()
        except KeyboardInterrupt:
            proc.terminate()
            proc.wait()
            code = 130
    print(f'preflight: exit={code}, elapsed={time.monotonic() - started:.1f}s; log={log_name}', flush=True)
    return code


if __name__ == '__main__':
    sys.exit(main())
