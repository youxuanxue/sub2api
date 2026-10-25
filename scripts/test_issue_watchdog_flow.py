#!/usr/bin/env python3
"""Behavioral checks for the shared watchdog, with GitHub isolated at the API boundary."""
import copy
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qs, urlparse

SPEC = importlib.util.spec_from_file_location(
    "watchdog_run", Path(__file__).parent / "upstream/watchdog-run.py")
mod = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(mod)


def upstream_issue(number=900001, **kwargs):
    return {"number": number, "title": "OAuth 429 error", "body": "repro",
            "state": "open", "updated_at": "2026-09-11T00:00:00Z",
            "html_url": f"https://github.com/example/upstream/issues/{number}", **kwargs}


def finding(source, number=900001):
    ref = f"{source['repo']}#{number}"
    return {"number": number, "upstream": ref, "url": mod.engine.issue_url(ref),
            "impact": "high", "title": "Confirmed bug", "rationale": "Reproduced in TokenKey"}


class FakeGitHub:
    def __init__(self, issues=()):
        self.issues = copy.deepcopy(list(issues))
        self.calls = []
        self.comments = []
        self.labels = {source["label"] for source in mod.SOURCES.values()}

    def __call__(self, *args, payload=None):
        self.calls.append((args, payload))
        if payload is None:
            if args[-1] == "user":
                return {"id": 1, "login": "watchdog"}
            if "/collaborators/" in args[-1]:
                return {"permission": "write" if "/maintainer/" in args[-1] else "read"}
            if "/comments?" in args[-1]:
                number = int(urlparse(args[-1]).path.split("/")[-2])
                return [copy.deepcopy([row for row in self.comments if row["issue_number"] == number])]
            if "/labels?" in args[-1]:
                return [[{"name": name} for name in self.labels]]
            label = parse_qs(urlparse(args[-1]).query).get("labels", [None])[0]
            return [copy.deepcopy([row for row in self.issues if label is None or
                                   label in {entry["name"] for entry in row["labels"]}])]
        if "PATCH" in args:
            number = int(args[-1].rsplit("/", 1)[1])
            row = (next(row for row in self.comments if row["id"] == number) if "/issues/comments/" in args[-1]
                   else next(row for row in self.issues if row["number"] == number))
            row.update(payload)
            if "labels" in payload:
                row["labels"] = [{"name": name} for name in payload["labels"]]
        elif args[-1].endswith("/comments"):
            row = {**payload, "id": len(self.comments) + 1,
                   "issue_number": int(args[-1].split("/")[-2])}
            self.comments.append(row)
        elif args[-1].endswith("/labels"):
            self.labels.add(payload["name"])
            row = payload
        else:
            row = {**payload, "number": len(self.issues) + 1, "state": "open",
                   "labels": [{"name": label} for label in payload["labels"]]}
            self.issues.append(row)
        row.setdefault("user", {"id": 1, "login": "watchdog"})
        return copy.deepcopy(row)

    @property
    def writes(self):
        return [call for call in self.calls if call[1] is not None]


