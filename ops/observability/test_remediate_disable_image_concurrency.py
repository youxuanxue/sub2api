#!/usr/bin/env python3
"""Behavioral checks for remediate-disable-image-concurrency.sh fail-closed verify."""
from __future__ import annotations

import pathlib
import subprocess
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops" / "observability" / "remediate-disable-image-concurrency.sh"


def _enabled_line_present(env_text: str) -> bool:
    proc = subprocess.run(
        ["grep", "-qx", "GATEWAY_IMAGE_CONCURRENCY_ENABLED=false"],
        input=env_text,
        text=True,
        check=False,
    )
    return proc.returncode == 0


class DisableImageConcurrencyVerifyTest(unittest.TestCase):
    def test_script_parses(self) -> None:
        parsed = subprocess.run(["bash", "-n", str(SCRIPT)], capture_output=True, text=True)
        self.assertEqual(parsed.returncode, 0, msg=parsed.stderr)

    def test_dry_run_exits_before_backup_without_apply(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        apply_at = text.index('APPLY" != "yes-disable-image-concurrency"')
        backup_at = text.index("backup .env")
        recreate_at = text.index("recreate $container")
        self.assertLess(apply_at, backup_at)
        self.assertLess(backup_at, recreate_at)

    def test_grep_qx_accepts_exact_false_and_rejects_true(self) -> None:
        self.assertTrue(_enabled_line_present("GATEWAY_IMAGE_CONCURRENCY_ENABLED=false\n"))
        self.assertFalse(_enabled_line_present("GATEWAY_IMAGE_CONCURRENCY_ENABLED=true\n"))
        self.assertFalse(_enabled_line_present("GATEWAY_IMAGE_CONCURRENCY_ENABLED=falsee\n"))
        self.assertFalse(_enabled_line_present(""))

    def test_recreate_uses_timeout_and_fail_closed_enabled(self) -> None:
        text = SCRIPT.read_text(encoding="utf-8")
        self.assertIn("--timeout 30", text)
        self.assertIn("docker kill -s USR1", text)
        self.assertIn('grep -qx \'GATEWAY_IMAGE_CONCURRENCY_ENABLED=false\'', text)
        self.assertIn("::error::$container env missing GATEWAY_IMAGE_CONCURRENCY_ENABLED=false", text)


if __name__ == "__main__":
    unittest.main()
