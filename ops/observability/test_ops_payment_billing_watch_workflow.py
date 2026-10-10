#!/usr/bin/env python3
"""Contract checks for ops-payment-billing-watch.yml."""

from __future__ import annotations

import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github" / "workflows" / "ops-payment-billing-watch.yml"


class OpsPaymentBillingWatchWorkflowTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.text = WORKFLOW.read_text(encoding="utf-8")

    def test_schedule_covers_alert_and_monday_weekly(self) -> None:
        self.assertIn("17,47 * * * *", self.text)
        self.assertIn("0 1 * * 1", self.text)

    def test_uses_shared_feishu_secrets_and_oidc(self) -> None:
        self.assertIn("TK_FEISHU_WEBHOOK_URL", self.text)
        self.assertIn("TK_FEISHU_SIGNING_SECRET", self.text)
        self.assertIn("AWS_OIDC_ROLE_ARN", self.text)
        self.assertIn("configure-aws-credentials", self.text)

    def test_probe_and_delivery_owners(self) -> None:
        self.assertIn("ops/observability/probe-payment-billing-watch.sh", self.text)
        self.assertIn("ops/observability/payment_billing_watch.py", self.text)
        self.assertIn("run-probe.sh", self.text)
        self.assertIn("--target prod", self.text)

    def test_dry_run_does_not_require_feishu_when_true(self) -> None:
        self.assertIn('"$DRY_RUN" != "true"', self.text)
        self.assertIn("retention-days: 3", self.text)

    def test_dedupe_state_not_saved_on_dry_run(self) -> None:
        self.assertIn("env.DRY_RUN != 'true'", self.text)
        self.assertIn("payment-billing-watch-state", self.text)


if __name__ == "__main__":
    unittest.main()
