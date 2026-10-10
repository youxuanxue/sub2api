#!/usr/bin/env python3
"""Wave B compressed-freeze helpers: watermark + SQL/plan for OLTP refresh + dedup delta.

Does not talk to AWS/SSM by itself. Operators (or an SSM wrapper) run the emitted
SQL on frozen AWS / pre-seeded HZ. Default is plan-only.

Intent (see deploy/hetzner/WAVE-B-PROD-CUTOVER-RUNBOOK.md Mode C):
  1. Ahead of freeze: full precious restore of dump A on HZ; capture watermark.
  2. Freeze AWS writes.
  3. C-lite gate: if frozen AWS OLTP counts == HZ pre-seed, skip OLTP dump/restore
     and only append usage_billing_dedup id > watermark; else C-full OLTP refresh.
  4. Reconcile counts; then DNS / Caddy / Edge CIDR.

This avoids DROP DATABASE + replaying ~14M dedup rows inside the freeze window.
"""

from __future__ import annotations

import argparse
import json
import sys
from typing import Any, Literal

# Partition / bulky reconstructible log parents (precious dump already excludes
# their row data). Also skip the large append-only ledger — that uses id watermark.
# SSOT for both SQL list filter and pg_dump --exclude-table-data globs.
BULKY_EXCLUDE_STEMS = (
    "usage_logs",
    "ops_error_logs",
    "ops_system_logs",
    "qa_records",
    "qa_archive",
)
EXCLUDE_TABLE_REGEX = r"^(" + "|".join(BULKY_EXCLUDE_STEMS) + r")"
DEDUP_TABLE = "usage_billing_dedup"

# OLTP cardinality keys used for C-lite admission (must match frozen AWS vs HZ).
# Dedup is allowed to diverge — that is the delta path.
OLTP_GATE_KEYS = (
    "accounts",
    "users",
    "api_keys",
    "groups",
    "settings",
)

# Tables whose row counts must match AWS after delta apply (canary + cutover gate).
RECONCILE_TABLES = (
    "users",
    "accounts",
    "api_keys",
    "groups",
    "settings",
    DEDUP_TABLE,
)

