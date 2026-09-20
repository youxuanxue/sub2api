#!/usr/bin/env python3
"""Contract tests for scripts/stage0/dispatch-prod-deploy.sh.

Uses fake gh. No GitHub/network.
"""
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "dispatch-prod-deploy.sh"
_REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
_NORMALIZE = _REPO_ROOT / "ops/stage0/normalize-deploy-tag.sh"
_VALIDATE = _REPO_ROOT / "ops/stage0/validate-deploy-tag.sh"


class DispatchProdDeployTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.repo = pathlib.Path(self._tmp.name) / "repo"
        self.repo.mkdir()
        (self.repo / "scripts/stage0").mkdir(parents=True)
        (self.repo / "ops/stage0").mkdir(parents=True)
        shutil.copy(_SCRIPT, self.repo / "scripts/stage0/dispatch-prod-deploy.sh")
        (self.repo / "scripts/stage0/dispatch-prod-deploy.sh").chmod(0o755)
        shutil.copy(_NORMALIZE, self.repo / "ops/stage0/normalize-deploy-tag.sh")
        (self.repo / "ops/stage0/normalize-deploy-tag.sh").chmod(0o755)
        shutil.copy(_VALIDATE, self.repo / "ops/stage0/validate-deploy-tag.sh")
        (self.repo / "ops/stage0/validate-deploy-tag.sh").chmod(0o755)
        self.fakebin = self.repo / "fakebin"
        self.fakebin.mkdir()
        self.gh_log = self.repo / "gh-args.log"
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
            ["bash", "scripts/stage0/dispatch-prod-deploy.sh", *args],
            cwd=self.repo,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def _gh_args(self) -> str:
        return self.gh_log.read_text()

    def test_deploy_strips_leading_v(self) -> None:
        proc = self._run("--operation", "deploy", "--tag", "v1.8.243")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("workflow=deploy-stage0.yml", proc.stdout)
        self.assertIn("tag=1.8.243", proc.stdout)
        self.assertIn("operation=deploy", self._gh_args())
        self.assertIn("tag=1.8.243", self._gh_args())
        self.assertNotIn("tag=v1.8.243", self._gh_args())

    def test_warm_dispatches_warm_workflow(self) -> None:
        proc = self._run("--operation", "warm", "--tag", "1.2.3")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("workflow=warm-image-stage0.yml", proc.stdout)
        self.assertIn("tag=1.2.3", self._gh_args())
        self.assertNotIn("operation=", self._gh_args())

    def test_replay_replace_receipt(self) -> None:
        receipt = "a" * 64
        proc = self._run(
            "--operation", "replay", "--tag", "1.2.3", "--replace-receipt", receipt
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn(f"replace_receipt={receipt}", self._gh_args())

    def test_deploy_replay_receipt_for_promote(self) -> None:
        receipt = "b" * 64
        proc = self._run(
            "--operation", "deploy", "--tag", "1.2.3", "--replay-receipt", receipt
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn(f"replay_receipt={receipt}", self._gh_args())

    def test_rejects_replay_receipt_on_replay(self) -> None:
        proc = self._run(
            "--operation", "replay", "--tag", "1.2.3", "--replay-receipt", "x" * 64
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("--replay-receipt", proc.stderr)
        self.assertFalse(self.gh_log.exists())

    def test_rejects_malformed_tag(self) -> None:
        proc = self._run("--operation", "deploy", "--tag", "vv1.2.3")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("invalid --tag", proc.stderr)
        self.assertFalse(self.gh_log.exists())

    def test_workflow_ref(self) -> None:
        proc = self._run(
            "--operation", "deploy", "--tag", "1.2.3", "--ref", "chore/drain-fix"
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("--ref chore/drain-fix", self._gh_args())
        self.assertIn("tag=1.2.3", self._gh_args())


if __name__ == "__main__":
    unittest.main()
