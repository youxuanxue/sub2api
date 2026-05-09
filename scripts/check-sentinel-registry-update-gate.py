#!/usr/bin/env python3
"""
check-sentinel-registry-update-gate.py — require sentinel registry updates when
load-bearing TK/NewAPI surfaces change.

This is the hard gate behind the recurring review note: "补充必要的 upstream
merge 覆写防护门禁". Existing sentinel checkers prove that current literals are
still present; this checker proves that PRs changing known hotspot files also
update the relevant registry in the same branch.

Exit codes:
  0 — no hotspot/sentinel-covered source changed, or matching registry changed.
  1 — source changed without a matching sentinel registry update.
  2 — git/registry state is malformed or comparison base cannot be resolved.
"""
from __future__ import annotations

import argparse
import fnmatch
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Iterable

REPO_ROOT = Path(__file__).resolve().parent.parent
REGISTRY_GLOB = "scripts/*-sentinels.json"
DEFAULT_BASE = "origin/main"

# Hotspots that repeatedly need explicit upstream-overwrite guards. Exact files
# already listed in a sentinel registry are also guarded automatically; these
# patterns catch newly introduced or still-unregistered TK/NewAPI surfaces.
HOTSPOT_PATTERNS: dict[str, list[str]] = {
    "frontend/src/components/account/CreateAccountModal.vue": ["scripts/newapi-sentinels.json", "scripts/frontend-tk-sentinels.json"],
    "frontend/src/components/account/EditAccountModal.vue": ["scripts/newapi-sentinels.json", "scripts/frontend-tk-sentinels.json"],
    "frontend/src/components/account/AccountNewApiPlatformFields.vue": ["scripts/newapi-sentinels.json"],
    "frontend/src/components/account/ModelWhitelistSelector.vue": ["scripts/newapi-sentinels.json"],
    "frontend/src/components/account/QuotaLimitCard.vue": ["scripts/frontend-tk-sentinels.json"],
    "frontend/src/components/common/ModelIcon.vue": ["scripts/newapi-sentinels.json"],
    "frontend/src/composables/useTkAccountNewApiPlatform.ts": ["scripts/newapi-sentinels.json"],
    "frontend/src/composables/useModelWhitelist.ts": ["scripts/newapi-sentinels.json"],
    "frontend/src/constants/gatewayPlatforms.ts": ["scripts/newapi-sentinels.json"],
    "frontend/src/composables/usePlatformOptions.ts": ["scripts/newapi-sentinels.json"],
    "backend/internal/integration/newapi/**/*.go": ["scripts/newapi-sentinels.json"],
    "backend/internal/**/*_tk_*.go": ["scripts/newapi-sentinels.json", "scripts/gateway-tk-sentinels.json"],
    "backend/internal/relay/bridge/*.go": ["scripts/newapi-sentinels.json"],
}


def run_git(args: list[str], check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=check,
    )


def resolve_base(explicit_base: str | None) -> str | None:
    base = explicit_base or os.environ.get("SENTINEL_GATE_BASE_REF")
    if not base:
        github_base = os.environ.get("GITHUB_BASE_REF")
        base = f"origin/{github_base}" if github_base else DEFAULT_BASE

    current_branch = run_git(["branch", "--show-current"], check=False).stdout.strip()
    if not explicit_base and not os.environ.get("SENTINEL_GATE_BASE_REF") and not os.environ.get("GITHUB_BASE_REF"):
        if current_branch in {"main", "master"}:
            return None

    if run_git(["rev-parse", "--verify", f"{base}^{{commit}}"], check=False).returncode == 0:
        return base

    # CI checkout may only have refs/remotes/origin/main after fetch-depth:0;
    # local checkouts may have main but not origin/main if remote setup is unusual.
    fallback = "main" if base == DEFAULT_BASE else None
    if fallback and run_git(["rev-parse", "--verify", f"{fallback}^{{commit}}"], check=False).returncode == 0:
        return fallback

    print(
        f"FATAL: cannot resolve comparison base `{base}`. Fetch origin/main or set SENTINEL_GATE_BASE_REF.",
        file=sys.stderr,
    )
    return "__UNRESOLVED__"


