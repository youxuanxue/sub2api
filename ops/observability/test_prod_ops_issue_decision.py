#!/usr/bin/env python3
from __future__ import annotations

import datetime as dt
import unittest

from ops.observability.prod_ops_issue_decision import decide_issue_action


class ProdOpsIssueDecisionTest(unittest.TestCase):
    NOW = dt.datetime(2026, 7, 23, 8, 30, tzinfo=dt.timezone.utc)

    def test_open_signature_is_updated(self) -> None:
        decision = decide_issue_action(
            [
                {"number": 10, "state": "OPEN", "closedAt": None},
                {"number": 9, "state": "CLOSED", "closedAt": "2026-07-22T08:30:00Z"},
            ],
            now=self.NOW,
        )

        self.assertEqual(decision, {"action": "update", "number": 10})

    def test_recently_closed_signature_is_suppressed(self) -> None:
        decision = decide_issue_action(
            [{"number": 11, "state": "CLOSED", "closedAt": "2026-07-23T08:15:33Z"}],
            now=self.NOW,
        )

        self.assertEqual(decision["action"], "suppress")
        self.assertEqual(decision["number"], 11)
        self.assertEqual(decision["closed_at"], "2026-07-23T08:15:33Z")

    def test_expired_closed_signature_allows_creation(self) -> None:
        decision = decide_issue_action(
            [{"number": 12, "state": "CLOSED", "closedAt": "2026-07-15T08:29:59Z"}],
            now=self.NOW,
        )

        self.assertEqual(decision, {"action": "create"})

    def test_invalid_closed_timestamp_fails_closed(self) -> None:
        decision = decide_issue_action(
            [{"number": 13, "state": "CLOSED", "closedAt": "invalid"}],
            now=self.NOW,
        )

        self.assertEqual(
            decision,
            {"action": "suppress", "number": 13, "closed_at": "unknown"},
        )


if __name__ == "__main__":
    unittest.main()