class WatchdogFlowTest(unittest.TestCase):
    def test_incremental_fetch_preserves_unseen_and_applies_closure(self):
        source = next(iter(mod.SOURCES.values()))
        previous = {"version": 1, "repo": source["repo"], "cursor": "2026-09-11T01:00:00Z",
                    "issues": [upstream_issue(1), upstream_issue(2)]}
        calls = []

        def api(*args):
            calls.append(args)
            return [[upstream_issue(2, state="closed"), upstream_issue(3)],
                    [upstream_issue(4, pull_request={})]]

        snapshot = mod.fetch_snapshot(source, previous, "2026-09-11T02:00:00Z", api)
        self.assertEqual([row["number"] for row in snapshot["issues"]], [1, 3])
        params = parse_qs(urlparse(calls[0][-1]).query)
        self.assertEqual(params["since"], ["2026-09-11T00:55:00+00:00"])
        self.assertEqual(params["state"], ["all"])
        self.assertEqual(previous["issues"][1]["state"], "open")
        self.assertEqual(snapshot["cursor"], "2026-09-11T02:00:00Z")

    def test_missing_checkpoint_bootstraps_all_open_pages(self):
        calls = []

        def api(*args):
            calls.append(args)
            return [[upstream_issue(1)], [upstream_issue(2)]]

        source = next(iter(mod.SOURCES.values()))
        result = mod.fetch_snapshot(source, None, "2026-09-11T00:00:00Z", api)
        self.assertEqual([row["number"] for row in result["issues"]], [1, 2])
        params = parse_qs(urlparse(calls[0][-1]).query)
        self.assertNotIn("since", params)
        self.assertEqual(params["state"], ["open"])

    def test_keyword_candidates_remain_visible_without_claiming_high_risk(self):
        for source in mod.SOURCES.values():
            classifier = mod.module("classifier_test", source["classifier"])
            issue = upstream_issue()
            entry = classifier.classify(issue)
            report = mod.engine.build_report([issue], {"issues": [entry]}, {"issues": []}, [])
            self.assertEqual([row["number"] for row in report["high_unresolved"]], [])
            self.assertEqual(report["needs_review"][0]["upstream"], entry["upstream"])
            self.assertIn("待核实：1", mod.engine.report_markdown(report))

    def test_missing_and_empty_anchors_revoke_runtime_fixed_classification(self):
        with tempfile.TemporaryDirectory() as tmp:
            missing_path = Path(tmp) / "missing.go"
            for specs in [[f"{missing_path}:old implementation"], []]:
                triage = {"issues": [{"upstream": "example/upstream#1", "url": "url",
                                       "impact": "fixed", "tokenkey_status": "fixed_in_tokenkey"}]}
                report = mod.engine.build_report([upstream_issue(1)], triage, {"issues": []},
                                                [{"upstream": "example/upstream#1",
                                                  "fixed_if_all_present": specs}])
                self.assertEqual(report["anchors_present"], [])
                self.assertEqual(len(report["fact_check_missing"]), 1)
                self.assertEqual(report["needs_review"][0]["tokenkey_status"], "needs_tokenkey_review")

    def test_explicit_issue_is_selected_without_promoting_its_risk(self):
        source = next(iter(mod.SOURCES.values()))
        classifier = mod.module("forced_classifier", source["classifier"])
        issue = upstream_issue(state="closed")
        report = mod.engine.build_report([issue], {"issues": [classifier.classify(issue)]},
                                         {"issues": []}, [], str(issue["number"]))
        self.assertEqual(report["selected_issue"]["number"], issue["number"])
        self.assertEqual(report["selected_issue"]["impact"], "needs_review")
        self.assertEqual(report["high_unresolved"], [])

    def test_related_candidates_publish_without_high_risk_and_fixed_are_excluded(self):
        source = mod.SOURCES["upstream"]
        item = {**finding(source), "topics": ["billing"], "impact": "medium"}
        report = {"high_unresolved": []}
        api = FakeGitHub()
        with patch.object(mod, "resolved_refs", return_value=set()):
            mod.sync_issues(source, report, "example/fork", api, entries=[item])
        self.assertEqual(len(api.issues), 1)
        self.assertIn(item["upstream"], api.comments[0]["body"])
        with patch.object(mod, "resolved_refs", return_value={item["upstream"]}):
            mod.sync_issues(source, report, "example/fork", api, entries=[item])
        # Settled work leaves the queue; it is not kept as a struck-through line.
        self.assertNotIn(item["upstream"], api.comments[0]["body"])
        self.assertEqual(api.issues[0]["state"], "closed")

    def test_queue_holds_judged_work_only_and_keyword_leads_become_intel(self):
        """The Issue is for work TokenKey still owes. A keyword hit is a lead, not a
        task -- the classifier's own rationale says it is unverified -- so it must
        not enter the queue. An unknown or missing status fails closed into the
        queue: a visible extra task is cheaper than a silently lost one."""
        source = mod.SOURCES["upstream"]
        base = {**finding(source), "topics": ["billing"], "impact": "medium"}

        def entry(number, status):
            ref = f"{source['repo']}#{number}"
            return {**base, "number": number, "upstream": ref,
                    "url": mod.engine.issue_url(ref), "tokenkey_status": status}

        entries = [entry(1, "candidate_unverified"), entry(2, "not_prioritized"),
                   entry(3, "needs_tokenkey_review"), entry(4, "partially_mitigated"),
                   entry(5, "fixed_in_tokenkey"), entry(6, "intentional_policy"),
                   dict(base, number=7, upstream=f"{source['repo']}#7",
                        url=mod.engine.issue_url(f"{source['repo']}#7"))]
        queued, intel = mod.partition_entries(entries)
        self.assertEqual([item["number"] for item in queued], [3, 4, 7])
        self.assertEqual([item["number"] for item in intel], [1, 2])

        # Keyword leads are not dropped on the floor: they are reported so the
        # rotating intel Issue can carry them, and nothing is lost in the meantime.
        report = {"high_unresolved": []}
        api = FakeGitHub()
        with patch.object(mod, "resolved_refs", return_value=set()):
            outcome = mod.sync_issues(source, report, "example/fork", api, entries=entries)
        self.assertEqual((outcome["queued"], outcome["intel"]), (3, 2))
        self.assertEqual(len(report["intel_pending_carrier"]), 2)
        body = "".join(comment["body"] for comment in api.comments)
        for number in (3, 4, 7):
            self.assertIn(f"{source['repo']}#{number}", body)
        for number in (1, 2, 5, 6):
            self.assertNotIn(f"{source['repo']}#{number}", body)
        self.assertIn("当前待处理：3", api.issues[0]["body"])

    def test_resolutions_use_grouped_refs_and_revoke_missing_anchors(self):
        source = mod.SOURCES["upstream"]
        entry = {"upstream": source["repo"] + "#1, #2", "fixed_if_all_present": ["fixed.py:proof"]}
        with (patch.dict(mod.ledger.SOURCES, {"upstream": source}, clear=True),
              patch.object(mod.ledger, "load_ledger", return_value=[entry]),
              patch.object(mod.engine, "check_fact", return_value={"ok": True})):
            self.assertEqual(mod.resolved_refs(), {source["repo"] + "#1", source["repo"] + "#2"})
            with patch.object(mod.engine, "check_fact", return_value={"ok": False}):
                self.assertEqual(mod.resolved_refs(), set())

    def test_malformed_region_is_not_overwritten(self):
        with self.assertRaisesRegex(ValueError, "malformed"):
            mod.managed_body("human text " + mod.BEGIN, "replacement")

    def test_fix_prompt_preserves_source_branch_and_ledger(self):
        for name, source in mod.SOURCES.items():
            with tempfile.TemporaryDirectory() as tmp, patch.object(mod.engine, "set_output") as output:
                work = Path(tmp)
                item = finding(source)
                (work / "report.json").write_text(json.dumps({"selected_issue": item}))
                mod.prepare_fix(name, work)
                output.assert_any_call("branch", source["branch"] + str(item["number"]))
                prompt = (work / "fix-prompt.txt").read_text()
                self.assertIn(f"--ledger {name}", prompt)
                self.assertIn("behavioral", prompt)
                self.assertIn("Never merge", prompt)
                self.assertIn("without a closing keyword", prompt)

    def test_fix_without_candidate_fails_before_launching_agent(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            (work / "report.json").write_text(json.dumps({"selected_issue": None}))
            with self.assertRaisesRegex(ValueError, "no fix candidate"):
                mod.prepare_fix(next(iter(mod.SOURCES)), work)

    def test_scan_derives_fix_artifact_from_curated_evidence_only(self):
        for source in mod.SOURCES.values():
            with self.subTest(source=source["repo"]), tempfile.TemporaryDirectory() as tmp:
                work = Path(tmp) / "reports"
                fact = {"upstream": source["repo"] + "#900001", "summary": "Regression covered",
                        "fixed_if_all_present": ["fixed.py:proved"]}
                with (patch.object(mod.ledger, "load_ledger", return_value=[fact]),
                      patch.object(mod.engine, "check_fact", return_value={"ok": True})):
                    report = mod.scan(source, work, Path(tmp) / "state.json",
                                      api=lambda *args: [[upstream_issue()]])
                triage = json.loads((work / "triage.json").read_text())
                fixes = json.loads((work / "fixes.json").read_text())
                self.assertEqual(triage["issues"][0]["tokenkey_status"], "fixed_in_tokenkey")
                self.assertEqual(fixes["issues"][0]["summary"], fact["summary"])
                self.assertEqual(report["needs_review"], [])

    def test_failed_sync_does_not_advance_checkpoint_and_retry_converges(self):
        source = next(iter(mod.SOURCES.values()))
        with tempfile.TemporaryDirectory() as tmp:
            checkpoint = Path(tmp) / "state.json"
            previous = {"version": 1, "repo": source["repo"], "cursor": "2026-09-10T00:00:00Z",
                        "issues": []}
            checkpoint.write_text(json.dumps(previous))
            before = checkpoint.read_bytes()
            work = Path(tmp) / "report"
            api = lambda *args: [[upstream_issue()]]
            with (patch.object(mod, "sync_issues", side_effect=RuntimeError("GitHub unavailable")),
                  self.assertRaisesRegex(RuntimeError, "GitHub unavailable")):
                mod.scan(source, work, checkpoint, target_repo="example/fork", api=api)
            self.assertEqual(checkpoint.read_bytes(), before)
            with patch.object(mod, "sync_issues"):
                report = mod.scan(source, work, checkpoint, target_repo="example/fork", api=api)
            saved = json.loads(checkpoint.read_text())
            self.assertEqual([row["number"] for row in saved["issues"]], [900001])
            self.assertNotEqual(saved["cursor"], previous["cursor"])
            self.assertEqual(len(report["needs_review"]), 1)


if __name__ == "__main__":
    unittest.main()
