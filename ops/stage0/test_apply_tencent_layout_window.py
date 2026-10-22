#!/usr/bin/env python3
"""Smoke tests for apply-tencent-layout-window.sh (no live SSM)."""
from __future__ import annotations

import pathlib
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "apply-tencent-layout-window.sh"


class ApplyTencentLayoutWindowTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        proc = subprocess.run(["bash", "-n", str(_SCRIPT)], capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_apply_guard_and_rollback_hooks_present(self) -> None:
        body = _SCRIPT.read_text(encoding="utf-8")
        self.assertIn('yes-layout-window-cutover', body)
        self.assertIn("rollback_if_past_stop", body)
        self.assertIn("trap ", body)

    def test_unknown_apply_exits_nonzero(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bindir = pathlib.Path(tmp)
            (bindir / "sudo").write_text("#!/bin/sh\nexec \"$@\"\n", encoding="utf-8")
            (bindir / "docker").write_text(
                "#!/bin/sh\n"
                "case \"$1\" in\n"
                "  info) echo /tmp/docker-root ;;\n"
                "  ps) printf '%s\\n' tokenkey ;;\n"
                "  *) echo ok ;;\n"
                "esac\n",
                encoding="utf-8",
            )
            for name in ("sudo", "docker"):
                (bindir / name).chmod(0o755)
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env={"PATH": f"{bindir}:/usr/bin:/bin", "APPLY": "nope", "HOME": tmp},
                capture_output=True,
                text=True,
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("unknown APPLY", proc.stdout + proc.stderr)


if __name__ == "__main__":
    unittest.main()
