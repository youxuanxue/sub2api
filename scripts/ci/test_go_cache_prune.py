#!/usr/bin/env python3
"""Prune plan for the five managed Go cache families."""

from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parent / "go_cache_prune.py"
SPEC = importlib.util.spec_from_file_location("ci_go_cache_prune", SCRIPT)
assert SPEC and SPEC.loader
go_cache_prune = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = go_cache_prune
SPEC.loader.exec_module(go_cache_prune)
BUDGET_BYTES = go_cache_prune.BUDGET_BYTES
FAMILIES = go_cache_prune.FAMILIES
OVERFLOW_DROP_ORDER = go_cache_prune.OVERFLOW_DROP_ORDER
plan_prune = go_cache_prune.plan_prune
family_fits = go_cache_prune.family_fits



def _cache(
    key: str,
    size: int,
    *,
    ref: str = "refs/heads/main",
    cache_id: int = 1,
    created_at: str = "2026-08-29T00:00:00Z",
) -> dict[str, object]:
    return {
        "id": cache_id,
        "key": key,
        "sizeInBytes": size,
        "ref": ref,
        "createdAt": created_at,
    }


class GoCachePruneTest(unittest.TestCase):
    def test_keeps_latest_and_previous_when_under_budget(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-aaa", 100, cache_id=1),
            _cache("Linux-gomod-v1-bbb", 110, cache_id=2),
            _cache("Linux-gobuild-test-v1-aaa", 200, cache_id=3),
            _cache("Linux-gobuild-integration-v1-aaa", 50, cache_id=4),
            _cache("Linux-gobuild-analysis-v1-aaa", 40, cache_id=5),
            _cache("Linux-go-release-v1-aaa", 80, cache_id=6),
        ]
        plan = plan_prune(caches, budget_bytes=10_000)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, ())
        self.assertEqual(set(plan.keep_ids), {1, 2, 3, 4, 5, 6})

    def test_drops_previous_generation_largest_first(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-old", 3, cache_id=1),
            _cache("Linux-gomod-v1-new", 4, cache_id=10),
            _cache("Linux-gobuild-test-v1-old", 5, cache_id=2),
            _cache("Linux-gobuild-test-v1-new", 4, cache_id=11),
            _cache("Linux-gobuild-integration-v1-new", 1, cache_id=12),
            _cache("Linux-gobuild-analysis-v1-new", 1, cache_id=13),
            _cache("Linux-go-release-v1-new", 1, cache_id=14),
        ]
        plan = plan_prune(caches, budget_bytes=15)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, (2,))

    def test_deletes_older_than_previous_generation(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-c", 1, cache_id=3),
            _cache("Linux-gomod-v1-b", 1, cache_id=2),
            _cache("Linux-gomod-v1-a", 1, cache_id=1),
            _cache("Linux-gobuild-test-v1-a", 1, cache_id=4),
            _cache("Linux-gobuild-integration-v1-a", 1, cache_id=5),
            _cache("Linux-gobuild-analysis-v1-a", 1, cache_id=6),
            _cache("Linux-go-release-v1-a", 1, cache_id=7),
        ]
        plan = plan_prune(caches, budget_bytes=100)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, (1,))
        self.assertIn(2, plan.keep_ids)
        self.assertIn(3, plan.keep_ids)

    def test_ignores_non_main_and_unmanaged_prefixes(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-a", 1, cache_id=1),
            _cache("Linux-gobuild-test-v1-a", 1, cache_id=2),
            _cache("Linux-gobuild-integration-v1-a", 1, cache_id=3),
            _cache("Linux-gobuild-analysis-v1-a", 1, cache_id=4),
            _cache("Linux-go-release-v1-a", 1, cache_id=5),
            _cache("Linux-gomod-v1-pr", 999, cache_id=6, ref="refs/pull/1/merge"),
            _cache("Linux-frontend-a", 999, cache_id=7),
            _cache("setup-go-Linux-x64", 999, cache_id=8),
        ]
        plan = plan_prune(caches, budget_bytes=100)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, ())
        self.assertNotIn(6, plan.keep_ids)
        self.assertNotIn(7, plan.keep_ids)
        self.assertNotIn(8, plan.keep_ids)

    def test_legacy_go_cache_keys_are_not_managed(self) -> None:
        caches = [
            _cache("Linux-gomod-dependency-hash", 100, cache_id=1),
            _cache(
                "Linux-gobuild-integration-nodwarf-v2-dependency-2026-08-29",
                100,
                cache_id=2,
            ),
            _cache("Linux-go-release-dependency-33223135749", 100, cache_id=3),
        ]
        plan = plan_prune(caches, budget_bytes=1)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.keep_ids, ())
        self.assertEqual(plan.delete_ids, ())

    def test_generation_order_uses_created_at_not_numeric_id(self) -> None:
        caches = [
            _cache(
                "Linux-gomod-v1-old",
                1,
                cache_id=999,
                created_at="2026-08-27T00:00:00Z",
            ),
            _cache(
                "Linux-gomod-v1-previous",
                1,
                cache_id=2,
                created_at="2026-08-28T00:00:00Z",
            ),
            _cache(
                "Linux-gomod-v1-latest",
                1,
                cache_id=1,
                created_at="2026-08-29T00:00:00Z",
            ),
        ]
        plan = plan_prune(caches, budget_bytes=100)
        self.assertEqual(plan.delete_ids, (999,))
        self.assertEqual(set(plan.keep_ids), {1, 2})

    def test_overflow_drops_lowest_priority_latest_first(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-a", 4, cache_id=1),
            _cache("Linux-gobuild-test-v1-a", 4, cache_id=2),
            _cache("Linux-gobuild-integration-v1-a", 4, cache_id=3),
            _cache("Linux-gobuild-analysis-v1-a", 4, cache_id=4),
            _cache("Linux-go-release-v1-a", 4, cache_id=5),
        ]
        plan = plan_prune(caches, budget_bytes=10)
        self.assertTrue(plan.ok)
        # 20 → drop analysis (4) then release (4) → kept 12 still over 10 →
        # drop integration (4) → kept test+gomod = 8.
        self.assertEqual(set(plan.delete_ids), {3, 4, 5})
        self.assertEqual(set(plan.keep_ids), {1, 2})
        self.assertEqual(set(plan.overflow_delete_ids), {3, 4, 5})
        evidence = " ".join(plan.evidence)
        self.assertTrue(all(family in evidence for family in FAMILIES), plan.evidence)
        self.assertIn("overflow_drop family=analysis", evidence)
        self.assertIn("overflow_drop family=release", evidence)
        self.assertIn("overflow_drop family=integration", evidence)

    def test_overflow_drop_order_prefers_analysis_before_release(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-a", 2, cache_id=1),
            _cache("Linux-gobuild-test-v1-a", 2, cache_id=2),
            _cache("Linux-gobuild-integration-v1-a", 2, cache_id=3),
            _cache("Linux-gobuild-analysis-v1-a", 3, cache_id=4),
            _cache("Linux-go-release-v1-a", 3, cache_id=5),
        ]
        # latest sum=12; dropping analysis alone → 9 ≤ 10.
        plan = plan_prune(caches, budget_bytes=10)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, (4,))
        self.assertEqual(plan.overflow_delete_ids, (4,))
        self.assertEqual(set(plan.keep_ids), {1, 2, 3, 5})
        self.assertEqual(OVERFLOW_DROP_ORDER[0], "analysis")
        self.assertFalse(family_fits(caches, "analysis", budget_bytes=10))
        self.assertTrue(family_fits(caches, "release", budget_bytes=10))
        self.assertFalse(family_fits(caches, "analysis", size=3, budget_bytes=10))
        self.assertTrue(family_fits(caches, "release", size=3, budget_bytes=10))

    def test_fits_with_size_models_pending_save(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-a", 2, cache_id=1),
            _cache("Linux-gobuild-test-v1-a", 2, cache_id=2),
            _cache("Linux-gobuild-integration-v1-a", 2, cache_id=3),
            _cache("Linux-gobuild-analysis-v1-a", 1, cache_id=4),
            _cache("Linux-go-release-v1-a", 2, cache_id=5),
        ]
        # current sum=9 ≤ 10, analysis fits; a pending 4-byte analysis tips over and drops.
        self.assertTrue(family_fits(caches, "analysis", budget_bytes=10))
        self.assertFalse(family_fits(caches, "analysis", size=4, budget_bytes=10))
        self.assertTrue(family_fits(caches, "test", size=2, budget_bytes=10))

    def test_fails_when_single_latest_exceeds_budget(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-a", 1, cache_id=1),
            _cache("Linux-gobuild-test-v1-a", 20, cache_id=2),
            _cache("Linux-gobuild-integration-v1-a", 1, cache_id=3),
            _cache("Linux-gobuild-analysis-v1-a", 1, cache_id=4),
            _cache("Linux-go-release-v1-a", 1, cache_id=5),
        ]
        plan = plan_prune(caches, budget_bytes=10)
        self.assertFalse(plan.ok)
        self.assertEqual(plan.delete_ids, ())
        # test is highest keep priority; after dropping others, test alone still overflows.
        self.assertIn(2, plan.keep_ids)

    def test_candidate_tie_breaks_by_key_ascending(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-zzz", 3, cache_id=2),
            _cache("Linux-gomod-v1-new", 1, cache_id=10),
            _cache("Linux-gobuild-test-v1-aaa", 3, cache_id=4),
            _cache("Linux-gobuild-test-v1-new", 1, cache_id=11),
            _cache("Linux-gobuild-integration-v1-new", 1, cache_id=12),
            _cache("Linux-gobuild-analysis-v1-new", 1, cache_id=13),
            _cache("Linux-go-release-v1-new", 1, cache_id=14),
        ]
        plan = plan_prune(caches, budget_bytes=10)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, (4,))

    def test_default_budget_is_six_gib(self) -> None:
        self.assertEqual(BUDGET_BYTES, 6 * 1024**3)

    @patch("subprocess.check_output", return_value="[]")
    def test_inventory_is_main_scoped_created_at_sorted_and_exhaustive(self, run) -> None:
        go_cache_prune._list_caches()
        args = run.call_args.args[0]
        self.assertIn("--ref", args)
        self.assertIn("refs/heads/main", args)
        self.assertIn("--sort", args)
        self.assertIn("created_at", args)
        self.assertIn("--limit", args)
        limit = int(args[args.index("--limit") + 1])
        self.assertGreaterEqual(limit, 10_000)
        fields = args[args.index("--json") + 1]
        self.assertIn("createdAt", fields)

    def test_check_exits_nonzero_when_overflow_heal_is_required(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-a", 2, cache_id=1),
            _cache("Linux-gobuild-test-v1-a", 2, cache_id=2),
            _cache("Linux-gobuild-integration-v1-a", 2, cache_id=3),
            _cache("Linux-gobuild-analysis-v1-a", 3, cache_id=4),
            _cache("Linux-go-release-v1-a", 3, cache_id=5),
        ]

        def _plan(caches_arg, budget_bytes=go_cache_prune.BUDGET_BYTES):
            del budget_bytes
            return plan_prune(caches_arg, budget_bytes=10)

        def _fits(caches_arg, family, *, size=None, budget_bytes=go_cache_prune.BUDGET_BYTES):
            del budget_bytes
            return family_fits(caches_arg, family, size=size, budget_bytes=10)

        with patch.object(go_cache_prune, "_list_caches", return_value=caches):
            with patch.object(go_cache_prune, "plan_prune", side_effect=_plan):
                self.assertEqual(go_cache_prune.main(["--check"]), 1)
            with patch.object(go_cache_prune, "family_fits", side_effect=_fits):
                self.assertEqual(go_cache_prune.main(["--fits", "analysis"]), 1)
                self.assertEqual(go_cache_prune.main(["--fits", "release"]), 0)


if __name__ == "__main__":
    unittest.main()
