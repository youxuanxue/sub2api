#!/usr/bin/env python3
"""
check-anthropic-baseline-sync.py — verify that the Anthropic OAuth tier
baseline JSON's documented cooldown ladder stays in sync with the live
constants in `backend/internal/service/ratelimit_service.go`.

The cooldown ladder (30s / 2min / 10min) is owned by Go code — JSON
documents the same values for ops audit so deploy reviewers can read the
baseline without grepping the codebase. The two MUST stay in lockstep,
otherwise the JSON becomes misleading documentation.

Reads:
  - deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json
      policy.cooldown_ladder_seconds  (e.g. [30, 120, 600])
      policy.cooldown_tier_ttl_minutes (e.g. 30)
  - backend/internal/service/ratelimit_service.go
      anthropicCooldownTierLadder    (var [] time.Duration)
      anthropicCooldownTierTTLMinutes (const int)

Exit codes:
  0  — JSON and Go constants agree.
  1  — drift detected (values mismatch).
  2  — file missing, parse failure, or required symbol not found.

Usage:
  python3 scripts/sentinels/check-anthropic-baseline-sync.py
  python3 scripts/sentinels/check-anthropic-baseline-sync.py --quiet  # only print on failure
  python3 scripts/sentinels/check-anthropic-baseline-sync.py --json   # machine-readable

Why this exists:
  Before this guard, the JSON's `temp_unschedulable_rules` carried 429/529
  cooldown durations that conflicted with the Go ladder (PR #337,
  2026-05-21). Operators reading the JSON saw "30 min cooldown on 429"
  but production actually applied 30s/2min/10min via the ladder. The
  rules were removed; the ladder values were promoted into the JSON
  policy block so the JSON is authoritative for ops dashboards. This
  script keeps the two ends honest.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
BASELINE_JSON = (
    REPO_ROOT
    / "deploy"
    / "aws"
    / "stage0"
    / "anthropic-oauth-stability-baselines-tiered.json"
)
RATELIMIT_GO = REPO_ROOT / "backend" / "internal" / "service" / "ratelimit_service.go"


def fatal(msg: str, code: int = 2) -> "None":
    print(f"FATAL: {msg}", file=sys.stderr)
    sys.exit(code)


def load_json_policy() -> dict:
    if not BASELINE_JSON.is_file():
        fatal(f"baseline file not found: {BASELINE_JSON.relative_to(REPO_ROOT)}")
    try:
        with BASELINE_JSON.open("r", encoding="utf-8") as f:
            data = json.load(f)
    except json.JSONDecodeError as exc:
        fatal(f"baseline JSON parse error: {exc}")
    policy = data.get("policy")
    if not isinstance(policy, dict):
        fatal("baseline JSON missing 'policy' object")
    return policy


_LADDER_RE = re.compile(
    r"anthropicCooldownTierLadder\s*=\s*\[\]time\.Duration\s*\{([^}]*)\}",
    re.MULTILINE | re.DOTALL,
)
_TTL_RE = re.compile(
    r"anthropicCooldownTierTTLMinutes\s*=\s*(\d+)",
    re.MULTILINE,
)
_DURATION_TOKEN_RE = re.compile(
    r"(\d+)\s*\*\s*time\.(Second|Minute|Hour)|(\d+)\s*time\.(Second|Minute|Hour)",
)


def _token_to_seconds(value: int, unit: str) -> int:
    if unit == "Second":
        return value
    if unit == "Minute":
        return value * 60
    if unit == "Hour":
        return value * 3600
    fatal(f"unsupported time unit in ladder: {unit}")
    return 0  # unreachable


def parse_go_constants() -> tuple[list[int], int]:
    if not RATELIMIT_GO.is_file():
        fatal(
            f"ratelimit_service.go not found: {RATELIMIT_GO.relative_to(REPO_ROOT)}"
        )
    try:
        text = RATELIMIT_GO.read_text(encoding="utf-8")
    except OSError as exc:
        fatal(f"cannot read ratelimit_service.go: {exc}")

    ladder_match = _LADDER_RE.search(text)
    if not ladder_match:
        fatal("anthropicCooldownTierLadder declaration not found")
    inner = ladder_match.group(1)
    seconds: list[int] = []
    for m in _DURATION_TOKEN_RE.finditer(inner):
        if m.group(1) is not None:
            value = int(m.group(1))
            unit = m.group(2)
        else:
            value = int(m.group(3))
            unit = m.group(4)
        seconds.append(_token_to_seconds(value, unit))
    if not seconds:
        fatal("anthropicCooldownTierLadder body parsed empty")

    ttl_match = _TTL_RE.search(text)
    if not ttl_match:
        fatal("anthropicCooldownTierTTLMinutes constant not found")
    ttl_minutes = int(ttl_match.group(1))

    return seconds, ttl_minutes


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--quiet",
        action="store_true",
        help="suppress success output; only print on failure",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="emit machine-readable JSON report",
    )
    args = parser.parse_args()

    policy = load_json_policy()
    json_ladder = policy.get("cooldown_ladder_seconds")
    json_ttl = policy.get("cooldown_tier_ttl_minutes")
    if not isinstance(json_ladder, list) or not all(
        isinstance(v, int) for v in json_ladder
    ):
        fatal("policy.cooldown_ladder_seconds must be a JSON array of ints")
    if not isinstance(json_ttl, int):
        fatal("policy.cooldown_tier_ttl_minutes must be an int")

    go_ladder, go_ttl = parse_go_constants()

    ladder_ok = list(json_ladder) == go_ladder
    ttl_ok = json_ttl == go_ttl
    ok = ladder_ok and ttl_ok

    if args.json:
        print(
            json.dumps(
                {
                    "ok": ok,
                    "ladder": {
                        "json": json_ladder,
                        "go": go_ladder,
                        "ok": ladder_ok,
                    },
                    "ttl_minutes": {
                        "json": json_ttl,
                        "go": go_ttl,
                        "ok": ttl_ok,
                    },
                },
                indent=2,
                sort_keys=True,
            )
        )
        return 0 if ok else 1

    if ok:
        if not args.quiet:
            print(
                "OK: anthropic-oauth-stability-baselines-tiered.json policy "
                f"in sync with ratelimit_service.go "
                f"(ladder={go_ladder}s, ttl={go_ttl}min)"
            )
        return 0

    print(
        "FAIL: anthropic-oauth-stability-baselines-tiered.json policy DRIFT vs "
        "backend/internal/service/ratelimit_service.go",
        file=sys.stderr,
    )
    if not ladder_ok:
        print(
            f"  cooldown_ladder_seconds: JSON={json_ladder} vs Go={go_ladder}",
            file=sys.stderr,
        )
    if not ttl_ok:
        print(
            f"  cooldown_tier_ttl_minutes: JSON={json_ttl} vs Go={go_ttl}",
            file=sys.stderr,
        )
    print(
        "Fix: update the JSON policy block to match the Go constants "
        "(Go owns the runtime; JSON documents for ops).",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
