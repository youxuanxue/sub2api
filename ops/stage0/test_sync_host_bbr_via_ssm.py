"""sync-host-bbr-via-ssm.sh target resolution must follow live prod, never guess."""

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("sync-host-bbr-via-ssm.sh")

AWS_STUB = """#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
with open(os.environ["CALLS"], "a") as handle:
    handle.write(json.dumps(args) + "\\n")
if "send-command" in args:
    print("cmd-fixture")
elif "--query" in args:
    query = args[args.index("--query") + 1]
    print("Success" if query == "Status" else "")
"""


class SyncHostBbrTargetTest(unittest.TestCase):
    def run_sync(self, target, *, resolver=None, region=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            aws = root / "aws"
            aws.write_text(AWS_STUB)
            aws.chmod(0o755)
            calls = root / "calls"
            env = {k: v for k, v in os.environ.items()
                   if k not in ("AWS_REGION", "AWS_DEFAULT_REGION", "PROD_SSM_TARGET")}
            env.update(PATH=f"{root}:{env['PATH']}", CALLS=str(calls),
                       STAGE0_SSM_OUTPUT_DIR=str(root / "out"))
            if region:
                env["AWS_REGION"] = region
            if resolver is not None:
                fake = root / "resolve.py"
                fake.write_text(resolver)
                env["SYNC_BBR_RESOLVE_PROD"] = str(fake)
            proc = subprocess.run(["bash", str(SCRIPT), target], env=env,
                                  capture_output=True, text=True, check=False)
            rows = [json.loads(l) for l in calls.read_text().splitlines()] if calls.exists() else []
        return proc, rows

    def test_prod_resolves_tencent_hybrid_identity_and_region(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            aws = root / "aws"
            aws.write_text(AWS_STUB)
            aws.chmod(0o755)
            fake = root / "resolve.py"
            fake.write_text(
                "print('instance_id=mi-0123456789abcdef0')\nprint('ssm_region=eu-west-2')\n"
            )
            out = root / "out"
            env = {k: v for k, v in os.environ.items()
                   if k not in ("AWS_REGION", "AWS_DEFAULT_REGION", "PROD_SSM_TARGET")}
            env.update(
                PATH=f"{root}:{env['PATH']}",
                STAGE0_SSM_OUTPUT_DIR=str(out),
                SYNC_BBR_RESOLVE_PROD=str(fake),
            )
            proc = subprocess.run(
                ["bash", str(SCRIPT), "prod"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertTrue((out / "ssm-params.json").is_file())
            self.assertIn("render-only", proc.stdout)

    def test_resolver_failure_fails_closed(self):
        proc, calls = self.run_sync("prod", resolver="import sys\nsys.exit(1)\n")
        self.assertEqual(proc.returncode, 2)
        self.assertIn("refusing to guess", proc.stderr)
        self.assertEqual(calls, [])

    def test_egress_proxy_name_is_rejected_with_fleet_pointer(self):
        proc, calls = self.run_sync("usw-eval1", region="us-east-1")
        self.assertEqual(proc.returncode, 2)
        self.assertIn("deploy/hetzner/README.md", proc.stderr)
        self.assertEqual(calls, [])

    def test_explicit_id_render_only_ok_under_tat_default(self):
        # STAGE0_SSM_OUTPUT_DIR forces render-only; TAT default does not need AWS_REGION.
        proc, calls = self.run_sync("mi-0123456789abcdef0")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(calls, [])

    def test_explicit_id_requires_region_under_ssm_break_glass(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            aws = root / "aws"
            aws.write_text(AWS_STUB)
            aws.chmod(0o755)
            calls = root / "calls"
            env = {k: v for k, v in os.environ.items()
                   if k not in ("AWS_REGION", "AWS_DEFAULT_REGION", "PROD_SSM_TARGET")}
            env.update(
                PATH=f"{root}:{env['PATH']}",
                CALLS=str(calls),
                TOKENKEY_REMOTE_VIA="ssm",
                STAGE0_SSM_OUTPUT_DIR=str(root / "out"),
            )
            proc = subprocess.run(
                ["bash", str(SCRIPT), "mi-0123456789abcdef0"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 2, proc.stdout)
            self.assertIn("no SSM region", proc.stderr)
            self.assertFalse(calls.exists())


if __name__ == "__main__":
    unittest.main()
