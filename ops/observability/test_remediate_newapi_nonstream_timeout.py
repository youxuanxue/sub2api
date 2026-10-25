#!/usr/bin/env python3
"""Behavioral checks for remediate-newapi-nonstream-timeout.sh safety rails."""
from __future__ import annotations

import pathlib
import subprocess
import sys
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops" / "observability" / "remediate-newapi-nonstream-timeout.sh"

sys.path.insert(0, str(ROOT / "ops" / "stage0"))
from first_output_timeout_ssot import (  # noqa: E402
    EXPECTED_ENV_NEWAPI_NONSTREAM,
    NEWAPI_CHAT_NONSTREAM_FIRST_OUTPUT_MAX,
)

NEWAPI_NONSTREAM_DEFAULT = EXPECTED_ENV_NEWAPI_NONSTREAM[
    "GATEWAY_NEWAPI_CHAT_NONSTREAM_FIRST_OUTPUT_TIMEOUT"
]


class NewAPINonstreamTimeoutRemediateTest(unittest.TestCase):
    def test_script_parses(self) -> None:
        parsed = subprocess.run(["bash", "-n", str(SCRIPT)], capture_output=True, text=True)
        self.assertEqual(parsed.returncode, 0, msg=parsed.stderr)

    def test_dry_run_before_mutate(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        apply_at = text.index('APPLY" != "$EXPECT_APPLY"')
        backup_at = text.index("=== backup ===")
        cutover_call = text.index('commit_cutover "$INACTIVE"')
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
        self.assertNotIn('docker kill -s USR1 "$ACTIVE_CTR"', pre)
        self.assertIn('docker kill -s USR1 "$name"', text[drain:])

    def test_want_timeout_matches_ssot_default_and_max(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        # MODE=nonstream keeps the SSOT-owned default and max.
        self.assertIn(
            f'WANT_TIMEOUT="${{WANT_TIMEOUT:-{NEWAPI_NONSTREAM_DEFAULT}}}"',
            text,
        )
        self.assertIn(f"WANT_MAX={NEWAPI_CHAT_NONSTREAM_FIRST_OUTPUT_MAX}", text)
        self.assertIn("yes-newapi-nonstream-timeout", text)
        # MODE=stream carries its own key, bound and APPLY token. The stream key
        # is deliberately absent from the SSOT tables (it may differ per
        # deployment), so assert that rather than look it up there.
        self.assertIn("KEY=GATEWAY_NEWAPI_CHAT_FIRST_OUTPUT_TIMEOUT", text)
        self.assertIn("yes-newapi-stream-timeout", text)
        self.assertNotIn(
            "GATEWAY_NEWAPI_CHAT_FIRST_OUTPUT_TIMEOUT",
            str(sorted(EXPECTED_ENV_NEWAPI_NONSTREAM)),
        )

    def _reject(self, want: str, mode: str) -> subprocess.CompletedProcess[str]:
        # No APPLY token, so the worst case is a dry run; bounds are checked
        # before any host inspection, which is what these cases exercise.
        return subprocess.run(
            ["bash", str(SCRIPT)],
            capture_output=True,
            text=True,
            env={"PATH": "/usr/bin:/bin", "MODE": mode, "WANT_TIMEOUT": want},
        )

    def test_bounds_enforced_per_mode(self) -> None:
        # Guard must fire in both directions: reject what is out of range for
        # the mode, and do NOT reject what is in range for it.
        for mode, bad in (
            ("nonstream", "901"),
            ("nonstream", "0"),  # nonstream has no inherit semantics
            ("stream", "61"),
            ("stream", "120"),  # the old stream SSOT value is out of this key's range
        ):
            with self.subTest(mode=mode, want=bad):
                proc = self._reject(bad, mode)
                self.assertEqual(proc.returncode, 2, msg=proc.stdout + proc.stderr)
                self.assertIn("out of allowed range", proc.stdout + proc.stderr)

        for mode, good in (("nonstream", "600"), ("stream", "12"), ("stream", "0")):
            with self.subTest(mode=mode, want=good):
                proc = self._reject(good, mode)
                self.assertNotIn("out of allowed range", proc.stdout + proc.stderr)

    def test_unknown_mode_refused(self) -> None:
        proc = self._reject("12", "bogus")
        self.assertEqual(proc.returncode, 2, msg=proc.stdout + proc.stderr)
        self.assertIn("MODE must be nonstream|stream", proc.stdout + proc.stderr)

    def test_cutover_syncs_gemini_web_control_alias(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        cutover = text.index("cutover_ok upstream=")
        sync_call = text.index('sync_gemini_web_control_alias "${color}"')
        self.assertLess(sync_call, cutover)
        self.assertIn("TCP:${target}:8080", text)
        self.assertIn("tokenkey-gemini-web", text)


if __name__ == "__main__":
    unittest.main()
