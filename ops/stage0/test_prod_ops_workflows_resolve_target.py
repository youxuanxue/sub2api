#!/usr/bin/env python3
"""Prod ops workflows must use cutover-aware resolvers where they target one host."""
from __future__ import annotations

import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github" / "workflows"

ROOT_SCRIPTS = ROOT / "ops" / "stage0"


class ProdOpsWorkflowResolveTargetTest(unittest.TestCase):
    def test_container_log_policy_uses_tat_not_oidc(self) -> None:
        text = (WORKFLOWS / "ops-stage0-container-log-policy.yml").read_text(encoding="utf-8")
        self.assertIn("environment: tencent-ops", text)
        self.assertIn("--via tat", text)
        self.assertIn("run-probe.sh", text)
        self.assertIn("probe-sync-container-log-policy.sh", text)
        self.assertIn("--node", text)
        self.assertNotIn("aws-actions/configure-aws-credentials", text)
        self.assertNotIn("AWS_OIDC_ROLE_ARN", text)
        self.assertNotIn("id-token: write", text)
        self.assertNotIn("resolve_prod_ssm_target.py", text)
        self.assertNotIn("sync-container-log-policy-via-ssm.sh", text)

    def test_warm_workflow_warms_every_app_node(self) -> None:
        text = (WORKFLOWS / "warm-image-stage0.yml").read_text(encoding="utf-8")
        self.assertIn("revision:", text)
        self.assertIn("python3 deploy/domestic/warm_fanout.py warm-all", text)
        self.assertIn("deploy/tencent/candidate-usw-20261022.json", text)
        self.assertIn("--transport ghcr", text)
        self.assertIn("--channel tat", text)
        self.assertNotIn("--channel auto", text)
        self.assertIn("environment: tencent-ops", text)
        self.assertIn("TENCENTCLOUD_SECRET_ID", text)
        self.assertNotIn("aws-actions/configure-aws-credentials", text)
        self.assertNotIn("AWS_OIDC_ROLE_ARN", text)
        self.assertNotIn("id-token: write", text)
        self.assertIn("--receipt-out prod-warm-receipt.json", text)
        self.assertIn("prod-warm-receipt.json", text)
        # Multi-node sequential warm needs headroom beyond the old single-host 15m.
        self.assertRegex(text, r"timeout-minutes:\s*(2[5-9]|[3-9]\d|[1-9]\d{2,})\b")

    def test_ghcr_prune_timer_workflow_uses_tat_on_all_apps(self) -> None:
        text = (WORKFLOWS / "ops-stage0-ghcr-prune-timer.yml").read_text(encoding="utf-8")
        self.assertIn("python3 ops/stage0/sync_ghcr_prune_timer.py", text)
        self.assertIn("deploy/tencent/candidate-usw-20261022.json", text)
        self.assertIn("--via tat", text)
        self.assertIn("environment: tencent-ops", text)
        self.assertIn("TENCENTCLOUD_SECRET_ID", text)
        self.assertNotIn("aws-actions/configure-aws-credentials", text)
        self.assertNotIn("AWS_OIDC_ROLE_ARN", text)
        self.assertNotIn("id-token: write", text)
        self.assertNotIn("sync-ghcr-prune-timer-via-ssm.sh", text)
        self.assertNotIn("resolve_prod_ssm_target.py", text)
        self.assertIn("prod-ghcr-prune-timer-receipt.json", text)

    def test_feishu_sync_uses_tat_not_oidc(self) -> None:
        workflow = (WORKFLOWS / "prod-feishu-config-sync.yml").read_text(encoding="utf-8")
        self.assertIn("environment: tencent-ops", workflow)
        self.assertIn("--via tat", workflow)
        self.assertIn("run-probe.sh", workflow)
        self.assertNotIn("aws-actions/configure-aws-credentials", workflow)
        self.assertNotIn("AWS_OIDC_ROLE_ARN", workflow)
        self.assertNotIn("id-token: write", workflow)
        self.assertNotIn("ap-northeast-1", workflow)

    def test_error_clustering_installer_is_cutover_aware(self) -> None:
        text = (ROOT_SCRIPTS / "deploy-error-clustering-binary.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("resolve_prod_ssm_target.py", text)
        self.assertIn("--node", text)
        self.assertNotIn("Outputs[?OutputKey==`InstanceId`].OutputValue", text)
        # Hybrid dash rejects bare pipefail; must wrap before remote transport.
        self.assertIn("ssm_wrap_bash_commands.py", text)
        wrap_at = text.index("ssm_wrap_bash_commands.py")
        transport_at = text.index("execute_remote_params.py", wrap_at)
        self.assertLess(wrap_at, transport_at)

    def test_error_clustering_installer_go_image_matches_module(self) -> None:
        # Official golang images set GOTOOLCHAIN=local; a stale image tag
        # fails the build when tools/error_clustering/go.mod advances.
        installer = (ROOT_SCRIPTS / "deploy-error-clustering-binary.sh").read_text(
            encoding="utf-8"
        )
        go_mod = (ROOT / "tools" / "error_clustering" / "go.mod").read_text(
            encoding="utf-8"
        )
        require = next(
            line.split()[1]
            for line in go_mod.splitlines()
            if line.startswith("go ")
        )
        self.assertIn(f"golang:{require}-alpine", installer)

    def test_resolve_prod_running_tag_is_cutover_aware(self) -> None:
        text = (ROOT_SCRIPTS / "resolve-prod-running-tag-via-ssm.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("resolve_prod_ssm_target.py", text)
        self.assertIn("--node", text)
        self.assertNotIn("Outputs[?OutputKey==`InstanceId`].OutputValue", text)

    def test_dispatch_refuses_legacy_deploy_stage0_entry(self) -> None:
        # Workflow body retained for sentinel/contract tests; daily entry is
        # refused by scripts/stage0/dispatch-prod-deploy.sh (closeout A14).
        text = (ROOT / "scripts" / "stage0" / "dispatch-prod-deploy.sh").read_text(
            encoding="utf-8"
        )
        self.assertIn("legacy-deploy|replay|smoke-only", text)
        self.assertIn("retired", text)


if __name__ == "__main__":
    unittest.main()
