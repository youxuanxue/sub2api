#!/usr/bin/env python3
"""Migrate prod Grok edge relay stubs from legacy newapi transport identity.

Default mode is dry-run: print candidate accounts/groups and the SQL that would
run. Apply and rollback require explicit subcommands so this tool is safe to run
from an agent session.

Expected connection:
  DATABASE_URL=postgres://... python ops/grok/migrate-grok-relay-stubs.py dry-run
  DATABASE_URL=postgres://... python ops/grok/migrate-grok-relay-stubs.py apply --yes
  DATABASE_URL=postgres://... python ops/grok/migrate-grok-relay-stubs.py rollback --snapshot-file snapshot.json --yes
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any


def die(msg: str) -> None:
    print(f"[migrate-grok-relay-stubs] error: {msg}", file=sys.stderr)
    sys.exit(1)


def database_url() -> str:
    dsn = os.environ.get("DATABASE_URL", "").strip()
    if not dsn:
        die("DATABASE_URL is required")
    return dsn


def psql(sql: str) -> str:
    proc = subprocess.run(
        ["psql", database_url(), "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1"],
        input=sql,
        text=True,
        capture_output=True,
        check=False,
    )
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr)
        die("psql command failed")
    return proc.stdout


CANDIDATE_SQL = r"""
WITH candidate_accounts AS (
  SELECT
    a.id,
    a.name,
    a.platform,
    a.type,
    a.channel_type,
    a.credentials,
    COALESCE(jsonb_agg(DISTINCT jsonb_build_object(
      'id', g.id,
      'name', g.name,
      'platform', g.platform
    )) FILTER (WHERE g.id IS NOT NULL), '[]'::jsonb) AS groups
  FROM accounts a
  LEFT JOIN account_groups ag ON ag.account_id = a.id
  LEFT JOIN groups g ON g.id = ag.group_id AND g.deleted_at IS NULL
  WHERE a.deleted_at IS NULL
    AND a.platform = 'newapi'
    AND a.type = 'apikey'
    AND COALESCE(a.credentials->>'base_url', '') ~ '^https://api-[a-z0-9]+\.tokenkey\.dev/?$'
    AND (
      lower(a.name) LIKE 'grok-%'
      OR COALESCE(a.credentials->>'mirror_platform', '') = 'grok'
      OR EXISTS (
        SELECT 1
        FROM account_groups ag2
        JOIN groups g2 ON g2.id = ag2.group_id AND g2.deleted_at IS NULL
        WHERE ag2.account_id = a.id
          AND (g2.platform = 'grok' OR lower(g2.name) LIKE '%grok%')
      )
    )
  GROUP BY a.id, a.name, a.platform, a.type, a.channel_type, a.credentials
)
SELECT COALESCE(jsonb_agg(to_jsonb(candidate_accounts) ORDER BY id), '[]'::jsonb)
FROM candidate_accounts;
"""


def load_candidates() -> list[dict[str, Any]]:
    raw = psql(CANDIDATE_SQL).strip()
    if not raw:
        return []
    return json.loads(raw)


def account_ids(candidates: list[dict[str, Any]]) -> list[int]:
    return [int(c["id"]) for c in candidates]


def group_ids(candidates: list[dict[str, Any]]) -> list[int]:
    ids: set[int] = set()
    for c in candidates:
        for g in c.get("groups") or []:
            if (g.get("platform") == "newapi") and ("grok" in (g.get("name") or "").lower()):
                ids.add(int(g["id"]))
    return sorted(ids)


def sql_text(value: Any) -> str:
    return "'" + str(value).replace("'", "''") + "'"


def apply_sql(candidates: list[dict[str, Any]]) -> str:
    aids = ",".join(str(i) for i in account_ids(candidates)) or "NULL"
    gids = ",".join(str(i) for i in group_ids(candidates)) or "NULL"
    return f"""
BEGIN;

UPDATE accounts
SET
  platform = 'grok',
  channel_type = 0,
  credentials = jsonb_set(
    COALESCE(credentials, '{{}}'::jsonb),
    '{{mirror_platform}}',
    to_jsonb('grok'::text),
    true
  ),
  updated_at = NOW()
WHERE id IN ({aids})
  AND platform = 'newapi'
  AND type = 'apikey'
  AND COALESCE(credentials->>'base_url', '') ~ '^https://api-[a-z0-9]+\\.tokenkey\\.dev/?$';

UPDATE groups
SET platform = 'grok', updated_at = NOW()
WHERE id IN ({gids})
  AND platform = 'newapi';

