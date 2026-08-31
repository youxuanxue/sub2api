#!/usr/bin/env python3
"""Plan (and optionally apply) GitHub Actions cache deletions for managed Go families.

When the five latest generations alone exceed BUDGET_BYTES, drop lowest-priority
family latest entries until the remainder fits. Priority (keep first):
test > gomod > integration > release > analysis.
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


@dataclass(frozen=True)
class PrunePlan:
    ok: bool
    delete_ids: tuple[int, ...]
    keep_ids: tuple[int, ...]
    evidence: tuple[str, ...]


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

    if latest_bytes > budget_bytes:
        keep = list(kept_latest.values()) + overflow_delete
        return PrunePlan(
            ok=False,
            delete_ids=(),
            keep_ids=tuple(int(item["id"]) for item in keep),
            evidence=tuple(evidence),
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
    )


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
        help="plan-only budget check; never deletes caches",
    )
    mode.add_argument(
        "--heal",
        action="store_true",
        help="apply prune plan, including overflow drops of lowest-priority latest caches",
    )
    args = parser.parse_args(argv)
    plan = plan_prune(_list_caches())
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
        print(f"go_cache_prune: ok keep={list(plan.keep_ids)} delete_plan={list(plan.delete_ids)}")
        return 0
    if plan.delete_ids:
        _delete_caches(plan.delete_ids)
    print(f"go_cache_prune: healed keep={list(plan.keep_ids)} deleted={list(plan.delete_ids)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
