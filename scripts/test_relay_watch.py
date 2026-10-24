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
    def publish(self, api, items, repo=REPO, **kwargs):
        return queue.sync_queue(repo, items, "example/fork", api, day="2026-10-21", **kwargs)

    def test_all_sources_share_one_issue_and_append_comments(self):
        api = FakeGitHub()
        self.publish(api, [candidate()])
        self.publish(api, [candidate()], repo="other/source")
        self.assertEqual(len(api.issues), 1)
        self.assertEqual(len(api.comments), 2)
        self.assertIn("example/relay#1", api.comments[0]["body"])
        self.assertIn("other/source#1", api.comments[1]["body"])
        self.assertIn("当前待处理：2", api.issues[0]["body"])
        api.calls.clear()
        self.publish(api, [candidate()])
        self.assertEqual(api.writes, [])

    def test_platform_queues_isolate_tasks_resolution_and_reopening(self):
        api = FakeGitHub()
        for platform in ("kiro", "openai", "google"):
            self.publish(api, [candidate()], platform=platform)
        self.publish(api, [candidate(2)], repo="other/kiro", platform="kiro")
        self.assertEqual([row["title"].split()[0] for row in api.issues],
                         ["[kiro]", "[openai]", "[google]"])
        self.assertIn("当前待处理：2", api.issues[0]["body"])
        self.assertIn("当前待处理：1", api.issues[1]["body"])
        google_body = api.issues[2]["body"]
        api.comments[1]["body"] = api.comments[1]["body"].replace("- [ ]", "- [x]")
        self.publish(api, [], platform="openai")
        self.assertEqual([row["state"] for row in api.issues], ["open", "closed", "open"])
        self.publish(api, [candidate(2)], platform="openai")
        self.assertEqual(len(api.issues), 3)
        self.assertEqual(api.issues[1]["state"], "open")
        self.assertEqual(api.issues[2]["body"], google_body)
        api.calls.clear()
        self.publish(api, [candidate()], platform="kiro")
        self.publish(api, [candidate(2)], platform="openai")
        self.publish(api, [candidate()], platform="google")
        self.assertEqual(api.writes, [])

    def test_legacy_marker_is_only_reused_for_upstream(self):
        api = FakeGitHub()
        self.publish(api, [candidate()])
        api.issues[0]["body"] = api.issues[0]["body"].replace(queue.platform_marker("upstream"), queue.MARKER)
        self.publish(api, [candidate()], platform="kiro")
        self.assertEqual(len(api.issues), 2)
        api.calls.clear()
        self.publish(api, [candidate()])
        self.assertEqual(api.writes, [])

    def test_platform_marker_prevents_duplicate_when_label_is_removed(self):
        api = FakeGitHub()
        self.publish(api, [candidate()], platform="kiro")
        api.issues[0]["labels"] = []
        api.calls.clear()
        self.publish(api, [candidate()], platform="kiro")
        self.assertEqual(len(api.issues), 1)
        self.assertEqual(api.writes, [])

    def test_untrusted_markers_cannot_suppress_findings_or_close_aggregate(self):
        api = FakeGitHub()
        self.publish(api, [candidate()], platform="kiro")
        forged = copy.deepcopy(api.comments[0])
        forged.update(id=2, user={"id": 9, "login": "outsider"})
        forged["body"] = forged["body"].replace("- [ ]", "- [x]")
        api.comments.append(forged)
        malicious_issue = copy.deepcopy(api.issues[0])
        malicious_issue.update(number=100, user={"id": 9, "login": "outsider"})
        api.issues.append(malicious_issue)
        api.calls.clear()
        self.publish(api, [candidate()], platform="kiro")
        self.assertEqual(api.writes, [])
        self.assertEqual(api.issues[0]["state"], "open")
        self.assertIn("当前待处理：1", api.issues[0]["body"])
        self.assertEqual(api.comments[1], forged)

    def test_repository_writer_decision_survives_publisher_rotation(self):
        api = FakeGitHub()
        self.publish(api, [candidate()], platform="kiro")
        api.issues[0]["user"] = {"id": 2, "login": "maintainer"}
        api.comments[0]["user"] = {"id": 2, "login": "maintainer"}
        api.comments[0]["body"] = api.comments[0]["body"].replace("- [ ]", "- [x]")
        self.publish(api, [candidate()], platform="kiro")
        self.assertEqual(len(api.issues), 1)
        self.assertEqual(api.issues[0]["state"], "closed")

    def test_next_day_adds_only_new_or_changed_findings(self):
        api = FakeGitHub()
        self.publish(api, [candidate()])
        first = api.comments[0]["body"]
        queue.sync_queue(REPO, [candidate(), candidate(2)], "example/fork", api, day="2026-10-22")
        self.assertEqual(len(api.comments), 2)
        self.assertEqual(api.comments[0]["body"], first)
        self.assertNotIn("/issues/1)", api.comments[1]["body"])
        self.assertIn("/issues/2)", api.comments[1]["body"])

    def test_stale_overlapping_scan_cannot_republish_old_discussion_state(self):
        api = FakeGitHub()
        self.publish(api, [candidate(updated_at="2026-10-21T01:00:00Z")])
        api.calls.clear()
        self.publish(api, [candidate()])
        self.assertEqual(api.writes, [])
        self.assertIn("2026-10-21T01:00:00Z", api.comments[0]["body"])

    def test_fixed_upstream_open_issue_is_removed_and_all_done_closes(self):
        api = FakeGitHub()
        self.publish(api, [candidate()], resolved={REPO + "#1"})
        self.assertEqual(api.writes, [])
        self.publish(api, [candidate()])
        self.publish(api, [], resolved={REPO + "#1"})
        self.assertIn("- [x]", api.comments[0]["body"])
        self.assertEqual(api.issues[0]["state"], "closed")
        api.calls.clear()
        self.publish(api, [candidate()], resolved={REPO + "#1"})
        self.assertEqual(api.writes, [])
        self.publish(api, [candidate(2)], resolved={REPO + "#1"})
        self.assertEqual(len(api.issues), 1)
        self.assertEqual(api.issues[0]["state"], "open")
        self.assertIn("当前待处理：1", api.issues[0]["body"])

    def test_settled_title_class_does_not_create_or_reopen_the_aggregate(self):
        import re
        api = FakeGitHub()
        classes = (("anthropics/claude-code", re.compile(r"desktop|sidebar", re.I),
                    re.compile(r"gateway|400", re.I), "TK不适用：client"),)
        desktop = {"title": "Desktop sidebar font is blurry on iPhone"}
        self.publish(api, [candidate(**desktop)], repo="anthropics/claude-code", title_classes=classes)
        self.assertEqual(api.issues, [])
        self.publish(api, [candidate()])
        api.comments[0]["body"] = api.comments[0]["body"].replace("- [ ]", "- [x]")
        self.publish(api, [candidate()])
        self.assertEqual(api.issues[0]["state"], "closed")
        comments = len(api.comments)
        self.publish(api, [candidate(2, **desktop)], repo="anthropics/claude-code", title_classes=classes)
        self.assertEqual(len(api.comments), comments)
        self.assertEqual(api.issues[0]["state"], "closed")
        self.publish(api, [candidate(3, title="gateway returns 400 invalid_request")],
                     repo="anthropics/claude-code", title_classes=classes)
        self.assertEqual(api.issues[0]["state"], "open")
        self.assertIn("anthropics/claude-code#3", api.comments[-1]["body"])

    def test_title_class_escape_hatch_reads_body_and_topics_not_just_title(self):
        """A body/topic gateway signal must survive a client-shaped title.

        Relevance is computed over title+body+comments, so a title-only
        dismissal drops leads whose only gateway evidence is in the body — and
        an incremental scan never offers them again.
        """
        import re
        api = FakeGitHub()
        classes = (("anthropics/claude-code", re.compile(r"sidebar|\blogin\b", re.I),
                    re.compile(r"gateway|invalid_request", re.I), "TK不适用：client"),)
        # Client-shaped title, gateway evidence only in the body.
        body_evidence = candidate(1, title="OAuth login fails after update",
                                  body="through a relay gateway, /v1/messages returns invalid_request 400")
        result = self.publish(api, [body_evidence], repo="anthropics/claude-code", title_classes=classes)
        self.assertEqual(result["dismissed"], [])
        self.assertIn("anthropics/claude-code#1", api.comments[-1]["body"])

        # Same title, evidence carried only as a derived topic.
        topic_evidence = candidate(2, title="Sidebar login is blurry", topics=["invalid_request"])
        result = self.publish(api, [topic_evidence], repo="anthropics/claude-code", title_classes=classes)
        self.assertEqual(result["dismissed"], [])
        self.assertIn("anthropics/claude-code#2", api.comments[-1]["body"])

        # Genuinely client-only: still dropped, and the drop is reported.
        comments = len(api.comments)
        client_only = candidate(3, title="Sidebar font is blurry on iPhone",
                                body="the sidebar renders at the wrong weight", topics=["conversion"])
        result = self.publish(api, [client_only], repo="anthropics/claude-code", title_classes=classes)
        self.assertEqual(result["dismissed"],
                         [{"upstream": "anthropics/claude-code#3", "note": "TK不适用：client"}])
        self.assertEqual(len(api.comments), comments)

    def test_human_checkbox_resolves_without_new_approval_or_ledger(self):
        api = FakeGitHub()
        self.publish(api, [candidate()])
        api.comments[0]["body"] = api.comments[0]["body"].replace("- [ ]", "- [x]") + "Human fix PR link\n"
        api.issues[0]["body"] = "Human notes\n" + api.issues[0]["body"]
        self.publish(api, [candidate(updated_at="2026-10-22T00:00:00Z")])
        self.assertEqual(len(api.comments), 1)
        self.assertTrue(api.comments[0]["body"].endswith("Human fix PR link\n"))
        self.assertTrue(api.issues[0]["body"].startswith("Human notes"))
        self.assertEqual(api.issues[0]["state"], "closed")

    def test_upstream_closure_and_merge_are_not_local_fixes(self):
        api = FakeGitHub()
        self.publish(api, [candidate(state="closed"), candidate(2, kind="pr", state="closed", merged_at="2026-10-21T00:00:00Z")])
        self.assertIn("当前待处理：2", api.issues[0]["body"])
        self.assertEqual(api.issues[0]["state"], "open")

    def test_upstream_title_is_data_and_task_remains_a_markdown_checkbox(self):
        api = FakeGitHub()
        self.publish(api, [candidate(title="[click](evil) @all\n<!-- injected -->")])
        body = api.comments[0]["body"]
        self.assertNotIn("<!-- injected -->", body)
        self.assertNotIn("@all", body)
        self.assertIn("\\[click\\](evil)", body)
        self.assertIn("\n- [ ] [example/relay#1]", body)
        api.calls.clear()
        self.publish(api, [candidate(title="[click](evil) @all\n<!-- injected -->")])
        self.assertEqual(api.writes, [])

    def test_revoked_fix_evidence_requeues_and_reopens_same_issue(self):
        api = FakeGitHub()
        self.publish(api, [candidate()])
        self.publish(api, [], resolved={REPO + "#1"})
        self.publish(api, [candidate()])
        self.assertEqual(api.issues[0]["state"], "open")
        self.assertIn("当前待处理：1", api.issues[0]["body"])

    def test_large_daily_backfill_splits_comments_not_issues(self):
        api = FakeGitHub()
        with patch.object(queue, "COMMENT_LIMIT", 800):
            self.publish(api, [candidate(n) for n in range(1, 20)])
            self.assertEqual(len(api.issues), 1)
            self.assertGreater(len(api.comments), 1)
            self.assertTrue(all(len(c["body"].encode()) < 800 for c in api.comments))
            api.calls.clear()
            self.publish(api, [candidate(n) for n in range(1, 20)])
            self.assertEqual(api.writes, [])

    def test_partial_comment_failure_replays_without_duplicate_issue_or_tasks(self):
        api = FakeGitHub()
        def fail(*args, payload=None):
            if args[-1].endswith("/comments"):
                raise RuntimeError("comment failed")
            return api(*args, payload=payload)
        with self.assertRaisesRegex(RuntimeError, "comment failed"):
            self.publish(fail, [candidate()])
        self.publish(api, [candidate()])
        self.assertEqual(len(api.issues), 1)
        self.assertEqual(len(api.comments), 1)
        self.assertEqual(len(queue.RECORD.findall(api.comments[0]["body"])), 1)

    def test_duplicate_or_malformed_issue_fails_without_overwriting(self):
        api = FakeGitHub()
        self.publish(api, [candidate()])
        api.issues.append(copy.deepcopy(api.issues[0]))
        with self.assertRaisesRegex(ValueError, "duplicate"):
            self.publish(api, [candidate()])
        api.issues.pop()
        api.issues[0]["body"] += queue.BEGIN
        with self.assertRaisesRegex(ValueError, "malformed"):
            self.publish(api, [candidate()])


