"""Exercise production Feishu sync dry-run + TAT/ssm_execution fixture path."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO = Path(__file__).resolve().parents[2]


class SyncFeishuConfigTest(unittest.TestCase):
    def _seed_tree(self, root: Path) -> Path:
        ops = root / "ops" / "stage0"
        ops.mkdir(parents=True)
        shutil.copyfile(Path(__file__).with_name("sync-feishu-config.sh"), ops / "sync.sh")
        (root / "ops" / "lib").mkdir(parents=True)
        shutil.copyfile(
            REPO / "ops" / "lib" / "resolve_app_container.py",
            root / "ops" / "lib" / "resolve_app_container.py",
        )
        return ops / "sync.sh"

    def test_dry_run_emits_params_without_aws(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sync = self._seed_tree(root)
            out = root / "out"
            env = {
                **os.environ,
                "TK_FEISHU_WEBHOOK_URL": "https://example.test/private-hook",
                "TK_FEISHU_SIGNING_SECRET": "fixture-private-secret",
                "STAGE0_SSM_OUTPUT_DIR": str(out),
            }
            result = subprocess.run(
                ["bash", str(sync), "prod"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            params = json.loads((out / "ssm-params.json").read_text(encoding="utf-8"))
            self.assertEqual(len(params["commands"]), 1)
            self.assertTrue(params["commands"][0].startswith("bash -lc "))
            blob = result.stdout + result.stderr + (out / "ssm-params.json").read_text()
            # Secrets are embedded in the remote script payload (expected) but must
            # not appear on the operator-facing success banner lines alone — the
            # script still must not echo them as VERIFY/ok lines. Assert the dry-run
            # banner path does not print them outside the params file.
            self.assertNotIn(env["TK_FEISHU_WEBHOOK_URL"], result.stdout)
            self.assertNotIn(env["TK_FEISHU_SIGNING_SECRET"], result.stdout)
            self.assertIn(env["TK_FEISHU_WEBHOOK_URL"], blob)

    def test_remote_script_resolves_layout_exception_postgres(self) -> None:
        """Tencent USW keeps tk-archive-standby; do not hard-code tokenkey-postgres only."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sync = self._seed_tree(root)
            env = {
                **os.environ,
                "TK_FEISHU_WEBHOOK_URL": "https://example.test/private-hook",
                "TK_FEISHU_SIGNING_SECRET": "fixture-private-secret",
                "STAGE0_SSM_OUTPUT_DIR": str(root),
            }
            proc = subprocess.run(
                ["bash", str(sync), "prod"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            body = (root / "ssm-params.json").read_text()
            self.assertIn("tk-archive-standby", body)
            self.assertIn("tokenkey-postgres tk-archive-standby", body)
            self.assertNotIn("grep -qx tokenkey-postgres", body)
            self.assertIn("POSTGRES_CONTAINER=", body)
            self.assertIn("$POSTGRES_CONTAINER", body)

    def _run_live(self, *, stdout: str, fail: bool = False) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sync = self._seed_tree(root)
            # Minimal ssm_execution stub consumed by the live path.
            (root / "ops" / "stage0" / "ssm_execution.py").write_text(
                "def resolve_prod_instance():\n"
                "    return 'mi-0a1b2c3d4e5f60718'\n"
                "def data_container_preamble(**kwargs):\n"
                "    return ''\n"
                "def run_shell_b64(instance_id, shell_b64, comment, preamble=''):\n"
                f"    if {fail!r}:\n"
                "        raise SystemExit('remote failed')\n"
                f"    return {stdout!r}\n",
                encoding="utf-8",
            )
            env = {
                **os.environ,
                "TK_FEISHU_WEBHOOK_URL": "https://example.test/private-hook",
                "TK_FEISHU_SIGNING_SECRET": "fixture-private-secret",
                "TOKENKEY_REMOTE_VIA": "tat",
                "PROD_SSM_NODE": "data",
            }
            return subprocess.run(
                ["bash", str(sync), "prod"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
                cwd=str(root),
            )

    def test_verified_remote_success(self) -> None:
        result = self._run_live(
            stdout=(
                "FEISHU_ENABLED=true FEISHU_WEBHOOK_PRESENT=t FEISHU_SECRET_PRESENT=t\n"
                "FEISHU_SYNC_OK=1\nENV_FEISHU_SYNC_OK=1\n"
            )
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("feishu config sync complete", result.stdout)
        self.assertIn("PROD_SSM_NODE=data", result.stdout)
        self.assertIn("TOKENKEY_REMOTE_VIA=tat", result.stdout)
        self.assertNotIn("https://example.test/private-hook", result.stdout + result.stderr)
        self.assertNotIn("fixture-private-secret", result.stdout + result.stderr)

    def test_remote_failure_is_not_submission_success(self) -> None:
        result = self._run_live(stdout="", fail=True)
        self.assertNotEqual(result.returncode, 0)

    def test_missing_verification_receipt_fails(self) -> None:
        result = self._run_live(
            stdout="FEISHU_ENABLED=true FEISHU_WEBHOOK_PRESENT=t FEISHU_SECRET_PRESENT=t\n"
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("FEISHU_SYNC_OK=1", result.stderr)


if __name__ == "__main__":
    unittest.main()
