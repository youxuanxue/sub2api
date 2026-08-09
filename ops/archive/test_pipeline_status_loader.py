#!/usr/bin/env python3
"""Tests for pipeline_status.yaml loader."""

from __future__ import annotations

import pathlib
import sys
import unittest

_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import pipeline_status_loader as loader  # noqa: E402


class PipelineStatusLoaderTest(unittest.TestCase):
    def test_evidence_layout_derives_attachment_names_from_ssot(self) -> None:
        layout = loader.load_evidence_layout()
        self.assertEqual(layout.cleanup_hold_glob, "data-layer-cleanup-hold-*.json")
        self.assertEqual(
            layout.cleanup_release_receipt_glob,
            "data-layer-cleanup-hold-release*.json",
        )
        self.assertEqual(layout.tables, ("ops_error_logs", "ops_system_logs"))
        self.assertEqual(
            layout.export_ledger_name("ops_error_logs"),
            "data-layer-ops-error-logs-export-ledger.json",
        )
        self.assertEqual(
            layout.closeout_receipt_name("ops_system_logs"),
            "data-layer-ops-system-logs-archive-closeout.json",
        )
        self.assertEqual(
            layout.tail_export_ledger_name("ops_error_logs"),
            "data-layer-ops-error-logs-tail-export-ledger.json",
        )
        self.assertEqual(
            layout.tail_promote_ledger_name("ops_system_logs"),
            "data-layer-ops-system-logs-tail-promote-ledger.json",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
