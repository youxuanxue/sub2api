#!/usr/bin/env python3
"""SSM outcome propagation; no AWS calls or database recreation."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("sync-prod-pg-tuning-via-ssm.sh")


class SyncProdPGTuningTest(unittest.TestCase):
    def run_sync(self, status, code, *, apply=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            aws = root / "aws"
            aws.write_text("""#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
if 'send-command' in args:
    print('fake-command')
elif 'wait' in args:
    sys.exit(0 if os.environ['SSM_STATUS'] == 'Success' else 255)
else:
    print(json.dumps(dict(Status=os.environ['SSM_STATUS'], ResponseCode=int(os.environ['SSM_CODE']), Stdout='', Stderr='remote diagnostics')))
""")
            aws.chmod(0o755)
            result = subprocess.run(
                ["bash", str(SCRIPT), "i-testprod", *(["--apply"] if apply else [])],
                env={**os.environ, "PATH": f"{root}:{os.environ['PATH']}",
                     "TOKENKEY_PROD_INSTANCE_ID": "i-testprod", "STAGE0_SSM_OUTPUT_DIR": str(root),
                     "SSM_STATUS": status, "SSM_CODE": str(code)},
                capture_output=True, text=True, check=False,
            )
            params = json.loads((root / "ssm-params-pg-tuning.json").read_text())
            return result, params["commands"]

    def test_success_and_read_only_default(self):
        result, commands = self.run_sync("Success", 0)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any("--force-recreate" in command for command in commands))

    def test_apply_uses_overlay_and_preserves_env_overrides(self):
        result, commands = self.run_sync("Success", 0, apply=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any('-f "$OVERLAY" up -d --no-deps --force-recreate postgres' in command for command in commands))
        self.assertFalse(any("upsert POSTGRES_" in command for command in commands))

    def test_failure_timeout_and_unfinished_command_fail_closed(self):
        for status, code in [("Failed", 1), ("TimedOut", -1), ("InProgress", -1), ("Success", 1)]:
            with self.subTest(status=status, code=code):
                result, _ = self.run_sync(status, code, apply=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("did not succeed", result.stderr)


if __name__ == "__main__":
    unittest.main()
