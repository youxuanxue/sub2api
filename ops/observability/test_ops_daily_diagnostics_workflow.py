#!/usr/bin/env python3
"""Regression tests for Ops Daily Diagnostics workflow control-flow guards."""
from __future__ import annotations

import contextlib
import io
import json
import os
import textwrap
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "ops-daily-diagnostics.yml"


def workflow_text() -> str:
    return WORKFLOW.read_text(encoding="utf-8")


def extract_runtime_params_commands() -> list[str]:
    marker = 'python3 - <<\'PY\' > "$OUT/runtime-params.json"\n'
    text = workflow_text()
    start = text.index(marker) + len(marker)
    end = text.index("\n          PY", start)
    script = textwrap.dedent(text[start:end])
    old_since = os.environ.get("DIAGNOSTICS_LOG_SINCE")
    os.environ["DIAGNOSTICS_LOG_SINCE"] = "24h"
    try:
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            exec(compile(script, str(WORKFLOW), "exec"), {})
        return json.loads(stdout.getvalue())["commands"]
    finally:
        if old_since is None:
            os.environ.pop("DIAGNOSTICS_LOG_SINCE", None)
        else:
            os.environ["DIAGNOSTICS_LOG_SINCE"] = old_since


class OpsDailyDiagnosticsWorkflowTest(unittest.TestCase):
    def test_internal_health_probe_uses_drain_immune_live_endpoint(self) -> None:
        commands = extract_runtime_params_commands()
        internal_start = commands.index("echo ===INTERNAL_HEALTH===")
        internal_probe = commands[internal_start + 1]
        self.assertIn("/health/live", internal_probe)
        self.assertNotIn("http://localhost:8080/health;", internal_probe)

    def test_internal_health_probe_cannot_abort_log_signal_collection(self) -> None:
        commands = extract_runtime_params_commands()
        internal_start = commands.index("echo ===INTERNAL_HEALTH===")
        internal_probe = commands[internal_start + 1]
        internal_end = commands.index("echo ===END_INTERNAL_HEALTH===")
        log_start = commands.index("echo ===LOG_SIGNAL_COUNTS===")

        self.assertLess(internal_start, internal_end)
        self.assertLess(internal_end, log_start)
        self.assertTrue(
            internal_probe.startswith("if sudo docker compose "),
            msg=internal_probe,
        )
        self.assertIn("internal_health_status=failed exit=$rc", internal_probe)
        self.assertIn("then echo; echo internal_health_status=ok", internal_probe)

    def test_prod_runtime_wait_uses_named_300_second_deadline(self) -> None:
        text = workflow_text()
        self.assertIn("RUNTIME_WAIT_SECONDS=180", text)
        self.assertIn('if [ "$TARGET_ID" = "prod" ]; then', text)
        self.assertIn("RUNTIME_WAIT_SECONDS=300", text)
        self.assertIn("DEADLINE=$(( $(date +%s) + RUNTIME_WAIT_SECONDS ))", text)
        self.assertIn(
            "after waiting up to ${RUNTIME_WAIT_SECONDS}s",
            text,
        )

    def test_caddy_findings_split_stream_abort_and_access_errors(self) -> None:
        text = workflow_text()
        self.assertIn('"kind": "caddy_stream_abort"', text)
        self.assertIn('"kind": "caddy_access_error"', text)
        self.assertIn("caddy_incomplete >= 1000", text)
        self.assertIn("caddy_access_error >= 10", text)
        self.assertNotIn('"kind": "caddy_incomplete_response"', text)

    def test_missing_target_reports_skipped_when_diagnose_cancelled(self) -> None:
        text = workflow_text()
        self.assertIn("DISCOVER_TARGETS_RESULT", text)
        self.assertIn("DIAGNOSE_TARGETS_RESULT", text)
        self.assertIn('diagnose_result not in ("cancelled", "skipped")', text)

        text = workflow_text()
        init = text.index("RUNTIME_SSM_TRANSPORT_OK=false")
        send_success = text.index("RUNTIME_SSM_TRANSPORT_OK=true", init)
        suppress = text.index('elif [ "$RUNTIME_SSM_TRANSPORT_OK" != "true" ]; then')
        lightsail = text.index('elif [ "${TARGET_PLATFORM:-ec2}" = "lightsail" ]; then')
        binary_check = text.index("error_clustering_binary_check", lightsail)

        self.assertLess(init, send_success)
        self.assertLess(send_success, suppress)
        self.assertLess(suppress, lightsail)
        self.assertLess(lightsail, binary_check)
        self.assertIn(
            "Runtime SSM SendCommand failed; suppressing downstream error_clustering SSM checks",
            text,
        )


if __name__ == "__main__":
    unittest.main()
