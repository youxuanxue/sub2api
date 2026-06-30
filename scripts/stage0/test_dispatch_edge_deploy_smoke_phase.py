#!/usr/bin/env python3
"""Contract tests for dispatch-edge-deploy.sh smoke_phase defaults.

Uses fake gh + resolve-edge-deploy-route.py. No GitHub/network.
"""
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "dispatch-edge-deploy.sh"


class DispatchEdgeDeploySmokePhaseTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.repo = pathlib.Path(self._tmp.name) / "repo"
        self.repo.mkdir()
        (self.repo / "scripts/stage0").mkdir(parents=True)
        shutil.copy(_SCRIPT, self.repo / "scripts/stage0/dispatch-edge-deploy.sh")
        (self.repo / "scripts/stage0/dispatch-edge-deploy.sh").chmod(0o755)
        self.fakebin = self.repo / "fakebin"
        self.fakebin.mkdir()
        self.gh_log = self.repo / "gh-args.log"

        (self.repo / "scripts/stage0/resolve-edge-deploy-route.py").write_text(
            textwrap.dedent(
                """\
                #!/usr/bin/env python3
                print("workflow_file=deploy-edge-lightsail-stage0.yml")
                print("confirm_flag=confirm_instance")
                print("confirm_value=fake-instance")
                print("platform=lightsail")
                """
            ),
        )
        (self.repo / "scripts/stage0/resolve-edge-deploy-route.py").chmod(0o755)

        (self.fakebin / "gh").write_text(
            textwrap.dedent(
                """\
                #!/usr/bin/env bash
                printf '%s\\n' "$*" >> gh-args.log
                exit 0
                """
            ),
        )
        (self.fakebin / "gh").chmod(0o755)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _run(self, *args: str) -> subprocess.CompletedProcess:
        env = {**os.environ, "PATH": f"{self.fakebin}:{os.environ.get('PATH', '')}"}
        return subprocess.run(
            ["bash", "scripts/stage0/dispatch-edge-deploy.sh", *args],
            cwd=self.repo,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def _gh_args(self) -> str:
        return self.gh_log.read_text()

    def test_upgrade_defaults_smoke_phase_infra(self) -> None:
        proc = self._run("--edge-id", "uk1", "--operation", "upgrade", "--tag", "1.2.3")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("smoke_phase=infra", proc.stdout)
        self.assertIn("smoke_phase=infra", self._gh_args())

    def test_rollback_defaults_smoke_phase_infra(self) -> None:
        proc = self._run("--edge-id", "uk1", "--operation", "rollback", "--tag", "1.2.2")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("smoke_phase=infra", proc.stdout)
        self.assertIn("smoke_phase=infra", self._gh_args())

    def test_smoke_defaults_smoke_phase_full(self) -> None:
        proc = self._run("--edge-id", "uk1", "--operation", "smoke")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("smoke_phase=full", proc.stdout)
        self.assertIn("smoke_phase=full", self._gh_args())

    def test_upgrade_explicit_full_overrides_default(self) -> None:
        proc = self._run(
            "--edge-id",
            "uk1",
            "--operation",
            "upgrade",
            "--tag",
            "1.2.3",
            "--smoke-phase",
            "full",
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("smoke_phase=full", proc.stdout)
        self.assertIn("smoke_phase=full", self._gh_args())


if __name__ == "__main__":
    unittest.main()
