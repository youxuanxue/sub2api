#!/usr/bin/env python3
import datetime as dt
import importlib.util
import pathlib
import json
import os
import subprocess
import tempfile
import shutil
from unittest import mock
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("detect-bad-sessions.py")
SPEC = importlib.util.spec_from_file_location("detect_bad_sessions", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(MODULE)


def row(request_id, at, status=499, latency=60_000, **extra):
    value = {
        "request_id": request_id,
        "created_at": at.isoformat().replace("+00:00", "Z"),
        "status_code": status,
        "response_latency_ms": latency,
        "error_type": "context_canceled" if status == 499 else "upstream_error",
        "error_message": "client disconnected" if status == 499 else "upstream failed",
        "account_id": 42,
        "user_id": 16,
        "api_key_id": 8,
        "platform": "anthropic",
        "model": "claude-opus-4-7",
    }
    value.update(extra)
    return value


class DetectBadSessionsTest(unittest.TestCase):
    def test_long_client_disconnect_run_is_candidate(self):
        base = dt.datetime(2026, 7, 20, 9, 16, tzinfo=dt.timezone.utc)
        errors = [row(f"r{i}", base + dt.timedelta(minutes=i), latency=60_000 if i == 0 else 330_000) for i in range(5)]
        logs = {f"r{i}": {"session_hash": "abcdef0123456789"} for i in range(5)}
        grouped, coverage = MODULE.build_events(errors, [], logs)
        candidates = MODULE.detect_candidates(grouped)
        self.assertEqual(coverage["error_rows_with_session"], 5)
        self.assertEqual(len(candidates), 1)
        self.assertEqual(candidates[0]["max_consecutive_failures"], 5)
        self.assertIn("upstream_or_gateway_stall_before_client_disconnect", candidates[0]["hypotheses"])

    def test_success_breaks_consecutive_run_and_non_actionable_is_ignored(self):
        base = dt.datetime(2026, 7, 20, 9, 0, tzinfo=dt.timezone.utc)
        errors = [row("r1", base), row("r2", base + dt.timedelta(minutes=1)), row("r3", base + dt.timedelta(minutes=3))]
        successes = [{"request_id": "ok", "session_id": None, "user_id": 16, "api_key_id": 8, "created_at": (base + dt.timedelta(minutes=2)).isoformat()}]
        logs = {"r1": {"session_hash": "same"}, "r2": {"session_hash": "same"}, "r3": {"session_hash": "same"}, "ok": {"session_hash": "same"}}
        grouped, _ = MODULE.build_events(errors, successes, logs)
        self.assertEqual(MODULE.detect_candidates(grouped), [])

        errors.append(row("bad", base + dt.timedelta(minutes=4), status=401, latency=1_000, error_type="invalid_api_key"))
        logs["bad"] = {"session_hash": "same"}
        grouped, _ = MODULE.build_events(errors, successes, logs)
        self.assertEqual(MODULE.detect_candidates(grouped), [])

    def test_provider_failures_report_account_hypothesis(self):
        base = dt.datetime(2026, 7, 20, 9, 0, tzinfo=dt.timezone.utc)
        errors = [
            row("r1", base, status=529, latency=10_000, upstream_status_code=529, error_owner="provider"),
            row("r2", base + dt.timedelta(seconds=5), status=502, latency=11_000, upstream_status_code=502, error_owner="provider"),
            row("r3", base + dt.timedelta(seconds=10), status=529, latency=12_000, upstream_status_code=529, error_owner="provider"),
        ]
        logs = {f"r{i}": {"session_hash": "provider-session", "account_id": 7} for i in range(1, 4)}
        grouped, _ = MODULE.build_events(errors, [], logs)
        candidates = MODULE.detect_candidates(grouped)
        self.assertEqual(candidates[0]["accounts"], [42])
        self.assertIn("provider_or_account_failure", candidates[0]["hypotheses"])

    def test_access_only_long_cancels_are_available_for_unscoped_scan(self):
        base = dt.datetime(2026, 7, 20, 9, 0, tzinfo=dt.timezone.utc)
        logs = {
            f"r{i}": {
                "session_hash": "access-session",
                "user_id": 16,
                "api_key_id": 8,
                "access_status": 499,
                "access_latency_ms": 45_000,
                "completed_at": (base + dt.timedelta(minutes=i)).isoformat().replace("+00:00", "Z"),
            }
            for i in range(3)
        }
        grouped, coverage = MODULE.build_events(MODULE.access_log_failures(logs), [], logs)
        candidates = MODULE.detect_candidates(grouped)
        self.assertEqual(coverage["error_rows_with_session"], 3)
        self.assertEqual(candidates[0]["failure_classes"], {"client_cancel": 3})


    def test_partial_usage_does_not_hide_failure_and_duplicate_errors_count_once(self):
        base = dt.datetime(2026, 7, 20, 9, 0, tzinfo=dt.timezone.utc)
        errors = [row(f"r{i}", base + dt.timedelta(minutes=i)) for i in range(3)]
        logs = {r["request_id"]: {"session_hash": "same"} for r in errors}
        successes = [{**r, "session_id": None} for r in errors]
        grouped, _ = MODULE.build_events(errors + errors, successes, logs)
        candidate = MODULE.detect_candidates(grouped)[0]
        self.assertEqual(candidate["request_count"], 3)
        self.assertEqual(candidate["max_consecutive_failures"], 3)

    def test_session_names_are_scoped_to_user_and_key(self):
        base = dt.datetime(2026, 7, 20, 9, 0, tzinfo=dt.timezone.utc)
        errors = [row(f"r{i}", base, session_id="same", user_id=user, api_key_id=key)
                  for i, (user, key) in enumerate([(16, 8), (17, 8), (16, 9)])]
        grouped, _ = MODULE.build_events(errors, [], {})
        self.assertEqual(MODULE.detect_candidates(grouped), [])
        unknown = [row(f"r{i}", base, session_id="same", user_id=None, api_key_id=None) for i in range(3)]
        grouped, coverage = MODULE.build_events(unknown, [], {})
        self.assertEqual(MODULE.detect_candidates(grouped), [])
        self.assertEqual(coverage["error_rows_with_session"], 0)

    def test_console_and_json_logs_preserve_full_session_and_access_metadata(self):
        lines = [
            '2026-07-20 INFO sticky.session_hash_generated ' + json.dumps({
                "request_id": "r1", "session_hash": "full-session-hash", "user_id": 16, "api_key_id": 8}),
            json.dumps({"msg": "sticky.scheduler_entry", "request_id": "r1", "session_hash": "full-ses"}),
            json.dumps({"msg": "http request completed", "request_id": "r1", "status_code": 499,
                        "latency_ms": 330000, "completed_at": "2026-07-20T09:00:00Z", "account_id": 42}),
            'invalid line',
        ]
        logs = MODULE.parse_gateway_log_lines(lines)
        self.assertEqual(logs["r1"]["session_hash"], "full-session-hash")
        event = MODULE.access_log_failures(logs)[0]
        self.assertEqual((event["user_id"], event["api_key_id"], event["response_latency_ms"]), (16, 8, 330000))

    def test_live_report_scopes_access_rows_and_marks_missing_correlation(self):
        base = dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=10)
        lines = []
        for i, user in enumerate([16, 16, 16, 17, 17, 17]):
            lines.append(json.dumps({"msg": "sticky.session_hash_generated", "request_id": f"r{i}",
                                    "session_hash": "same", "user_id": user, "api_key_id": 8, "model": "test-model"}))
            lines.append(json.dumps({"msg": "http request completed", "request_id": f"r{i}",
                                    "status_code": 499, "latency_ms": 330000,
                                    "completed_at": (base + dt.timedelta(seconds=i)).isoformat()}))
        queries = []
        def query(sql, container):
            queries.append(sql)
            return [row("missing", base)] if "FROM ops_error_logs" in sql else []
        with mock.patch.dict(os.environ, {"USER_ID": "16", "MODEL": "test-model"}, clear=True), \
             mock.patch.object(MODULE, "_resolve_container", return_value="tokenkey-blue"), \
             mock.patch.object(MODULE, "_run_json_query", side_effect=query), \
             mock.patch.object(MODULE, "_docker_log_lines", return_value=lines):
            report = MODULE.live_report()
        self.assertEqual(report["status"], "partial")
        self.assertEqual(report["coverage"]["access_only_failures"], 3)
        self.assertEqual([(c["user_id"], c["max_consecutive_failures"]) for c in report["candidates"]], [(16, 3)])
        self.assertNotIn("session_id IS NOT NULL", queries[1])
        self.assertIn("ul.user_id = 16", queries[1])

    def test_wire_2xx_breaks_access_failure_run(self):
        base = dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=10)
        lines = [json.dumps({"msg": "http request completed", "request_id": f"r{i}",
                            "status_code": status, "latency_ms": 330000,
                            "user_id": 16, "api_key_id": 8,
                            "completed_at": (base + dt.timedelta(seconds=i)).isoformat()})
                 for i, status in enumerate([499, 499, 200, 499])]
        lines += [json.dumps({"msg": "sticky.session_hash_generated", "request_id": f"r{i}", "session_hash": "same"})
                  for i in range(4)]
        with mock.patch.dict(os.environ, {}, clear=True), \
             mock.patch.object(MODULE, "_resolve_container", return_value="tokenkey-blue"), \
             mock.patch.object(MODULE, "_run_json_query", side_effect=[[], []]), \
             mock.patch.object(MODULE, "_docker_log_lines", return_value=lines):
            report = MODULE.live_report()
        self.assertEqual(report["status"], "no_candidates")
        self.assertEqual(report["coverage"]["success_rows_with_session"], 1)

    def test_wrapper_executes_python_and_propagates_failure(self):
        # Missing resolver is intentional: exercises Bash -> Python and error exit,
        # without touching Docker, the database, or a live host.
        with tempfile.TemporaryDirectory() as folder:
            script = pathlib.Path(folder) / "probe-bad-sessions.sh"
            shutil.copy2(MODULE_PATH.with_name(script.name), script)
            shutil.copy2(MODULE_PATH, pathlib.Path(folder) / MODULE_PATH.name)
            result = subprocess.run(["bash", str(script)],
                                    env={**os.environ, "CONTAINER": "auto", "TK_RESOLVER_PY": "/nonexistent"},
                                    capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(json.loads(result.stderr)["status"], "error")


if __name__ == "__main__":
    unittest.main()
