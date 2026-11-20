#!/usr/bin/env python3
"""Contract: Fleet pgdump restore-canary OIDC workflow is retired."""

from __future__ import annotations

import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github" / "workflows" / "ops-pgdump-restore-canary.yml"


class PgdumpRestoreCanaryWorkflowTest(unittest.TestCase):
    def test_oidc_canary_workflow_removed(self) -> None:
        self.assertFalse(
            WORKFLOW.exists(),
            "ops-pgdump-restore-canary.yml deleted per tencent-ops closeout (A12)",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
