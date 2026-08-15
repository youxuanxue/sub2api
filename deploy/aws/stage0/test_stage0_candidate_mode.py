#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import json
import os
import subprocess
import tempfile
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
BOOTSTRAP = REPO_ROOT / "deploy/aws/stage0/stage0-ec2-bootstrap.sh"
CANDIDATE_SSM = REPO_ROOT / "ops/stage0/update_ec2_edge_candidate_via_ssm.sh"


class Stage0CandidateModeTest(unittest.TestCase):
    def test_candidate_mode_stops_public_and_write_services_after_bootstrap(self) -> None:
        text = BOOTSTRAP.read_text(encoding="utf-8")
        self.assertIn('if [ "${TK_CANDIDATE_MODE:-0}" = "1" ]', text)
        self.assertIn("systemctl disable --now tokenkey.service tokenkey-pgdump.timer", text)
        self.assertIn("docker compose --env-file /var/lib/tokenkey/.env up -d --no-deps postgres redis", text)
        self.assertIn("CANDIDATE_READY", text)

    def test_candidate_update_keeps_app_and_caddy_stopped(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env = os.environ.copy()
            env.update(
                {
                    "STAGE0_RENDER_ONLY": "1",
                    "STAGE0_SSM_OUTPUT_DIR": tmp,
                }
            )
            completed = subprocess.run(
                ["bash", str(CANDIDATE_SSM), "1.8.141", "i-0123456789abcdef0"],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(0, completed.returncode, completed.stderr)
            params = json.loads((pathlib.Path(tmp) / "ssm-params.json").read_text())
            commands = "\n".join(params["commands"])

        self.assertIn("pull tokenkey", commands)
        self.assertIn(".write-owner-locked", commands)
        self.assertIn(".target-write-owner-active", commands)
        self.assertIn("flock -n", commands)
        self.assertIn("stop tokenkey caddy", commands)
        self.assertIn("tokenkey-postgres", commands)
        self.assertIn("tokenkey-redis", commands)
        self.assertNotIn("up -d", commands)
        self.assertNotIn("start tokenkey", commands)
        self.assertNotIn("start caddy", commands)

    def test_candidate_update_rejects_invalid_tag_before_rendering_ssm(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env = os.environ.copy()
            env.update({"STAGE0_RENDER_ONLY": "1", "STAGE0_SSM_OUTPUT_DIR": tmp})
            completed = subprocess.run(
                ["bash", str(CANDIDATE_SSM), "1.8.141; touch /tmp/injected", "i-0123456789abcdef0"],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                capture_output=True,
                check=False,
            )

            self.assertNotEqual(0, completed.returncode)
            self.assertFalse((pathlib.Path(tmp) / "ssm-params.json").exists())
            self.assertIn("tag must match", completed.stderr)


if __name__ == "__main__":
    unittest.main()
