#!/usr/bin/env python3
"""Queue comments must stay clickable without GitHub cross-reference syntax."""
import importlib.util
import re
import unittest
from pathlib import Path

from scripts.test_issue_watchdog_flow import FakeGitHub

SPEC = importlib.util.spec_from_file_location(
    "watchdog_queue_under_test", Path(__file__).with_name("watchdog_queue.py"))
queue = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(queue)

# owner/repo#n, and a github issue/PR URL whose number is not interrupted.
XREF_REF = re.compile(r"[\w.-]+/[\w.-]+#\d+")
XREF_URL = re.compile(r"https://github\.com/[\w.-]+/[\w.-]+/(?:issues|pull)/\d+")
REPO = "Wei-Shaw/sub2api"


def _item(number, kind="issue"):
    return {"number": number, "title": "xref noise", "updated_at": "2026-11-03T00:00:00Z",
            "kind": kind, "state": "open"}


class WatchdogQueueXrefTest(unittest.TestCase):
    def test_new_comment_omits_autolink_forms_and_round_trips_ref(self):
        api = FakeGitHub()
        items = [_item(3023), _item(88, kind="pr")]
        queue.sync_queue(REPO, items, "example/fork", api, day="2026-11-03")
        self.assertEqual(len(api.comments), 1)
        body = api.comments[0]["body"]
        self.assertIsNone(XREF_REF.search(body), body)
        self.assertIsNone(XREF_URL.search(body), body)
        pasted = body.replace("\u200b", "")
        self.assertIn(f"https://github.com/{REPO}/issues/3023", pasted)
        self.assertIn(f"https://github.com/{REPO}/pull/88", pasted)
        self.assertEqual(
            [queue.canonical_ref(row[2]) for row in queue.RECORD.findall(body)],
            [f"{REPO}#3023", f"{REPO}#88"])
        api.calls.clear()
        queue.sync_queue(REPO, items, "example/fork", api, day="2026-11-03")
        self.assertEqual(api.writes, [])

    def test_legacy_watchdog_item_comment_reads_the_same_ref(self):
        api = FakeGitHub()
        item = _item(3023)
        ref = f"{REPO}#3023"
        queue.sync_queue(REPO, [item], "example/fork", api, day="2026-11-03")
        body = api.comments[0]["body"]
        legacy = body.replace(
            f"[{REPO} issues/3023](https://github.com/{REPO}/issues/\u200b3023)",
            f"[{ref}](https://github.com/{REPO}/issues/3023)")
        legacy = legacy.replace(f"watchdog-item:{REPO}/issues/3023", f"watchdog-item:{ref}")
        self.assertIn(f"watchdog-item:{ref} ", legacy)
        self.assertNotIn(f"watchdog-item:{REPO}/issues/3023", legacy)
        parsed = queue.RECORD.findall(legacy)
        self.assertEqual(len(parsed), 1)
        self.assertEqual(queue.canonical_ref(parsed[0][2]), ref)
        api.comments[0]["body"] = legacy
        api.calls.clear()
        queue.sync_queue(REPO, [item], "example/fork", api, day="2026-11-03")
        self.assertEqual(api.writes, [])


if __name__ == "__main__":
    unittest.main()
