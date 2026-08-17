#!/usr/bin/env python3
"""Regression tests for Ops Daily Diagnostics workflow control-flow guards."""
from __future__ import annotations

import contextlib
import datetime as dt
import io
import json
import os
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

import yaml

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "ops-daily-diagnostics.yml"
REPAIR_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "ops-repair-draft.yml"
ISSUE_LIFECYCLE_NOW = dt.datetime(2026, 7, 23, 9, 0, tzinfo=dt.timezone.utc)
GITHUB_ACTIONS_MAX_EXPRESSION_CHARS = 21_000

if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))


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


def run_issue_lifecycle(
    *,
    issue_candidates: list[dict],
    gh_issues: list[dict],
    open_prod_ops_issues: list[dict] | None = None,
) -> tuple[list[list[str]], str]:
    finding = {
        "target_id": "prod",
        "kind": "openai_previous_response_fallback",
        "signature": "log-signal|prod|openai-previous-response-fallback",
        "title": "OpenAI previous response fallback",
        "status": "issue_candidate",
        "severity": "high",
        "summary": "fallback count exceeded threshold",
    }
    candidates = issue_candidates or [finding]
    calls: list[list[str]] = []

    def fake_run(args, **kwargs):
        calls.append(list(args))
        if args[:3] == ["gh", "issue", "list"]:
            if "--state" in args and "open" in args:
                return subprocess.CompletedProcess(
                    args, 0, stdout=json.dumps(open_prod_ops_issues or []),
                )
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps(gh_issues))
        if args[:3] == ["gh", "issue", "comment"]:
            return subprocess.CompletedProcess(args, 0, stdout="")
        if args[:3] == ["gh", "issue", "create"]:
            return subprocess.CompletedProcess(args, 0, stdout="https://github.com/example/issues/99")
        if args[:3] == ["gh", "issue", "close"]:
            return subprocess.CompletedProcess(args, 0, stdout="")
        if args[:3] == ["gh", "issue", "view"]:
            return subprocess.CompletedProcess(args, 0, stdout="https://github.com/example/issues/42\n")
        if args[:2] == ["gh", "label"]:
            return subprocess.CompletedProcess(args, 0, stdout="")
        raise AssertionError(f"unexpected subprocess.run: {args!r}")

    report = {
        "run_url": "https://example/run",
        "run_id": "123",
        "issue_candidates": candidates,
    }
    stdout = io.StringIO()
    from ops.observability.prod_ops_issue_decision import decide_issue_action as real_decide_issue_action

    def fixed_decide_issue_action(issues: list[dict]) -> dict:
        return real_decide_issue_action(issues, now=ISSUE_LIFECYCLE_NOW)

    try:
        with mock.patch("ops.observability.open_prod_ops_issues.sh", side_effect=fake_run):
            with mock.patch(
                "ops.observability.open_prod_ops_issues.decide_issue_action",
                fixed_decide_issue_action,
            ):
                with contextlib.redirect_stdout(stdout):
                    from ops.observability.open_prod_ops_issues import sync_issues

                    sync_issues(report, {})
    except Exception:
        raise
    return calls, stdout.getvalue()


