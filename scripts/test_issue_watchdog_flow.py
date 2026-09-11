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
        self.labels = {source["label"] for source in mod.SOURCES.values()}

    def __call__(self, *args, payload=None):
        self.calls.append((args, payload))
        if payload is None:
            if "/labels?" in args[-1]:
                return [[{"name": name} for name in self.labels]]
            return [copy.deepcopy(self.issues)]
        if "PATCH" in args:
            number = int(args[-1].rsplit("/", 1)[1])
            row = next(row for row in self.issues if row["number"] == number)
            row.update(payload)
        elif args[-1].endswith("/labels"):
            self.labels.add(payload["name"])
            row = payload
        else:
            row = {**payload, "number": len(self.issues) + 1, "state": "open",
                   "labels": [{"name": label} for label in payload["labels"]]}
            self.issues.append(row)
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

    def test_create_then_replay_performs_no_writes_for_either_source(self):
        for source in mod.SOURCES.values():
            api = FakeGitHub()
            report = {"high_unresolved": [finding(source)]}
            mod.sync_issues(source, report, "example/fork", api)
            self.assertEqual(len(api.issues), 1)
            api.calls.clear()
            report["generated_at"] = "a later run"
            report["run_url"] = "another run"
            mod.sync_issues(source, report, "example/fork", api)
            self.assertEqual(api.writes, [])

    def test_changed_evidence_updates_region_preserving_human_text_and_title(self):
        source = next(iter(mod.SOURCES.values()))
        item = finding(source)
        body = "Operator decision\n\n" + mod.managed_body("", mod.issue_content(item)) + "\nHuman follow-up"
        api = FakeGitHub([{"number": 12, "state": "open", "title": item["upstream"] + " customized",
                          "body": body, "labels": [{"name": source["label"]}]}])
        item["rationale"] = "New reproduction evidence"
        mod.sync_issues(source, {"high_unresolved": [item]}, "example/fork", api)
        self.assertEqual(len(api.writes), 1)
        self.assertTrue(api.issues[0]["body"].startswith("Operator decision"))
        self.assertTrue(api.issues[0]["body"].endswith("Human follow-up"))
        self.assertIn("New reproduction evidence", api.issues[0]["body"])
        self.assertTrue(api.issues[0]["title"].endswith("customized"))

    def test_closed_legacy_issue_is_not_recreated_or_reopened(self):
        for source in mod.SOURCES.values():
            item = finding(source)
            api = FakeGitHub([{"number": 12, "state": "closed", "title": "legacy title", "body": "notes",
                              "labels": [{"name": f"{source['prefix']}-issue:{item['number']}"}]}])
            mod.sync_issues(source, {"high_unresolved": [item]}, "example/fork", api)
            self.assertEqual(api.writes, [])

    def test_existing_issue_tracks_downgrade_and_fix_without_opening_keyword_issues(self):
        source = next(iter(mod.SOURCES.values()))
        item = finding(source)
        api = FakeGitHub()
        mod.sync_issues(source, {"high_unresolved": [item]}, "example/fork", api)
        for status, note in [("needs_tokenkey_review", "Recorded anchors missing"),
                             ("fixed_in_tokenkey", "Fix PR merged with a regression test")]:
            changed = {**item, "impact": "needs_review" if status != mod.engine.FIXED_STATUS else "fixed",
                       "tokenkey_status": status, "rationale": note}
            unrelated = {**finding(source, 900002), "impact": "needs_review"}
            api.calls.clear()
            mod.sync_issues(source, {"high_unresolved": []}, "example/fork", api,
                            entries=[changed, unrelated])
            self.assertEqual(len(api.issues), 1)
            self.assertEqual(len(api.writes), 1)
            self.assertIn(note, api.issues[0]["body"])
            self.assertEqual(api.issues[0]["state"], "open")
            api.calls.clear()
            mod.sync_issues(source, {"high_unresolved": []}, "example/fork", api,
                            entries=[changed, unrelated])
            self.assertEqual(api.writes, [])

    def test_anchors_alone_do_not_close_tracking_issue(self):
        source = next(iter(mod.SOURCES.values()))
        api = FakeGitHub()
        mod.sync_issues(source, {"high_unresolved": [], "anchors_present": [finding(source)]},
                        "example/fork", api)
        self.assertEqual(api.writes, [])

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
                self.assertIn(f"--ledger {name} --apply", prompt)
                self.assertIn("behavioral", prompt)
                self.assertIn("Never merge", prompt)

    def test_fix_without_candidate_fails_before_launching_agent(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            (work / "report.json").write_text(json.dumps({"selected_issue": None}))
            with self.assertRaisesRegex(ValueError, "no fix candidate"):
                mod.prepare_fix(next(iter(mod.SOURCES)), work)

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
