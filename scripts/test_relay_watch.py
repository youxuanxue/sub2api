"""Behavior tests for durable candidate discovery and publishing."""
import copy
import importlib.util
import json
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qs, urlparse

from scripts.test_issue_watchdog_flow import FakeGitHub, upstream_issue

SPEC = importlib.util.spec_from_file_location("relay_watch", Path(__file__).parent / "upstream/relay-watch.py")
mod = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(mod)
queue = mod.watchdog.queue
NOW = datetime(2026, 10, 21, tzinfo=timezone.utc)
REPO = "example/relay"


def candidate(number=1, **kwargs):
    return {"number": number, "title": "OAuth refresh fails", "updated_at": "2026-10-20T01:00:00Z",
            "kind": "issue", "state": "open", **kwargs}


class QueueTest(unittest.TestCase):
    def test_unverified_candidates_create_queue_without_high_risk_claim_and_replay_is_readonly(self):
        api = FakeGitHub()
        counts = queue.sync_queue(REPO, [candidate()], "example/fork", api)
        self.assertEqual(counts["created"], 1)
        self.assertIn("待核实", api.issues[0]["body"])
        self.assertEqual(api.issues[0]["labels"], [{"name": queue.LABEL}])
        api.calls.clear()
        queue.sync_queue(REPO, [candidate()], "example/fork", api)
        self.assertEqual(api.writes, [])

    def test_new_updates_preserve_old_findings_and_human_decisions(self):
        api = FakeGitHub()
        queue.sync_queue(REPO, [candidate()], "example/fork", api)
        api.issues[0]["body"] = "Operator notes\n" + api.issues[0]["body"] + "\nFix PR link"
        api.issues[0]["labels"].append({"name": "human-label"})
        queue.sync_queue(REPO, [candidate(2)], "example/fork", api)
        body = api.issues[0]["body"]
        self.assertIn("/issues/1)", body)
        self.assertIn("/issues/2)", body)
        self.assertTrue(body.startswith("Operator notes"))
        self.assertTrue(body.endswith("Fix PR link"))
        self.assertIn({"name": "human-label"}, api.issues[0]["labels"])

    def test_bounded_buckets_and_closed_operator_decision(self):
        api = FakeGitHub()
        with patch.object(queue, "PAGE_SIZE", 1):
            queue.sync_queue(REPO, [candidate(1), candidate(100)], "example/fork", api)
        self.assertEqual(len(api.issues), 2)
        api.issues[0]["state"] = "closed"
        api.calls.clear()
        queue.sync_queue(REPO, [candidate(1, title="new evidence")], "example/fork", api)
        self.assertEqual(api.writes, [])
        queue.sync_queue(REPO, [candidate(2)], "example/fork", api)
        self.assertEqual(len(api.issues), 2)
        self.assertEqual(api.issues[0]["state"], "closed")
        self.assertIn("/issues/2)", api.issues[1]["body"])
        api.issues[1]["state"] = "closed"
        queue.sync_queue(REPO, [candidate(3)], "example/fork", api)
        self.assertEqual(len(api.issues), 3)
        self.assertIn("/issues/3)", api.issues[2]["body"])

    def test_merge_update_is_not_inferred_from_closed_and_titles_are_data(self):
        api = FakeGitHub()
        queue.sync_queue(REPO, [candidate(kind="pr", state="closed", title="[click](evil) @all\n<!-- injected -->")], "example/fork", api)
        self.assertIn("pr/closed", api.issues[0]["body"])
        self.assertNotIn("<!-- injected -->", api.issues[0]["body"])
        queue.sync_queue(REPO, [candidate(kind="pr", state="closed", merged_at="2026-10-21T00:00:00Z")], "example/fork", api)
        self.assertIn("pr/merged", api.issues[0]["body"])
        self.assertEqual(api.issues[0]["body"].count("<!-- candidate:1 -->"), 1)

    def test_older_scan_cannot_regress_newer_discussion_evidence(self):
        api = FakeGitHub()
        queue.sync_queue(REPO, [candidate(updated_at="2026-10-21T00:00:00Z")], "example/fork", api)
        api.calls.clear()
        queue.sync_queue(REPO, [candidate(title="stale", updated_at="2026-10-20T01:00:00+00:00")], "example/fork", api)
        self.assertEqual(api.writes, [])

    def test_duplicate_or_malformed_queue_fails_without_overwriting(self):
        api = FakeGitHub()
        queue.sync_queue(REPO, [candidate()], "example/fork", api)
        api.issues.append(copy.deepcopy(api.issues[0]))
        with self.assertRaisesRegex(ValueError, "duplicate"):
            queue.sync_queue(REPO, [candidate()], "example/fork", api)
        api.issues.pop()
        api.issues[0]["body"] += queue.BEGIN
        with self.assertRaisesRegex(ValueError, "malformed"):
            queue.sync_queue(REPO, [candidate()], "example/fork", api)

    def test_existing_watchdog_publishes_pending_without_promoting_risk(self):
        api = FakeGitHub()
        source = mod.watchdog.SOURCES["upstream"]
        entry = {**candidate(), "upstream": source["repo"] + "#1", "impact": "needs_review"}
        report = {"high_unresolved": [], "needs_review": [entry]}
        mod.watchdog.sync_issues(source, report, "example/fork", api)
        self.assertEqual(api.writes, [])
        mod.watchdog.sync_review_queue(source, report, "example/fork", api)
        self.assertEqual(len(api.issues), 1)
        self.assertEqual(report["high_unresolved"], [])


