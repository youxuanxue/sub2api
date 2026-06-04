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
  deploy/aws/stage0/anthropic-http-mimicry-baselines.json
    == backend/internal/baseline/anthropic-http-mimicry-baselines.json

Third assertion (migration immutability):
  backend/migrations/tk_012_...sql tiers seed == FROZEN_TK012_SEED (its original,
  already-applied values). The seed is the fresh-DB bootstrap projection only; the
  live source of truth for tier values is the JSON, which TierService.ensure-
  SeededFromBaseline UPSERTs into the tiers table on every boot. tk_012 is an
  applied migration and is therefore IMMUTABLE (CLAUDE.md §2 / §5.x) — editing it
  to "re-sync" with a raised JSON is what broke uk1 on 2026-06-03 (boot-time
  checksum-mismatch outage). This guard fails BEFORE release if anyone edits the
  frozen seed again. To raise live baselines, edit the JSON only (never tk_012);
  add a NEW migration if a fresh-DB seed change is truly required.

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
    # HTTP mimicry baseline (UA version + per-model betas) — embedded so the
    # config reconciler self-heals settings.claude_code_user_agent_version /
    # claude_code_http_mimicry_manifest toward it without an operator sync-runtime.
    "anthropic-http-mimicry-baselines.json",
]

# Column order of the tk_012 `INSERT INTO tiers (...)` seed.
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

