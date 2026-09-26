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

    def test_discards_oversized_latest_without_sacrificing_other_families(self) -> None:
        caches = [
            _cache("Linux-gomod-v1-a", 1, cache_id=1),
            _cache("Linux-gobuild-test-v1-a", 20, cache_id=2),
            _cache("Linux-gobuild-integration-v1-a", 1, cache_id=3),
            _cache("Linux-gobuild-analysis-v1-a", 1, cache_id=4),
            _cache("Linux-go-release-v1-a", 1, cache_id=5),
        ]
        plan = plan_prune(caches, budget_bytes=10)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, (2,))
        self.assertEqual(set(plan.keep_ids), {1, 3, 4, 5})
        self.assertFalse(family_fits(caches, "test", budget_bytes=10))
        self.assertTrue(family_fits(caches, "test", size=4, budget_bytes=10))

    def test_single_oversized_cache_can_be_evicted_for_cold_rebuild(self) -> None:
        caches = [_cache("Linux-gobuild-test-v1-a", 20, cache_id=2)]
        plan = plan_prune(caches, budget_bytes=10)
        self.assertTrue(plan.ok)
        self.assertEqual(plan.delete_ids, (2,))
        self.assertEqual(plan.keep_ids, ())

    def test_save_budget_measures_paths_and_never_deletes_remote_caches(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as root:
            payload = Path(root) / "entry"
            payload.write_bytes(b"compiled")
            with patch.object(go_cache_prune, "_list_caches", return_value=[]), \
                    patch.object(go_cache_prune, "family_fits", return_value=False) as fits, \
                    patch.object(go_cache_prune, "_delete_caches") as delete:
                self.assertEqual(go_cache_prune.main(["--save-budget", "test", "--path", root]), 0)
                self.assertGreater(fits.call_args.kwargs["size"], payload.stat().st_size)
                delete.assert_not_called()

    def test_save_budget_fails_on_missing_directory_or_inventory_failure(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as root:
            with patch.object(go_cache_prune, "_list_caches", return_value=[]):
                with self.assertRaisesRegex(ValueError, "directory unavailable"):
                    go_cache_prune.main(["--save-budget", "test", "--path", str(Path(root) / "missing")])
        with patch.object(go_cache_prune, "_list_caches", side_effect=OSError("inventory unavailable")):
            with self.assertRaisesRegex(OSError, "inventory unavailable"):
                go_cache_prune.main(["--save-budget", "test"])

    def test_save_budget_fails_on_unreadable_subtree_without_output(self) -> None:
        import contextlib
        import io
        import os
        import tempfile

        with tempfile.TemporaryDirectory() as root:
            denied = Path(root) / "denied"
            denied.mkdir()
            (denied / "large-cache").write_bytes(b"compiled")
            scandir = os.scandir

            def read_directory(path):
                if Path(path) == denied:
                    raise PermissionError("cache subtree unavailable")
                return scandir(path)

            output = io.StringIO()
            with patch.object(go_cache_prune, "_list_caches", return_value=[]), \
                    patch("os.scandir", side_effect=read_directory), \
                    contextlib.redirect_stdout(output):
                with self.assertRaisesRegex(PermissionError, "cache subtree unavailable"):
                    go_cache_prune.main(["--save-budget", "test", "--path", root])
            self.assertEqual(output.getvalue(), "")

    def test_save_budget_counts_nested_files_without_following_symlinks(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as root:
            nested = Path(root) / "nested"
            nested.mkdir()
            (nested / "entry").write_bytes(b"compiled")
            link = nested / "cycle"
            link.symlink_to(Path(root), target_is_directory=True)
            with patch.object(go_cache_prune, "_list_caches", return_value=[]), \
                    patch.object(go_cache_prune, "family_fits", return_value=True) as fits:
                self.assertEqual(go_cache_prune.main(["--save-budget", "test", "--path", root]), 0)
            # The walk still counts nested files once and never follows the
            # symlink cycle; family_fits receives that raw total normalized to
            # the compressed bytes the budget is expressed in.
            raw = 1024 * 1024 + 3 * 1024 + 8 + link.lstat().st_size
            self.assertEqual(
                fits.call_args.kwargs["size"],
                go_cache_prune.estimate_compressed_size(raw),
            )

    def test_heal_actually_deletes_an_oversized_single_snapshot(self) -> None:
        caches = [_cache("Linux-gobuild-test-v1-too-large", BUDGET_BYTES + 1, cache_id=23)]
        with patch.object(go_cache_prune, "_list_caches", return_value=caches), \
                patch.object(go_cache_prune, "_delete_caches") as delete:
            self.assertEqual(go_cache_prune.main(["--heal"]), 0)
            delete.assert_called_once_with((23,))

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

    def test_save_budget_admits_real_main_snapshots(self) -> None:
        # Regression for the unit mismatch that froze all five families.
        # --save-budget can only measure the UNCOMPRESSED tree, but the budget
        # and `gh cache list` are in COMPRESSED bytes. Comparing them directly
        # made every family report fits=False, so warm-release-cache skipped
        # every save for days while still exiting 0, and required CI restored
        # snapshots ~71 backend commits stale.
        #
        # Inventory = real compressed sizeInBytes from main on 2026-09-25;
        # raw = the exact walk totals those runs logged. All five must be
        # admitted: their true compressed total is ~4.16 GiB, well under budget.
        inventory = {
            "test": 1317276175,
            "gomod": 611470268,
            "integration": 1046443064,
            "analysis": 737013033,
            "release": 751774505,
        }
        caches = [
            _cache(f"{go_cache_prune.PREFIXES[family]}main", size, cache_id=index)
            for index, (family, size) in enumerate(inventory.items(), start=1)
        ]
        self.assertLess(sum(inventory.values()), BUDGET_BYTES)

        observed_raw = {
            "test": 6609579398,
            "integration": 4732505521,
            "analysis": 3861668299,
            "release": 4215714343,
        }
        for family, raw in observed_raw.items():
            with self.subTest(family=family):
                self.assertGreater(raw, BUDGET_BYTES * 0.5)  # raw really is huge
                estimated = go_cache_prune.estimate_compressed_size(raw)
                self.assertTrue(
                    go_cache_prune.family_fits(caches, family, size=estimated),
                    f"{family} must be admitted; raw={raw} est={estimated}",
                )
                # Stay conservative: never predict a save smaller than what the
                # registry actually stored for this family.
                self.assertGreaterEqual(estimated, inventory[family])

    def test_save_budget_still_rejects_a_genuinely_oversized_save(self) -> None:
        # The normalization must not defang the gate: a tree whose compressed
        # estimate alone exceeds the budget is still refused.
        caches = [
            _cache(f"{go_cache_prune.PREFIXES[family]}main", 512 * 1024**2, cache_id=index)
            for index, family in enumerate(go_cache_prune.FAMILIES, start=1)
        ]
        huge_raw = int(BUDGET_BYTES * go_cache_prune.SAVE_COMPRESSION_DIVISOR * 2)
        self.assertFalse(
            go_cache_prune.family_fits(
                caches, "release", size=go_cache_prune.estimate_compressed_size(huge_raw)
            )
        )

    def test_audit_staleness_flags_frozen_and_missing_families(self) -> None:
        from datetime import datetime, timedelta, timezone

        now = datetime(2026, 9, 25, 15, 0, tzinfo=timezone.utc)

        def at(hours_ago: float) -> str:
            return (now - timedelta(hours=hours_ago)).strftime("%Y-%m-%dT%H:%M:%SZ")

        fresh = [
            _cache(
                f"{go_cache_prune.PREFIXES[family]}main",
                1024,
                cache_id=index,
                created_at=at(1),
            )
            for index, family in enumerate(go_cache_prune.FAMILIES, start=1)
        ]
        ok, lines = go_cache_prune.audit_staleness(fresh, now=now)
        self.assertTrue(ok)
        self.assertEqual(len(lines), len(go_cache_prune.FAMILIES))
        # Age-limited families report "ok"; gomod reports "present" (exempt).
        self.assertFalse(any("STALE" in line or "MISSING" in line for line in lines))

        # One frozen family fails the audit and is named.
        frozen = list(fresh)
        frozen[-1] = _cache(
            f"{go_cache_prune.PREFIXES['release']}main",
            1024,
            cache_id=99,
            created_at=at(200),
        )
        ok, lines = go_cache_prune.audit_staleness(frozen, now=now)
        self.assertFalse(ok)
        self.assertTrue(any("release" in line and "STALE" in line for line in lines))

        # A family with no snapshot at all is a failure, not a pass.
        ok, lines = go_cache_prune.audit_staleness([], now=now)
        self.assertFalse(ok)
        self.assertEqual(len(lines), len(go_cache_prune.FAMILIES))
        self.assertTrue(all("MISSING" in line for line in lines))

        # Non-main refs must not satisfy a family.
        tag_scoped = [
            {
                "id": 1,
                "key": f"{go_cache_prune.PREFIXES['release']}tag",
                "sizeInBytes": 1024,
                "ref": "refs/tags/v1.0.0",
                "createdAt": at(1),
            }
        ]
        ok, _ = go_cache_prune.audit_staleness(tag_scoped, now=now)
        self.assertFalse(ok)

    def test_audit_staleness_exempts_gomod_from_the_age_limit(self) -> None:
        # gomod's key covers only go.mod / go.sum / .new-api-ref, so a correctly
        # cached module tree hits and is legitimately never re-saved; it ages for
        # weeks while perfectly healthy. Age-limiting it would make the audit a
        # permanent false positive. Presence is still asserted.
        from datetime import datetime, timedelta, timezone

        now = datetime(2026, 9, 25, 15, 0, tzinfo=timezone.utc)

        def at(hours_ago: float) -> str:
            return (now - timedelta(hours=hours_ago)).strftime("%Y-%m-%dT%H:%M:%SZ")

        self.assertIn("gomod", go_cache_prune.AGE_EXEMPT_FAMILIES)
        caches = [
            _cache(
                f"{go_cache_prune.PREFIXES[family]}main",
                1024,
                cache_id=index,
                # Ancient gomod, fresh everything else.
                created_at=at(900 if family == "gomod" else 1),
            )
            for index, family in enumerate(go_cache_prune.FAMILIES, start=1)
        ]
        ok, lines = go_cache_prune.audit_staleness(caches, now=now)
        self.assertTrue(ok, f"gomod age must not fail the audit: {lines}")
        self.assertTrue(any("gomod" in line and "STALE" not in line for line in lines))

        # But a missing gomod snapshot is still a failure.
        without_gomod = [
            cache
            for cache in caches
            if not str(cache["key"]).startswith(go_cache_prune.PREFIXES["gomod"])
        ]
        ok, _ = go_cache_prune.audit_staleness(without_gomod, now=now)
        self.assertFalse(ok)

        # The four source-fingerprinted families are NOT exempt.
        for family in set(go_cache_prune.FAMILIES) - go_cache_prune.AGE_EXEMPT_FAMILIES:
            with self.subTest(family=family):
                aged = [
                    _cache(
                        f"{go_cache_prune.PREFIXES[name]}main",
                        1024,
                        cache_id=index,
                        created_at=at(900 if name == family else 1),
                    )
                    for index, name in enumerate(go_cache_prune.FAMILIES, start=1)
                ]
                ok, lines = go_cache_prune.audit_staleness(aged, now=now)
                self.assertFalse(ok, f"{family} must be age-limited: {lines}")

    def test_audit_staleness_exits_nonzero_from_cli(self) -> None:
        stale = [
            _cache(
                f"{go_cache_prune.PREFIXES[family]}main",
                1024,
                cache_id=index,
                created_at="2020-01-01T00:00:00Z",
            )
            for index, family in enumerate(go_cache_prune.FAMILIES, start=1)
        ]
        with patch.object(go_cache_prune, "_list_caches", return_value=stale), \
                patch.object(go_cache_prune, "_delete_caches") as delete:
            self.assertEqual(go_cache_prune.main(["--audit-staleness"]), 1)
            delete.assert_not_called()

    def test_estimate_compressed_size_contract(self) -> None:
        self.assertEqual(go_cache_prune.estimate_compressed_size(0), 0)
        self.assertEqual(
            go_cache_prune.estimate_compressed_size(3000, divisor=3.0), 1000
        )
        # Pessimistic against the ~4.5x floor measured across all families, so
        # an overestimate skips one upload rather than uploading a doomed save.
        self.assertLessEqual(go_cache_prune.SAVE_COMPRESSION_DIVISOR, 4.5)
        self.assertGreater(go_cache_prune.SAVE_COMPRESSION_DIVISOR, 1.0)
        with self.assertRaises(ValueError):
            go_cache_prune.estimate_compressed_size(-1)
        with self.assertRaises(ValueError):
            go_cache_prune.estimate_compressed_size(1, divisor=0)

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