class OpsDailyDiagnosticsWorkflowTest(unittest.TestCase):
    def test_expression_interpolated_run_blocks_fit_github_limit(self) -> None:
        workflow = yaml.safe_load(workflow_text())
        oversized: list[str] = []

        for job_name, job in workflow["jobs"].items():
            for step in job.get("steps", []):
                run = step.get("run")
                if not isinstance(run, str) or "${{" not in run:
                    continue
                if len(run) >= GITHUB_ACTIONS_MAX_EXPRESSION_CHARS:
                    oversized.append(
                        f"{job_name}/{step.get('name', '<unnamed>')}={len(run)}"
                    )

        self.assertEqual(
            oversized,
            [],
            "GitHub rejects expression-interpolated run blocks at 21000 characters",
        )

    def test_data_layer_safety_is_independent_from_capacity(self) -> None:
        text = workflow_text()
        self.assertIn("probe-data-layer-safety.sh", text)
        self.assertIn("data_layer_safety_verdict.py", text)
        self.assertIn("data-layer-safety|$TARGET_ID|$SF_KIND", text)
        self.assertLess(text.index("CAP_VERDICT="), text.index("SAFETY_VERDICT="))

        safety_start = text.index("SAFETY_OUT=")
        safety_end = text.index("ARCHIVE_SIGNAL=", safety_start)
        snapshot_block = text[safety_start:safety_end]
        self.assertIn("data_layer_snapshot_signal.sh", snapshot_block)
        self.assertNotIn("describe-instances", snapshot_block)
        self.assertNotIn("BlockDeviceMappings[0]", snapshot_block)
        self.assertNotIn("describe-snapshots", snapshot_block)

    def test_qa_phase2_live_health_probe_is_wired_for_prod(self) -> None:
        text = workflow_text()
        self.assertIn("probe-qa-phase2-live-health.sh", text)
        self.assertIn("prod_phase2_live_health.py --from-probe-stdin", text)
        self.assertIn("jq -e . >/dev/null", text)
        self.assertNotIn("qa_phase2_live_health_eval_failed\"}}')", text)
        self.assertIn("qa-phase2-live-health|$TARGET_ID", text)
        self.assertIn("qa-raw-archive-iam|$TARGET_ID", text)
        self.assertLess(text.index("SAFETY_VERDICT="), text.index("PHASE2_PROBE="))

    def test_qa_bundle_infra_and_real_canary_are_wired_for_prod(self) -> None:
        text = workflow_text()
        self.assertIn(
            "QA_BUNDLE_VERIFY_MODE=discovery bash ops/qa/verify_qa_bundle_infra.sh",
            text,
        )
        self.assertIn("run-qa-bundle-canary-via-ssm.sh", text)
        self.assertIn("qa-bundle-infra|$TARGET_ID", text)
        self.assertIn("qa-bundle-canary|$TARGET_ID", text)
        self.assertLess(text.index("PHASE2_PROBE="), text.index("QA_BUNDLE_INFRA="))
        self.assertLess(text.index("QA_BUNDLE_INFRA="), text.index("QA_BUNDLE_CANARY="))

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

    def test_issue_lifecycle_comments_on_open_signature(self) -> None:
        calls, output = run_issue_lifecycle(
            issue_candidates=[],
            gh_issues=[{"number": 42, "state": "OPEN", "closedAt": None, "createdAt": "2026-07-20T08:00:00Z"}],
        )
        self.assertTrue(any(call[:4] == ["gh", "issue", "list", "--label"] for call in calls))
        comment_calls = [call for call in calls if call[:3] == ["gh", "issue", "comment"]]
        self.assertEqual(len(comment_calls), 1)
        self.assertEqual(comment_calls[0][:4], ["gh", "issue", "comment", "42"])
        self.assertTrue(str(comment_calls[0][5]).endswith("issue-1.md"))
        self.assertIn("prod-ops-issue", str(comment_calls[0][5]))
        self.assertNotIn(["gh", "issue", "create"], [call[:3] for call in calls])
        self.assertIn("updated issue #42 for ops-sig:", output)

    def test_issue_lifecycle_suppresses_recently_closed_signature(self) -> None:
        calls, output = run_issue_lifecycle(
            issue_candidates=[],
            gh_issues=[{"number": 43, "state": "CLOSED", "closedAt": "2026-07-23T08:15:33Z", "createdAt": "2026-07-20T08:00:00Z"}],
        )
        self.assertFalse(any(call[:3] == ["gh", "issue", "comment"] for call in calls))
        self.assertFalse(any(call[:3] == ["gh", "issue", "create"] for call in calls))
        self.assertIn("suppressed ops-sig:", output)
        self.assertIn("remains in the 7-day cooldown", output)

    def test_issue_lifecycle_creates_when_closed_signature_expired(self) -> None:
        calls, output = run_issue_lifecycle(
            issue_candidates=[],
            gh_issues=[{"number": 44, "state": "CLOSED", "closedAt": "2026-07-01T08:00:00Z", "createdAt": "2026-06-28T08:00:00Z"}],
        )
        self.assertTrue(any(call[:3] == ["gh", "issue", "create"] for call in calls))
        self.assertNotIn(["gh", "issue", "comment"], [call[:3] for call in calls])
        self.assertIn("created issue for ops-sig:", output)

    def test_issue_lifecycle_fail_closed_when_closed_at_unknown(self) -> None:
        calls, output = run_issue_lifecycle(
            issue_candidates=[],
            gh_issues=[{"number": 45, "state": "CLOSED", "closedAt": "invalid", "createdAt": "2026-07-20T08:00:00Z"}],
        )
        self.assertFalse(any(call[:3] == ["gh", "issue", "create"] for call in calls))
        self.assertIn("unknown closedAt; fail-closed suppress", output)

    def test_issue_lifecycle_closes_recovered_open_signatures(self) -> None:
        from ops.observability.open_prod_ops_issues import signature_label

        active_sig = signature_label("log-signal|prod|openai-previous-response-fallback")
        stale_sig = signature_label("data-layer-safety|prod|partition_coverage")
        calls, output = run_issue_lifecycle(
            issue_candidates=[],
            gh_issues=[{"number": 42, "state": "OPEN", "closedAt": None, "createdAt": "2026-07-20T08:00:00Z"}],
            open_prod_ops_issues=[
                {
                    "number": 1553,
                    "labels": [{"name": "prod-ops"}, {"name": stale_sig}],
                },
                {
                    "number": 42,
                    "labels": [{"name": "prod-ops"}, {"name": active_sig}],
                },
            ],
        )
        self.assertTrue(any(call[:4] == ["gh", "issue", "close", "1553"] for call in calls))
        self.assertFalse(any(call[:4] == ["gh", "issue", "close", "42"] for call in calls))
        self.assertIn("closed issue #1553", output)

    def test_workflow_uses_prod_ops_issue_sync_module(self) -> None:
        text = workflow_text()
        self.assertIn("python3 ops/observability/open_prod_ops_issues.py", text)
        self.assertIn("decision-and-issue:", text)
        self.assertNotIn("from ops.observability.daily_error_report import issue_analysis_markdown", text)

    def test_decision_and_issue_sets_pythonpath(self) -> None:
        text = workflow_text()
        start = text.index("decision-and-issue:")
        block = text[start : start + 3000]
        self.assertIn("PYTHONPATH: .", block)
        self.assertIn("open_prod_ops_issues.py", block)

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
        self.assertIn("open_prod_ops_issues.py", text)
        self.assertIn("## Deterministic error analysis", Path("ops/observability/open_prod_ops_issues.py").read_text(encoding="utf-8"))
        self.assertIn("if: needs.aggregate-report.result == 'success'", text)
        self.assertNotIn("needs.aggregate-report.outputs.needs_issue == 'true'", text)

        repair = REPAIR_WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("uses: ./.github/actions/run-headless-agent", repair)
        self.assertIn('max_budget_usd: "8.00"', repair)


if __name__ == "__main__":
    unittest.main()