# FROZEN_TK012_SEED — the original, already-applied tk_012 tiers seed (the values
# present when tk_012 first ran against every existing DB; == git tag v1.7.64).
# tk_012 is an applied migration → IMMUTABLE (CLAUDE.md §2 / §5.x). The seed must
# stay byte-equal to this forever; raising live tier baselines is done via the
# JSON (ensureSeededFromBaseline UPSERTs it on boot), never by editing tk_012.
# Re-syncing the seed to a raised JSON broke uk1 on 2026-06-03 (checksum-mismatch
# boot outage); this constant makes that a preflight/CI failure before release.
FROZEN_TK012_SEED = {
    "l1": {"name": "l1", "concurrency": 4, "priority": 1, "rate_multiplier": 1.0, "base_rpm": 14, "max_sessions": 30, "rpm_sticky_buffer": 5, "session_idle_timeout_minutes": 8, "window_cost_limit": 600, "window_cost_sticky_reserve": 0, "cache_ttl_override_enabled": False, "cache_ttl_override_target": "1h", "tls_profile_name": "tk_canonical_cc_oauth"},
    "l2": {"name": "l2", "concurrency": 6, "priority": 2, "rate_multiplier": 1.0, "base_rpm": 28, "max_sessions": 60, "rpm_sticky_buffer": 10, "session_idle_timeout_minutes": 8, "window_cost_limit": 600, "window_cost_sticky_reserve": 0, "cache_ttl_override_enabled": False, "cache_ttl_override_target": "1h", "tls_profile_name": "tk_canonical_cc_oauth"},
    "l3": {"name": "l3", "concurrency": 8, "priority": 3, "rate_multiplier": 1.0, "base_rpm": 42, "max_sessions": 90, "rpm_sticky_buffer": 15, "session_idle_timeout_minutes": 8, "window_cost_limit": 600, "window_cost_sticky_reserve": 0, "cache_ttl_override_enabled": True, "cache_ttl_override_target": "1h", "tls_profile_name": "tk_canonical_cc_oauth"},
    "l4": {"name": "l4", "concurrency": 10, "priority": 4, "rate_multiplier": 1.0, "base_rpm": 56, "max_sessions": 120, "rpm_sticky_buffer": 20, "session_idle_timeout_minutes": 8, "window_cost_limit": 600, "window_cost_sticky_reserve": 0, "cache_ttl_override_enabled": True, "cache_ttl_override_target": "1h", "tls_profile_name": "tk_canonical_cc_oauth"},
    "l5": {"name": "l5", "concurrency": 12, "priority": 5, "rate_multiplier": 1.0, "base_rpm": 56, "max_sessions": 150, "rpm_sticky_buffer": 20, "session_idle_timeout_minutes": 8, "window_cost_limit": 600, "window_cost_sticky_reserve": 0, "cache_ttl_override_enabled": True, "cache_ttl_override_target": "1h", "tls_profile_name": "tk_canonical_cc_oauth"},
}


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
    """Compute the effective per-tier row from the tier baseline JSON.

    Mirrors backend/internal/baseline/tier.go EffectiveBaselineForTier:
    extra = shared_baseline.extra overlaid with tiers[id].baseline.extra;
    concurrency/priority/rate_multiplier come from tiers[id].baseline.account;
    tls_profile_name comes from shared_baseline.tls_profile.name.

    NOTE: this is the LIVE-tier source of truth (it equals what
    ensureSeededFromBaseline UPSERTs into the tiers table on boot). It is used by
    ops/anthropic/manage-anthropic-config.py (_load_expected_tiers) for live
    tiers-TABLE drift detection — NOT for the frozen tk_012 migration seed, which
    is immutable and guarded against FROZEN_TK012_SEED above.
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


def diff_seed_vs_frozen(
    seed: dict[str, dict], frozen: dict[str, dict]
) -> list[str]:
    """Diff the parsed tk_012 seed against its immutable frozen original."""
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
    missing = set(frozen) - set(seed)
    extra = set(seed) - set(frozen)
    for t in sorted(missing):
        problems.append(f"tier {t}: present in frozen original, missing from tk_012 seed")
    for t in sorted(extra):
        problems.append(f"tier {t}: present in tk_012 seed, not in frozen original")
    for t in sorted(set(seed) & set(frozen)):
        s, e = seed[t], frozen[t]
        for col in SEED_COLUMNS:
            sv, ev = s.get(col), e.get(col)
            equal = _num_eq(sv, ev) if col in NUMERIC else (sv == ev)
            if not equal:
                problems.append(
                    f"tier {t}.{col}: seed={sv!r} != frozen-original={ev!r}"
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

    # Third assertion: tk_012 migration seed == its FROZEN original (immutability).
    seed_problems: list[str] = []
    if not MIGRATION_PATH.is_file():
        fatal(f"file not found: {MIGRATION_PATH.relative_to(REPO_ROOT)}")
    seed = parse_migration_seed(MIGRATION_PATH.read_text(encoding="utf-8"))
    seed_problems = diff_seed_vs_frozen(seed, FROZEN_TK012_SEED)
    seed_ok = not seed_problems
    ok_all = ok_all and seed_ok
    results.append(("tk_012 seed == frozen original (immutable)", seed_ok))

    if args.json:
        print(json.dumps({"ok": ok_all, "pairs": {n: o for n, o in results}, "seed_problems": seed_problems}, indent=2, sort_keys=True))
        return 0 if ok_all else 1

    if ok_all:
        if not args.quiet:
            for name, _ in results[: len(PAIRS)]:
                print(f"OK: embedded {name} matches deploy/aws/stage0 source")
            print("OK: tk_012 migration seed matches its frozen original (immutable)")
        return 0

    if seed_problems:
        print(
            "FAIL: tk_012 tiers seed was EDITED — an applied migration is immutable "
            "(CLAUDE.md §2 / §5.x). Editing it breaks the boot-time migration "
            "checksum guard on every already-migrated DB (uk1 outage, 2026-06-03):",
            file=sys.stderr,
        )
        for p in seed_problems:
            print(f"  {p}", file=sys.stderr)
        print(
            "Fix: restore tk_012_create_tiers_and_account_tier_id.sql to its original "
            "seed (FROZEN_TK012_SEED). To raise LIVE tier baselines, edit ONLY the "
            f"tier baseline JSON ({TIER_BASELINE_NAME}) — TierService.ensureSeeded"
            "FromBaseline UPSERTs it into the tiers table on boot. If a fresh-DB seed "
            "change is genuinely needed, add a NEW migration (never edit tk_012).",
            file=sys.stderr,
        )
        if all(ok for _, ok in results[: len(PAIRS)]):
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
