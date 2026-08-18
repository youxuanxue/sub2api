#!/usr/bin/env python3
"""Remote host implementation for the operator-driven usage_logs partition cutover."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import re
import subprocess
import sys
from collections.abc import Iterable
from typing import Any


TARGET_RE = re.compile(r"(?:prod|edge:[a-z][a-z0-9]{1,15})")
BOUND_CONSTRAINT = "usage_logs_partition_upper"
BOUND_COMMENT_PREFIX = "tokenkey-usage-partition-upper-v1:"
QUERY_INDEX = "idx_usage_logs_request_api_key_partition"
UPPER_RE = re.compile(r"20[0-9]{2}-[0-9]{2}-[0-9]{2}T00:00:00Z")
KNOWN_INCOMING_FK_TABLES = {"billing_usage_entries"}
BILLING_DEDUP_READY_SQL = """
EXISTS (
  SELECT 1
  FROM pg_index i
  WHERE i.indrelid = to_regclass('public.usage_billing_dedup')
    AND i.indisunique
    AND i.indisvalid
    AND i.indisready
    AND i.indpred IS NULL
    AND i.indexprs IS NULL
    AND i.indnkeyatts = 2
    AND (
      SELECT array_agg(a.attname ORDER BY key_column.ordinality)
      FROM unnest(i.indkey::smallint[]) WITH ORDINALITY
        AS key_column(attnum, ordinality)
      JOIN pg_attribute a
        ON a.attrelid = i.indrelid AND a.attnum = key_column.attnum
      WHERE key_column.ordinality <= i.indnkeyatts
    ) = ARRAY['request_id'::name, 'api_key_id'::name]
)
""".strip()


class UsagePartitionError(RuntimeError):
    """Fail-closed usage partition migration error."""


def _canonical_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True)


def _target(value: str) -> str:
    if TARGET_RE.fullmatch(value) is None:
        raise UsagePartitionError("target must be prod or edge:<id>")
    return value


def _target_token(target: str) -> str:
    return _target(target).replace(":", "-")


def prepare_confirmation(target: str) -> str:
    return f"tokenkey-{_target_token(target)}-usage-daily-prepare-v1"


def cutover_confirmation_prefix(target: str) -> str:
    return f"tokenkey-{_target_token(target)}-usage-daily-cutover-v1:"


def abort_confirmation_prefix(target: str) -> str:
    return f"tokenkey-{_target_token(target)}-usage-daily-abort-v1:"


def _upper(value: str) -> str:
    if not UPPER_RE.fullmatch(value):
        raise UsagePartitionError("partition upper must be a canonical UTC day boundary")
    try:
        dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise UsagePartitionError("partition upper is invalid") from exc
    return value


def _row_count(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise UsagePartitionError("usage partition row count is invalid")
    return value


def _utc_timestamp(value: Any) -> dt.datetime:
    if not isinstance(value, str):
        raise UsagePartitionError("PostgreSQL server clock is invalid")
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise UsagePartitionError("PostgreSQL server clock is invalid") from exc
    if parsed.tzinfo is None:
        raise UsagePartitionError("PostgreSQL server clock is invalid")
    return parsed.astimezone(dt.timezone.utc)


def _psql(sql: str, *, timeout_seconds: int = 360) -> str:
    command = [
        "docker",
        "exec",
        "tokenkey-postgres",
        "psql",
        "-U",
        "tokenkey",
        "-d",
        "tokenkey",
        "-X",
        "-A",
        "-t",
        "-v",
        "ON_ERROR_STOP=1",
        "-c",
        sql,
    ]
    try:
        completed = subprocess.run(
            command,
            capture_output=True,
            text=True,
            timeout=timeout_seconds,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise UsagePartitionError("PostgreSQL migration command could not run") from exc
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "psql failed").strip()
        raise UsagePartitionError(f"PostgreSQL migration command failed: {detail[:600]}")
    return completed.stdout.strip()


def _query_json(sql: str, *, timeout_seconds: int = 360) -> dict[str, Any]:
    output = _psql(sql, timeout_seconds=timeout_seconds)
    try:
        value = json.loads([line for line in output.splitlines() if line.strip()][-1])
    except (IndexError, json.JSONDecodeError) as exc:
        raise UsagePartitionError("PostgreSQL migration probe returned invalid JSON") from exc
    if not isinstance(value, dict):
        raise UsagePartitionError("PostgreSQL migration probe must return a JSON object")
    return value


def status() -> dict[str, Any]:
    return _query_json(
        f"""
