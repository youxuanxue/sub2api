#!/usr/bin/env python3
"""Unit tests for Prod Ops issue sync helpers."""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

REPO_ROOT = Path(__file__).resolve().parents[2]
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from ops.observability.open_prod_ops_issues import (  # noqa: E402
    recover_body,
    signature_label,
    sync_issues,
)


class OpenProdOpsIssuesTest(unittest.TestCase):
    def test_signature_label_is_stable(self) -> None:
        sig = "daily-errors|prod|ok"
        self.assertEqual(signature_label(sig), signature_label(sig))
        self.assertTrue(signature_label(sig).startswith("ops-sig:"))

    def test_sync_issues_closes_open_signature_not_in_active_findings(self) -> None:
        active_sig = signature_label("data-layer-safety|prod|ebs_snapshot_freshness")
        stale_sig = signature_label("data-layer-safety|prod|partition_coverage")
        report = {
            "run_url": "https://github.com/youxuanxue/sub2api/actions/runs/1",
            "run_id": "123",
            "issue_candidates": [
                {
                    "target_id": "prod",
                    "kind": "ebs_snapshot_freshness",
                    "signature": "data-layer-safety|prod|ebs_snapshot_freshness",
                    "title": "Data-layer protection gate failed: ebs_snapshot_freshness (prod)",
                    "status": "issue_candidate",
                    "severity": "error",
                    "summary": "latest completed EBS snapshot is missing, stale, or future-dated",
                }
            ],
        }
        calls: list[list[str]] = []

        def fake_run(args, **kwargs):
            calls.append(list(args))
            if args[:3] == ["gh", "issue", "list"] and "--state" in args and "all" in args:
                return subprocess.CompletedProcess(args, 0, stdout="[]")
            if args[:3] == ["gh", "issue", "list"]:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout=json.dumps([
                        {
                            "number": 1553,
                            "labels": [
                                {"name": "prod-ops"},
                                {"name": stale_sig},
                                {"name": "finding:partition_coverage"},
                            ],
                        },
                        {
                            "number": 1554,
                            "labels": [
                                {"name": "prod-ops"},
                                {"name": active_sig},
                                {"name": "finding:ebs_snapshot_freshness"},
                            ],
                        },
                    ]),
                )
            if args[:3] == ["gh", "issue", "create"]:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout="https://github.com/youxuanxue/sub2api/issues/9999",
                )
            if args[:3] == ["gh", "issue", "close"]:
                return subprocess.CompletedProcess(args, 0, stdout="")
            if args[:3] == ["gh", "issue", "view"]:
                return subprocess.CompletedProcess(
                    args,
                    0,
                    stdout="https://github.com/youxuanxue/sub2api/issues/1553\n",
                )
            if args[:2] == ["gh", "label"]:
                return subprocess.CompletedProcess(args, 0, stdout="")
            raise AssertionError(f"unexpected subprocess.run: {args!r}")

        with tempfile.TemporaryDirectory() as tmp:
            cache_dir = Path(tmp)
            with patch("ops.observability.open_prod_ops_issues.sh", side_effect=fake_run):
                links = sync_issues(report, {}, cache_dir=cache_dir)

        closed = [link for link in links if link.get("status") == "closed"]
        self.assertEqual(len(closed), 1)
        self.assertEqual(closed[0]["number"], 1553)
        self.assertTrue(any(call[:4] == ["gh", "issue", "close", "1553"] for call in calls))
        self.assertFalse(any(call[:3] == ["gh", "issue", "close", "1554"] for call in calls))

    def test_recover_body_mentions_run(self) -> None:
        body = recover_body(run_url="https://example/run", run_id="42")
        self.assertIn("https://example/run", body)
        self.assertIn("prod-ops-report-42", body)


if __name__ == "__main__":
    unittest.main()
