#!/usr/bin/env python3
"""Plan the short final freeze for a fresh-host migration.

The formal cutover path deliberately uses a small, reviewable set of operations:
schema check, a refresh of all non-log OLTP tables, and a ledger reconciliation.
This module emits a plan and guarded commands; it never stops the source or edits
DNS. Large-table row fingerprints remain an offline audit tool only.

Freeze SQL owner (do not invent alternatives in ad-hoc cutover scripts):

  ALTER DATABASE tokenkey SET default_transaction_read_only = on;

Never use ALTER SYSTEM for this — it cannot run inside a transaction block and
already aborted a prod freeze window (2026-10-17 Tokyo cutover).
"""
from __future__ import annotations

import argparse
import json
import re
from typing import Any

CORE_OLTP_TABLES = (
    "accounts",
    "users",
    "api_keys",
    "groups",
    "settings",
)
APPEND_ONLY_TABLE = "usage_billing_dedup"
ARCHIVE_TABLE = "usage_billing_dedup_archive"
OLTP_BULKY_EXCLUDE_STEMS = (
    "usage_logs",
    "ops_error_logs",
    "ops_system_logs",
    "qa_records",
    "qa_archive",
)
OLTP_EXCLUDE_REGEX = r"^(" + "|".join(OLTP_BULKY_EXCLUDE_STEMS) + r")"
MAX_FREEZE_SECONDS = 900

# ops-sql-coverage: these SQL generators are transport/destructive fragments
# covered by the plan tests and must not be executed by the generic self-check.
SELF_CHECK_EXEMPT = {
    "append_only_guard_sql": "requires a real cutover watermark",
    "dedup_delta_sql": "COPY transport fragment; covered by plan tests",
    "dedup_full_reload_sql": "destructive candidate-only reload fragment",
}


def _ident(value: str) -> str:
    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", value):
        raise ValueError("unsafe SQL identifier")
    return '"' + value + '"'


def schema_check_sql() -> str:
    # QA raw partitions are intentionally excluded from this migration. Their
    # rolling partition set may differ between source and candidate, while the
    # application schema and precious OLTP schema must still match exactly.
    return """SELECT md5(string_agg(format('%s|%s|%s|%s|%s',table_schema,table_name,column_name,data_type,ordinal_position), E'\\n' ORDER BY table_schema,table_name,ordinal_position)) FROM information_schema.columns WHERE table_schema='public' AND table_name NOT LIKE 'qa_records%';"""


def watermark_sql() -> str:
    return """SELECT json_build_object(
  'dedup_max_id', COALESCE(max(id), 0),
  'dedup_count', count(*),
  'accounts', (SELECT count(*) FROM accounts), -- ops-allow-soft-deleted
  'users', (SELECT count(*) FROM users), -- ops-allow-soft-deleted
  'api_keys', (SELECT count(*) FROM api_keys), -- ops-allow-soft-deleted
  'groups', (SELECT count(*) FROM groups), -- ops-allow-soft-deleted
  'settings', (SELECT count(*) FROM settings)
) FROM usage_billing_dedup;"""


def list_oltp_tables_sql() -> str:
    """List every refreshable non-log table; callers must use this live result."""
    return f"""SELECT c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind='r'
  AND c.relname !~ '{OLTP_EXCLUDE_REGEX}'
  AND c.relname NOT IN ('{APPEND_ONLY_TABLE}', '{ARCHIVE_TABLE}')
ORDER BY c.relname;"""


def append_only_guard_sql(watermark_id: int, expected_prefix_count: int | None = None,
                          expected_archive_count: int | None = None) -> str:
    if watermark_id < 0:
        raise ValueError("watermark must be non-negative")
    expected = ""
    if expected_prefix_count is not None:
        if expected_prefix_count < 0:
            raise ValueError("expected prefix count must be non-negative")
        expected = f",\n  'expected_prefix_count', {expected_prefix_count},\n  'prefix_count_matches', ((SELECT count(*) FROM usage_billing_dedup WHERE id <= {watermark_id}) = {expected_prefix_count})"
    # Archive has no monotonic id; when it is part of the guarded path, require
    # its count to remain unchanged during the freeze as well.
    if expected_archive_count is not None:
        if expected_archive_count < 0:
            raise ValueError("expected archive count must be non-negative")
        expected += f",\n  'expected_archive_count', {expected_archive_count},\n  'archive_count_matches', ((SELECT count(*) FROM usage_billing_dedup_archive) = {expected_archive_count})"
    return f"""SELECT json_build_object(
  'watermark_id', {watermark_id},
  'prefix_count', (SELECT count(*) FROM usage_billing_dedup WHERE id <= {watermark_id}),
  'prefix_max_id', (SELECT COALESCE(max(id), 0) FROM usage_billing_dedup WHERE id <= {watermark_id}),
  'delta_count', (SELECT count(*) FROM usage_billing_dedup WHERE id > {watermark_id}),
  'delta_max_id', (SELECT COALESCE(max(id), 0) FROM usage_billing_dedup WHERE id > {watermark_id}){expected}
);"""


def pg_dump_oltp_args() -> list[str]:
    """Dump all non-log OLTP rows; the live table query defines the scope."""
    return [
        "pg_dump", "--format=custom", "--data-only", "--no-owner", "--no-acl",
        *[f"--exclude-table-data={stem}*" for stem in OLTP_BULKY_EXCLUDE_STEMS],
        f"--exclude-table-data=public.{APPEND_ONLY_TABLE}",
        f"--exclude-table-data=public.{ARCHIVE_TABLE}",
    ]


