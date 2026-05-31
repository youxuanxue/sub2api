#!/usr/bin/env python3
"""
check-tier-baseline-embed.py — verify the embedded Anthropic tier/stub baselines
under backend/internal/baseline/ stay SEMANTICALLY IDENTICAL to the canonical
sources under deploy/aws/stage0/.

Why this exists (CLAUDE.md §10, memory "anthropic tier baseline 单一源"):
  The backend now embeds the tier baseline (and the stub-pool policy) so the
  in-process ApplyTier action and the per-node config reconciler can derive the
  desired account config WITHOUT an operator laptop / SSM round-trip. `go:embed`
  cannot reach outside the backend Go module, so the JSON had to be COPIED into
  backend/internal/baseline/. A copy is a divergence risk: if someone edits the
  deploy/aws/stage0 source (or the embedded copy) without updating the other, the
  UI/reconciler would silently apply a stale tier baseline while the Python
  fleet pipeline applies the new one. This sentinel makes that drift a hard
  preflight + CI failure instead of a silent split-brain.

Compared pairs (semantic JSON equality — key order / whitespace ignored):
  deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json
    == backend/internal/baseline/anthropic-oauth-stability-baselines-tiered.json
  deploy/aws/stage0/anthropic-stub-pool-baselines.json
    == backend/internal/baseline/anthropic-stub-pool-baselines.json

Exit codes:
  0  — every embedded copy matches its deploy source.
  1  — drift detected (semantic mismatch).
  2  — file missing or parse failure.

Usage:
  python3 scripts/sentinels/check-tier-baseline-embed.py
  python3 scripts/sentinels/check-tier-baseline-embed.py --quiet  # only print on failure
  python3 scripts/sentinels/check-tier-baseline-embed.py --json   # machine-readable
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
DEPLOY_DIR = REPO_ROOT / "deploy" / "aws" / "stage0"
EMBED_DIR = REPO_ROOT / "backend" / "internal" / "baseline"
MIGRATION_PATH = (
    REPO_ROOT / "backend" / "migrations" / "tk_012_create_tiers_and_account_tier_id.sql"
)
TIER_BASELINE_NAME = "anthropic-oauth-stability-baselines-tiered.json"

# (filename, deploy source, embedded copy)
PAIRS = [
    "anthropic-oauth-stability-baselines-tiered.json",
    "anthropic-stub-pool-baselines.json",
]

# Column order of the tk_012 `INSERT INTO tiers (...)` seed. The migration seed
# is the pre-startup bootstrap projection of the git baseline JSON; it MUST equal
# the JSON-derived effective per-tier values (otherwise a fresh DB boots with
# stale tier numbers until ensureSeededFromBaseline runs). Guarding it here makes
# a JSON edit that is not mirrored into the seed a hard failure (plan risk #6).
SEED_COLUMNS = [
    "name",
    "concurrency",
    "priority",
    "rate_multiplier",
    "base_rpm",
    "max_sessions",
    "rpm_sticky_buffer",
    "session_idle_timeout_minutes",
    "window_cost_limit",
    "window_cost_sticky_reserve",
    "cache_ttl_override_enabled",
    "cache_ttl_override_target",
    "tls_profile_name",
]


def fatal(msg: str, code: int = 2) -> "None":
    print(f"FATAL: {msg}", file=sys.stderr)
    sys.exit(code)


def load_json(path: Path) -> object:
    if not path.is_file():
        fatal(f"file not found: {path.relative_to(REPO_ROOT)}")
    try:
        with path.open("r", encoding="utf-8") as f:
            return json.load(f)
    except json.JSONDecodeError as exc:
        fatal(f"JSON parse error in {path.relative_to(REPO_ROOT)}: {exc}")


def canonical(obj: object) -> str:
    # sort_keys collapses key-order differences; separators strip whitespace.
    return json.dumps(obj, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def effective_tiers_from_json(doc: dict) -> dict[str, dict]:
    """Compute the effective per-tier seed row from the tier baseline JSON.

    Mirrors backend/internal/baseline/tier.go EffectiveBaselineForTier:
    extra = shared_baseline.extra overlaid with tiers[id].baseline.extra;
    concurrency/priority/rate_multiplier come from tiers[id].baseline.account;
    tls_profile_name comes from shared_baseline.tls_profile.name.
    """
    shared = doc.get("shared_baseline") or {}
    shared_extra = shared.get("extra") or {}
    tls_name = ((shared.get("tls_profile") or {}).get("name"))
    order = (doc.get("policy") or {}).get("tier_order") or sorted((doc.get("tiers") or {}))
    out: dict[str, dict] = {}
    for tier_id in order:
        tier = (doc.get("tiers") or {}).get(tier_id) or {}
        base = tier.get("baseline") or {}
        account = base.get("account") or {}
        extra = {**shared_extra, **(base.get("extra") or {})}
        out[tier_id] = {
            "name": tier_id,
            "concurrency": account.get("concurrency"),
            "priority": account.get("priority"),
            "rate_multiplier": account.get("rate_multiplier"),
            "base_rpm": extra.get("base_rpm"),
            "max_sessions": extra.get("max_sessions"),
            "rpm_sticky_buffer": extra.get("rpm_sticky_buffer"),
            "session_idle_timeout_minutes": extra.get("session_idle_timeout_minutes"),
            "window_cost_limit": extra.get("window_cost_limit"),
            "window_cost_sticky_reserve": extra.get("window_cost_sticky_reserve"),
            "cache_ttl_override_enabled": extra.get("cache_ttl_override_enabled"),
            "cache_ttl_override_target": extra.get("cache_ttl_override_target"),
            "tls_profile_name": tls_name,
        }
    return out


def _parse_sql_scalar(token: str) -> object:
    token = token.strip()
    if token.lower() == "true":
        return True
    if token.lower() == "false":
        return False
    if token.startswith("'") and token.endswith("'"):
        return token[1:-1]
    # numeric
    if re.fullmatch(r"-?\d+", token):
        return int(token)
    try:
        return float(token)
    except ValueError:
        return token


def parse_migration_seed(text: str) -> dict[str, dict]:
    """Parse the `('lN', ...)` VALUES rows from the tk_012 tiers seed."""
    rows: dict[str, dict] = {}
    for line in text.splitlines():
        stripped = line.strip().rstrip(",")
        if not stripped.startswith("('l") or not stripped.endswith(")"):
            continue
        inner = stripped[1:-1]
        tokens = [_parse_sql_scalar(t) for t in inner.split(",")]
        if len(tokens) != len(SEED_COLUMNS):
            fatal(
                f"tk_012 seed row has {len(tokens)} columns, expected "
                f"{len(SEED_COLUMNS)}: {stripped}"
            )
        row = dict(zip(SEED_COLUMNS, tokens))
        rows[row["name"]] = row
    return rows


def _num_eq(a: object, b: object) -> bool:
    try:
        return float(a) == float(b)
    except (TypeError, ValueError):
        return a == b


def diff_seed_vs_effective(
    seed: dict[str, dict], effective: dict[str, dict]
) -> list[str]:
    problems: list[str] = []
    NUMERIC = {
        "concurrency",
        "priority",
        "rate_multiplier",
        "base_rpm",
        "max_sessions",
        "rpm_sticky_buffer",
        "session_idle_timeout_minutes",
        "window_cost_limit",
        "window_cost_sticky_reserve",
    }
    missing = set(effective) - set(seed)
    extra = set(seed) - set(effective)
    for t in sorted(missing):
        problems.append(f"tier {t}: present in JSON, missing from tk_012 seed")
    for t in sorted(extra):
        problems.append(f"tier {t}: present in tk_012 seed, missing from JSON")
    for t in sorted(set(seed) & set(effective)):
        s, e = seed[t], effective[t]
        for col in SEED_COLUMNS:
            sv, ev = s.get(col), e.get(col)
            equal = _num_eq(sv, ev) if col in NUMERIC else (sv == ev)
            if not equal:
                problems.append(
                    f"tier {t}.{col}: seed={sv!r} != JSON-effective={ev!r}"
                )
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="suppress success output")
    parser.add_argument("--json", action="store_true", help="emit machine-readable JSON report")
    args = parser.parse_args()

    results = []
    ok_all = True
    for name in PAIRS:
        deploy_path = DEPLOY_DIR / name
        embed_path = EMBED_DIR / name
        deploy_obj = load_json(deploy_path)
        embed_obj = load_json(embed_path)
        ok = canonical(deploy_obj) == canonical(embed_obj)
        ok_all = ok_all and ok
        results.append((name, ok))

    # Third assertion: tk_012 migration seed == embedded tier-baseline effective values.
    seed_problems: list[str] = []
    if not MIGRATION_PATH.is_file():
        fatal(f"file not found: {MIGRATION_PATH.relative_to(REPO_ROOT)}")
    embed_tier_doc = load_json(EMBED_DIR / TIER_BASELINE_NAME)
    effective = effective_tiers_from_json(embed_tier_doc)
    seed = parse_migration_seed(MIGRATION_PATH.read_text(encoding="utf-8"))
    seed_problems = diff_seed_vs_effective(seed, effective)
    seed_ok = not seed_problems
    ok_all = ok_all and seed_ok
    results.append(("tk_012 seed == embedded tier-baseline effective", seed_ok))

    if args.json:
        print(json.dumps({"ok": ok_all, "pairs": {n: o for n, o in results}, "seed_problems": seed_problems}, indent=2, sort_keys=True))
        return 0 if ok_all else 1

    if ok_all:
        if not args.quiet:
            for name, _ in results[:2]:
                print(f"OK: embedded {name} matches deploy/aws/stage0 source")
            print("OK: tk_012 migration seed matches embedded tier-baseline effective values")
        return 0

    if seed_problems:
        print(
            "FAIL: tk_012 tiers seed DRIFT vs embedded tier baseline "
            f"({TIER_BASELINE_NAME}):",
            file=sys.stderr,
        )
        for p in seed_problems:
            print(f"  {p}", file=sys.stderr)
        print(
            "Fix: update the INSERT INTO tiers (...) VALUES rows in "
            "backend/migrations/tk_012_create_tiers_and_account_tier_id.sql to match "
            "the JSON-derived effective values (shared_baseline.extra overlaid with "
            "tiers[id].baseline.extra; concurrency/priority/rate_multiplier from "
            "tiers[id].baseline.account).",
            file=sys.stderr,
        )
        if all(ok for _, ok in results[:2]):
            return 1

    print("FAIL: embedded anthropic baseline DRIFT vs deploy/aws/stage0 source", file=sys.stderr)
    for name, ok in results:
        if not ok:
            print(
                f"  {name}: backend/internal/baseline/ != deploy/aws/stage0/",
                file=sys.stderr,
            )
    print(
        "Fix: re-copy the deploy/aws/stage0 source into backend/internal/baseline/ "
        "(they must be byte-for-byte semantically identical). The deploy/aws/stage0 "
        "JSON is the canonical source; the embedded copy exists only because "
        "go:embed cannot reach outside the backend module.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
