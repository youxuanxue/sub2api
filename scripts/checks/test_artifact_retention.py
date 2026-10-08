#!/usr/bin/env python3
"""High-churn workflows must declare short artifact retention."""

from __future__ import annotations

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github" / "workflows"

# Scheduled / frequently uploaded evidence — keep storage bounded.
REQUIRED = {
    "pricing-registry-sensor.yml": 3,
    "model-release-watch.yml": 3,
    "ops-daily-diagnostics.yml": 3,
    "client-fidelity-watch.yml": 3,
    "upstream-issue-watchdog.yml": 7,  # scan artifacts; fix output may be shorter
}


class ArtifactRetentionContractTest(unittest.TestCase):
    def test_high_churn_uploads_declare_retention(self) -> None:
        missing: list[str] = []
        for name, max_days in REQUIRED.items():
            text = (WORKFLOWS / name).read_text(encoding="utf-8")
            uploads = list(re.finditer(r"uses:\s*actions/upload-artifact@", text))
            self.assertTrue(uploads, name)
            for match in uploads:
                # Look ahead until next top-level step or EOF for retention-days.
                start = match.start()
                nxt = text.find("\n      - ", start + 1)
                block = text[start: nxt if nxt != -1 else len(text)]
                found = re.search(r"retention-days:\s*(\d+)", block)
                if not found:
                    missing.append(f"{name}:upload@{match.start()}")
                    continue
                days = int(found.group(1))
                if days > max_days:
                    missing.append(f"{name}:retention {days}>{max_days}")
        self.assertEqual(missing, [])


if __name__ == "__main__":
    unittest.main()