SELECT row_to_json(v) FROM (
  SELECT
    now() AS server_clock,
    EXISTS (
      SELECT 1 FROM pg_partitioned_table
      WHERE partrelid = to_regclass('public.usage_logs')
    ) AS partitioned,
    COALESCE((
      SELECT convalidated FROM pg_constraint
      WHERE conrelid = to_regclass('public.usage_logs')
        AND conname = '{BOUND_CONSTRAINT}'
    ), false) AS bound_validated,
    EXISTS (
      SELECT 1 FROM pg_constraint
      WHERE conrelid = to_regclass('public.usage_logs')
        AND conname = '{BOUND_CONSTRAINT}'
    ) AS bound_exists,
    COALESCE((
      SELECT obj_description(oid, 'pg_constraint') LIKE '{BOUND_COMMENT_PREFIX}%'
      FROM pg_constraint
      WHERE conrelid = to_regclass('public.usage_logs')
        AND conname = '{BOUND_CONSTRAINT}'
    ), false) AS bound_operator_owned,
    (
      SELECT substring(
        obj_description(oid, 'pg_constraint')
        FROM length('{BOUND_COMMENT_PREFIX}') + 1
      )
      FROM pg_constraint
      WHERE conrelid = to_regclass('public.usage_logs')
        AND conname = '{BOUND_CONSTRAINT}'
        AND obj_description(oid, 'pg_constraint') LIKE '{BOUND_COMMENT_PREFIX}%'
    ) AS legacy_upper_exclusive,
    (SELECT count(*) FROM usage_logs) AS row_count
) v
"""
    )


def prepare(target: str, confirmation: str) -> dict[str, Any]:
    target = _target(target)
    if confirmation != prepare_confirmation(target):
        raise UsagePartitionError("usage partition prepare confirmation is invalid")
    before = status()
    if before.get("partitioned") is True:
        raise UsagePartitionError("usage_logs is already partitioned")
    if before.get("bound_exists") is True:
        if before.get("bound_operator_owned") is not True:
            raise UsagePartitionError(
                "existing usage_logs partition bound is not operator-owned"
            )
        upper = _upper(str(before.get("legacy_upper_exclusive", "")))
        if _utc_timestamp(before.get("server_clock")) >= _utc_timestamp(upper):
            raise UsagePartitionError(
                "existing usage_logs partition bound has expired; abort is required"
            )
    else:
        upper = _upper(
            _psql(
                "SELECT to_char(date_trunc('day', now() AT TIME ZONE 'UTC') + interval '2 days', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')"
            ).strip()
        )
    inventory = _query_json(
        f"""
