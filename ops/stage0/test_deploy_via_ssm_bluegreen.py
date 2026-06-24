#!/usr/bin/env python3
"""Tests for ops/stage0/deploy_via_ssm_bluegreen.sh.

The prod blue/green deploy primitive renders a compact SSM command list that
base64-delivers the real host script. These tests use the STAGE0_RENDER_ONLY
seam so they can assert the generated contract without touching AWS.
"""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "deploy_via_ssm_bluegreen.sh"
_PROD_IID = "i-0prod000000000000"
_EDGE_IID = "mi-0edge000000000000"


def _render(instance_id: str = _PROD_IID, tag: str = "1.8.99", env_extra: dict | None = None):
    out_dir = pathlib.Path(tempfile.mkdtemp(prefix="bluegreen-render-"))
    env = {
        **os.environ,
        "STAGE0_RENDER_ONLY": "1",
        "STAGE0_SSM_OUTPUT_DIR": str(out_dir),
    }
    if env_extra:
        env.update(env_extra)
    proc = subprocess.run(
        ["bash", str(_SCRIPT), tag, instance_id],
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    params = None
    remote = None
    params_path = out_dir / "ssm-params.json"
    remote_path = out_dir / "bluegreen-remote.sh"
    if params_path.exists():
        params = json.loads(params_path.read_text())
    if remote_path.exists():
        remote = remote_path.read_text()
    return proc, params, remote


class BlueGreenRenderTest(unittest.TestCase):
    def test_rejects_lightsail_edge_ids(self) -> None:
        proc, params, remote = _render(_EDGE_IID, env_extra={"EDGE_ID": "us2"})
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("prod-only primitive", proc.stderr)
        self.assertIsNone(params)
        self.assertIsNone(remote)

    def test_remote_script_parses(self) -> None:
        proc, params, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIsNotNone(params)
        self.assertIsNotNone(remote)
        parsed = subprocess.run(
            ["bash", "-n"],
            input=remote,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(parsed.returncode, 0, msg=parsed.stderr)

    def test_renders_bluegreen_contract(self) -> None:
        proc, params, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        assert remote is not None
        commands = params["commands"]
        joined = "\n".join(commands)

        self.assertEqual(params.get("executionTimeout"), ["1200"])
        self.assertIn("/tmp/tokenkey-bluegreen-deploy.sh", joined)
        self.assertIn("TAG='1.8.99'", joined)
        self.assertIn("QA_CAPTURE_EXPORT_STORAGE_BUCKET='tokenkey-prod-qa-exports-682751977094'", joined)
        self.assertIn("MEDIA_STORAGE_BUCKET='tokenkey-prod-media-682751977094'", joined)
        self.assertIn("GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS='8'", joined)

        self.assertIn("docker-compose.bluegreen.yml", remote)
        self.assertIn("active-color", remote)
        self.assertIn("tokenkey-blue", remote)
        self.assertIn("tokenkey-green", remote)
        self.assertIn("TOKENKEY_IMAGE_BLUE", remote)
        self.assertIn("TOKENKEY_IMAGE_GREEN", remote)
        self.assertIn("write_caddy_for_color", remote)
        self.assertIn("render_caddy_with_upstream", remote)
        self.assertIn("could not rewrite exactly one reverse_proxy upstream", remote)
        self.assertIn("wait_ready", remote)
        self.assertIn("http://localhost:8080/health", remote)
        self.assertIn("drain_container \"${active_container}\"", remote)
        self.assertIn("sudo docker rm -f tokenkey", remote)
        self.assertIn("DATABASE_HOST=tokenkey-postgres", remote)
        self.assertIn("REDIS_HOST=tokenkey-redis", remote)

    def test_values_are_env_overridable(self) -> None:
        proc, params, _ = _render(env_extra={
            "QA_CAPTURE_EXPORT_STORAGE_BUCKET": "custom-qa",
            "MEDIA_STORAGE_BUCKET": "custom-media",
            "GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS": "16",
        })
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        joined = "\n".join(params["commands"])
        self.assertIn("QA_CAPTURE_EXPORT_STORAGE_BUCKET='custom-qa'", joined)
        self.assertIn("MEDIA_STORAGE_BUCKET='custom-media'", joined)
        self.assertIn("GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS='16'", joined)


if __name__ == "__main__":
    unittest.main()
