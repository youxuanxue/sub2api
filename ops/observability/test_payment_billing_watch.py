#!/usr/bin/env python3
from __future__ import annotations

import datetime as dt
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))

from ops.observability.payment_billing_watch import (  # noqa: E402
    build_alert_decision,
    build_weekly_card,
    build_weekly_report,
    deliver,
    is_monday_shanghai,
    parse_snapshot,
)


def sample_snapshot(**overrides):
    base = {
        "schema_version": 1,
        "db_now_utc": "2026-10-10T12:00:00Z",
        "anomaly_window_minutes": 15,
        "rapid_window_seconds": 60,
        "period_totals": [
            {
                "period": "last_7d",
                "completed_n": 2,
                "completed_amount": 100.0,
                "non_completed_n": 12,
                "non_completed_amount": 600.0,
                "completed_users": 2,
            },
            {
                "period": "prev_calendar_month",
                "completed_n": 0,
                "completed_amount": 0,
                "non_completed_n": 0,
                "non_completed_amount": 0,
                "completed_users": 0,
            },
            {
                "period": "current_calendar_month",
                "completed_n": 3,
                "completed_amount": 101.0,
                "non_completed_n": 17,
                "non_completed_amount": 643.0,
                "completed_users": 3,
            },
            {
                "period": "all_time",
                "completed_n": 3,
                "completed_amount": 101.0,
                "non_completed_n": 17,
                "non_completed_amount": 643.0,
                "completed_users": 3,
            },
        ],
        "completed_by_provider": [
            {
                "period": "last_7d",
                "provider_key": "easypay",
                "payment_type": "usdt",
                "n": 2,
                "amount": 100.0,
            }
        ],
        "admin_credits": [
            {
                "period": "last_7d",
                "notes_kind": "admin_adjust",
                "n": 1,
                "amount": 1000.0,
                "users": 1,
            }
        ],
        "completed_detail_7d": [
            {
                "id": 13,
                "user_id": 73,
                "user_email": "a@example.com",
                "amount": 50.0,
                "payment_type": "usdt",
                "provider_key": "easypay",
                "payment_trade_no": "",
                "out_trade_no": "sub2_x",
                "created_at_utc": "2026-10-08T09:11:33Z",
                "paid_at_utc": "2026-10-08T09:11:33Z",
                "paid_after_seconds": 0.1,
            }
        ],
        "anomaly_cancel_storms": [
            {
                "user_id": 73,
                "user_email": "a@example.com",
                "n": 6,
                "sum_amount": 300.0,
                "providers": ["stripe"],
                "statuses": ["CANCELLED"],
                "first_at_utc": "2026-10-08T09:11:30Z",
                "last_at_utc": "2026-10-08T09:11:32Z",
            }
        ],
        "anomaly_rapid_creates": [
            {
                "user_id": 73,
                "user_email": "a@example.com",
                "n": 7,
                "sum_amount": 350.0,
                "statuses": ["CANCELLED", "COMPLETED"],
                "first_at_utc": "2026-10-08T09:11:30Z",
                "last_at_utc": "2026-10-08T09:11:33Z",
                "span_seconds": 3.16,
            }
        ],
        "anomaly_suspicious_completed": [
            {
                "id": 13,
                "user_id": 73,
                "user_email": "a@example.com",
                "amount": 50.0,
                "provider_key": "easypay",
                "payment_type": "usdt",
                "payment_trade_no": "",
                "out_trade_no": "sub2_x",
                "created_at_utc": "2026-10-08T09:11:33Z",
                "paid_at_utc": "2026-10-08T09:11:33Z",
                "paid_after_seconds": 0.1,
            }
        ],
        "providers": [
            {
                "id": 1,
                "provider_key": "stripe",
                "name": "stripe",
                "enabled": True,
                "payment_mode": "",
                "supported_types": "card,link",
            },
            {
                "id": 2,
                "provider_key": "easypay",
                "name": "USDT 支付",
                "enabled": True,
                "payment_mode": "popup",
                "supported_types": "usdt",
            },
        ],
    }
    base.update(overrides)
    return base


