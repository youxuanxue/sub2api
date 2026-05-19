#!/usr/bin/env python3
# Verify anthropic OAuth tier baseline values match between the JSON source of
# truth and the SQL apply template's VALUES table. Both files must be edited in
# lockstep when adding/changing a tier; without a mechanical check, the SQL
# comment "Source of truth: ...JSON" is just a prose rule.
#
# Sources:
#   deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json
#   deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
JSON_PATH = REPO_ROOT / "deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json"
SQL_PATH = REPO_ROOT / "deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql"

JSON_ACCOUNT_FIELDS = ("concurrency", "priority")
JSON_EXTRA_INT_FIELDS = (
    "base_rpm",
    "rpm_sticky_buffer",
    "max_sessions",
    "session_idle_timeout_minutes",
    "window_cost_limit",
    "window_cost_sticky_reserve",
)
JSON_EXTRA_BOOL_FIELDS = ("cache_ttl_override_enabled",)
ALL_FIELDS = JSON_ACCOUNT_FIELDS + JSON_EXTRA_INT_FIELDS + JSON_EXTRA_BOOL_FIELDS

# Matches a row of the tier_cfg VALUES block, e.g.
#   ('l3'::text, 3::int, 30::int, 10::int, 6::int, 5::int, 8::int, 480::int, 0::int, true::boolean)
# Column order is fixed by the SQL `AS v(stability_tier, concurrency, priority,
# base_rpm, rpm_sticky_buffer, max_sessions, session_idle_timeout_minutes,
# window_cost_limit, window_cost_sticky_reserve, cache_ttl_override_enabled)` declaration.
_VALUES_ROW_RE = re.compile(
    r"\(\s*'(?P<name>[a-z0-9_]+)'::text\s*,\s*"
    r"(?P<concurrency>\d+)::int\s*,\s*"
    r"(?P<priority>\d+)::int\s*,\s*"
    r"(?P<base_rpm>\d+)::int\s*,\s*"
    r"(?P<rpm_sticky_buffer>\d+)::int\s*,\s*"
    r"(?P<max_sessions>\d+)::int\s*,\s*"
    r"(?P<session_idle_timeout_minutes>\d+)::int\s*,\s*"
    r"(?P<window_cost_limit>\d+)::int\s*,\s*"
    r"(?P<window_cost_sticky_reserve>\d+)::int\s*,\s*"
    r"(?P<cache_ttl_override_enabled>true|false)::boolean\s*\)"
)


def parse_json_tiers() -> dict[str, dict[str, int]]:
    data = json.loads(JSON_PATH.read_text())
    out: dict[str, dict[str, int]] = {}
    for name, cfg in (data.get("tiers") or {}).items():
        baseline = (cfg or {}).get("baseline") or {}
        account = baseline.get("account") or {}
        extra = baseline.get("extra") or {}
        row: dict[str, int] = {}
        for f in JSON_ACCOUNT_FIELDS:
            row[f] = account.get(f)
        for f in JSON_EXTRA_INT_FIELDS:
            row[f] = extra.get(f)
        for f in JSON_EXTRA_BOOL_FIELDS:
            row[f] = extra.get(f, data["shared_baseline"]["extra"].get(f))
        out[name] = row
    return out


def parse_sql_tiers() -> dict[str, dict[str, int]]:
    text = SQL_PATH.read_text()
    out: dict[str, dict[str, int]] = {}
    for m in _VALUES_ROW_RE.finditer(text):
        name = m.group("name")
        row = {f: int(m.group(f)) for f in JSON_ACCOUNT_FIELDS + JSON_EXTRA_INT_FIELDS}
        for f in JSON_EXTRA_BOOL_FIELDS:
            row[f] = m.group(f) == "true"
        out[name] = row
    return out


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="suppress success output")
    args = parser.parse_args()

    json_tiers = parse_json_tiers()
    sql_tiers = parse_sql_tiers()

    json_names = set(json_tiers)
    sql_names = set(sql_tiers)

    errors: list[str] = []
    only_json = sorted(json_names - sql_names)
    only_sql = sorted(sql_names - json_names)
    if only_json:
        errors.append(f"tier(s) in JSON missing from SQL VALUES: {only_json}")
    if only_sql:
        errors.append(f"tier(s) in SQL VALUES missing from JSON: {only_sql}")

    for name in sorted(json_names & sql_names):
        for field in ALL_FIELDS:
            jv = json_tiers[name].get(field)
            sv = sql_tiers[name].get(field)
            if jv != sv:
                errors.append(f"tier {name!r} field {field!r}: JSON={jv!r} SQL={sv!r}")

    if errors:
        print("FAIL: anthropic tier baseline drift between JSON and SQL apply template:")
        for e in errors:
            print(f"  - {e}")
        print(f"  JSON: {JSON_PATH.relative_to(REPO_ROOT)}")
        print(f"  SQL : {SQL_PATH.relative_to(REPO_ROOT)}")
        return 1

    if not args.quiet:
        names = ", ".join(sorted(json_names))
        print(
            f"ok: anthropic tier baseline JSON and SQL VALUES are in sync "
            f"({len(json_names)} tiers: {names})"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