SELECT row_to_json(v) FROM (
  SELECT
    ({BILLING_DEDUP_READY_SQL}) AS billing_dedup_ready,
    COALESCE((
      SELECT json_agg(json_build_object('name', ci.relname, 'primary', i.indisprimary))
      FROM pg_index i
      JOIN pg_class ci ON ci.oid = i.indexrelid
      WHERE i.indrelid = to_regclass('public.usage_logs') AND i.indisunique
    ), '[]'::json) AS unique_indexes,
    COALESCE((
      SELECT json_agg(json_build_object('table', c.conrelid::regclass::text, 'name', c.conname))
      FROM pg_constraint c
      WHERE c.contype = 'f' AND c.confrelid = to_regclass('public.usage_logs')
    ), '[]'::json) AS incoming_foreign_keys
) v
"""
    )
    incoming = inventory.get("incoming_foreign_keys")
    if not isinstance(incoming, list) or any(
        not isinstance(item, dict) or item.get("table") not in KNOWN_INCOMING_FK_TABLES
        for item in incoming
    ):
        raise UsagePartitionError("usage_logs has an unapproved incoming foreign key")
    if inventory.get("billing_dedup_ready") is not True:
        raise UsagePartitionError(
            "usage billing dedup unique key is absent or invalid"
        )

    _psql(
        f"CREATE INDEX CONCURRENTLY IF NOT EXISTS {QUERY_INDEX} ON usage_logs (request_id, api_key_id)",
        timeout_seconds=900,
    )
    _psql(
        f"""
DO $$
DECLARE existing oid; existing_comment text;
BEGIN
  SELECT oid, obj_description(oid, 'pg_constraint') INTO existing, existing_comment
  FROM pg_constraint
  WHERE conrelid = to_regclass('public.usage_logs') AND conname = '{BOUND_CONSTRAINT}';
  IF existing IS NOT NULL
    AND existing_comment IS DISTINCT FROM '{BOUND_COMMENT_PREFIX}{upper}' THEN
    RAISE EXCEPTION 'existing usage_logs partition bound is not operator-owned';
  END IF;
  IF existing IS NULL THEN
    ALTER TABLE usage_logs ADD CONSTRAINT {BOUND_CONSTRAINT}
      CHECK (created_at < TIMESTAMPTZ '{upper}') NOT VALID;
  END IF;
END $$;
COMMENT ON CONSTRAINT {BOUND_CONSTRAINT} ON usage_logs
  IS '{BOUND_COMMENT_PREFIX}{upper}';
ALTER TABLE usage_logs VALIDATE CONSTRAINT {BOUND_CONSTRAINT};
"""
    )
    after = status()
    if (
        after.get("bound_validated") is not True
        or after.get("bound_operator_owned") is not True
        or after.get("legacy_upper_exclusive") != upper
    ):
        raise UsagePartitionError("usage_logs fixed upper CHECK was not validated")
    return {
        "mode": "usage_logs_daily_partition_prepare",
        "target": target,
        "legacy_upper_exclusive": upper,
        "row_count_before": _row_count(before.get("row_count")),
        "bound_constraint": BOUND_CONSTRAINT,
        "bound_validated": True,
        "query_index": QUERY_INDEX,
        "inventory": inventory,
        "required_cutover_confirmation": cutover_confirmation_prefix(target) + upper,
        "source_rows_copied": False,
        "deletion_authorized": False,
    }


def abort(target: str, upper: str, confirmation: str) -> dict[str, Any]:
    target = _target(target)
    upper = _upper(upper)
    if confirmation != abort_confirmation_prefix(target) + upper:
        raise UsagePartitionError("usage partition abort confirmation is invalid")
    _psql(
        f"""
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SELECT pg_advisory_xact_lock(hashtext('tokenkey-usage-logs-daily-partition-v1'));

DO $$
DECLARE existing oid; existing_comment text;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = to_regclass('public.usage_logs')) THEN
    RAISE EXCEPTION 'usage_logs is already partitioned';
  END IF;
  SELECT oid, obj_description(oid, 'pg_constraint') INTO existing, existing_comment
  FROM pg_constraint
  WHERE conrelid = to_regclass('public.usage_logs') AND conname = '{BOUND_CONSTRAINT}';
  IF existing IS NULL THEN
    RAISE EXCEPTION 'operator-owned usage_logs partition bound is absent';
  END IF;
  IF existing_comment IS DISTINCT FROM '{BOUND_COMMENT_PREFIX}{upper}' THEN
    RAISE EXCEPTION 'existing usage_logs partition bound is not operator-owned';
  END IF;
