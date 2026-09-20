#!/usr/bin/env python3
"""Unit tests for ops/stage0/normalize-deploy-tag.sh."""
from __future__ import annotations

import pathlib
import subprocess
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "normalize-deploy-tag.sh"


class NormalizeDeployTagTest(unittest.TestCase):
    def _run(self, *args: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["bash", str(_SCRIPT), *args],
            capture_output=True,
            text=True,
            check=False,
        )

    def test_passthrough_bare(self) -> None:
        proc = self._run("1.8.243")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(proc.stdout.strip(), "1.8.243")

    def test_strips_leading_v(self) -> None:
        proc = self._run("v1.8.243")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(proc.stdout.strip(), "1.8.243")

    def test_strips_v_from_prerelease(self) -> None:
        proc = self._run("v1.8.243-rc.1")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(proc.stdout.strip(), "1.8.243-rc.1")

    def test_rejects_double_v(self) -> None:
        proc = self._run("vv1.8.243")
        self.assertNotEqual(proc.returncode, 0)

    def test_rejects_empty(self) -> None:
        proc = self._run("")
        self.assertNotEqual(proc.returncode, 0)


if __name__ == "__main__":
    unittest.main()