class PaymentBillingWatchTest(unittest.TestCase):
    def test_cli_help(self) -> None:
        completed = subprocess.run(
            [sys.executable, str(ROOT / "ops/observability/payment_billing_watch.py"), "--help"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("--mode", completed.stdout)

    def test_parse_rejects_bad_schema(self) -> None:
        with self.assertRaisesRegex(Exception, "schema_version"):
            parse_snapshot({"schema_version": 99})

    def test_weekly_report_is_scannable_and_flags_suspicious(self) -> None:
        snap = sample_snapshot(
            completed_by_provider=[
                {
                    "period": "last_7d",
                    "provider_key": "easypay",
                    "payment_type": "usdt",
                    "n": 2,
                    "amount": 100.0,
                }
            ],
            admin_credits=[
                {
                    "period": "last_7d",
                    "notes_kind": "admin_adjust",
                    "n": 1,
                    "amount": 1000.0,
                    "users": 1,
                },
                {
                    "period": "last_7d",
                    "notes_kind": "payment_fulfillment",
                    "n": 2,
                    "amount": 100.0,
                    "users": 2,
                },
                {
                    "period": "all_time",
                    "notes_kind": "admin_adjust",
                    "n": 38,
                    "amount": 2108273.0,
                    "users": 12,
                },
            ],
            completed_detail_7d=[
                {
                    "id": 13,
                    "user_id": 73,
                    "user_email": "a@example.com",
                    "amount": 50.0,
                    "payment_type": "usdt",
                    "provider_key": "easypay",
                    "payment_trade_no": "",
                    "paid_after_seconds": 0.1,
                },
                {
                    "id": 2,
                    "user_id": 1,
                    "user_email": "admin@tokenkey.dev",
                    "amount": 1.0,
                    "payment_type": "stripe",
                    "provider_key": "stripe",
                    "payment_trade_no": "pi_x",
                    "paid_after_seconds": 12.0,
                },
            ],
        )
        report = build_weekly_report(snap)
        self.assertIn("**本周实收**", report)
        self.assertIn("$100", report)
        self.assertIn("本月", report)
        self.assertIn("累计", report)
        self.assertIn("EasyPay / USDT", report)
        self.assertIn("管理员加款", report)
        self.assertNotIn("2108273", report)  # all-time admin noise stays out
        self.assertNotIn("支付履约", report)  # already counted as 实收
        self.assertIn("无上游单号", report)
        self.assertIn("⚠", report)
        self.assertLess(report.index("⚠"), report.index("`#2`"))
        card = build_weekly_card(snap)
        self.assertEqual(card["header"]["template"], "orange")
        self.assertIn("需关注", card["header"]["title"]["content"])

    def test_alert_fires_on_new_anomalies(self) -> None:
        decision = build_alert_decision(sample_snapshot(), prev_keys=set())
        self.assertTrue(decision["should_alert"])
        self.assertIn("cancel_storm", decision["message"])
        self.assertIn("rapid_create", decision["message"])
        self.assertIn("suspicious_completed", decision["message"])
        self.assertGreaterEqual(len(decision["new_keys"]), 3)

    def test_alert_dedupes_known_keys(self) -> None:
        snap = sample_snapshot()
        first = build_alert_decision(snap, prev_keys=set())
        second = build_alert_decision(snap, prev_keys=set(first["active_keys"]))
        self.assertFalse(second["should_alert"])
        self.assertEqual(second["new_keys"], [])

    def test_monday_shanghai_gate(self) -> None:
        monday = dt.datetime(2026, 10, 12, 1, 0, tzinfo=dt.timezone.utc)  # 09:00 CST Monday
        sunday = dt.datetime(2026, 10, 11, 1, 0, tzinfo=dt.timezone.utc)
        self.assertTrue(is_monday_shanghai(monday))
        self.assertFalse(is_monday_shanghai(sunday))

    def test_deliver_dry_run_weekly_force(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            state = root / "keys.json"
            snap_path = root / "snap.json"
            snap_path.write_text(json.dumps(sample_snapshot()), encoding="utf-8")
            completed = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / "ops/observability/payment_billing_watch.py"),
                    "--snapshot",
                    str(snap_path),
                    "--mode",
                    "all",
                    "--force-weekly",
                    "--dry-run",
                    "--state-file",
                    str(state),
                ],
                cwd=ROOT,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertIn("支付周报", completed.stdout)
            self.assertIn("本周实收", completed.stdout)
            self.assertIn("支付异常告警", completed.stdout)
            self.assertFalse(state.exists())  # dry-run must not advance state

    def test_deliver_saves_state_when_not_dry_run_with_mocked_post(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            state = root / "keys.json"
            # Monkeypatch post_feishu via deliver's import path by using dry_run False
            # but empty webhook would fail — use dry_run path for state: call deliver
            # with a stub by temporarily patching module attribute.
            import ops.observability.payment_billing_watch as mod

            posted: list[str] = []

            def fake_post(message, *, webhook_url, signing_secret, opener=None, now=None):
                posted.append(message)

            original = mod.post_feishu
            mod.post_feishu = fake_post  # type: ignore[assignment]
            try:
                result = deliver(
                    snapshot=sample_snapshot(),
                    mode="alert",
                    state_file=state,
                    dry_run=False,
                    webhook_url="https://example.invalid/hook",
                    signing_secret="secret",
                    force_weekly=False,
                    now=dt.datetime(2026, 10, 10, 12, 0, tzinfo=dt.timezone.utc),
                )
            finally:
                mod.post_feishu = original  # type: ignore[assignment]
            self.assertIn("alert-delivered", result["actions"])
            self.assertTrue(state.is_file())
            self.assertEqual(len(posted), 1)
            saved = json.loads(state.read_text(encoding="utf-8"))
            self.assertIn("payment:cancel_storm:user:73", saved["active_keys"])


if __name__ == "__main__":
    unittest.main()
