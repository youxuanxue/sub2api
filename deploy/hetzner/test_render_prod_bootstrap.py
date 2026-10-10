#!/usr/bin/env python3
"""Smoke checks for render-prod-bootstrap.sh artifact."""
from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
RENDER = HERE / "render-prod-bootstrap.sh"
OUT = HERE / "generated-prod-user-data.sh"


class RenderProdBootstrapTests(unittest.TestCase):
    def test_render_writes_volume_and_shebang(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env_out = Path(tmp) / "generated-prod-user-data.sh"
            proc = subprocess.run(
                ["bash", str(RENDER)],
                cwd=str(HERE.parent.parent),
                env={**os.environ, "OUT": str(env_out)},
                capture_output=True,
                text=True,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
            body = env_out.read_text(encoding="utf-8")
        self.assertTrue(body.startswith("#!/bin/bash"))
        self.assertIn("VOLUME_ID", body)
        self.assertIn("scsi-0HC_Volume_", body)
        self.assertIn("/var/lib/tokenkey", body)
        self.assertIn("HETZNER_PROD_BOOTSTRAP", body)
        self.assertIn("tokenkey/hetzner/prod/stage0/env-secrets-backup", body)
        self.assertIn("tokenkey-pgdump.timer", body)
        self.assertIn("tokenkey-disk-metrics.timer", body)
        self.assertIn("tokenkey-ghcr-prune-daily.timer", body)
        self.assertIn("TOKENKEY_PGDUMP_S3_URI", body)
        self.assertIn("QA_CAPTURE_ENABLED=false", body)

    def test_committed_artifact_matches_render_when_present(self) -> None:
        if not OUT.exists():
            self.skipTest("generated-prod-user-data.sh not committed yet")
        proc = subprocess.run(
            ["bash", str(RENDER), "--check"],
            cwd=str(HERE.parent.parent),
            capture_output=True,
            text=True,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)


if __name__ == "__main__":
    unittest.main()
