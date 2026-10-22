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
    """Inner host script lines from runtime-params.json builder.

    Hybrid SSM wraps the array as a single ``bash -c '…'`` command so dash
    can start the document; tests assert the unwrapped body.
    """
    from ops.stage0.ssm_wrap_bash_commands import unwrap_commands

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
        return unwrap_commands(json.loads(stdout.getvalue())["commands"])
    finally:
        if old_since is None:
            os.environ.pop("DIAGNOSTICS_LOG_SINCE", None)
        else:
            os.environ["DIAGNOSTICS_LOG_SINCE"] = old_since


def extract_runtime_params_raw_commands() -> list[str]:
    """Raw SSM commands array (still wrapped) for Hybrid dash regression."""
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
    def test_restore_canary_receipt_uses_existing_runtime_probe(self) -> None:
        commands = extract_runtime_params_commands()
        self.assertIn("echo ===PHYSICAL_BACKUP_HEALTH_RECEIPT===", commands)
        self.assertIn("echo ===END_PHYSICAL_BACKUP_HEALTH_RECEIPT===", commands)

        text = workflow_text()
        self.assertIn("postgres_backup_verdict.py", text)
        self.assertIn("physical-backup|$TARGET_ID", text)
        runtime_start = text.index('python3 - <<\'PY\' > "$OUT/runtime-params.json"')
        runtime_end = text.index("if [ \"$INCLUDE_ERROR_CLUSTERING\"", runtime_start)
        runtime_block = text[runtime_start:runtime_end]
        self.assertNotIn("run-probe.sh", runtime_block)

    def test_large_restore_receipt_survives_runtime_ssm_transport(self) -> None:
        from ops.observability.probe_output_transport import decode

        commands = extract_runtime_params_commands()
        start = commands.index("echo ===PHYSICAL_BACKUP_HEALTH_RECEIPT===") + 1
        end = commands.index("echo ===END_PHYSICAL_BACKUP_HEALTH_RECEIPT===")
        command = "\n".join(commands[start:end])
        row = {"rows": 100000, "total_cost": "123.4567890123", "actual_cost": "12.3456789012",
               "min_created_at": "2026-01-01T00:00:00.000000Z",
               "max_created_at": "2026-01-31T23:59:59.123456Z"}
        source = {f"usage_logs_{i}": row for i in range(100)}
        receipt = json.dumps({"usage_verification": {"source": source, "restored": source}})
        self.assertGreater(len(receipt), 24000)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "receipt.json"
            fixture.write_text(receipt)
            sudo = root / "sudo"
            sudo.write_text('#!/bin/sh\nexec cat "$TEST_RECEIPT"\n')
            sudo.chmod(0o755)
            env = {**os.environ, "PATH": directory + os.pathsep + os.environ["PATH"],
                   "TEST_RECEIPT": str(fixture)}
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", command],
                                    env=env, capture_output=True, text=True, check=True)
            self.assertLess(len(result.stdout), 23000)
            self.assertEqual(decode(result.stdout).decode(), receipt)
            fixture.unlink()
            missing = subprocess.run(["bash", "-euo", "pipefail", "-c", command],
                                     env=env, capture_output=True, text=True, check=True)
            self.assertEqual(decode(missing.stdout), b"")

    def test_runtime_ssm_params_wrap_pipefail_in_bash_c_for_hybrid_dash(self) -> None:
        """Hybrid RunShellScript starts under dash; pipefail must be inside bash -c."""
        raw = extract_runtime_params_raw_commands()
        self.assertEqual(len(raw), 1, raw)
        self.assertTrue(raw[0].startswith("bash -c "), raw[0][:80])
        body = extract_runtime_params_commands()
        self.assertEqual(body[0], "set -euo pipefail")
        self.assertIn("echo ===INTERNAL_HEALTH===", body)

        text = workflow_text()
        for marker in (
            'python3 - <<\'PY\' > "$OUT/runtime-params.json"',
            'python3 - <<\'PY\' > "$OUT/error-clustering-params.json"',
            "python3 - <<'PY' > ssm-params.json",
        ):
            start = text.index(marker)
            end = text.index("\n          PY", start)
            block = text[start:end]
            self.assertIn(
                '["bash -c " + shlex.quote("\\n".join(commands))]',
                block,
                msg=f"missing Hybrid bash -c wrap near {marker}",
            )

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
        self.assertIn("DATA_LAYER_SNAPSHOT_APPLICABLE=false", snapshot_block)
        self.assertIn('INSTANCE_ID" == mi-*', snapshot_block)
        self.assertIn('"$TARGET_REGION" "$STACK_NAME" "$INSTANCE_ID"', snapshot_block)
        self.assertNotIn("describe-instances", snapshot_block)
        self.assertNotIn("BlockDeviceMappings[0]", snapshot_block)
        self.assertNotIn("describe-snapshots", snapshot_block)

    def test_qa_phase2_live_health_probe_is_wired_for_prod(self) -> None:
        text = workflow_text()
        self.assertIn("probe-qa-phase2-live-health.sh", text)
        self.assertIn("prod_phase2_live_health.py --from-probe-stdin", text)
        self.assertIn("--legacy-aws --json", text)
        self.assertIn("jq -e . >/dev/null", text)
        self.assertNotIn("qa_phase2_live_health_eval_failed\"}}')", text)
        self.assertIn("qa-phase2-live-health|$TARGET_ID", text)
        self.assertIn("qa-raw-archive-iam|$TARGET_ID", text)
        self.assertLess(text.index("SAFETY_VERDICT="), text.index("PHASE2_PROBE="))



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
        self.assertTrue(any(call[:2] == ["gh", "label"] for call in calls))
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

    def test_prod_diagnose_uses_resolve_prod_ssm_target(self) -> None:
        text = workflow_text()
        self.assertIn("ops/stage0/resolve_prod_ssm_target.py", text)
        # Job-level PROD_SSM_TARGET so Run target diagnostics + run-probe inherit
        # cutover (step-only env left diagnose on auto → AccessDenied cascade).
        job = text.index("  diagnose-targets:")
        job_env = text.index("\n    env:\n", job)
        first_step = text.index("\n    steps:", job_env)
        self.assertIn(
            "PROD_SSM_TARGET: ${{ vars.PROD_SSM_TARGET || 'auto' }}",
            text[job_env:first_step],
        )
        # Live-host and config audit are both prod-only; no edge target path remains.
        audit = text.index("Assert live-host state (prod daily audit)")
        config = text.index("Audit prod configuration independently of deployment")
        diagnose = text.index("- name: Run target diagnostics")
        self.assertLess(audit, config)
        self.assertLess(config, diagnose)
        self.assertIn("resolve_prod_ssm_target.py", text[audit:config])
        self.assertNotIn("assert-first-output-timeouts.sh", text[audit:config])
        self.assertNotIn("TARGET_KIND != 'prod'", text[audit:config])
        self.assertIn("resolve_prod_ssm_target.py", text[config:diagnose + 600])
        self.assertIn(
            'if [ "$TARGET_KIND" = "prod" ]; then',
            text[diagnose : diagnose + 2500],
        )
        # Error-clustering installer must reuse resolved INSTANCE_ID (Hybrid mi-*).
        install = text.index("bash ops/stage0/deploy-error-clustering-binary.sh")
        window = text[install - 220 : install + 80]
        self.assertIn("INSTANCE_ID=\"$INSTANCE_ID\"", window)
        self.assertNotIn("STACK=\"$STACK_NAME\"", window)

    def test_missing_target_reports_skipped_when_diagnose_cancelled(self) -> None:
        text = workflow_text()
        self.assertIn("DISCOVER_TARGETS_RESULT", text)
        self.assertIn("DIAGNOSE_TARGETS_RESULT", text)
        self.assertIn('diagnose_result not in ("cancelled", "skipped")', text)

        text = workflow_text()
        init = text.index("RUNTIME_SSM_TRANSPORT_OK=false")
        send_success = text.index("RUNTIME_SSM_TRANSPORT_OK=true", init)
        suppress = text.index('elif [ "$RUNTIME_SSM_TRANSPORT_OK" != "true" ]; then', send_success)
        binary_check = text.index("error_clustering_binary_check", suppress)

        self.assertLess(init, send_success)
        self.assertLess(send_success, suppress)
        self.assertLess(suppress, binary_check)
        self.assertIn(
            "Runtime SSM SendCommand failed; suppressing downstream error_clustering SSM checks",
            text,
        )

    def test_aggregate_finds_single_artifact_flat_target_report(self) -> None:
        """download-artifact flattens when artifacts.length===1; rglob must find it.

        Prod-only fleets upload one target artifact. With merge-multiple=false the
        action still extracts that single match directly into path/, so
        */target-report.json misses and falsely opens missing_target_reports (#2689).
        """
        text = workflow_text()
        aggregate = text.index("Aggregate target reports")
        window = text[aggregate : aggregate + 2500]
        self.assertIn('root.rglob("target-report.json")', window)
        self.assertNotIn('root.glob("*/target-report.json")', window)

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            flat = root / "target-report.json"
            nested = root / "prod-ops-target-prod-1" / "target-report.json"
            nested.parent.mkdir()
            payload = {"target": {"target_id": "prod"}, "findings": []}
            flat.write_text(json.dumps(payload), encoding="utf-8")
            nested.write_text(json.dumps({**payload, "target": {"target_id": "nested"}}), encoding="utf-8")
            found = sorted(p.relative_to(root).as_posix() for p in root.rglob("target-report.json"))
            self.assertEqual(
                found,
                ["prod-ops-target-prod-1/target-report.json", "target-report.json"],
            )
            # Old one-level glob only sees the nested layout — the prod-only failure mode.
            self.assertEqual(
                [p.relative_to(root).as_posix() for p in sorted(root.glob("*/target-report.json"))],
                ["prod-ops-target-prod-1/target-report.json"],
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

    def test_daily_error_probe_runs_without_a_pgdump_target_variable(self) -> None:
        text = workflow_text()
        start = text.index("            # Canonical daily error ledger:")
        end = text.index("\n          fi\n", start)
        script = textwrap.dedent(text[start:end])
        # Execute the actual workflow branch under nounset. Only its external
        # probe/report commands are stubbed; no AWS or production calls occur.
        stubs = r'''
        bash() { printf '%s\n' "$@" > "$OUT/probe-args"; return "$PROBE_EXIT"; }
        python3() { return 0; }
        jq() {
          case "$2" in
            .status) printf 'ok\n' ;;
            .summary) printf 'clean\n' ;;
            *) return 1 ;;
          esac
        }
        add_finding() { printf '%s\n' "$2" > "$OUT/finding-status"; }
        '''
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = {**os.environ, "OUT": directory, "STDERR": str(root / "stderr"),
                   "SINCE_HOURS": "24", "TARGET_ID": "prod"}
            env.pop("PROBE_TARGET", None)
            for code, status in [(0, "ok"), (1, "manual_ops")]:
                with self.subTest(probe_exit=code):
                    result = subprocess.run(
                        ["bash", "-euo", "pipefail", "-c", stubs + script],
                        env={**env, "PROBE_EXIT": str(code)},
                        capture_output=True, text=True,
                    )
                    self.assertEqual(result.returncode, 0, result.stderr)
                    args = (root / "probe-args").read_text().splitlines()
                    self.assertEqual(args[args.index("--target") + 1], "prod")
                    self.assertEqual(args[args.index("--env") + 1], "WINDOW_HOURS=24")
                    self.assertEqual((root / "finding-status").read_text().strip(), status)

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


class DataContainerResolutionTest(unittest.TestCase):
    """Host-side PG/Redis names come from the canonical resolver, never literals."""

    def _heredoc(self, marker: str) -> str:
        jobs = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))["jobs"]
        for step in jobs["log-dump"]["steps"]:
            run = step.get("run") or ""
            if marker in run:
                start = run.index(marker) + len(marker)
                return run[start:run.index("\nPY\n", start)]
        raise AssertionError("log-dump heredoc not found")

    def _log_dump_commands(self, container: str) -> str:
        script = self._heredoc("python3 - <<'PY' > ssm-params.json\n")
        env = {**os.environ, "INPUT_CONTAINER": container, "INPUT_SINCE": "10m",
               "INPUT_TAIL": "100", "GREP_B64": ""}
        out = subprocess.run([sys.executable, "-c", script], cwd=REPO_ROOT, env=env,
                             capture_output=True, text=True, check=True).stdout
        return json.loads(out)["commands"][0]

    def test_log_dump_resolves_logical_containers(self) -> None:
        pg = self._log_dump_commands("tokenkey-postgres")
        self.assertIn("tk-archive-standby", pg)
        self.assertIn('docker logs "$LOG_CONTAINER"', pg)
        redis = self._log_dump_commands("tokenkey-redis")
        self.assertIn("tk-cutover-prod-redis", redis)
        app = self._log_dump_commands("tokenkey")
        self.assertIn("active-color", app)
        self.assertNotIn("docker logs tokenkey ", app)
        caddy = self._log_dump_commands("tokenkey-caddy")
        self.assertIn("LOG_CONTAINER=tokenkey-caddy", caddy)

    def test_inline_commands_never_hardcode_data_container_exec(self) -> None:
        text = WORKFLOW.read_text(encoding="utf-8")
        # The repo-wide guard (scripts/checks/data-container-resolver.py) covers
        # literal names; here pin that every data-container pick uses the owner.
        self.assertNotIn("exec tokenkey-postgres", text)
        self.assertNotIn("exec tokenkey-redis", text)
        self.assertIn('candidates=_tk.POSTGRES_CANDIDATES', text)
        self.assertIn('candidates=_tk.REDIS_CANDIDATES', text)


if __name__ == "__main__":
    unittest.main()
