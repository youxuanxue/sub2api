#!/usr/bin/env python3
"""Behavior tests for the model-surface bundle drift command owner."""

from __future__ import annotations

import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest


REPO_ROOT = Path(__file__).resolve().parents[2]
CHECK = REPO_ROOT / "scripts" / "checks" / "check-model-surface-bundle.sh"


class ModelSurfaceBundleCheckTest(unittest.TestCase):
    def fake_go_env(self, temp_dir: Path) -> tuple[dict[str, str], Path]:
        capture = temp_dir / "go.capture"
        fake_go = temp_dir / "go"
        fake_go.write_text(
            "#!/bin/sh\n"
            "{ printf '%s\\n' \"$PWD\"; printf '%s\\n' \"$@\"; } > \"$GO_CAPTURE\"\n",
            encoding="utf-8",
        )
        fake_go.chmod(fake_go.stat().st_mode | stat.S_IXUSR)
        env = os.environ.copy()
        env["PATH"] = f"{temp_dir}{os.pathsep}{env['PATH']}"
        env["GO_CAPTURE"] = str(capture)
        return env, capture

    def test_default_check_invokes_go_owner_from_backend(self) -> None:
        with tempfile.TemporaryDirectory() as raw_temp_dir:
            temp_dir = Path(raw_temp_dir)
            env, capture = self.fake_go_env(temp_dir)

            completed = subprocess.run(
                ["bash", str(CHECK)],
                cwd=REPO_ROOT,
                env=env,
                check=False,
                capture_output=True,
                text=True,
            )

            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertEqual(
                capture.read_text(encoding="utf-8").splitlines(),
                [
                    str(REPO_ROOT / "backend"),
                    "run",
                    "./cmd/account-model-mapping",
                    "bundle",
                    "--check",
                    "../ops/pricing/model-surface-bundle.json",
                ],
            )

    def test_shared_defer_env_cannot_skip_direct_check(self) -> None:
        with tempfile.TemporaryDirectory() as raw_temp_dir:
            temp_dir = Path(raw_temp_dir)
            env, capture = self.fake_go_env(temp_dir)
            env["PREFLIGHT_DEFER_GO_ARTIFACT_DRIFT"] = "1"
            env["GITHUB_ACTIONS"] = "true"

            completed = subprocess.run(
                ["bash", str(CHECK)],
                cwd=REPO_ROOT,
                env=env,
                check=False,
                capture_output=True,
                text=True,
            )

            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertTrue(capture.exists())


if __name__ == "__main__":
    unittest.main()
