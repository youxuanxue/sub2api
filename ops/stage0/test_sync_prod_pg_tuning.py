#!/usr/bin/env python3
"""SSM outcome propagation; no AWS calls or database recreation."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import sys
sys.path.insert(0, str(Path(__file__).resolve().parent))
from ssm_wrap_bash_commands import unwrap_commands

SCRIPT = Path(__file__).with_name("sync-prod-pg-tuning-via-ssm.sh")


class SyncProdPGTuningTest(unittest.TestCase):
    def run_sync(self, transport_rc=0, *, apply=False, instance="i-testprod", render_only=True):
        env = {key: value for key, value in os.environ.items() if key not in ("AWS_REGION", "AWS_DEFAULT_REGION")}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake_exec = root / "execute_remote_params.py"
            fake_exec.write_text(
                f"""#!/usr/bin/env python3
import sys
raise SystemExit({transport_rc})
"""
            )
            fake_exec.chmod(0o755)
            run_env = {
                **env,
                "TOKENKEY_PROD_INSTANCE_ID": instance,
                "STAGE0_EXECUTE_REMOTE_PARAMS": str(fake_exec),
            }
            run_kw = {"capture_output": True, "text": True, "check": False}
            if render_only:
                run_env["STAGE0_RENDER_ONLY"] = "1"
                run_env["STAGE0_SSM_OUTPUT_DIR"] = str(root)
                run_kw["cwd"] = str(root)
            else:
                run_kw["cwd"] = str(root)
            result = subprocess.run(
                ["bash", str(SCRIPT), instance, *(["--apply"] if apply else [])],
                env=run_env,
                **run_kw,
            )
            params = json.loads((root / "ssm-params-pg-tuning.json").read_text())
            return result, unwrap_commands(params["commands"])

    def assert_remote_shell_parses(self, commands):
        script = "\n".join(commands) + "\n"
        parsed = subprocess.run(["bash", "-n"], input=script, text=True, capture_output=True)
        self.assertEqual(parsed.returncode, 0, parsed.stderr)
        self.assertNotIn("name||", script)
        self.assertNotIn("re-run", script)

    def test_success_and_read_only_default(self):
        result, commands = self.run_sync(render_only=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any("--force-recreate" in command for command in commands))
        self.assert_remote_shell_parses(commands)
        self.assertTrue(any("SHOW shared_buffers" in command for command in commands))

    def test_hybrid_pin_uses_hcloud_registration_region(self):
        result, _ = self.run_sync(render_only=True, instance="mi-0123456789abcdef0")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_apply_uses_overlay_and_preserves_env_overrides(self):
        result, commands = self.run_sync(render_only=True, apply=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any('-f "$OVERLAY" up -d --no-deps --force-recreate postgres' in command for command in commands))
        self.assertFalse(any("upsert POSTGRES_" in command for command in commands))
        self.assert_remote_shell_parses(commands)
        joined = "\n".join(commands)
        self.assertIn("SHOW jit", joined)
        self.assertIn("base64 -d", joined)

    def test_remote_transport_failure_fail_closed(self):
        result, _ = self.run_sync(transport_rc=1, apply=True, render_only=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("did not succeed", result.stderr)


if __name__ == "__main__":
    unittest.main()
