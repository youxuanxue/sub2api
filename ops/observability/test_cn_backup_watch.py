#!/usr/bin/env python3
"""The cn-prod backup watch must read the data machine that still writes the receipt."""
from __future__ import annotations

from pathlib import Path
import re
import unittest


WORKFLOW = Path(__file__).resolve().parents[2] / ".github" / "workflows" / "ops-cn-backup-watch.yml"


class CnBackupWatchTest(unittest.TestCase):
    def test_receipt_is_read_from_the_data_machine(self) -> None:
        text = WORKFLOW.read_text(encoding="utf-8")
        targets = re.findall(r"run-probe\.sh --target (\S+)", text)
        self.assertEqual(targets, ["cn-prod-data"])
        # The finding identity stays the site. The data machine is only where
        # the archive-check timer writes after the 2026-11-09 move.
        self.assertIn('--target-id cn-prod', text)
        self.assertNotIn("--target cn-prod ", text)
        self.assertNotIn("--target cn-prod\n", text)


if __name__ == "__main__":
    unittest.main()
