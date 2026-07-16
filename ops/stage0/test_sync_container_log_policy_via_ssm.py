#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name("sync-container-log-policy-via-ssm.sh")


class SyncContainerLogPolicyTest(unittest.TestCase):
    def test_rendered_command_recreates_only_caddy_with_rollback(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env = os.environ.copy()
            env.update({"STAGE0_RENDER_ONLY": "1", "STAGE0_SSM_OUTPUT_DIR": tmp})
            proc = subprocess.run(
                ["bash", str(SCRIPT), "i-0stub"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr)
            payload = json.loads((pathlib.Path(tmp) / "ssm-params.json").read_text())
            commands = "\n".join(payload["commands"])
            self.assertIn("trap rollback ERR", commands)
            self.assertIn("--force-recreate caddy", commands)
            self.assertNotIn("--force-recreate postgres", commands)
            self.assertNotIn("--force-recreate redis", commands)
            self.assertIn('[ "$MAX_SIZE" = 100m ]', commands)
            self.assertIn('[ "$MAX_FILE" = 5 ]', commands)
            self.assertIn("sync-container-log-policy: OK", commands)
            parsed = subprocess.run(
                ["bash", "-n"], input=commands, capture_output=True, text=True, check=False
            )
            self.assertEqual(parsed.returncode, 0, msg=parsed.stderr)

    def test_rejects_non_prod_instance_shape(self) -> None:
        proc = subprocess.run(
            ["bash", str(SCRIPT), "mi-edge"], capture_output=True, text=True, check=False
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("prod EC2 instance id", proc.stderr)


if __name__ == "__main__":
    unittest.main()