END $$;

ALTER TABLE usage_logs DROP CONSTRAINT {BOUND_CONSTRAINT};
COMMIT;
""",
        timeout_seconds=60,
    )
    after = status()
    if after.get("partitioned") is True or after.get("bound_exists") is True:
        raise UsagePartitionError("usage_logs partition prepare abort verification failed")
    return {
        "mode": "usage_logs_daily_partition_abort",
        "target": target,
        "legacy_upper_exclusive": upper,
        "bound_constraint": BOUND_CONSTRAINT,
        "bound_removed": True,
        "source_rows_copied": False,
        "deletion_authorized": False,
    }


def build_cutover_sql(upper: str, minimum_legacy_row_count: int) -> str:
    upper = _upper(upper)
    minimum_legacy_row_count = _row_count(minimum_legacy_row_count)
    return f"""
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
SELECT pg_advisory_xact_lock(hashtext('tokenkey-usage-logs-daily-partition-v1'));
LOCK TABLE usage_logs IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE unexpected text;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = to_regclass('public.usage_logs')) THEN
    RAISE EXCEPTION 'usage_logs is already partitioned';
  END IF;
  IF now() >= TIMESTAMPTZ '{upper}' THEN
    RAISE EXCEPTION 'validated partition upper has expired';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = to_regclass('public.usage_logs')
      AND conname = '{BOUND_CONSTRAINT}'
      AND convalidated
      AND obj_description(oid, 'pg_constraint') = '{BOUND_COMMENT_PREFIX}{upper}'
  ) THEN
    RAISE EXCEPTION 'fixed upper CHECK is absent, stale, or unvalidated';
  END IF;
  IF NOT ({BILLING_DEDUP_READY_SQL}) THEN
    RAISE EXCEPTION 'usage billing dedup unique key is absent or invalid';
  END IF;
  SELECT string_agg(c.conrelid::regclass::text || '.' || c.conname, ',') INTO unexpected
  FROM pg_constraint c
  WHERE c.contype = 'f'
    AND c.confrelid = to_regclass('public.usage_logs')
    AND c.conrelid <> to_regclass('public.billing_usage_entries');
  IF unexpected IS NOT NULL THEN
    RAISE EXCEPTION 'unapproved incoming usage_logs foreign keys: %', unexpected;
  END IF;
END $$;

DO $$
DECLARE item record;
BEGIN
  FOR item IN
    SELECT c.conrelid::regclass::text AS table_name, c.conname
    FROM pg_constraint c
    WHERE c.contype = 'f' AND c.confrelid = to_regclass('public.usage_logs')
  LOOP
    EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', item.table_name, item.conname);
  END LOOP;
END $$;

CREATE TEMP TABLE usage_logs_partition_index_map (
  legacy_index_oid oid PRIMARY KEY,
  parent_name name NOT NULL,
  is_unique boolean NOT NULL
) ON COMMIT DROP;

INSERT INTO usage_logs_partition_index_map (legacy_index_oid, parent_name, is_unique)
SELECT i.indexrelid, ci.relname, i.indisunique
FROM pg_index i
JOIN pg_class ci ON ci.oid = i.indexrelid
WHERE i.indrelid = to_regclass('public.usage_logs');

ALTER TABLE usage_logs RENAME TO usage_logs_legacy;

DO $$
DECLARE item record; renamed text;
BEGIN
  FOR item IN
    SELECT legacy_index_oid, parent_name
    FROM usage_logs_partition_index_map
    ORDER BY parent_name
  LOOP
    renamed := 'usage_logs_legacy_idx_' || item.legacy_index_oid;
    IF to_regclass('public.' || quote_ident(renamed)) IS NOT NULL THEN
      RAISE EXCEPTION 'legacy usage_logs index rename target exists: %', renamed;
    END IF;
    EXECUTE format(
      'ALTER INDEX %s RENAME TO %I', item.legacy_index_oid::regclass, renamed
    );
  END LOOP;
