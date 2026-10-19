#!/usr/bin/env python3
"""Behavioral checks for remediate-newapi-nonstream-timeout.sh safety rails."""
from __future__ import annotations

import pathlib
import subprocess
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops" / "observability" / "remediate-newapi-nonstream-timeout.sh"


class NewAPINonstreamTimeoutRemediateTest(unittest.TestCase):
    def test_script_parses(self) -> None:
        parsed = subprocess.run(["bash", "-n", str(SCRIPT)], capture_output=True, text=True)
        self.assertEqual(parsed.returncode, 0, msg=parsed.stderr)

    def test_dry_run_before_mutate(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        apply_at = text.index('APPLY" != "yes-newapi-nonstream-timeout"')
        backup_at = text.index("=== backup ===")
        cutover_call = text.index("commit_cutover \"$INACTIVE\"")
        self.assertLess(apply_at, backup_at)
        self.assertLess(backup_at, cutover_call)

    def test_refuses_active_drain_before_cutover(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        # Live upstream must stay serving until Caddy cutover; USR1 only on standby.
        bring_up = text.index("bring up inactive")
        cutover = text.index("=== cutover to")
        drain = text.index("drain standby")
        self.assertLess(bring_up, cutover)
        self.assertLess(cutover, drain)
        # No USR1 against the pre-cutover active path.
        pre = text[:cutover]
        self.assertNotIn("docker kill -s USR1 \"$ACTIVE_CTR\"", pre)
        self.assertIn("docker kill -s USR1 \"$name\"", text[drain:])

    def test_want_timeout_capped_at_900(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        self.assertIn("WANT_TIMEOUT\" -gt 900", text)
        self.assertIn("yes-newapi-nonstream-timeout", text)


if __name__ == "__main__":
    unittest.main()
