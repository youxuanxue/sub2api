from __future__ import annotations

import importlib.util
import contextlib
import io
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("apply-model-mapping-live.py")
SPEC = importlib.util.spec_from_file_location("apply_model_mapping_live", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class RemoveLiveShellTest(unittest.TestCase):
    def test_account_name_rejects_shell_and_sql_literal_breakers(self) -> None:
        for value in ("bad'name", 'bad"name', "bad$(id)", "bad`id`", "bad\\name", "bad\nname"):
            with self.subTest(value=value):
                self.assertIsNone(MODULE._NAME_RE.fullmatch(value))
        self.assertIsNotNone(MODULE._NAME_RE.fullmatch("ds-官 primary"))

    def test_guard_validation_rejects_trailing_newline(self) -> None:
        validator = getattr(MODULE, "validate_guard_fields", None)
        self.assertTrue(callable(validator), "live mutations must share one guard validator")
        for name, platform in (("safe\n", "newapi"), ("safe", "newapi\n")):
            with self.subTest(name=name, platform=platform):
                with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                    validator(name, platform)

    def _run_shell(self, guard_count: str, after_present: str) -> subprocess.CompletedProcess[str]:
        builder = getattr(MODULE, "build_remove_live_shell", None)
        self.assertTrue(callable(builder), "remove-live must expose a testable shell builder")
        with tempfile.TemporaryDirectory() as tmp:
            fake_psql = Path(tmp) / "psql"
            fake_psql.write_text(
                """#!/usr/bin/env bash
set -euo pipefail
args="$*"
if [[ "$args" == *"SELECT count(*) FROM accounts"* ]]; then
  printf '%s\\n' "$FAKE_GUARD_COUNT"
elif [[ "$args" == *"credentials->'model_mapping') ?|"* ]]; then
  printf '%s\\n' "$FAKE_AFTER_PRESENT"
elif [[ "$args" == *"SELECT string_agg"* ]]; then
  printf '\\n'
else
  cat >/dev/null
  printf 'INSERT 0 1\\n'
fi
""",
                encoding="utf-8",
            )
            fake_psql.chmod(0o755)
            shell = builder(
                account_id=39,
                name="ds-官",
                platform="newapi",
                channel_type=43,
                keys=["deepseek-v4-flash-0731"],
                sql_b64="U0VMRUNUIDE7",
                psql=str(fake_psql),
            )
            env = os.environ | {
                "FAKE_GUARD_COUNT": guard_count,
                "FAKE_AFTER_PRESENT": after_present,
            }
            return subprocess.run(
                ["bash"],
                input=shell,
                text=True,
                capture_output=True,
                env=env,
                check=False,
            )

    def test_wrong_guard_fails_before_apply(self) -> None:
        result = self._run_shell(guard_count="0", after_present="f")

        self.assertNotEqual(0, result.returncode)
        self.assertNotIn("APPLY_OK", result.stdout)
        self.assertIn("guard matched 0 accounts", result.stderr)

    def test_failed_postcondition_does_not_report_success(self) -> None:
        result = self._run_shell(guard_count="1", after_present="t")

        self.assertNotEqual(0, result.returncode)
        self.assertNotIn("APPLY_OK", result.stdout)
        self.assertIn("target keys remain after removal", result.stderr)

    def test_verified_removal_reports_success(self) -> None:
        result = self._run_shell(guard_count="1", after_present="f")

        self.assertEqual(0, result.returncode, result.stderr)
        self.assertIn("APPLY_OK", result.stdout)


if __name__ == "__main__":
    unittest.main()