END $$;

CREATE TABLE usage_logs (
  LIKE usage_logs_legacy INCLUDING DEFAULTS INCLUDING STORAGE INCLUDING COMMENTS
) PARTITION BY RANGE (created_at);

DO $$
DECLARE seq_name text;
BEGIN
  seq_name := pg_get_serial_sequence('public.usage_logs_legacy', 'id');
  IF seq_name IS NULL THEN
    RAISE EXCEPTION 'usage_logs id sequence cannot be resolved';
  END IF;
  EXECUTE format('ALTER SEQUENCE %s OWNED BY usage_logs.id', seq_name);
END $$;

DO $$
DECLARE item record; parent_name text; index_def text; using_at integer;
BEGIN
  FOR item IN
    SELECT m.parent_name, pg_get_indexdef(m.legacy_index_oid) AS definition
    FROM usage_logs_partition_index_map m
    WHERE NOT m.is_unique
    ORDER BY m.parent_name
  LOOP
    parent_name := item.parent_name;
    using_at := strpos(item.definition, ' USING ');
    IF using_at = 0 THEN
      RAISE EXCEPTION 'cannot reconstruct usage_logs index %', item.parent_name;
    END IF;
    index_def := format('CREATE INDEX %I ON ONLY usage_logs', parent_name)
      || substring(item.definition FROM using_at);
    EXECUTE index_def;
  END LOOP;
END $$;

DO $$
DECLARE item record; definition text;
BEGIN
  FOR item IN
    SELECT conname, pg_get_constraintdef(oid) AS definition
    FROM pg_constraint
    WHERE conrelid = to_regclass('public.usage_logs_legacy') AND contype = 'f'
    ORDER BY conname
  LOOP
    definition := item.definition;
    EXECUTE format('ALTER TABLE usage_logs ADD CONSTRAINT %I %s', item.conname, definition);
  END LOOP;
END $$;

DO $$
DECLARE item record; definition text;
BEGIN
  FOR item IN
    SELECT conname, pg_get_constraintdef(oid) AS definition
    FROM pg_constraint
    WHERE conrelid = to_regclass('public.usage_logs_legacy')
      AND contype = 'c'
      AND conname <> '{BOUND_CONSTRAINT}'
    ORDER BY conname
  LOOP
    definition := item.definition;
    IF definition NOT LIKE '%NOT VALID%' THEN
      definition := definition || ' NOT VALID';
    END IF;
    EXECUTE format('ALTER TABLE usage_logs ADD CONSTRAINT %I %s', item.conname, definition);
  END LOOP;
END $$;

DO $$
DECLARE item record;
BEGIN
  FOR item IN
    SELECT conname
    FROM pg_constraint
    WHERE conrelid = to_regclass('public.usage_logs_legacy')
      AND contype = 'c'
      AND conname <> '{BOUND_CONSTRAINT}'
    ORDER BY conname
  LOOP
    EXECUTE format('ALTER TABLE usage_logs_legacy DROP CONSTRAINT %I', item.conname);
  END LOOP;
  FOR item IN
    SELECT conname, pg_get_constraintdef(oid) AS definition
    FROM pg_constraint
    WHERE conrelid = to_regclass('public.usage_logs')
      AND contype = 'c'
      AND conname <> '{BOUND_CONSTRAINT}'
    ORDER BY conname
  LOOP
    EXECUTE format(
      'ALTER TABLE usage_logs_legacy ADD CONSTRAINT %I %s',
      item.conname,
      item.definition
    );
  END LOOP;
END $$;

ALTER TABLE usage_logs ATTACH PARTITION usage_logs_legacy
  FOR VALUES FROM (MINVALUE) TO (TIMESTAMPTZ '{upper}');