class CollectTest(unittest.TestCase):
    def test_platform_routing_uses_repo_scope_and_specific_or_shared_evidence(self):
        kiro = candidate(1, platform_hints=["kiro", "openai"])
        google = candidate(2, platform_hints=["google"])
        shared = candidate(3, platform_hints=[])
        groups = mod.platform_findings([kiro, google, shared], ("kiro", "openai", "google"))
        self.assertEqual([row["number"] for row in groups["kiro"]], [1, 3])
        self.assertEqual([row["number"] for row in groups["openai"]], [1, 3])
        self.assertEqual([row["number"] for row in groups["google"]], [2, 3])
        self.assertEqual(mod.platform_findings([google], ("kiro",)), {"kiro": [google]})
        hints = mod.platform_hints({"title": "Account refresh", "labels": [{"name": "codex"}]},
                                   [{"body": "Gemini / Antigravity has the same bug"}])
        self.assertEqual(hints, ["openai", "google"])
        with self.assertRaisesRegex(ValueError, "unknown tracking platform"):
            mod.platform_findings([shared], ("typo",))

    def test_partial_platform_publish_failure_keeps_cursor_and_retry_deduplicates(self):
        api = FakeGitHub()
        with tempfile.TemporaryDirectory() as tmp:
            checkpoint = Path(tmp) / "state.json"
            state = {"version": 1, "repo": REPO, "cursor": NOW.isoformat()}
            def fail(*args, payload=None):
                if payload and payload.get("title", "").startswith("[google]"):
                    raise RuntimeError("Google publication failed")
                return api(*args, payload=payload)
            with (patch.object(mod, "collect", return_value=(state, [candidate()])),
                  patch.object(mod.watchdog, "resolved_refs", return_value=set())):
                with self.assertRaisesRegex(RuntimeError, "Google publication failed"):
                    mod.scan_source(REPO, Path(tmp), checkpoint, "example/fork", api=fail,
                                    platforms=("openai", "google"))
                self.assertFalse(checkpoint.exists())
                report = mod.scan_source(REPO, Path(tmp), checkpoint, "example/fork", api=api,
                                         platforms=("openai", "google"))
            self.assertEqual(report["sync"]["openai"]["commented"], 0)
            self.assertEqual(report["sync"]["google"]["commented"], 1)
            self.assertEqual(len(api.issues), 2)
            self.assertEqual(json.loads(checkpoint.read_text())["cursor"], NOW.isoformat())

    def test_common_pr_boilerplate_is_not_stream_or_auth_evidence(self):
        self.assertEqual(mod.topics({"title": "README typo", "body": "Author notes: upstream issues and assertions"}), [])
        self.assertEqual(mod.topics({"title": "SSE stream truncated", "body": ""}), ["conversion"])
        self.assertEqual(mod.topics({"title": "AI Studio API Key format changed", "body": ""}), ["account"])

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

    def test_bootstrap_includes_old_open_issues_and_recent_merged_prs(self):
        def api(*args):
            endpoint = args[-1]
            if "/comments?" in endpoint:
                return [[]]
            if parse_qs(urlparse(endpoint).query).get("state") == ["open"]:
                return [[upstream_issue(1, updated_at="2020-01-01T00:00:00Z")]]
            return [[upstream_issue(2, state="closed", pull_request={"merged_at": "2026-10-20T00:00:00Z"})]]
        _, findings = mod.collect(REPO, None, NOW, api=api)
        self.assertEqual([row["number"] for row in findings], [1, 2])
        self.assertEqual(findings[1]["merged_at"], "2026-10-20T00:00:00Z")

    def test_platform_assignment_change_replays_before_acknowledging(self):
        with tempfile.TemporaryDirectory() as tmp:
            checkpoint = Path(tmp) / "state.json"
            previous = {"version": 1, "repo": REPO, "cursor": NOW.isoformat(), "platforms": ["upstream"], "context": {}}
            checkpoint.write_text(json.dumps(previous))
            with patch.object(mod, "collect", return_value=(dict(previous), [])) as collect:
                mod.scan_source(REPO, Path(tmp), checkpoint, platforms=("kiro",))
                self.assertIsNone(collect.call_args.args[1])
                self.assertEqual(json.loads(checkpoint.read_text()), previous)
                mod.scan_source(REPO, Path(tmp), checkpoint, platforms=("upstream",))
                self.assertEqual(collect.call_args.args[1], previous)

    def test_comment_classification_survives_later_neutral_reply_and_merge(self):
        first_comment = {"issue_url": f"https://api.github.com/repos/{REPO}/issues/1",
                         "body": "Gemini account quota fails", "updated_at": "2026-10-20T00:00:00Z",
                         "html_url": f"https://github.com/{REPO}/issues/1#issuecomment-1"}
        def initial(*args):
            if "/comments?" in args[-1]:
                return [[first_comment]]
            return [[upstream_issue(1, title="Help", body="details", pull_request={"merged_at": None})]]
        state, findings = mod.collect(REPO, None, NOW, api=initial)
        self.assertEqual(findings[0]["platform_hints"], ["google"])
        def later(*args):
            if "/comments?" in args[-1]:
                return [[{**first_comment, "body": "Thanks, done", "updated_at": "2026-10-22T00:00:00Z"}]]
            return [[upstream_issue(1, title="Help", body="details", state="closed",
                                    updated_at="2026-10-22T00:00:00Z",
                                    pull_request={"merged_at": "2026-10-22T00:00:00Z"})]]
        _, findings = mod.collect(REPO, state, datetime(2026, 10, 22, tzinfo=timezone.utc), api=later)
        self.assertEqual(len(findings), 1)
        self.assertEqual(findings[0]["merged_at"], "2026-10-22T00:00:00Z")
        groups = mod.platform_findings(findings, ("openai", "google"))
        self.assertEqual(groups["openai"], [])
        self.assertEqual(groups["google"], findings)

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
