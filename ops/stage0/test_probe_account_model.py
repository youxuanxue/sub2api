#!/usr/bin/env python3
"""Static regression tests for probe_account_model.sh."""
from __future__ import annotations

import pathlib
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe_account_model.sh"


class ProbeAccountModelTest(unittest.TestCase):
    def test_reusable_group_ensure_uses_two_step_returning_id(self) -> None:
        script = _SCRIPT.read_text()
        start = script.index('if [[ "$PROBE_REUSE_MODE" == "1" ]]; then\n  GROUP_ID=')
        end = script.index('else\n  GROUP_ID=', start)
        group_ensure = script[start:end]

        self.assertIn("SELECT id::text", group_ensure)
        self.assertIn("if [[ -n \"$GROUP_ID\" ]]; then", group_ensure)
        self.assertIn("UPDATE groups", group_ensure)
        self.assertIn("INSERT INTO groups", group_ensure)
        self.assertIn("RETURNING id;", group_ensure)
        self.assertNotIn("ON CONFLICT", group_ensure)
        self.assertNotIn("WITH existing AS", group_ensure)
        self.assertNotIn("FROM picked", group_ensure)


if __name__ == "__main__":
    unittest.main()