DO $$
DECLARE start_at timestamptz := TIMESTAMPTZ '{upper}'; day_offset integer;
BEGIN
  FOR day_offset IN 0..7 LOOP
    EXECUTE format(
      'CREATE TABLE usage_logs_%s PARTITION OF usage_logs FOR VALUES FROM (%L) TO (%L)',
      to_char((start_at + day_offset * interval '1 day') AT TIME ZONE 'UTC', 'YYYYMMDD'),
      start_at + day_offset * interval '1 day',
      start_at + (day_offset + 1) * interval '1 day'
    );
  END LOOP;
END $$;

DO $$
BEGIN
  IF (SELECT count(*) FROM ONLY usage_logs_legacy) < {minimum_legacy_row_count} THEN
    RAISE EXCEPTION 'usage_logs legacy row count drifted below prepare receipt';
  END IF;
END $$;
COMMIT;
"""


# The shared ops SQL execution fixture intentionally has no usage_logs schema.
# This generator is executed end-to-end on local PostgreSQL 16, including its
# catalog rewrite, by UsageLogsDailyPartitionPostgresTest instead.
SELF_CHECK_EXEMPT: dict[str, str] = {
    "BILLING_DEDUP_READY_SQL": (
        "executed by ops/migration/test_usage_logs_daily_partition.py::"
        "UsageLogsDailyPartitionPostgresTest during prepare and cutover"
    ),
    "build_cutover_sql": (
        "covered by ops/migration/test_usage_logs_daily_partition.py::"
        "UsageLogsDailyPartitionPostgresTest"
    )
}


def iter_self_check_sql() -> list[tuple[str, str]]:
    return []


def verify(upper: str, minimum_legacy_row_count: int) -> dict[str, Any]:
    upper = _upper(upper)
    minimum_legacy_row_count = _row_count(minimum_legacy_row_count)
    result = _query_json(
        f"""
