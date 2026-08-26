#!/usr/bin/env python3
"""Regression tests for probe_account_model.sh."""
from __future__ import annotations

import json
import pathlib
import shutil
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe_account_model.sh"


class ProbeAccountModelTest(unittest.TestCase):
    def test_missing_reserved_resources_reports_structured_setup_error(self) -> None:
        with tempfile.TemporaryDirectory(prefix="probe-account-model-test-") as td:
            isolated_script = pathlib.Path(td) / "probe_account_model.sh"
            shutil.copy2(_SCRIPT, isolated_script)

            proc = subprocess.run(
                ["bash", str(isolated_script)],
                capture_output=True,
                text=True,
                check=False,
            )

        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        payload = json.loads(proc.stdout)
        self.assertEqual(payload["verdict"], "setup_error")
        self.assertIn("missing probe_reserved_resources.sh companion", payload["error"])
        self.assertNotIn("command not found", proc.stderr)

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
        self.assertIn("allow_messages_dispatch = true", group_ensure)
        self.assertIn("model_routing, allow_messages_dispatch, supported_model_scopes", group_ensure)
        self.assertIn("false, '{}'::jsonb, true, '[", group_ensure)
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
        # The resolver itself is the canonical shared owner, not a local copy:
        # a re-introduced hand-rolled loop here is what let a STOPPED container be
        # reported as live. Assert the sourced call, and that no copy came back.
        self.assertIn("resolve-app-container.sh", script)
        self.assertIn('tk_resolve_app_container "$APP_CONTAINER"', script)
        self.assertIn("app container unresolved", script)
        self.assertNotIn("resolve_app_container() {", script)
        self.assertNotIn("for candidate in tokenkey tokenkey-blue tokenkey-green", script)

    def test_reuse_mode_unbinds_stale_probe_groups_before_bind(self) -> None:
        script = _SCRIPT.read_text()
        self.assertIn("probe_reserved_resources.sh", script)
        self.assertIn("tk_probe_unbind_account_from_stale_probe_groups", script)
        self.assertIn('if [[ "$PROBE_REUSE_MODE" == "1" ]]; then', script)
        self.assertIn("${SCRIPT_DIR}/probe_reserved_resources.sh", script)
        unbind_at = script.index("tk_probe_unbind_account_from_stale_probe_groups")
        bind_at = script.index("INSERT INTO account_groups (account_id, group_id, priority, created_at)")
        self.assertLess(unbind_at, bind_at)

    def test_rebinding_group_notifies_scheduler_snapshot(self) -> None:
        script = _SCRIPT.read_text()
        bind_at = script.index("INSERT INTO account_groups (account_id, group_id, priority, created_at)")
        key_at = script.index('if [[ "$PROBE_REUSE_MODE" == "1" ]]; then\n  NEW_API_KEY=', bind_at)
        binding_sql = script[bind_at:key_at]

        self.assertIn("INSERT INTO scheduler_outbox", binding_sql)
        self.assertIn("'group_changed'", binding_sql)
        self.assertIn("${GROUP_ID}", binding_sql)

        cleanup_at = script.index("cleanup() {")
        trap_at = script.index("trap cleanup EXIT", cleanup_at)
        cleanup_sql = script[cleanup_at:trap_at]
        self.assertIn("INSERT INTO scheduler_outbox", cleanup_sql)
        self.assertIn("'group_changed'", cleanup_sql)

    def test_probe_script_wires_verdict_module_and_embeddings_endpoint(self) -> None:
        script = _SCRIPT.read_text()

        self.assertIn("messages|count_tokens|chat|responses|embeddings", script)
        self.assertIn('elif endpoint == "embeddings":', script)
        self.assertIn('"input": prompt', script)
        self.assertIn('embeddings) PATH_SUFFIX="/v1/embeddings"', script)
        self.assertIn("PROBE_SCRIPT_DIR", script)
        self.assertIn("from probe_account_model_verdict import classify_probe_verdict", script)
        self.assertIn("probe_account_model_verdict.py", script)


if __name__ == "__main__":
    unittest.main()
