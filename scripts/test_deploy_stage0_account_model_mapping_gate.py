#!/usr/bin/env python3
"""Workflow shape tests for the prod account model_mapping release gate."""
from __future__ import annotations

import pathlib
import unittest

_WORKFLOW = pathlib.Path(__file__).resolve().parents[1] / ".github/workflows/deploy-stage0.yml"


class DeployStage0AccountModelMappingGateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.text = _WORKFLOW.read_text()

    def test_gate_runs_before_ssm_deploy(self) -> None:
        gate = self.text.index("Pre-deploy account model_mapping SSOT gate")
        deploy = self.text.index("Deploy via SSM Run-Command")
        self.assertLess(gate, deploy)
        self.assertIn("manage-account-model-mapping-runtime.py release-gate", self.text)
        self.assertIn('--prod-instance-id "$INSTANCE_ID"', self.text)
        self.assertIn('--ssot-repo-root "$GITHUB_WORKSPACE/.cache/release-source"', self.text)

    def test_go_helper_uses_release_tag_source(self) -> None:
        gate = self.text.index("Pre-deploy account model_mapping SSOT gate")
        release_checkout = self.text.index("Checkout release source for account model_mapping SSOT")
        new_api = self.text.index("Checkout release new-api sibling for account model_mapping SSOT")
        setup_go = self.text.index("Setup Go for account model_mapping SSOT gate")
        self.assertLess(release_checkout, new_api)
        self.assertLess(new_api, setup_go)
        self.assertLess(setup_go, gate)
        self.assertIn("ref: ${{ format('v{0}', env.INPUT_TAG) }}", self.text)
        self.assertIn('bash scripts/ci/ensure-new-api-sibling.sh "$GITHUB_WORKSPACE/.cache/release-source"', self.text)
        self.assertIn("go-version-file: .cache/release-source/backend/go.mod", self.text)


if __name__ == "__main__":
    unittest.main()
