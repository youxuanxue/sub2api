"""Behavior tests for Hetzner user-data render artifact."""
from __future__ import annotations

import pathlib
import subprocess
import unittest

HERE = pathlib.Path(__file__).resolve().parent
REPO_ROOT = HERE.parents[1]
RENDER = HERE / "render-bootstrap.sh"
GENERATED = HERE / "generated-user-data.sh"


class HetznerRenderBootstrapTests(unittest.TestCase):
    def test_check_passes_for_committed_artifact(self):
        proc = subprocess.run(
            ["bash", str(RENDER), "--check"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("OK", proc.stdout)

    def test_generated_restores_hetzner_isolated_secrets_path(self):
        content = GENERATED.read_text(encoding="utf-8")
        self.assertIn(
            '"/tokenkey/hetzner/${EDGE_ID}/stage0/env-secrets-backup"',
            content,
        )
        self.assertNotIn(
            '"/tokenkey/edge/${EDGE_ID}/stage0/env-secrets-backup"',
            content,
        )
        self.assertIn("tokenkey-hetzner-bootstrap.log", content)

    def test_generated_installs_awscliv2_not_apt_awscli(self):
        content = GENERATED.read_text(encoding="utf-8")
        self.assertIn("awscli-exe-linux-", content)
        self.assertNotIn("gettext-base awscli", content)
        self.assertIn("GHCR PAT unavailable", content)
        self.assertIn(
            "ExecStartPre=-/usr/bin/docker compose --env-file /var/lib/tokenkey/.env pull",
            content,
        )


if __name__ == "__main__":
    unittest.main()
