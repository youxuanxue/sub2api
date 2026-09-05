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

    def test_reuse_mode_delegates_resource_lifecycle_to_shared_owner(self) -> None:
        script = _SCRIPT.read_text()

        self.assertIn('if [[ "$PROBE_REUSE_MODE" == "1" ]]; then', script)
        self.assertIn('tk_probe_prepare_platform_reuse_probe "$PLATFORM" "$ACCOUNT_ID"', script)
        self.assertIn('PROBE_SCOPE="${TK_PROBE_SCOPE}"', script)
        self.assertIn('GROUP_ID="${TK_PROBE_GROUP_ID}"', script)
        self.assertIn('API_KEY_ID="${TK_PROBE_KEY_ID}"', script)
        self.assertIn('API_KEY="${TK_PROBE_KEY}"', script)
        self.assertIn('GROUP_NAME="$(tk_probe_group_name "$PROBE_SCOPE")"', script)
        self.assertIn('KEY_NAME="$(tk_probe_key_name "$PROBE_SCOPE")"', script)
        self.assertNotIn('SELECT id::text\n  FROM groups', script)
        self.assertNotIn('NEW_API_KEY="$(new_probe_api_key)"', script)

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

    def test_reuse_mode_uses_shared_probe_helper(self) -> None:
        script = _SCRIPT.read_text()
        self.assertIn("probe_reserved_resources.sh", script)
        self.assertIn('if [[ "$PROBE_REUSE_MODE" == "1" ]]; then', script)
        self.assertIn("${SCRIPT_DIR}/probe_reserved_resources.sh", script)
        self.assertIn('tk_probe_prepare_platform_reuse_probe "$PLATFORM" "$ACCOUNT_ID"', script)
        self.assertIn('tk_probe_cleanup_named_group "$GROUP_ID" "$GROUP_NAME" "$KEY_NAME" reusable', script)
        self.assertNotIn("tk_probe_unbind_account_from_stale_probe_groups", script)

    def test_oneoff_supported_model_scopes_json_keeps_shell_escaped_quotes(self) -> None:
        script = _SCRIPT.read_text()

        expected = r"""false, '{}'::jsonb, true, '[\"claude\", \"gemini_text\", \"gemini_image\"]'::jsonb,"""
        broken = """false, '{}'::jsonb, true, '["claude", "gemini_text", "gemini_image"]'::jsonb,"""
        self.assertIn(expected, script)
        self.assertNotIn(broken, script)

    def test_rebinding_group_notifies_scheduler_snapshot(self) -> None:
        script = _SCRIPT.read_text()
        self.assertIn('INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)', script)
        self.assertIn("VALUES ('group_changed', NULL, ${GROUP_ID}, NULL);", script)

        cleanup_at = script.index("cleanup() {")
        trap_at = script.index("trap cleanup EXIT", cleanup_at)
        cleanup_sql = script[cleanup_at:trap_at]
        self.assertIn("tk_probe_cleanup_named_group", cleanup_sql)

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
