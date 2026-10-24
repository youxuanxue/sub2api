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
        self.assertIn("- [x]", api.comments[0]["body"])
        self.assertEqual(api.issues[0]["state"], "closed")

    def test_settled_relay_refs_stay_resolved_without_anchors(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            directory = root / "ops/issue-watchdog"
            directory.mkdir(parents=True)
            settled = directory / "relay-settled.txt"
            settled.write_text("# comment\n\nQuorinex/Kiro-Go#114  # covered\n")
            with patch.object(mod.ledger, "load_ledger", return_value=[]):
                self.assertEqual(mod.resolved_refs(root), {"Quorinex/Kiro-Go#114"})
            settled.write_text("not-a-ref\n")
            with patch.object(mod.ledger, "load_ledger", return_value=[]):
                with self.assertRaisesRegex(ValueError, "expected owner/repo#n"):
                    mod.resolved_refs(root)

    def test_not_applicable_scope_is_required_and_confines_multi_platform_repos(self):
        multi, single = "router-for-me/CLIProxyAPI", "HanaokaYuzu/Gemini-API"
        manifest = {"version": 1, "sources": [
            {"repo": multi, "platforms": ["openai", "google"], "focus": ""},
            {"repo": single, "platforms": ["google"], "focus": ""}]}
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            directory = root / "ops/issue-watchdog"
            directory.mkdir(parents=True)
            (directory / "relays.json").write_text(json.dumps(manifest))
            settled = directory / "relay-settled.txt"

            # A google triage cannot silently settle the openai tracking Issue.
            settled.write_text(f"{multi}#1  # [google] TK不适用：非 Google 平台\n")
            self.assertEqual(mod.settled_refs(root, platform="google"), {f"{multi}#1"})
            self.assertEqual(mod.settled_refs(root, platform="openai"), set())

            # TK已覆盖 is a TokenKey-side fact, so it holds for every platform.
            settled.write_text(f"{multi}#2  # TK已覆盖：TestSomething\n")
            for platform in ("google", "openai"):
                self.assertEqual(mod.settled_refs(root, platform=platform), {f"{multi}#2"})

            # A single-platform repository needs no scope.
            settled.write_text(f"{single}#3  # TK不适用：对方仓库 CI/依赖升级\n")
            self.assertEqual(mod.settled_refs(root, platform="google"), {f"{single}#3"})

            # An untagged not-applicable line for a multi-platform repository fails.
            settled.write_text(f"{multi}#4  # TK不适用：非 Google 平台\n")
            with self.assertRaisesRegex(ValueError, "needs a .platform. scope"):
                mod.settled_refs(root, platform="google")

            # A scope the manifest does not track for that repository fails.
            settled.write_text(f"{single}#5  # [openai] TK不适用：非 Google 平台\n")
            with self.assertRaisesRegex(ValueError, "not tracked by openai"):
                mod.settled_refs(root, platform="google")

            # Duplicates stay rejected even when the scopes differ.
            settled.write_text(f"{multi}#6  # [google] TK不适用：x\n"
                               f"{multi}#6  # [openai] TK不适用：y\n")
            with self.assertRaisesRegex(ValueError, "duplicate"):
                mod.settled_refs(root, platform="google")

    def test_shipped_settled_ledger_scopes_every_tracked_platform(self):
        for platform in ("kiro", "openai", "google", "upstream"):
            self.assertIsInstance(mod.settled_refs(platform=platform), set)
        google = mod.settled_refs(platform="google")
        openai = mod.settled_refs(platform="openai")
        self.assertTrue(google - openai)
        self.assertNotIn("router-for-me/CLIProxyAPI#6622", openai)
        self.assertIn("router-for-me/CLIProxyAPI#6622", google)

    def test_resolutions_use_grouped_refs_and_revoke_missing_anchors(self):
        source = mod.SOURCES["upstream"]
        entry = {"upstream": source["repo"] + "#1, #2", "fixed_if_all_present": ["fixed.py:proof"]}
        with (patch.dict(mod.ledger.SOURCES, {"upstream": source}, clear=True),
              patch.object(mod.ledger, "load_ledger", return_value=[entry]),
              patch.object(mod, "settled_refs", return_value=set()),
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
