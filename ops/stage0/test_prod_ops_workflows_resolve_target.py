#!/usr/bin/env python3
"""Prod ops workflows must resolve the control plane via resolve_prod_ssm_target."""
from __future__ import annotations

import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github" / "workflows"

# Host-mutating / warm paths that must follow the cutover-aware resolver.
_PROD_RESOLVE_WORKFLOWS = (
    "warm-image-stage0.yml",
    "ops-stage0-pg-dump-refresh.yml",
    "ops-stage0-host-mem-guard.yml",
    "ops-stage0-ghcr-prune-timer.yml",
    "sync-docs-to-pages.yml",
)

ROOT_SCRIPTS = ROOT / "ops" / "stage0"


class ProdOpsWorkflowResolveTargetTest(unittest.TestCase):
    def test_listed_workflows_use_resolve_prod_ssm_target(self) -> None:
        for name in _PROD_RESOLVE_WORKFLOWS:
            with self.subTest(workflow=name):
                text = (WORKFLOWS / name).read_text(encoding="utf-8")
                self.assertIn("ops/stage0/resolve_prod_ssm_target.py", text)
                self.assertIn("PROD_SSM_TARGET", text)
                self.assertIn("ssm_region", text)
                # Must not keep a bare CFN InstanceId resolve as the only path.
                self.assertNotIn(
                    "Outputs[?OutputKey==`InstanceId`].OutputValue",
                    text,
                )

    def test_error_clustering_installer_is_cutover_aware(self) -> None:
        text = (ROOT_SCRIPTS / "deploy-error-clustering-binary.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("resolve_prod_ssm_target.py", text)
        self.assertNotIn("Outputs[?OutputKey==`InstanceId`].OutputValue", text)
        # Hybrid dash rejects bare pipefail; must wrap before send-command.
        self.assertIn("ssm_wrap_bash_commands.py", text)
        wrap_at = text.index("ssm_wrap_bash_commands.py")
        send_at = text.index("aws ssm send-command")
        self.assertLess(wrap_at, send_at)

    def test_resolve_prod_running_tag_is_cutover_aware(self) -> None:
        text = (ROOT_SCRIPTS / "resolve-prod-running-tag-via-ssm.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("resolve_prod_ssm_target.py", text)
        self.assertNotIn("Outputs[?OutputKey==`InstanceId`].OutputValue", text)

    def test_pgdump_restore_canary_passes_prod_ssm_target(self) -> None:
        # Canary reaches prod via run-probe (indirect resolve). Without
        # PROD_SSM_TARGET in job env, OIDC AccessDenied on the flip param
        # silent-fallbacked to standby EC2 after Wave B cutover.
        text = (WORKFLOWS / "ops-pgdump-restore-canary.yml").read_text(encoding="utf-8")
        self.assertIn("PROD_SSM_TARGET", text)
        self.assertIn("vars.PROD_SSM_TARGET || 'auto'", text)
        self.assertIn("run-probe.sh", text)


if __name__ == "__main__":
    unittest.main()
