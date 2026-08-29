#!/usr/bin/env python3
"""Plan GitHub Actions cache deletions for the five managed Go families."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Iterable

BUDGET_BYTES = 6 * 1024**3
DEFAULT_REF = "refs/heads/main"
FAMILIES = ("gomod", "test", "integration", "analysis", "release")
PREFIXES = {
    "gomod": "Linux-gomod-",
    "test": "Linux-gobuild-test-",
    "integration": "Linux-gobuild-integration-",
    "analysis": "Linux-gobuild-analysis-",
    "release": "Linux-go-release-",
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

    latest: list[dict[str, object]] = []
    candidates: list[dict[str, object]] = []
    obsolete: list[dict[str, object]] = []
    evidence: list[str] = []
    for family in FAMILIES:
        entries = sorted(grouped[family], key=lambda item: int(item["id"]), reverse=True)
        if entries:
            latest.append(entries[0])
            evidence.append(f"{family} latest={entries[0]['key']} size={entries[0]['sizeInBytes']}")
        if len(entries) >= 2:
            candidates.append(entries[1])
        obsolete.extend(entries[2:])

    latest_bytes = sum(int(item["sizeInBytes"]) for item in latest)
    if latest_bytes > budget_bytes:
        return PrunePlan(
            ok=False,
            delete_ids=(),
            keep_ids=tuple(int(item["id"]) for item in latest),
            evidence=tuple(evidence),
        )

    remaining = latest_bytes + sum(int(item["sizeInBytes"]) for item in candidates)
    delete = list(obsolete)
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

    keep = latest + keep_candidates
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
        ["gh", "cache", "list", "--limit", "200", "--json", "id,key,sizeInBytes,ref"],
        text=True,
    )
    payload = json.loads(raw)
    if not isinstance(payload, list):
        raise RuntimeError("gh cache list returned a non-list")
    return payload


def main(argv: list[str] | None = None) -> int:
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="plan-only budget check; never deletes caches",
    )
    args = parser.parse_args(argv)
    if not args.check:
        print("go_cache_prune: apply is a separate approval gate", file=__import__("sys").stderr)
        return 2
    plan = plan_prune(_list_caches())
    for line in plan.evidence:
        print(line)
    if not plan.ok:
        print("go_cache_prune: latest generations exceed the 6 GiB budget", file=__import__("sys").stderr)
        return 1
    print(f"go_cache_prune: ok keep={list(plan.keep_ids)} delete_plan={list(plan.delete_ids)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
