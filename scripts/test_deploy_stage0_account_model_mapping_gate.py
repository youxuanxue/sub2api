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

    def test_go_helper_environment_is_available(self) -> None:
        gate = self.text.index("Pre-deploy account model_mapping SSOT gate")
        new_api = self.text.index("./.github/actions/cache-and-checkout-new-api")
        setup_go = self.text.index("Setup Go for account model_mapping SSOT gate")
        self.assertLess(new_api, gate)
        self.assertLess(setup_go, gate)
        self.assertIn("go-version-file: backend/go.mod", self.text)


if __name__ == "__main__":
    unittest.main()
