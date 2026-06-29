#!/usr/bin/env python3
"""Static regression tests for probe_account_model.sh."""
from __future__ import annotations

import pathlib
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe_account_model.sh"


class ProbeAccountModelTest(unittest.TestCase):
    def test_reusable_group_ensure_uses_upsert_returning_id(self) -> None:
        script = _SCRIPT.read_text()
        start = script.index('if [[ "$PROBE_REUSE_MODE" == "1" ]]; then\n  GROUP_ID=')
        end = script.index('else\n  GROUP_ID=', start)
        group_ensure = script[start:end]

        self.assertIn("ON CONFLICT (name) WHERE (deleted_at IS NULL)", group_ensure)
        self.assertIn("DO UPDATE SET", group_ensure)
        self.assertIn("RETURNING id;", group_ensure)
        self.assertNotIn("WITH existing AS", group_ensure)
        self.assertNotIn("FROM picked", group_ensure)


if __name__ == "__main__":
    unittest.main()