def changed_files(base: str, head: str) -> set[str]:
    proc = run_git(["diff", "--name-only", "--diff-filter=ACMRTUXB", f"{base}...{head}"])
    return {line.strip() for line in proc.stdout.splitlines() if line.strip()}


def registry_paths() -> list[Path]:
    return sorted(REPO_ROOT.glob(REGISTRY_GLOB))


def load_standard_sentinels(path: Path) -> list[dict]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"FATAL: invalid JSON in {path.relative_to(REPO_ROOT)}: {exc}")
    sentinels = data.get("sentinels")
    if not isinstance(sentinels, list):
        return []
    return [entry for entry in sentinels if isinstance(entry, dict)]


def covered_paths_by_registry() -> dict[str, set[str]]:
    out: dict[str, set[str]] = {}
    for registry in registry_paths():
        rel_registry = registry.relative_to(REPO_ROOT).as_posix()
        sentinels = load_standard_sentinels(registry)
        if not sentinels:
            continue
        out.setdefault(rel_registry, set())
        for entry in sentinels:
            if isinstance(entry.get("path"), str):
                out[rel_registry].add(entry["path"])
    return out


def matching_hotspot_registries(path: str) -> set[str]:
    registries: set[str] = set()
    for pattern, suggested in HOTSPOT_PATTERNS.items():
        if fnmatch.fnmatch(path, pattern):
            registries.update(suggested)
    return registries


def compact(items: Iterable[str]) -> str:
    values = sorted(set(items))
    return ", ".join(values) if values else "(none)"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", help="git ref to compare against (default: GITHUB_BASE_REF or origin/main)")
    parser.add_argument("--head", default="HEAD", help="git ref to compare as the change head (default: HEAD)")
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    args = parser.parse_args()

    base = resolve_base(args.base)
    if base is None:
        if not args.quiet:
            print("sentinel registry update gate: skip on main/master without explicit base")
        return 0
    if base == "__UNRESOLVED__":
        return 2

    if run_git(["rev-parse", "--verify", f"{args.head}^{{commit}}"], check=False).returncode != 0:
        print(f"FATAL: cannot resolve head `{args.head}`", file=sys.stderr)
        return 2

    changed = changed_files(base, args.head)
    if not changed:
        if not args.quiet:
            print(f"sentinel registry update gate: no changes vs {base}...{args.head}")
        return 0

    coverage = covered_paths_by_registry()
    changed_registries = {p for p in changed if fnmatch.fnmatch(p, REGISTRY_GLOB)}
    violations: list[tuple[str, set[str], set[str]]] = []

    for path in sorted(changed):
        if fnmatch.fnmatch(path, REGISTRY_GLOB):
            continue
        related = {registry for registry, paths in coverage.items() if path in paths}
        related.update(matching_hotspot_registries(path))
        if not related:
            continue
        if changed_registries.isdisjoint(related):
            violations.append((path, related, changed_registries))

    if not violations:
        if not args.quiet:
            print(
                "sentinel registry update gate: ok "
                f"(changed registries: {compact(changed_registries)})"
            )
        return 0

    print("sentinel registry update gate: FAIL", file=sys.stderr)
    print(
        "  Load-bearing TK/NewAPI surfaces changed without updating the matching sentinel registry.",
        file=sys.stderr,
    )
    for path, related, seen in violations:
        print(f"  - {path}", file=sys.stderr)
        print(f"      update one of: {compact(related)}", file=sys.stderr)
        print(f"      changed registries in this diff: {compact(seen)}", file=sys.stderr)
    print(
        "  Fix: add/adjust the relevant sentinel literal+rationale in the same PR. "
        "If the change is genuinely not load-bearing, record that decision by updating the registry rationale rather than relying on review memory.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
