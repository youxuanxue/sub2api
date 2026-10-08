#!/usr/bin/env python3
"""Unit tests for plan_account_usage_report period parsing and markdown render."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

try:
    from ops.observability import plan_account_usage_report as REPORT
except ModuleNotFoundError:
    import plan_account_usage_report as REPORT


def _account(
    *,
    plan_kind: str,
    account_id: int,
    name: str,
    target: str = "prod",
    month_cost: float = 10.0,
    max5h: float = 1.5,
    max7d: float = 8.0,
    peak_rpm: int = 12,
    peak_tpm: int = 1_500_000,
) -> dict:
    return {
        "target": target,
        "plan_kind": plan_kind,
        "account_id": account_id,
        "name": name,
        "created_at_utc": "2026-09-05T06:11:44+00:00",
        "month_period": {
            "reqs": 100,
            "tokens": 2000,
            "total_cost": month_cost,
            "actual_cost": month_cost - 0.1,
        },
        "max_5h": {
            "total_cost": max5h,
            "window_start": "2026-09-22T17:00:00+00:00",
            "window_end": "2026-09-22T22:00:00+00:00",
        },
        "max_7d": {
            "total_cost": max7d,
            "window_start": "2026-09-21T16:00:00+00:00",
            "window_end": "2026-09-28T16:00:00+00:00",
        },
        "peaks": {
            "peak_rpm": peak_rpm,
            "peak_tpm": peak_tpm,
            "peak_rpm_at": "2026-09-22T18:33:00+00:00",
            "peak_tpm_at": "2026-09-22T19:55:00+00:00",
        },
    }


class PlanAccountUsageReportTest(unittest.TestCase):
    def test_parse_month_shanghai_bounds(self) -> None:
        start, end = REPORT.parse_month("2026-09")
        self.assertEqual(start.isoformat(), "2026-09-01T00:00:00+08:00")
        self.assertEqual(end.isoformat(), "2026-10-01T00:00:00+08:00")

    def test_parse_month_december(self) -> None:
        start, end = REPORT.parse_month("2026-12")
        self.assertEqual(start.isoformat(), "2026-12-01T00:00:00+08:00")
        self.assertEqual(end.isoformat(), "2027-01-01T00:00:00+08:00")

    def test_format_psql_timestamptz(self) -> None:
        start, _ = REPORT.parse_month("2026-09")
        self.assertEqual(REPORT.format_psql_timestamptz(start), "2026-09-01 00:00:00+08")

    def test_parse_probe_rows_ignores_summary(self) -> None:
        stdout = "\n".join(
            [
                "=== header ===",
                json.dumps(_account(plan_kind="ali_token_plan", account_id=1, name="a")),
                json.dumps({"plan_kind": "ali_token_plan", "accounts": 1}),
            ]
        )
        rows = REPORT.parse_probe_rows(stdout, target="prod")
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["target"], "prod")
        self.assertEqual(rows[0]["account_id"], 1)

    def test_render_markdown_uses_tpm_millions_without_dollar(self) -> None:
        start, end = REPORT.parse_month("2026-09")
        manifest = {
            "period_start": start.isoformat(),
            "period_end": end.isoformat(),
            "sampled_at_utc": "2026-10-08T12:00:00Z",
            "targets": ["prod", "edge:us3"],
        }
        documents = [
            {
                "target": "prod",
                "accounts": [
                    _account(
                        plan_kind="ali_token_plan",
                        account_id=129,
                        name="ali-token-plan",
                        peak_tpm=1_050_163,
                    ),
                    _account(
                        plan_kind="volcengine_agent_plan",
                        account_id=88,
                        name="volcengine-agent-plan",
                        month_cost=204.14,
                        peak_tpm=4_824_966,
                    ),
                ],
            },
            {
                "target": "edge:us3",
                "accounts": [
                    _account(
                        plan_kind="volcengine_agent_plan",
                        account_id=21,
                        name="volcengine-agent-plan",
                        target="edge:us3",
                        month_cost=36.15,
                        peak_tpm=0,
                    )
                ],
            },
        ]
        md = REPORT.render_markdown(manifest=manifest, documents=documents)
        self.assertNotIn("$", md)
        self.assertIn("peak_tpm_m", md)
        self.assertIn("| 1.05 |", md)
        self.assertIn("| 4.82 |", md)
        self.assertIn("非重叠", md)
        self.assertIn("| prod | Ali Token Plan | 1 | 10.00 |", md)
        self.assertIn("| edge | VolcEngine Agent Plan | 1 | 36.15 |", md)

    def test_load_raw_dir_roundtrip(self) -> None:
        start, end = REPORT.parse_month("2026-09")
        with tempfile.TemporaryDirectory() as tmp:
            raw_dir = Path(tmp)
            manifest = {
                "period_start": start.isoformat(),
                "period_end": end.isoformat(),
                "sampled_at_utc": "2026-10-08T12:00:00Z",
                "targets": ["prod"],
            }
            doc = {
                "target": "prod",
                "accounts": [
                    _account(plan_kind="nvidia_build", account_id=138, name="nvidia-build-1")
                ],
            }
            (raw_dir / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
            (raw_dir / "prod.json").write_text(json.dumps(doc), encoding="utf-8")
            loaded_manifest, documents = REPORT.load_raw_dir(raw_dir)
            md = REPORT.render_markdown(manifest=loaded_manifest, documents=documents)
            self.assertIn("nvidia-build-1", md)
            self.assertIn("### 3.4 NVIDIA Build", md)


if __name__ == "__main__":
    unittest.main()
