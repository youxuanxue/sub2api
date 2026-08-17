#!/usr/bin/env python3
"""Tests for edge-only QA capture disable injection in deploy_via_ssm.sh."""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "deploy_via_ssm.sh"
_PROD_IID = "i-0prod000000000000"
_EDGE_IID = "mi-0edge000000000000"


def _render(instance_id: str, tag: str = "1.8.99", env_extra: dict | None = None):
    out_dir = tempfile.mkdtemp(prefix="edge-qa-capture-render-")
    env = {**os.environ, "STAGE0_RENDER_ONLY": "1", "STAGE0_SSM_OUTPUT_DIR": out_dir}
    if env_extra:
        env.update(env_extra)
    proc = subprocess.run(
        ["bash", str(_SCRIPT), tag, instance_id],
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    params = json.loads((pathlib.Path(out_dir) / "ssm-params.json").read_text())
    return proc, params["commands"]


def _edge_qa_cmds(commands: list[str]) -> list[str]:
    return [c for c in commands if "QA_CAPTURE_ENABLED" in c]


class EdgeQACaptureDisableRenderTest(unittest.TestCase):
    def test_edge_injects_exactly_two_guarded_commands(self) -> None:
        proc, commands = _render(_EDGE_IID, env_extra={"EDGE_ID": "us6"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        edge_cmds = _edge_qa_cmds(commands)
        self.assertEqual(len(edge_cmds), 2, msg=edge_cmds)
        env_cmd, compose_cmd = edge_cmds
        self.assertIn("QA_CAPTURE_ENABLED=false", env_cmd)
        self.assertIn("sed -i", env_cmd)
        self.assertIn("SERVER_FRONTEND_URL", compose_cmd)
        self.assertIn("QA_CAPTURE_ENABLED=${QA_CAPTURE_ENABLED:-}", compose_cmd)

    def test_prod_gets_no_edge_qa_capture_injection(self) -> None:
        proc, commands = _render(_PROD_IID)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(_edge_qa_cmds(commands), [])

    def test_prod_minus_edge_counts_prod_only_and_edge_only_blocks(self) -> None:
        _, prod = _render(_PROD_IID)
        _, edge = _render(_EDGE_IID, env_extra={"EDGE_ID": "us6"})
        prod_only = sum(
            1
            for c in prod
            if "QA_BUNDLE_" in c
            or "QA_ARCHIVE_" in c
            or "MEDIA_STORAGE_" in c
            or "GATEWAY_IMAGE_CONCURRENCY" in c
        )
        edge_only = len(_edge_qa_cmds(edge))
        self.assertEqual(prod_only, 8)
        self.assertEqual(edge_only, 2)
        self.assertEqual(len(prod) - len(edge), prod_only - edge_only)


if __name__ == "__main__":
    unittest.main()
