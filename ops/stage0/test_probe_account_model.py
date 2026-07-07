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
        end = script.index('else\n  psql_capture_numeric GROUP_ID "failed to insert one-off probe group', start)
        group_ensure = script[start:end]

        self.assertIn("SELECT id::text", group_ensure)
        self.assertIn("if [[ -n \"$GROUP_ID\" ]]; then", group_ensure)
        self.assertIn("UPDATE groups", group_ensure)
        self.assertIn("INSERT INTO groups", group_ensure)
        self.assertIn("RETURNING id;", group_ensure)
        self.assertIn("psql_capture_numeric GROUP_ID", group_ensure)
        self.assertIn("supported_model_scopes", group_ensure)
        self.assertIn("messages_dispatch_model_config", group_ensure)
        self.assertIn("models_list_config", group_ensure)
        self.assertIn("claude", group_ensure)
        self.assertIn("gemini_text", group_ensure)
        self.assertIn("gemini_image", group_ensure)
        self.assertNotIn("ON CONFLICT", group_ensure)
        self.assertNotIn("WITH existing AS", group_ensure)
        self.assertNotIn("FROM picked", group_ensure)

    def test_psql_id_capture_is_quiet_and_reports_sql_errors(self) -> None:
        script = _SCRIPT.read_text()

        self.assertIn("-X -q -A -t -v ON_ERROR_STOP=1", script)
        self.assertIn("psql_capture_numeric() {", script)
        self.assertIn("2>\"$errfile\"", script)
        self.assertIn("fail_json \"${message}: ${err:-psql failed}\"", script)
        self.assertIn("no numeric id returned", script)

    def test_app_container_auto_resolves_blue_green(self) -> None:
        script = _SCRIPT.read_text()

        self.assertIn('APP_CONTAINER="${APP_CONTAINER:-auto}"', script)
        self.assertIn("resolve_app_container() {", script)
        self.assertIn("/var/lib/tokenkey/active-color", script)
        self.assertIn("app container not running", script)

    def test_reuse_mode_unbinds_stale_probe_groups_before_bind(self) -> None:
        script = _SCRIPT.read_text()
        self.assertIn("probe_reserved_resources.sh", script)
        self.assertIn("tk_probe_unbind_account_from_stale_probe_groups", script)
        self.assertIn('if [[ "$PROBE_REUSE_MODE" == "1" ]]; then', script)
        self.assertIn("${SCRIPT_DIR}/probe_reserved_resources.sh", script)
        unbind_at = script.index("tk_probe_unbind_account_from_stale_probe_groups")
        bind_at = script.index("INSERT INTO account_groups (account_id, group_id, priority, created_at)")
        self.assertLess(unbind_at, bind_at)


if __name__ == "__main__":
    unittest.main()
