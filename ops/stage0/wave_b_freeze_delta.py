#!/usr/bin/env python3
"""Wave B compressed-freeze helpers: watermark + SQL/plan for OLTP refresh + dedup delta.

Does not talk to AWS/SSM by itself. Operators (or an SSM wrapper) run the emitted
SQL on frozen AWS / pre-seeded HZ. Default is plan-only.

Intent (see deploy/hetzner/WAVE-B-PROD-CUTOVER-RUNBOOK.md Mode C):
  1. Ahead of freeze: full precious restore of dump A on HZ; capture watermark.
  2. Freeze AWS writes.
  3. Refresh small/medium OLTP tables from frozen AWS; append usage_billing_dedup
     rows with id > watermark (ON CONFLICT DO NOTHING).
  4. Reconcile counts; then DNS / Caddy / Edge CIDR.

This avoids DROP DATABASE + replaying ~14M dedup rows inside the freeze window.
"""

from __future__ import annotations

import argparse
import json
import sys
from typing import Any

# Partition / bulky reconstructible log parents (precious dump already excludes
# their row data). Also skip the large append-only ledger — that uses id watermark.
EXCLUDE_TABLE_REGEX = (
    r"^(usage_logs|ops_error_logs|ops_system_logs|qa_records|qa_archive)"
)
DEDUP_TABLE = "usage_billing_dedup"

# Tables whose row counts must match AWS after delta apply (canary + cutover gate).
RECONCILE_TABLES = (
    "users",
    "accounts",
    "api_keys",
    "groups",
    "settings",
    DEDUP_TABLE,
)


def list_oltp_tables_sql() -> str:
    return f"""
SELECT c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind = 'r'
  AND c.relname !~ '{EXCLUDE_TABLE_REGEX}'
  AND c.relname <> '{DEDUP_TABLE}'
ORDER BY 1;
""".strip()


def watermark_sql() -> str:
    # Cutover watermarks compare full table cardinality to the precious dump
    # (includes soft-deleted rows). ops-allow-soft-deleted: intentional.
    return f"""
SELECT json_build_object(
  'dedup_max_id', COALESCE(max(id), 0),
  'dedup_count', count(*),
  'accounts', (SELECT count(*) FROM accounts), -- ops-allow-soft-deleted
  'users', (SELECT count(*) FROM users), -- ops-allow-soft-deleted
  'api_keys', (SELECT count(*) FROM api_keys), -- ops-allow-soft-deleted
  'settings', (SELECT count(*) FROM settings)
)
FROM {DEDUP_TABLE};
""".strip()


def truncate_oltp_sql(tables: list[str]) -> str:
    if not tables:
        raise ValueError("oltp table list is empty")
    quoted = ", ".join(f'"{t}"' for t in tables)
    return f"TRUNCATE {quoted} RESTART IDENTITY CASCADE;"


def dedup_delta_copy_sql(watermark_id: int) -> str:
    if watermark_id < 0:
        raise ValueError("watermark_id must be >= 0")
    return (
        f"COPY (SELECT id, request_id, api_key_id, request_fingerprint, created_at "
        f"FROM {DEDUP_TABLE} WHERE id > {int(watermark_id)} ORDER BY id) TO STDOUT WITH (FORMAT csv, HEADER true);"
    )


def dedup_delta_apply_sql() -> str:
    """Target-side: load CSV from stdin into a temp table then insert-missing."""
    return f"""
CREATE TEMP TABLE _wave_b_dedup_delta (
  id bigint,
  request_id varchar(255),
  api_key_id bigint,
  request_fingerprint varchar(64),
  created_at timestamptz
);
COPY _wave_b_dedup_delta FROM STDIN WITH (FORMAT csv, HEADER true);
INSERT INTO {DEDUP_TABLE} (id, request_id, api_key_id, request_fingerprint, created_at)
SELECT id, request_id, api_key_id, request_fingerprint, created_at
FROM _wave_b_dedup_delta
ON CONFLICT (request_id, api_key_id) DO NOTHING;
SELECT setval(
  pg_get_serial_sequence('{DEDUP_TABLE}', 'id'),
  GREATEST(
    (SELECT COALESCE(max(id), 1) FROM {DEDUP_TABLE}),
    (SELECT COALESCE(max(id), 1) FROM _wave_b_dedup_delta)
  )
);
""".strip()


def reconcile_sql() -> str:
    # Full-table counts vs frozen AWS (precious includes soft-deleted).
    # ops-allow-soft-deleted: intentional for cutover parity.
    parts = [
        f"'{t}', (SELECT count(*) FROM {t}) -- ops-allow-soft-deleted"
        if t in {"users", "accounts", "api_keys", "groups"}
        else f"'{t}', (SELECT count(*) FROM {t})"
        for t in RECONCILE_TABLES
    ]
    return "SELECT json_build_object(\n  " + ",\n  ".join(parts) + "\n);"


