#!/usr/bin/env python3
"""Regression tests for Ops Daily Diagnostics workflow control-flow guards."""
from __future__ import annotations

import contextlib
import io
import json
import os
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "ops-daily-diagnostics.yml"
REPAIR_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "ops-repair-draft.yml"


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


def extract_log_signal_classifier() -> str:
    marker = "          import json\n          import pathlib\n          import sys\n\n          counts_path"
    text = workflow_text()
    start = text.index(marker)
    end = text.index("\n          PY", start)
    return textwrap.dedent(text[start:end])


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
            internal_probe.startswith('if [ -z "$APP_CONTAINER" ]'),
            msg=internal_probe,
        )
        self.assertIn('sudo docker exec "$APP_CONTAINER"', internal_probe)
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

    def test_feishu_owned_log_signals_do_not_become_issue_candidates(self) -> None:
        script = extract_log_signal_classifier()
        with tempfile.TemporaryDirectory() as tmp_raw:
            root = Path(tmp_raw)
            counts_path = root / "counts.json"
            findings_path = root / "findings.jsonl"
            counts_path.write_text(
                json.dumps({
                    "gemini_drive_scope_403": 10,
                    "openai_previous_response_fallback": 50,
                    "rate_limit_429_no_reset": 1,
                    "caddy_incomplete_response": 100,
                    "caddy_access_error": 100,
                }),
                encoding="utf-8",
            )
            old_argv = sys.argv
            sys.argv = ["log-signal-classifier", str(counts_path), str(findings_path), "prod", "24h"]
            try:
                exec(compile(script, str(WORKFLOW), "exec"), {})
            finally:
                sys.argv = old_argv

            findings = [json.loads(line) for line in findings_path.read_text(encoding="utf-8").splitlines()]
            statuses = {finding["kind"]: finding["status"] for finding in findings}
            self.assertEqual(statuses["gemini_drive_scope_403"], "warning")
            self.assertEqual(statuses["rate_limit_429_no_reset"], "warning")
            self.assertEqual(statuses["caddy_access_error"], "warning")
            self.assertEqual(statuses["openai_previous_response_fallback"], "issue_candidate")
            self.assertEqual(list(statuses.values()).count("issue_candidate"), 1)

    def test_issue_lifecycle_updates_open_and_cools_down_closed_signatures(self) -> None:
        text = workflow_text()
        self.assertIn("from ops.observability.prod_ops_issue_decision import decide_issue_action", text)
        self.assertIn("'--state', 'all'", text)
        self.assertIn("'number,state,closedAt,createdAt'", text)
        self.assertIn("decision['action'] == 'update'", text)
        self.assertIn("decision['action'] == 'suppress'", text)
        self.assertIn("remains in the 7-day cooldown", text)

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

    def test_runtime_diagnostics_resolve_active_blue_green_container(self) -> None:
        commands = extract_runtime_params_commands()
        init = commands.index("APP_CONTAINER=''")
        active = next(i for i, command in enumerate(commands) if "/var/lib/tokenkey/active-color" in command)
        health = next(i for i, command in enumerate(commands) if 'docker exec "$APP_CONTAINER"' in command)
        logs = next(i for i, command in enumerate(commands) if 'docker logs "$APP_CONTAINER"' in command)
        self.assertLess(init, active)
        self.assertLess(active, health)
        self.assertLess(health, logs)
        self.assertFalse(any("docker logs tokenkey --since" in command for command in commands))

    def test_daily_error_report_is_aggregated_and_dispatches_only_isolated_repair(self) -> None:
        text = workflow_text()
        self.assertIn("probe-daily-error-ledger.sh", text)
        self.assertIn("daily_error_report.py build", text)
        self.assertIn("daily_error_report.py aggregate", text)
        self.assertIn("alert_covered)", text)
        self.assertIn("Daily error anomalies are covered by Feishu", text)
        self.assertIn(
            'add_finding "error_cluster" "warning" "warning" "Persistent legacy error cluster',
            text,
        )
        self.assertNotIn('add_finding "error_cluster" "issue_candidate"', text)
        self.assertIn("top_repair_signature", text)
        self.assertIn("gh workflow run ops-repair-draft.yml", text)

        queue_start = text.index("  queue-repair-draft:")
        queue_end = text.index("\n  log-dump:", queue_start)
        queue = text[queue_start:queue_end]
        self.assertIn("actions: write", queue)
        self.assertIn("contents: read", queue)
        self.assertNotIn("id-token: write", queue)
        self.assertNotIn("aws ", queue)

    def test_daily_triage_is_deterministic_and_agent_budget_is_repair_only(self) -> None:
        text = workflow_text()
        self.assertNotIn("run-headless-agent", text)
        self.assertNotIn("setup-claude-code", text)
        self.assertNotRegex(text, r"\bclaude\s+(?:-p|--print)\b")
        self.assertNotIn("ANTHROPIC_AUTH_TOKEN", text)
        self.assertNotIn("CLAUDE_CODE_OAUTH_TOKEN", text)
        self.assertNotIn("max_budget_usd:", text)
        self.assertIn("issue_analysis_markdown", text)
        self.assertIn("## Deterministic error analysis", text)
        self.assertIn("needs.aggregate-report.outputs.needs_issue == 'true'", text)

        repair = REPAIR_WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("uses: ./.github/actions/run-headless-agent", repair)
        self.assertIn('max_budget_usd: "8.00"', repair)


if __name__ == "__main__":
    unittest.main()
