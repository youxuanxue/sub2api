#!/usr/bin/env python3
"""Exit-contract tests for the combined fingerprint orchestrator."""
from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "ops/fingerprint/capture-all-fingerprints.sh"


class CaptureAllExitContractTest(unittest.TestCase):
    def _run(self, codes: tuple[int, int, int, int], *args: str) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as tmp:
            paths: list[Path] = []
            for index, code in enumerate(codes):
                path = Path(tmp) / f"engine-{index}.sh"
                path.write_text(f"#!/usr/bin/env bash\nexit {code}\n", encoding="utf-8")
                paths.append(path)
            env = {
                **os.environ,
                "TOKENKEY_CAPTURE_ALL_CC": str(paths[0]),
                "TOKENKEY_CAPTURE_ALL_KIRO": str(paths[1]),
                "TOKENKEY_CAPTURE_ALL_ANTIGRAVITY": str(paths[2]),
                "TOKENKEY_CAPTURE_ALL_CODEX": str(paths[3]),
            }
            return subprocess.run(
                ["bash", str(SCRIPT), *args],
                capture_output=True,
                text=True,
                check=False,
                env=env,
            )

    def test_all_observed_and_aligned_exits_zero(self) -> None:
        result = self._run((0, 0, 0, 0))
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)

    def test_drift_exits_one(self) -> None:
        result = self._run((1, 0, 0, 0))
        self.assertEqual(1, result.returncode)

    def test_error_takes_precedence(self) -> None:
        result = self._run((1, 2, 0, 0))
        self.assertEqual(2, result.returncode)

    def test_incomplete_or_skipped_exits_three(self) -> None:
        self.assertEqual(3, self._run((3, 0, 0, 0)).returncode)
        self.assertEqual(3, self._run((0, 0, 0, 0), "--skip-cc").returncode)


if __name__ == "__main__":
    unittest.main()
