#!/usr/bin/env python3
"""Parse changed shell/Python scripts without executing their operational actions."""
import ast
import json
from pathlib import Path
import subprocess
import sys


def check(root: Path, paths: list[str]) -> int:
    errors = 0
    for relative in paths:
        path = root / relative
        if not path.is_file():
            continue  # Deleted files still participate in selection and reference gates.
        if path.suffix == '.sh':
            result = subprocess.run(['bash', '-n', str(path)], capture_output=True, text=True)
            if result.returncode:
                print(result.stderr, end='')
                errors += 1
        elif path.suffix == '.py':
            try:
                ast.parse(path.read_bytes(), filename=relative)
            except (SyntaxError, ValueError) as exc:
                print(f'{relative}: {exc}')
                errors += 1
    return 1 if errors else 0


if __name__ == '__main__':
    root = Path(__file__).resolve().parents[2]
    sys.exit(check(root, json.loads(Path(sys.argv[1]).read_text())['paths']))
