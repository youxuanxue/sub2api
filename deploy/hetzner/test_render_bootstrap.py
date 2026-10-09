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
        self.assertIn("GHCR docker login failed; continuing with anonymous pull", content)
        self.assertIn(
            "ExecStartPre=-/usr/bin/docker compose --env-file /var/lib/tokenkey/.env pull",
            content,
        )

    def test_generated_injects_qa_capture_disabled_into_compose(self):
        """Edge smoke requires in-container QA_CAPTURE_ENABLED=false; .env alone is insufficient."""
        content = GENERATED.read_text(encoding="utf-8")
        self.assertIn("QA_CAPTURE_ENABLED=false", content)
        self.assertIn(
            "QA_CAPTURE_ENABLED=${QA_CAPTURE_ENABLED:-false}",
            content,
        )
        self.assertIn("SERVER_FRONTEND_URL=", content)

    def test_provision_user_data_prefix_starts_with_shebang_before_exports(self):
        """Regression: Ubuntu cloud-init ignores exports-first user-data."""
        provision = (HERE / "provision-edge.sh").read_text(encoding="utf-8")
        marker = "user_data_file=\"$(mktemp)\""
        start = provision.index(marker)
        block = provision[start : start + 1200]
        shebang_at = block.index("#!/bin/bash")
        export_at = block.index("export EDGE_ID=")
        self.assertLess(
            shebang_at,
            export_at,
            "cloud-init requires shebang before env exports in user-data",
        )


if __name__ == "__main__":
    unittest.main()