# ops-sql-coverage: every *_sql symbol must be enumerated or exempted.
SELF_CHECK_EXEMPT: dict[str, str] = {
    "truncate_oltp_sql": "destructive TRUNCATE; requires live OLTP table list from list_oltp_tables_sql",
    "dedup_delta_copy_sql": "COPY TO STDOUT transport; covered by unit tests",
    "dedup_delta_apply_sql": "COPY FROM STDIN transport; covered by unit tests",
}


def iter_self_check_sql() -> list[tuple[str, str]]:
    """(label, rendered_sql) for the ops-sql-coverage real-Postgres self-check."""
    return [
        ("list_oltp_tables_sql", list_oltp_tables_sql()),
        ("watermark_sql", watermark_sql()),
        ("reconcile_sql", reconcile_sql()),
    ]


def estimate_freeze_minutes(
    *,
    dedup_delta_rows: int,
    oltp_dump_seconds: int = 45,
    oltp_restore_seconds: int = 60,
    dedup_rows_per_second: int = 5000,
) -> dict[str, Any]:
    """Rough freeze-window estimate after Mode C pre-seed (not a promise)."""
    dedup_secs = max(5, int(dedup_delta_rows / max(dedup_rows_per_second, 1)))
    data_secs = oltp_dump_seconds + oltp_restore_seconds + dedup_secs
    # freeze stop + reconcile + caddy/dns/cidr outside pure DB path
    total = data_secs + 180
    return {
        "dedup_delta_rows": dedup_delta_rows,
        "db_path_seconds": data_secs,
        "user_visible_seconds_estimate": total,
        "user_visible_minutes_estimate": round(total / 60.0, 1),
        "note": "assumes pre-seeded HZ + frozen AWS; excludes DNS TTL residual",
    }


def build_plan(watermark: dict[str, Any], oltp_tables: list[str]) -> dict[str, Any]:
    wm_id = int(watermark["dedup_max_id"])
    return {
        "mode": "wave_b_compress_freeze",
        "watermark": watermark,
        "oltp_table_count": len(oltp_tables),
        "oltp_tables": oltp_tables,
        "steps": [
            "B0: full precious restore of dump A on HZ (outside freeze)",
            "capture watermark on HZ (and optionally AWS) via watermark_sql",
            "B1: freeze AWS app containers",
            "export OLTP data-only from AWS excluding logs + usage_billing_dedup",
            "export dedup CSV for id > watermark",
            "HZ: stop app; TRUNCATE oltp tables; restore OLTP dump; apply dedup delta",
            "reconcile RECONCILE_TABLES counts AWS == HZ",
            "B4–B6: Caddy / DNS / Edge CIDR; start HZ app if needed",
        ],
        "sql": {
            "list_oltp_tables": list_oltp_tables_sql(),
            "watermark": watermark_sql(),
            "truncate_oltp": truncate_oltp_sql(oltp_tables) if oltp_tables else None,
            "dedup_delta_copy": dedup_delta_copy_sql(wm_id),
            "dedup_delta_apply": dedup_delta_apply_sql(),
            "reconcile": reconcile_sql(),
        },
        "estimate": estimate_freeze_minutes(
            dedup_delta_rows=max(0, int(watermark.get("aws_dedup_count", watermark["dedup_count"])) - int(watermark["dedup_count"]))
            if "aws_dedup_count" in watermark
            else 0
        ),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_wm = sub.add_parser("print-watermark-sql", help="SQL to capture watermark JSON")
    p_wm.set_defaults(fn="watermark_sql")

    p_list = sub.add_parser("print-list-oltp-sql", help="SQL listing OLTP tables to refresh")
    p_list.set_defaults(fn="list_oltp")

    p_plan = sub.add_parser("plan", help="Emit JSON plan from watermark + oltp table list")
    p_plan.add_argument("--watermark-json", required=True, help="JSON object from watermark_sql")
    p_plan.add_argument(
        "--oltp-tables",
        required=True,
        help="Comma-separated table names (from list_oltp_tables_sql)",
    )
    p_plan.set_defaults(fn="plan")

    p_est = sub.add_parser("estimate", help="Estimate freeze minutes from dedup delta rows")
    p_est.add_argument("--dedup-delta-rows", type=int, required=True)
    p_est.set_defaults(fn="estimate")

    args = parser.parse_args(argv)

    if args.fn == "watermark_sql":
        print(watermark_sql())
        return 0
    if args.fn == "list_oltp":
        print(list_oltp_tables_sql())
        return 0
    if args.fn == "estimate":
        print(json.dumps(estimate_freeze_minutes(dedup_delta_rows=args.dedup_delta_rows), indent=2))
        return 0
    if args.fn == "plan":
        watermark = json.loads(args.watermark_json)
        tables = [t.strip() for t in args.oltp_tables.split(",") if t.strip()]
        print(json.dumps(build_plan(watermark, tables), indent=2))
        return 0
    return 2


if __name__ == "__main__":
    sys.exit(main())
