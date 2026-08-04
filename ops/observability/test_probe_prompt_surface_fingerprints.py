#!/usr/bin/env python3
"""Validation tests for probe-prompt-surface-fingerprints.sh."""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-prompt-surface-fingerprints.sh"


class ProbePromptSurfaceFingerprintsTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SCRIPT)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_emits_fingerprint_rows(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            (fakebin / "docker").write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    # The canonical resolver asks for State.Running (not mere
                    # existence) and passes the name last.
                    if [ "$1" = inspect ]; then
                      for a in "$@"; do name="$a"; done
                      [ "$name" = tokenkey ] || exit 1
                      case "$*" in *State.Running*) echo true ;; esac
                      exit 0
                    fi
                    if [ "$1" = logs ]; then
                      cat <<'LOGS'
                    2026-07-01T00:00:00Z INFO gateway.anthropic_prompt_fingerprint {"request_id":"r1","identity_anchor_id":"claude_code_cli","surface_signature":"abc123"}
                    LOGS
                      exit 0
                    fi
                    exit 2
                    """
                ),
            )
            (fakebin / "docker").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "CONTAINER": "tokenkey",
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
            payload = json.loads(proc.stdout)
            self.assertEqual(payload["meta"]["container"], "tokenkey")
            self.assertEqual(len(payload["fingerprints"]), 1)
            self.assertEqual(payload["fingerprints"][0]["request_id"], "r1")


if __name__ == "__main__":
    unittest.main()