SELECT row_to_json(v) FROM (
  SELECT
    now() AS server_clock,
    EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = to_regclass('public.usage_logs')) AS partitioned,
    (SELECT count(*) FROM ONLY usage_logs_legacy) AS legacy_row_count,
    (SELECT count(*) FROM usage_logs) AS parent_row_count,
    EXISTS (
      SELECT 1 FROM pg_inherits
      WHERE inhparent = to_regclass('public.usage_logs')
        AND inhrelid = to_regclass('public.usage_logs_legacy')
    ) AS legacy_attached,
    NOT EXISTS (
      SELECT 1
      FROM generate_series(0, 7) AS expected(day_offset)
      WHERE NOT EXISTS (
        SELECT 1
        FROM pg_inherits
        WHERE inhparent = to_regclass('public.usage_logs')
          AND inhrelid = to_regclass(
            'public.usage_logs_' || to_char(
              TIMESTAMPTZ '{upper}' + expected.day_offset * interval '1 day',
              'YYYYMMDD'
            )
          )
      )
    ) AS daily_partitions_attached,
    NOT EXISTS (
      SELECT 1 FROM pg_index i
      WHERE i.indrelid = to_regclass('public.usage_logs') AND i.indisunique
    ) AS no_parent_global_unique,
    NOT EXISTS (
      SELECT 1 FROM pg_constraint c
      WHERE c.contype = 'f' AND c.confrelid = to_regclass('public.usage_logs_legacy')
    ) AS no_incoming_legacy_fk,
    NOT EXISTS (
      SELECT 1
      FROM pg_constraint legacy_constraint
      WHERE legacy_constraint.conrelid = to_regclass('public.usage_logs_legacy')
        AND legacy_constraint.contype IN ('c', 'f')
        AND legacy_constraint.conname <> '{BOUND_CONSTRAINT}'
        AND NOT EXISTS (
          SELECT 1
          FROM pg_constraint parent_constraint
          WHERE parent_constraint.conrelid = to_regclass('public.usage_logs')
            AND parent_constraint.contype = legacy_constraint.contype
            AND parent_constraint.conname = legacy_constraint.conname
        )
    ) AS constraints_preserved
) v
""",
        timeout_seconds=900,
    )
    required = (
        "partitioned",
        "legacy_attached",
        "daily_partitions_attached",
        "no_parent_global_unique",
        "no_incoming_legacy_fk",
        "constraints_preserved",
    )
    if any(result.get(key) is not True for key in required):
        raise UsagePartitionError("usage_logs partition cutover verification failed")
    legacy_row_count = _row_count(result.get("legacy_row_count"))
    parent_row_count = _row_count(result.get("parent_row_count"))
    if legacy_row_count < minimum_legacy_row_count:
        raise UsagePartitionError(
            "usage_logs legacy row count drifted below the prepare receipt"
        )
    if parent_row_count < legacy_row_count:
        raise UsagePartitionError("usage_logs row count drifted below the attached legacy count")
    return result


def cutover(
    target: str, upper: str, minimum_legacy_row_count: int, confirmation: str
) -> dict[str, Any]:
    target = _target(target)
    upper = _upper(upper)
    minimum_legacy_row_count = _row_count(minimum_legacy_row_count)
    if confirmation != cutover_confirmation_prefix(target) + upper:
        raise UsagePartitionError("usage partition cutover confirmation is invalid")
    _psql(
        build_cutover_sql(upper, minimum_legacy_row_count), timeout_seconds=120
    )
    result = verify(upper, minimum_legacy_row_count)
    return {
        "mode": "usage_logs_daily_partition_cutover",
        "target": target,
        "legacy_upper_exclusive": upper,
        "verification": result,
        "source_rows_copied": False,
        "deletion_authorized": False,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    status_parser = commands.add_parser("status")
    status_parser.add_argument("--target", required=True)
    prepare_parser = commands.add_parser("prepare")
    prepare_parser.add_argument("--target", required=True)
    prepare_parser.add_argument("--confirm", required=True)
    abort_parser = commands.add_parser("abort")
    abort_parser.add_argument("--target", required=True)
    abort_parser.add_argument("--legacy-upper-exclusive", required=True)
    abort_parser.add_argument("--confirm", required=True)
    verify_parser = commands.add_parser("verify")
    verify_parser.add_argument("--target", required=True)
    verify_parser.add_argument("--legacy-upper-exclusive", required=True)
    verify_parser.add_argument("--minimum-legacy-row-count", type=int, required=True)
    cutover_parser = commands.add_parser("cutover")
    cutover_parser.add_argument("--target", required=True)
    cutover_parser.add_argument("--legacy-upper-exclusive", required=True)
    cutover_parser.add_argument("--minimum-legacy-row-count", type=int, required=True)
    cutover_parser.add_argument("--confirm", required=True)
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        target = _target(args.target)
        if args.command == "status":
            payload = {
                "mode": "usage_logs_daily_partition_status",
                "target": target,
                **status(),
                "deletion_authorized": False,
            }
        elif args.command == "prepare":
            payload = prepare(target, args.confirm)
        elif args.command == "abort":
            payload = abort(target, args.legacy_upper_exclusive, args.confirm)
        elif args.command == "verify":
            payload = {
                "mode": "usage_logs_daily_partition_verify",
                "target": target,
                **verify(
                    args.legacy_upper_exclusive, args.minimum_legacy_row_count
                ),
                "deletion_authorized": False,
            }
        elif args.command == "cutover":
            payload = cutover(
                target,
                args.legacy_upper_exclusive,
                args.minimum_legacy_row_count,
                args.confirm,
            )
        else:  # pragma: no cover
            raise UsagePartitionError(f"unsupported command: {args.command}")
        print(_canonical_json(payload))
    except UsagePartitionError as exc:
        print(f"usage_logs partition migration refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
