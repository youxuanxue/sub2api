#!/usr/bin/env python3
"""Prune plan for the five managed Go cache families."""

from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import unittest

SCRIPT = Path(__file__).resolve().parent / "go_cache_prune.py"
SPEC = importlib.util.spec_from_file_location("ci_go_cache_prune", SCRIPT)
assert SPEC and SPEC.loader
go_cache_prune = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = go_cache_prune
SPEC.loader.exec_module(go_cache_prune)
BUDGET_BYTES = go_cache_prune.BUDGET_BYTES
FAMILIES = go_cache_prune.FAMILIES
plan_prune = go_cache_prune.plan_prune


def _cache(key: str, size: int, *, ref: str = "refs/heads/main", cache_id: int = 1) -> dict[str, object]:
    return {"id": cache_id, "key": key, "sizeInBytes": size, "ref": ref}


class GoCachePruneTest(unittest.TestCase):
    def test_keeps_latest_and_previous_when_under_budget(self) -> None:
        caches = [
            _cache("Linux-gomod-aaa", 100, cache_id=1),
            _cache("Linux-gomod-bbb", 110, cache_id=2),
            _cache("Linux-gobuild-test-aaa", 200, cache_id=3),
            _cache("Linux-gobuild-integration-aaa", 50, cache_id=4),
            _cache("Linux-gobuild-analysis-aaa", 40, cache_id=5),
            _cache("Linux-go-release-aaa", 80, cache_id=6),
        ]
        plan = plan_prune(caches, budget_bytes=10_000)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, ())
        self.assertEqual(set(plan.keep_ids), {1, 2, 3, 4, 5, 6})

    def test_drops_previous_generation_largest_first(self) -> None:
        caches = [
            _cache("Linux-gomod-old", 3, cache_id=1),
            _cache("Linux-gomod-new", 4, cache_id=10),
            _cache("Linux-gobuild-test-old", 5, cache_id=2),
            _cache("Linux-gobuild-test-new", 4, cache_id=11),
            _cache("Linux-gobuild-integration-new", 1, cache_id=12),
            _cache("Linux-gobuild-analysis-new", 1, cache_id=13),
            _cache("Linux-go-release-new", 1, cache_id=14),
        ]
        plan = plan_prune(caches, budget_bytes=15)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, (2,))

    def test_deletes_older_than_previous_generation(self) -> None:
        caches = [
            _cache("Linux-gomod-c", 1, cache_id=3),
            _cache("Linux-gomod-b", 1, cache_id=2),
            _cache("Linux-gomod-a", 1, cache_id=1),
            _cache("Linux-gobuild-test-a", 1, cache_id=4),
            _cache("Linux-gobuild-integration-a", 1, cache_id=5),
            _cache("Linux-gobuild-analysis-a", 1, cache_id=6),
            _cache("Linux-go-release-a", 1, cache_id=7),
        ]
        plan = plan_prune(caches, budget_bytes=100)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, (1,))
        self.assertIn(2, plan.keep_ids)
        self.assertIn(3, plan.keep_ids)

    def test_ignores_non_main_and_unmanaged_prefixes(self) -> None:
        caches = [
            _cache("Linux-gomod-a", 1, cache_id=1),
            _cache("Linux-gobuild-test-a", 1, cache_id=2),
            _cache("Linux-gobuild-integration-a", 1, cache_id=3),
            _cache("Linux-gobuild-analysis-a", 1, cache_id=4),
            _cache("Linux-go-release-a", 1, cache_id=5),
            _cache("Linux-gomod-pr", 999, cache_id=6, ref="refs/pull/1/merge"),
            _cache("Linux-frontend-a", 999, cache_id=7),
            _cache("setup-go-Linux-x64", 999, cache_id=8),
        ]
        plan = plan_prune(caches, budget_bytes=100)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, ())
        self.assertNotIn(6, plan.keep_ids)
        self.assertNotIn(7, plan.keep_ids)
        self.assertNotIn(8, plan.keep_ids)

    def test_fails_when_latest_generations_exceed_budget(self) -> None:
        caches = [
            _cache("Linux-gomod-a", 4, cache_id=1),
            _cache("Linux-gobuild-test-a", 4, cache_id=2),
            _cache("Linux-gobuild-integration-a", 4, cache_id=3),
            _cache("Linux-gobuild-analysis-a", 4, cache_id=4),
            _cache("Linux-go-release-a", 4, cache_id=5),
        ]
        plan = plan_prune(caches, budget_bytes=10)
        self.assertFalse(plan.ok)
        self.assertEqual(plan.delete_ids, ())
        self.assertEqual(set(plan.keep_ids), {1, 2, 3, 4, 5})
        evidence = " ".join(plan.evidence)
        self.assertTrue(all(family in evidence for family in FAMILIES), plan.evidence)

    def test_candidate_tie_breaks_by_key_ascending(self) -> None:
        caches = [
            _cache("Linux-gomod-zzz", 3, cache_id=2),
            _cache("Linux-gomod-new", 1, cache_id=10),
            _cache("Linux-gobuild-test-aaa", 3, cache_id=4),
            _cache("Linux-gobuild-test-new", 1, cache_id=11),
            _cache("Linux-gobuild-integration-new", 1, cache_id=12),
            _cache("Linux-gobuild-analysis-new", 1, cache_id=13),
            _cache("Linux-go-release-new", 1, cache_id=14),
        ]
        plan = plan_prune(caches, budget_bytes=10)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, (4,))

    def test_default_budget_is_six_gib(self) -> None:
        self.assertEqual(BUDGET_BYTES, 6 * 1024**3)


if __name__ == "__main__":
    unittest.main()