class CollectTest(unittest.TestCase):
    def test_common_pr_boilerplate_is_not_stream_or_auth_evidence(self):
        self.assertEqual(mod.topics({"title": "README typo", "body": "Author notes: upstream issues and assertions"}), [])
        self.assertEqual(mod.topics({"title": "SSE stream truncated", "body": ""}), ["conversion"])

    def test_paginated_issues_pr_merge_and_comment_only_evidence(self):
        calls = []
        def api(*args):
            calls.append(args)
            endpoint = args[-1]
            if "/issues/comments?" in endpoint:
                return [[{"issue_url": "https://api.github.com/repos/example/relay/issues/3",
                          "body": "OAuth refresh now returns 401", "updated_at": "2026-10-21T00:00:00Z",
                          "html_url": "https://github.com/example/relay/issues/3#issuecomment-5"}]]
            if endpoint.endswith("/issues/3"):
                return upstream_issue(3, title="Help", body="details", updated_at="2026-10-19T00:00:00Z")
            if endpoint.endswith("/pulls/2"):
                return {"merged_at": "2026-10-20T00:00:00Z", "merge_commit_sha": "abc"}
            return [[upstream_issue(1, pull_request={"merged_at": None}, state="closed")],
                    [upstream_issue(2, pull_request={}, state="closed"), upstream_issue(4, title="README typo", body="spelling")]]
        state, findings = mod.collect(REPO, None, NOW, api=api)
        self.assertEqual([row["number"] for row in findings], [1, 2, 3])
        self.assertIsNone(findings[0]["merged_at"])
        self.assertEqual(findings[1]["merge_commit_sha"], "abc")
        self.assertEqual(findings[2]["updated_at"], "2026-10-21T00:00:00Z")
        self.assertEqual(findings[2]["topics"], ["account"])
        self.assertEqual(state["cursor"], NOW.isoformat())
        self.assertIn("--paginate", calls[0])

    def test_incremental_overlap_and_identity_guard(self):
        calls = []
        def api(*args):
            calls.append(args)
            return [[]]
        previous = {"version": 1, "repo": REPO, "cursor": NOW.isoformat()}
        mod.collect(REPO, previous, NOW, api=api)
        for call in calls:
            self.assertEqual(parse_qs(urlparse(call[-1]).query)["since"], ["2026-10-20T23:55:00+00:00"])
        with self.assertRaisesRegex(ValueError, "mismatch"):
            mod.collect("other/repo", previous, NOW, api=api)

    def test_failed_sync_preserves_cursor_and_dry_run_does_not_acknowledge(self):
        with tempfile.TemporaryDirectory() as tmp:
            checkpoint = Path(tmp) / "state.json"
            initial = {"version": 1, "repo": REPO, "cursor": "2026-10-01T00:00:00+00:00"}
            checkpoint.write_text(json.dumps(initial))
            old = checkpoint.read_bytes()
            with patch.object(mod, "collect", return_value=({**initial, "cursor": NOW.isoformat()}, [candidate()])):
                mod.scan_source(REPO, Path(tmp) / "out", checkpoint)
                self.assertEqual(checkpoint.read_bytes(), old)
                with patch.object(queue, "sync_queue", side_effect=RuntimeError("write failed")):
                    with self.assertRaisesRegex(RuntimeError, "write failed"):
                        mod.scan_source(REPO, Path(tmp) / "out", checkpoint, "example/fork")
                self.assertEqual(checkpoint.read_bytes(), old)
                with patch.object(queue, "sync_queue", return_value={"created": 1}):
                    mod.scan_source(REPO, Path(tmp) / "out", checkpoint, "example/fork")
                self.assertEqual(json.loads(checkpoint.read_text())["cursor"], NOW.isoformat())


if __name__ == "__main__":
    unittest.main()