FreezePath = Literal["c_lite", "c_full_oltp"]


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
  'groups', (SELECT count(*) FROM groups), -- ops-allow-soft-deleted
  'settings', (SELECT count(*) FROM settings)
)
FROM {DEDUP_TABLE};
""".strip()


def decide_freeze_path(hz: dict[str, Any], aws_frozen: dict[str, Any]) -> dict[str, Any]:
    """Choose C-lite (dedup-only) vs C-full (OLTP refresh + dedup).

    C-lite requires every OLTP_GATE_KEYS count to match. Dedup may differ.
    Count-only cannot see in-place updates; runbook limits C-lite to fresh C0.
    """
    missing = [k for k in OLTP_GATE_KEYS if k not in hz or k not in aws_frozen]
    mismatches = {
        k: {"hz": hz.get(k), "aws": aws_frozen.get(k)}
        for k in OLTP_GATE_KEYS
        if k not in missing and int(hz[k]) != int(aws_frozen[k])
    }
    if missing:
        path: FreezePath = "c_full_oltp"
        reason = f"missing OLTP gate keys: {', '.join(missing)}"
    elif mismatches:
        path = "c_full_oltp"
        reason = "OLTP gate count mismatch; refresh OLTP then dedup"
    else:
        path = "c_lite"
        reason = "OLTP gate counts match; skip OLTP dump/restore (dedup-only)"
    hz_dedup = int(hz.get("dedup_count", 0))
    aws_dedup = int(aws_frozen.get("dedup_count", hz_dedup))
    hz_max = int(hz.get("dedup_max_id", 0))
    aws_max = int(aws_frozen.get("dedup_max_id", hz_max))
    return {
        "path": path,
        "reason": reason,
        "oltp_gate_keys": list(OLTP_GATE_KEYS),
        "mismatches": mismatches,
        "missing_keys": missing,
        "dedup_delta_rows": max(0, aws_dedup - hz_dedup),
        "dedup_delta_by_max_id": max(0, aws_max - hz_max),
        "skip_oltp_refresh": path == "c_lite",
    }


def pg_dump_exclude_table_data_args() -> list[str]:
    """Flags for Mode C OLTP data-only dump (same stems as EXCLUDE_TABLE_REGEX + dedup)."""
    args = [f"--exclude-table-data={stem}*" for stem in BULKY_EXCLUDE_STEMS]
    args.append(f"--exclude-table-data={DEDUP_TABLE}")
    return args


def truncate_oltp_sql(tables: list[str]) -> str:
    """Truncate OLTP refresh set without CASCADE.

    CASCADE would wipe FK children excluded from Mode C restore (notably
    usage_logs* → users/accounts/api_keys/groups). Use replica role so FKs from
    those excluded tables do not block TRUNCATE; restore puts matching PK ids back.
    """
    if not tables:
        raise ValueError("oltp table list is empty")
    quoted = ", ".join(f'"{t}"' for t in tables)
    return (
        "SET session_replication_role = replica;\n"
        f"TRUNCATE {quoted} RESTART IDENTITY;\n"
        "SET session_replication_role = DEFAULT;"
    )


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
    path: FreezePath = "c_full_oltp",
    oltp_dump_seconds: int | None = None,
    oltp_restore_seconds: int | None = None,
    ops_pad_seconds: int | None = None,
    dedup_rows_per_second: int = 5000,
) -> dict[str, Any]:
    """Rough freeze-window estimate after Mode C pre-seed (not a promise)."""
    if path == "c_lite":
        dump_s = 0 if oltp_dump_seconds is None else oltp_dump_seconds
        restore_s = 0 if oltp_restore_seconds is None else oltp_restore_seconds
        pad = 120 if ops_pad_seconds is None else ops_pad_seconds
        note = (
            "C-lite: dedup-only after OLTP gate match; pad=freeze/reconcile/caddy/dns/cidr; "
            "excludes DNS TTL residual"
        )
    else:
        dump_s = 45 if oltp_dump_seconds is None else oltp_dump_seconds
        restore_s = 60 if oltp_restore_seconds is None else oltp_restore_seconds
        pad = 180 if ops_pad_seconds is None else ops_pad_seconds
        note = (
            "C-full: OLTP dump+restore + dedup; pad=freeze/reconcile/caddy/dns/cidr; "
            "excludes DNS TTL residual"
        )
    dedup_secs = max(5, int(dedup_delta_rows / max(dedup_rows_per_second, 1)))
    data_secs = dump_s + restore_s + dedup_secs
    total = data_secs + pad
    return {
        "path": path,
        "dedup_delta_rows": dedup_delta_rows,
        "oltp_dump_seconds": dump_s,
        "oltp_restore_seconds": restore_s,
        "ops_pad_seconds": pad,
        "db_path_seconds": data_secs,
        "user_visible_seconds_estimate": total,
        "user_visible_minutes_estimate": round(total / 60.0, 1),
        "note": note,
    }


def build_plan(
    watermark: dict[str, Any],
    oltp_tables: list[str],
    *,
    aws_frozen: dict[str, Any] | None = None,
) -> dict[str, Any]:
    wm_id = int(watermark["dedup_max_id"])
    decision = None
    path: FreezePath = "c_full_oltp"
    if aws_frozen is not None:
        decision = decide_freeze_path(watermark, aws_frozen)
        path = decision["path"]  # type: ignore[assignment]
        dedup_delta = int(decision["dedup_delta_rows"])
    else:
        dedup_delta = (
            max(
                0,
                int(watermark.get("aws_dedup_count", watermark["dedup_count"]))
                - int(watermark["dedup_count"]),
            )
            if "aws_dedup_count" in watermark
            else 0
        )
    if path == "c_lite":
        steps = [
            "B0/C0: full precious restore of dump A on HZ (outside freeze); watermark",
            "B1: freeze AWS app containers",
            "C-lite gate: decide-path OLTP counts match → skip OLTP dump/restore",
            "export dedup CSV for id > watermark; HZ apply dedup_delta_apply",
            "reconcile RECONCILE_TABLES counts AWS == HZ",
            "B4–B6: Caddy / DNS / Edge CIDR; start HZ app if needed",
        ]
    else:
        steps = [
            "B0/C0: full precious restore of dump A on HZ (outside freeze); watermark",
            "B1: freeze AWS app containers",
            "C-full: export OLTP data-only from AWS excluding logs + usage_billing_dedup",
            "export dedup CSV for id > watermark",
            "HZ: stop app; TRUNCATE oltp tables; restore OLTP dump; apply dedup delta",
            "reconcile RECONCILE_TABLES counts AWS == HZ",
            "B4–B6: Caddy / DNS / Edge CIDR; start HZ app if needed",
        ]
    return {
        "mode": "wave_b_compress_freeze",
        "path": path,
        "decision": decision,
        "watermark": watermark,
        "oltp_table_count": len(oltp_tables),
        "oltp_tables": oltp_tables,
        "steps": steps,
        "sql": {
            "list_oltp_tables": list_oltp_tables_sql(),
            "watermark": watermark_sql(),
            "truncate_oltp": (
                None
                if path == "c_lite"
                else (truncate_oltp_sql(oltp_tables) if oltp_tables else None)
            ),
            "dedup_delta_copy": dedup_delta_copy_sql(wm_id),
            "dedup_delta_apply": dedup_delta_apply_sql(),
            "reconcile": reconcile_sql(),
        },
        "pg_dump_exclude_table_data": pg_dump_exclude_table_data_args(),
        "estimate": estimate_freeze_minutes(dedup_delta_rows=dedup_delta, path=path),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_wm = sub.add_parser("print-watermark-sql", help="SQL to capture watermark JSON")
    p_wm.set_defaults(fn="watermark_sql")

    p_list = sub.add_parser("print-list-oltp-sql", help="SQL listing OLTP tables to refresh")
    p_list.set_defaults(fn="list_oltp")

    p_plan = sub.add_parser("plan", help="Emit JSON plan from watermark + oltp table list")
    p_plan.add_argument("--watermark-json", required=True, help="JSON object from watermark_sql (HZ)")
    p_plan.add_argument(
        "--oltp-tables",
        required=True,
        help="Comma-separated table names (from list_oltp_tables_sql)",
    )
    p_plan.add_argument(
        "--aws-frozen-json",
        default=None,
        help="Optional frozen-AWS watermark JSON; enables C-lite vs C-full decision",
    )
    p_plan.set_defaults(fn="plan")

    p_est = sub.add_parser("estimate", help="Estimate freeze minutes from dedup delta rows")
    p_est.add_argument("--dedup-delta-rows", type=int, required=True)
    p_est.add_argument(
        "--path",
        choices=("c_lite", "c_full_oltp"),
        default="c_full_oltp",
        help="Freeze path (default c_full_oltp)",
    )
    p_est.set_defaults(fn="estimate")

    p_dec = sub.add_parser(
        "decide-path",
        help="C-lite vs C-full from HZ + frozen-AWS watermark JSON",
    )
    p_dec.add_argument("--hz-json", required=True, help="HZ watermark JSON")
    p_dec.add_argument("--aws-json", required=True, help="Frozen AWS watermark JSON")
    p_dec.set_defaults(fn="decide")

    p_excl = sub.add_parser(
        "print-pg-dump-exclude-args",
        help="Space-separated pg_dump --exclude-table-data flags for Mode C OLTP dump",
    )
    p_excl.set_defaults(fn="pg_dump_excl")

    args = parser.parse_args(argv)

    if args.fn == "watermark_sql":
        print(watermark_sql())
        return 0
    if args.fn == "list_oltp":
        print(list_oltp_tables_sql())
        return 0
    if args.fn == "estimate":
        print(
            json.dumps(
                estimate_freeze_minutes(
                    dedup_delta_rows=args.dedup_delta_rows,
                    path=args.path,
                ),
                indent=2,
            )
        )
        return 0
    if args.fn == "decide":
        hz = json.loads(args.hz_json)
        aws = json.loads(args.aws_json)
        decision = decide_freeze_path(hz, aws)
        decision["estimate"] = estimate_freeze_minutes(
            dedup_delta_rows=int(decision["dedup_delta_rows"]),
            path=decision["path"],
        )
        print(json.dumps(decision, indent=2))
        return 0
    if args.fn == "pg_dump_excl":
        print(" ".join(pg_dump_exclude_table_data_args()))
        return 0
    if args.fn == "plan":
        watermark = json.loads(args.watermark_json)
        tables = [t.strip() for t in args.oltp_tables.split(",") if t.strip()]
        aws_frozen = json.loads(args.aws_frozen_json) if args.aws_frozen_json else None
        print(json.dumps(build_plan(watermark, tables, aws_frozen=aws_frozen), indent=2))
        return 0
    return 2


if __name__ == "__main__":
    sys.exit(main())