INSERT INTO scheduler_outbox (event_type, payload, created_at)
VALUES ('full_rebuild', '{{"reason":"grok-relay-first-class-platform"}}'::jsonb, NOW());

COMMIT;
"""


def rollback_sql(snapshot: list[dict[str, Any]]) -> str:
    values = []
    group_values = []
    for c in snapshot:
        values.append(
            f"({int(c['id'])}, {sql_text(c['platform'])}::text, {int(c.get('channel_type') or 0)})"
        )
        for g in c.get("groups") or []:
            group_values.append(
                f"({int(g['id'])}, {sql_text(g['platform'])}::text)"
            )
    account_values = ",\n    ".join(values) or "(NULL::bigint, NULL::text, NULL::int)"
    group_values_sql = ",\n    ".join(group_values) or "(NULL::bigint, NULL::text)"
    return f"""
BEGIN;

WITH prev(id, platform, channel_type) AS (
  VALUES
    {account_values}
)
UPDATE accounts a
SET platform = prev.platform,
    channel_type = prev.channel_type,
    updated_at = NOW()
FROM prev
WHERE a.id = prev.id;

WITH prev(id, platform) AS (
  VALUES
    {group_values_sql}
)
UPDATE groups g
SET platform = prev.platform,
    updated_at = NOW()
FROM prev
WHERE g.id = prev.id;

INSERT INTO scheduler_outbox (event_type, payload, created_at)
VALUES ('full_rebuild', '{{"reason":"grok-relay-first-class-platform-rollback"}}'::jsonb, NOW());

COMMIT;
	"""


SELF_CHECK_EXEMPT: dict[str, str] = {}


def iter_self_check_sql() -> list[tuple[str, str]]:
    sample = [{
        "id": 9001,
        "platform": "newapi",
        "channel_type": 1,
        "groups": [{"id": 9011, "platform": "newapi", "name": "grok"}],
    }]
    return [
        ("CANDIDATE_SQL", CANDIDATE_SQL),
        ("apply_sql", apply_sql(sample)),
        ("rollback_sql", rollback_sql(sample)),
    ]


def print_summary(candidates: list[dict[str, Any]]) -> None:
    safe = []
    for c in candidates:
        creds = c.get("credentials") or {}
        safe.append({
            "id": c["id"],
            "name": c["name"],
            "platform": c["platform"],
            "type": c["type"],
            "channel_type": c.get("channel_type"),
            "base_url": creds.get("base_url"),
            "groups": c.get("groups") or [],
        })
    print(json.dumps(safe, ensure_ascii=False, indent=2))


def cmd_dry_run(args: argparse.Namespace) -> None:
    candidates = load_candidates()
    print_summary(candidates)
    print("\n-- apply SQL --")
    print(apply_sql(candidates))
    if args.snapshot_file:
        Path(args.snapshot_file).write_text(json.dumps(candidates, ensure_ascii=False, indent=2), encoding="utf-8")
        print(f"\nWrote snapshot: {args.snapshot_file}", file=sys.stderr)


def cmd_apply(args: argparse.Namespace) -> None:
    if not args.yes:
        die("apply requires --yes")
    candidates = load_candidates()
    if not candidates:
        die("no candidates found")
    if args.snapshot_file:
        Path(args.snapshot_file).write_text(json.dumps(candidates, ensure_ascii=False, indent=2), encoding="utf-8")
    psql(apply_sql(candidates))
    print(f"applied {len(candidates)} account migrations")


def cmd_rollback(args: argparse.Namespace) -> None:
    if not args.yes:
        die("rollback requires --yes")
    snapshot = json.loads(Path(args.snapshot_file).read_text(encoding="utf-8"))
    psql(rollback_sql(snapshot))
    print(f"rolled back {len(snapshot)} account migrations")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)

    dry = sub.add_parser("dry-run")
    dry.add_argument("--snapshot-file", help="optional path to write rollback snapshot JSON")
    dry.set_defaults(func=cmd_dry_run)

    apply = sub.add_parser("apply")
    apply.add_argument("--snapshot-file", required=True, help="write rollback snapshot before applying")
    apply.add_argument("--yes", action="store_true")
    apply.set_defaults(func=cmd_apply)

    rollback = sub.add_parser("rollback")
    rollback.add_argument("--snapshot-file", required=True)
    rollback.add_argument("--yes", action="store_true")
    rollback.set_defaults(func=cmd_rollback)

    args = parser.parse_args()
    args.func(args)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