def pg_dump_dedup_args() -> list[str]:
    """Dump both hot and archived idempotency keys when append-only is false."""
    return [
        "pg_dump", "--format=custom", "--data-only", "--no-owner", "--no-acl",
        f"--table=public.{APPEND_ONLY_TABLE}",
        f"--table=public.{ARCHIVE_TABLE}",
    ]


def dedup_full_reload_sql() -> str:
    # Deliberately no COMMIT: restore and reconcile must share this transaction.
    return f"""BEGIN;
SET LOCAL transaction_timeout='300s';
SET LOCAL lock_timeout='5s';
SET LOCAL session_replication_role=replica;
DELETE FROM public.{ARCHIVE_TABLE};
DELETE FROM public.{APPEND_ONLY_TABLE};"""


def iter_self_check_sql() -> list[tuple[str, str]]:
    """Return safe, read-only SQL used by the generic ops SQL coverage gate."""
    return [
        ("schema_check_sql", schema_check_sql()),
        ("list_oltp_tables_sql", list_oltp_tables_sql()),
        ("watermark_sql", watermark_sql()),
    ]


def pg_dump_core_args() -> list[str]:
    """Compatibility name; never return an unsafe partial OLTP dump."""
    return pg_dump_oltp_args()


def dedup_delta_sql(watermark_id: int) -> str:
    if watermark_id < 0:
        raise ValueError("watermark must be non-negative")
    return f"COPY (SELECT id, request_id, api_key_id, request_fingerprint, created_at FROM usage_billing_dedup WHERE id > {watermark_id} ORDER BY id) TO STDOUT WITH (FORMAT csv, HEADER true);"


def plan(source_schema: str, candidate_schema: str, watermark: dict[str, Any], *, freeze_seconds: int | None = None,
         dedup_strategy: str = "full_refresh", append_only_verified: bool = False) -> dict[str, Any]:
    if source_schema != candidate_schema:
        raise ValueError("schema mismatch: migrate schema before the freeze")
    max_id = int(watermark.get("dedup_max_id", -1))
    if max_id < 0:
        raise ValueError("watermark missing dedup_max_id")
    if freeze_seconds is not None and not 0 < freeze_seconds <= MAX_FREEZE_SECONDS:
        raise ValueError(f"freeze window must be 1..{MAX_FREEZE_SECONDS} seconds")
    if dedup_strategy not in {"full_refresh", "append_only"}:
        raise ValueError("dedup strategy must be full_refresh or append_only")
    if dedup_strategy == "append_only" and not append_only_verified:
        raise ValueError("append_only requires explicit verification evidence")
    expected_prefix_count = watermark.get("candidate_dedup_count")
    expected_archive_count = watermark.get("candidate_archive_count")
    if dedup_strategy == "append_only" and expected_prefix_count is None:
        raise ValueError("append_only requires candidate_dedup_count prefix evidence")
    if dedup_strategy == "append_only" and expected_archive_count is None:
        raise ValueError("append_only requires candidate_archive_count evidence")
    dedup_steps = (
        [
            "verify append-only evidence and old-prefix count/max-id, then append rows above watermark",
        ]
        if dedup_strategy == "append_only"
        else [
            "refresh usage_billing_dedup and usage_billing_dedup_archive from the frozen source dump",
        ]
    )
    return {
        "mode": "standard_freeze_delta",
        "schema_match": True,
        "requested_max_freeze_seconds": freeze_seconds or MAX_FREEZE_SECONDS,
        "runtime_deadline_implemented": False,
        "automatic_fallback_implemented": False,
        "core_oltp_tables": list(CORE_OLTP_TABLES),
        "oltp_refresh_scope": "all_non_log_public_tables",
        "append_only_table": APPEND_ONLY_TABLE,
        "dedup_strategy": dedup_strategy,
        "watermark": watermark,
        "steps": [
            "freeze source gateway, workers and scheduled writers",
            "recheck schema hash and core table counts",
            "full refresh all non-log OLTP tables from frozen source dump",
            *dedup_steps,
            "reconcile core rows, ledger count/max ID and application health",
            "start candidate and perform minimal real-request smoke",
        ],
        "commands": {
            "schema_check": schema_check_sql(),
            "list_oltp_tables": list_oltp_tables_sql(),
            "watermark": watermark_sql(),
            "oltp_dump": pg_dump_oltp_args(),
            "dedup_dump": pg_dump_dedup_args(),
            "dedup_full_reload": dedup_full_reload_sql(),
            "append_only_guard": append_only_guard_sql(max_id, int(expected_prefix_count), int(expected_archive_count)) if dedup_strategy == "append_only" else None,
            "dedup_delta": dedup_delta_sql(max_id) if dedup_strategy == "append_only" else None,
        },
        "freeze_seconds_requested": freeze_seconds,
        "requires_manual_cutover": True,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-schema", required=True)
    parser.add_argument("--candidate-schema", required=True)
    parser.add_argument("--watermark-json", required=True)
    parser.add_argument("--freeze-seconds", type=int)
    parser.add_argument("--dedup-strategy", choices=("full_refresh", "append_only"), default="full_refresh")
    parser.add_argument("--append-only-verified", action="store_true")
    args = parser.parse_args()
    try:
        output = plan(args.source_schema, args.candidate_schema,
                      json.loads(args.watermark_json), freeze_seconds=args.freeze_seconds,
                      dedup_strategy=args.dedup_strategy, append_only_verified=args.append_only_verified)
    except (ValueError, json.JSONDecodeError) as exc:
        print(f"standard freeze plan rejected: {exc}")
        return 1
    print(json.dumps(output, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
