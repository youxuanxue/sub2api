#!/usr/bin/env python3
"""Plan (and optionally apply) GitHub Actions cache deletions for managed Go families.

When the five latest generations alone exceed BUDGET_BYTES, drop lowest-priority
family latest entries until the remainder fits. Priority (keep first):
test > gomod > integration > release > analysis.

`--fits` asks whether a family would remain after that overflow logic (optionally
with a replacement latest size), so warm writers can skip save/warm instead of
uploading a cache that heal would immediately delete.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Iterable

BUDGET_BYTES = 6 * 1024**3
DEFAULT_REF = "refs/heads/main"
FAMILIES = ("gomod", "test", "integration", "analysis", "release")
# Drop first when latest generations overflow the budget.
OVERFLOW_DROP_ORDER = ("analysis", "release", "integration", "gomod", "test")
PREFIXES = {
    "gomod": "Linux-gomod-v1-",
    "test": "Linux-gobuild-test-v1-",
    "integration": "Linux-gobuild-integration-v1-",
    "analysis": "Linux-gobuild-analysis-v1-",
    "release": "Linux-go-release-v1-",
}
_SYNTHETIC_ID = -1
_SYNTHETIC_CREATED = "9999-12-31T23:59:59Z"


@dataclass(frozen=True)
class PrunePlan:
    ok: bool
    delete_ids: tuple[int, ...]
    keep_ids: tuple[int, ...]
    evidence: tuple[str, ...]
    overflow_delete_ids: tuple[int, ...] = ()


def _family_for(key: str) -> str | None:
    for family, prefix in PREFIXES.items():
        if key.startswith(prefix):
            return family
    return None


def plan_prune(
    caches: Iterable[dict[str, object]],
    *,
    budget_bytes: int = BUDGET_BYTES,
) -> PrunePlan:
    grouped: dict[str, list[dict[str, object]]] = {family: [] for family in FAMILIES}
    for cache in caches:
        if cache.get("ref") != DEFAULT_REF:
            continue
        family = _family_for(str(cache["key"]))
        if family is None:
            continue
        grouped[family].append(cache)

    latest_by_family: dict[str, dict[str, object]] = {}
    candidates: list[dict[str, object]] = []
    obsolete: list[dict[str, object]] = []
    evidence: list[str] = []
    for family in FAMILIES:
        entries = sorted(
            grouped[family],
            key=lambda item: (str(item["createdAt"]), int(item["id"])),
            reverse=True,
        )
        if entries:
            latest_by_family[family] = entries[0]
            evidence.append(f"{family} latest={entries[0]['key']} size={entries[0]['sizeInBytes']}")
        if len(entries) >= 2:
            candidates.append(entries[1])
        obsolete.extend(entries[2:])

    kept_latest = dict(latest_by_family)
    overflow_delete: list[dict[str, object]] = []
    latest_bytes = sum(int(item["sizeInBytes"]) for item in kept_latest.values())
    # Never drop the final surviving family: if it alone exceeds the budget the
    # plan is impossible and the warm job must fail closed.
    while latest_bytes > budget_bytes and len(kept_latest) > 1:
        dropped = False
        for family in OVERFLOW_DROP_ORDER:
            item = kept_latest.pop(family, None)
            if item is None:
                continue
            overflow_delete.append(item)
            size = int(item["sizeInBytes"])
            latest_bytes -= size
            evidence.append(
                f"overflow_drop family={family} key={item['key']} size={size}"
            )
            dropped = True
            break
        if not dropped:
            break

    overflow_ids = tuple(int(item["id"]) for item in overflow_delete)
    if latest_bytes > budget_bytes:
        keep = list(kept_latest.values()) + overflow_delete
        return PrunePlan(
            ok=False,
            delete_ids=(),
            keep_ids=tuple(int(item["id"]) for item in keep),
            evidence=tuple(evidence),
            overflow_delete_ids=overflow_ids,
        )

    remaining = latest_bytes + sum(int(item["sizeInBytes"]) for item in candidates)
    delete = list(obsolete) + overflow_delete
    keep_candidates: list[dict[str, object]] = []
    for candidate in sorted(
        candidates,
        key=lambda item: (-int(item["sizeInBytes"]), str(item["key"])),
    ):
        if remaining > budget_bytes:
            delete.append(candidate)
            remaining -= int(candidate["sizeInBytes"])
        else:
            keep_candidates.append(candidate)

    keep = list(kept_latest.values()) + keep_candidates
    return PrunePlan(
        ok=True,
        delete_ids=tuple(int(item["id"]) for item in delete),
        keep_ids=tuple(int(item["id"]) for item in keep),
        evidence=tuple(evidence),
        overflow_delete_ids=overflow_ids,
    )


def family_fits(
    caches: Iterable[dict[str, object]],
    family: str,
    *,
    size: int | None = None,
    budget_bytes: int = BUDGET_BYTES,
) -> bool:
    """Return True when the family's latest would be kept under the budget.

    When ``size`` is set, replace that family's latest with a synthetic entry of
    that size (modeling a pending cache save) before planning.
    """
    if family not in FAMILIES:
        raise ValueError(f"unknown family: {family}")
    items = [dict(cache) for cache in caches]
    if size is not None:
        prefix = PREFIXES[family]
        family_entries = [
            item
            for item in items
            if item.get("ref") == DEFAULT_REF and _family_for(str(item["key"])) == family
        ]
        key = (
            str(family_entries[0]["key"])
            if family_entries
            else f"{prefix}fits-synthetic"
        )
        items = [
            item
            for item in items
            if not (
                item.get("ref") == DEFAULT_REF and _family_for(str(item["key"])) == family
            )
        ]
        # Preserve previous generations as candidates by re-adding all but the
        # former latest (which we replace with the synthetic size).
        if family_entries:
            ordered = sorted(
                family_entries,
                key=lambda item: (str(item["createdAt"]), int(item["id"])),
                reverse=True,
            )
            items.extend(ordered[1:])
        items.append(
            {
                "id": _SYNTHETIC_ID,
                "key": key,
                "sizeInBytes": size,
                "ref": DEFAULT_REF,
                "createdAt": _SYNTHETIC_CREATED,
            }
        )
        plan = plan_prune(items, budget_bytes=budget_bytes)
        return plan.ok and _SYNTHETIC_ID in plan.keep_ids

    plan = plan_prune(items, budget_bytes=budget_bytes)
    if not plan.ok:
        return False
    latest_id = None
    grouped: list[dict[str, object]] = []
    for cache in items:
        if cache.get("ref") != DEFAULT_REF:
            continue
        if _family_for(str(cache["key"])) == family:
            grouped.append(cache)
    if not grouped:
        return True
    latest = sorted(
        grouped,
        key=lambda item: (str(item["createdAt"]), int(item["id"])),
        reverse=True,
    )[0]
    latest_id = int(latest["id"])
    return latest_id in plan.keep_ids


def _list_caches() -> list[dict[str, object]]:
    import json
    import subprocess

    raw = subprocess.check_output(
        [
            "gh",
            "cache",
            "list",
            "--ref",
            DEFAULT_REF,
            "--sort",
            "created_at",
            "--order",
            "desc",
            "--limit",
            "10000",
            "--json",
            "id,key,sizeInBytes,ref,createdAt",
        ],
        text=True,
    )
    payload = json.loads(raw)
    if not isinstance(payload, list):
        raise RuntimeError("gh cache list returned a non-list")
    return payload


def _delete_caches(cache_ids: Iterable[int]) -> None:
    import subprocess

    for cache_id in cache_ids:
        subprocess.check_call(
            ["gh", "cache", "delete", str(cache_id)],
        )


def main(argv: list[str] | None = None) -> int:
    import argparse
    import sys

    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument(
        "--check",
        action="store_true",
        help="plan-only; exit 1 when inventory needs overflow heal or cannot fit",
    )
    mode.add_argument(
        "--heal",
        action="store_true",
        help="apply prune plan, including overflow drops of lowest-priority latest caches",
    )
    mode.add_argument(
        "--fits",
        metavar="FAMILY",
        choices=FAMILIES,
        help="exit 0 when FAMILY's latest would be kept under budget (see --size)",
    )
    parser.add_argument(
        "--size",
        type=int,
        default=None,
        help="with --fits, model a pending save of this many bytes as FAMILY's latest",
    )
    args = parser.parse_args(argv)
    caches = _list_caches()
    if args.fits:
        if args.size is not None and args.size < 0:
            print("go_cache_prune: --size must be >= 0", file=sys.stderr)
            return 2
        ok = family_fits(caches, args.fits, size=args.size)
        print(f"go_cache_prune: fits family={args.fits} size={args.size} ok={ok}")
        return 0 if ok else 1

    plan = plan_prune(caches)
    for line in plan.evidence:
        print(line)
    if not plan.ok:
        print(
            "go_cache_prune: latest generations exceed the 6 GiB budget "
            "even after overflow drops",
            file=sys.stderr,
        )
        return 1
    if args.check:
        if plan.overflow_delete_ids:
            print(
                "go_cache_prune: inventory exceeds budget until overflow heal "
                f"drops ids={list(plan.overflow_delete_ids)}",
                file=sys.stderr,
            )
            print(
                f"go_cache_prune: needs_heal keep={list(plan.keep_ids)} "
                f"delete_plan={list(plan.delete_ids)}"
            )
            return 1
        print(f"go_cache_prune: ok keep={list(plan.keep_ids)} delete_plan={list(plan.delete_ids)}")
        return 0
    if plan.delete_ids:
        _delete_caches(plan.delete_ids)
    print(f"go_cache_prune: healed keep={list(plan.keep_ids)} deleted={list(plan.delete_ids)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
