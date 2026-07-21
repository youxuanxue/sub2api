#!/usr/bin/env python3
"""Analyze one TokenKey edge from read-only retained log evidence."""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import json
import re
import subprocess
import sys
from collections import defaultdict
from typing import Any, Iterable, Iterator, Sequence


SCHEMA_VERSION = 2
MAX_INTERVAL_MS = 86_400_000
DEFAULT_DAYS = 60
DEFAULT_MIN_SECONDS = 60
SETTLEMENT_LAG_SECONDS = 300


class CapacityReportError(RuntimeError):
    """Raised when collection or rendering cannot produce trustworthy output."""


def _copy_rows(sql: str) -> Iterator[dict[str, str]]:
    cmd = [
        "docker",
        "exec",
        "tokenkey-postgres",
        "psql",
        "-U",
        "tokenkey",
        "-d",
        "tokenkey",
        "-X",
        "-q",
        "-v",
        "ON_ERROR_STOP=1",
        "-c",
        "COPY (" + sql + ") TO STDOUT WITH (FORMAT CSV, HEADER TRUE)",
    ]
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    assert proc.stdout is not None
    for row in csv.DictReader(proc.stdout):
        yield row
    stderr = proc.stderr.read() if proc.stderr is not None else ""
    return_code = proc.wait()
    if return_code != 0:
        raise CapacityReportError(
            f"psql COPY failed rc={return_code}: {stderr[-4000:]}"
        )


def _one_row(sql: str) -> dict[str, str]:
    rows = list(_copy_rows(sql))
    if len(rows) != 1:
        raise CapacityReportError(f"expected one SQL row, got {len(rows)}")
    return rows[0]


def _int(value: Any, default: int = 0) -> int:
    if value in (None, ""):
        return default
    return int(value)


def _bool(value: Any) -> bool:
    return str(value).strip().lower() in {"1", "t", "true", "yes", "on"}


def _merge_ranges(ranges: Iterable[tuple[int, int]]) -> list[tuple[int, int]]:
    merged: list[list[int]] = []
    for start, end in sorted(ranges):
        if end <= start:
            end = start + 1
        if not merged or start > merged[-1][1]:
            merged.append([start, end])
        elif end > merged[-1][1]:
            merged[-1][1] = end
    return [(start, end) for start, end in merged]


def _day_key(timestamp_ms: int) -> str:
    value = dt.datetime.fromtimestamp(timestamp_ms / 1000, tz=dt.timezone.utc)
    return value.date().isoformat()


def _normalize_snapshot(raw: str) -> str:
    normalized = re.sub(r"([+-]\d{2})$", r"\1:00", raw.replace("Z", "+00:00"))
    normalized = re.sub(
        r"\.(\d{1,6})(?=[+-]\d{2}:\d{2}$)",
        lambda match: "." + match.group(1).ljust(6, "0"),
        normalized,
    )
    try:
        value = dt.datetime.fromisoformat(normalized)
    except ValueError as exc:
        raise CapacityReportError(f"invalid database snapshot timestamp: {raw!r}") from exc
    if value.tzinfo is None:
        raise CapacityReportError(f"database snapshot timestamp has no timezone: {raw!r}")
    return value.astimezone(dt.timezone.utc).isoformat(timespec="microseconds")


_DOCUMENT_KEYS = {"schema_version", "edge", "meta", "accounts"}
_META_KEYS = {
    "snapshot_at",
    "settlement_lag_seconds",
    "db_now_utc",
    "requested_days",
    "error_retention_days",
    "analysis_days",
    "min_sustain_seconds",
    "access_min_utc",
    "runtime_sampling_enabled",
}
_ACCOUNT_KEYS = {
    "account_id",
    "platform",
    "channel_type",
    "configured_concurrency",
    "coverage",
    "sources",
}
_COVERAGE_KEYS = {
    "usage_total",
    "usage_matched",
    "usage_match_pct",
    "invalid_access_rows",
    "invalid_usage_rows",
}
_METRIC_KEYS = {"peak", "observed", "repeated", "cross_day"}


def _exact_mapping(value: Any, expected: set[str], label: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise CapacityReportError(f"{label} must be an object")
    actual = set(value)
    if actual != expected:
        raise CapacityReportError(
            f"{label} keys mismatch: missing={sorted(expected-actual)} "
            f"unknown={sorted(actual-expected)}"
        )
    return value


def _non_negative_int(value: Any, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise CapacityReportError(f"{label} must be a non-negative integer")
    return value


def validate_document(
    document: Any, expected_edge: str | None = None
) -> dict[str, Any]:
    """Validate the exact persisted schema; unknown fields fail closed."""
    root = _exact_mapping(document, _DOCUMENT_KEYS, "document")
    if root["schema_version"] != SCHEMA_VERSION:
        raise CapacityReportError("probe returned unsupported schema_version")

    edge = root["edge"]
    if not isinstance(edge, str) or not re.fullmatch(r"[a-z]{2,4}[0-9]+", edge):
        raise CapacityReportError(f"invalid probe edge: {edge!r}")
    if expected_edge is not None and edge != expected_edge:
        raise CapacityReportError(
            f"probe edge mismatch: expected={expected_edge} got={edge}"
        )

    meta = _exact_mapping(root["meta"], _META_KEYS, "meta")
    snapshot_at = meta["snapshot_at"]
    if not isinstance(snapshot_at, str) or _normalize_snapshot(snapshot_at) != snapshot_at:
        raise CapacityReportError("meta.snapshot_at must be canonical UTC ISO-8601")
    for key in ("db_now_utc", "access_min_utc"):
        if not isinstance(meta[key], str):
            raise CapacityReportError(f"meta.{key} must be text")
    requested_days = _non_negative_int(meta["requested_days"], "meta.requested_days")
    retention_days = _non_negative_int(
        meta["error_retention_days"], "meta.error_retention_days"
    )
    analysis_days = _non_negative_int(meta["analysis_days"], "meta.analysis_days")
    min_seconds = _non_negative_int(
        meta["min_sustain_seconds"], "meta.min_sustain_seconds"
    )
    settlement = _non_negative_int(
        meta["settlement_lag_seconds"], "meta.settlement_lag_seconds"
    )
    if requested_days <= 0 or min_seconds <= 0:
        raise CapacityReportError("requested_days and min_sustain_seconds must be positive")
    if analysis_days > min(requested_days, retention_days):
        raise CapacityReportError("analysis_days exceeds retained evidence")
    if settlement != SETTLEMENT_LAG_SECONDS:
        raise CapacityReportError("unexpected settlement_lag_seconds")
    if not isinstance(meta["runtime_sampling_enabled"], bool):
        raise CapacityReportError("meta.runtime_sampling_enabled must be boolean")

    accounts = root["accounts"]
    if not isinstance(accounts, list):
        raise CapacityReportError("probe result has no accounts array")
    seen_ids: set[int] = set()
    for index, raw_account in enumerate(accounts):
        label = f"accounts[{index}]"
        account = _exact_mapping(raw_account, _ACCOUNT_KEYS, label)
        account_id = _non_negative_int(account["account_id"], f"{label}.account_id")
        if account_id in seen_ids:
            raise CapacityReportError(f"duplicate account_id on edge={edge}: {account_id}")
        seen_ids.add(account_id)
        platform = account["platform"]
        if not isinstance(platform, str) or not re.fullmatch(
            r"[a-z][a-z0-9_-]*", platform
        ):
            raise CapacityReportError(f"{label}.platform is invalid")
        _non_negative_int(account["channel_type"], f"{label}.channel_type")
        _non_negative_int(
            account["configured_concurrency"], f"{label}.configured_concurrency"
        )

        coverage = _exact_mapping(account["coverage"], _COVERAGE_KEYS, f"{label}.coverage")
        usage_total = _non_negative_int(
            coverage["usage_total"], f"{label}.coverage.usage_total"
        )
        usage_matched = _non_negative_int(
            coverage["usage_matched"], f"{label}.coverage.usage_matched"
        )
        if usage_matched > usage_total:
            raise CapacityReportError(f"{label}.coverage matched exceeds total")
        expected_pct = round(100 * usage_matched / usage_total, 2) if usage_total else None
        if coverage["usage_match_pct"] != expected_pct:
            raise CapacityReportError(f"{label}.coverage usage_match_pct is inconsistent")
        _non_negative_int(
            coverage["invalid_access_rows"], f"{label}.coverage.invalid_access_rows"
        )
        _non_negative_int(
            coverage["invalid_usage_rows"], f"{label}.coverage.invalid_usage_rows"
        )

        sources = _exact_mapping(account["sources"], {"F", "H"}, f"{label}.sources")
        for source in ("F", "H"):
            source_value = _exact_mapping(
                sources[source], {"pristine"}, f"{label}.sources.{source}"
            )
            metric = _exact_mapping(
                source_value["pristine"],
                _METRIC_KEYS,
                f"{label}.sources.{source}.pristine",
            )
            values = [
                _non_negative_int(metric[key], f"{label}.{source}.{key}")
                for key in ("peak", "observed", "repeated", "cross_day")
            ]
            if values != sorted(values, reverse=True):
                raise CapacityReportError(f"{label}.{source} metrics are not monotonic")
    return root


def summarize_runs(
    events: Sequence[tuple[int, int]],
    unsafe_ranges: Sequence[tuple[int, int]],
    min_seconds: int,
) -> dict[str, Any]:
    """Summarize half-open concurrency intervals and independent clean episodes."""
    empty = {
        "peak": 0,
        "observed": 0,
        "repeated": 0,
        "cross_day": 0,
    }
    if not events:
        return empty

    points: dict[int, list[int]] = defaultdict(lambda: [0, 0])
    for timestamp_ms, delta in events:
        points[timestamp_ms][0] += delta
    for start_ms, end_ms in unsafe_ranges:
        points[start_ms][1] += 1
        points[end_ms][1] -= 1

    timestamps = sorted(points)
    concurrency = 0
    unsafe_depth = 0
    previous_clean_level = 0
    peak = 0
    run_starts: dict[int, int] = {}
    all_runs: dict[int, list[tuple[int, int]]] = defaultdict(list)

    for index, timestamp_ms in enumerate(timestamps):
        concurrency += points[timestamp_ms][0]
        unsafe_depth += points[timestamp_ms][1]
        if concurrency < 0 or unsafe_depth < 0:
            raise CapacityReportError(
                f"negative sweep state at timestamp_ms={timestamp_ms}"
            )

        next_timestamp = (
            timestamps[index + 1] if index + 1 < len(timestamps) else None
        )
        has_segment = next_timestamp is not None and next_timestamp > timestamp_ms
        clean_level = concurrency if unsafe_depth == 0 and has_segment else 0

        if clean_level < previous_clean_level:
            for level in range(clean_level + 1, previous_clean_level + 1):
                start_ms = run_starts.pop(level, None)
                if start_ms is not None and timestamp_ms > start_ms:
                    all_runs[level].append((start_ms, timestamp_ms))
        elif clean_level > previous_clean_level:
            for level in range(previous_clean_level + 1, clean_level + 1):
                run_starts[level] = timestamp_ms

        if has_segment:
            peak = max(peak, clean_level)
        previous_clean_level = clean_level

    min_ms = min_seconds * 1000
    qualifying = {
        level: [run for run in runs if run[1] - run[0] >= min_ms]
        for level, runs in all_runs.items()
    }
    qualifying = {level: runs for level, runs in qualifying.items() if runs}

    observed = max(qualifying, default=0)
    repeated = max(
        (level for level, runs in qualifying.items() if len(runs) >= 3),
        default=0,
    )
    cross_day = max(
        (
            level
            for level, runs in qualifying.items()
            if len(runs) >= 3
            and len({_day_key(start_ms) for start_ms, _ in runs}) >= 2
        ),
        default=0,
    )

    return {
        "peak": peak,
        "observed": observed,
        "repeated": repeated,
        "cross_day": cross_day,
    }


def _error_sql(analysis_days: int, snapshot_at: str) -> str:
    return f"""
    WITH bounds AS MATERIALIZED (
      SELECT TIMESTAMPTZ '{snapshot_at}' AS upper_ts,
             TIMESTAMPTZ '{snapshot_at}'-make_interval(days=>{analysis_days}) AS lower_ts
    ), active AS MATERIALIZED (
      SELECT id FROM accounts
       WHERE deleted_at IS NULL AND status='active' AND schedulable
    ), access AS MATERIALIZED (
      SELECT l.id, l.account_id, l.request_id, l.client_request_id, l.created_at,
             (l.extra->>'latency_ms')::bigint AS latency_ms,
             CASE WHEN l.extra->>'status_code' ~ '^[0-9]+$'
                  THEN (l.extra->>'status_code')::int END AS status_code
        FROM ops_system_logs l
       WHERE l.created_at >= (SELECT lower_ts FROM bounds)
         AND l.created_at <= (SELECT upper_ts FROM bounds)
         AND l.component='http.access' AND l.message='http request completed'
         AND CASE WHEN l.extra->>'latency_ms' ~ '^[0-9]+$'
                  THEN (l.extra->>'latency_ms')::numeric > 0
                   AND (l.extra->>'latency_ms')::numeric <= {MAX_INTERVAL_MS}
                  ELSE false END
    ), assigned_access_final AS (
      SELECT 'final'::text AS kind, h.account_id,
             (extract(epoch FROM h.created_at)*1000)::bigint AS end_ms,
             h.latency_ms::bigint AS duration_ms
        FROM access h JOIN active a ON a.id=h.account_id
       WHERE h.status_code >= 400
    ), terminal_ops_base AS MATERIALIZED (
      SELECT e.*
        FROM ops_error_logs e
       WHERE e.created_at >= (SELECT lower_ts FROM bounds)
         AND e.created_at <= (SELECT upper_ts FROM bounds)
         AND COALESCE(e.is_count_tokens,false)=false
         AND lower(COALESCE(e.error_message,'')) NOT LIKE 'recovered %'
         AND (COALESCE(e.status_code,0) >= 400
              OR (COALESCE(e.stream,false) AND COALESCE(e.status_code,0) < 400))
    ), access_error_keys AS MATERIALIZED (
      SELECT h.id AS access_id, h.account_id, h.created_at,
             h.latency_ms, 'client'::text AS key_kind,
             h.client_request_id AS correlation_key, 1::int AS match_rank
        FROM access h WHERE NULLIF(h.client_request_id,'') IS NOT NULL
      UNION ALL
      SELECT h.id, h.account_id, h.created_at,
             h.latency_ms, 'request'::text, h.request_id, 2::int
        FROM access h WHERE NULLIF(h.request_id,'') IS NOT NULL
    ), terminal_ops_keys AS MATERIALIZED (
      SELECT e.id AS error_id, e.created_at AS error_at,
             e.account_id AS logged_account_id,
             'client'::text AS key_kind, e.client_request_id AS correlation_key
        FROM terminal_ops_base e WHERE NULLIF(e.client_request_id,'') IS NOT NULL
      UNION ALL
      SELECT e.id, e.created_at, e.account_id, 'request'::text, e.request_id
        FROM terminal_ops_base e WHERE NULLIF(e.request_id,'') IS NOT NULL
    ), terminal_ops_candidate_pairs AS MATERIALIZED (
      SELECT DISTINCT ON (e.error_id,h.access_id)
             e.error_id, h.access_id, h.account_id, h.created_at,
             h.latency_ms, h.match_rank,
             abs(extract(epoch FROM (h.created_at-e.error_at))) AS distance_s
        FROM terminal_ops_keys e JOIN access_error_keys h
          ON h.key_kind=e.key_kind AND h.correlation_key=e.correlation_key
         AND (e.key_kind='client' OR e.logged_account_id IS NULL
              OR h.account_id=e.logged_account_id)
       WHERE abs(extract(epoch FROM (h.created_at-e.error_at))) <= 86400
       ORDER BY e.error_id,h.access_id,h.match_rank,distance_s
    ), terminal_ops_ranked AS MATERIALIZED (
      SELECT p.*,
             min(p.match_rank) OVER (PARTITION BY p.error_id) AS best_match_rank
        FROM terminal_ops_candidate_pairs p
    ), terminal_ops_candidates AS MATERIALIZED (
      SELECT p.*,
             count(*) OVER (PARTITION BY p.error_id) AS candidate_count,
             row_number() OVER (
               PARTITION BY p.error_id
               ORDER BY p.distance_s,p.access_id
             ) AS rn
        FROM terminal_ops_ranked p
       WHERE p.match_rank=p.best_match_rank
    ), terminal_ops_best AS MATERIALIZED (
      SELECT * FROM terminal_ops_candidates WHERE rn=1 AND candidate_count=1
    ), terminal_ops_matched AS (
      SELECT e.id, e.account_id AS logged_account_id, e.created_at AS logged_at,
             h.account_id AS access_account_id, h.created_at AS access_at,
             h.latency_ms AS access_latency_ms
        FROM terminal_ops_base e
        LEFT JOIN terminal_ops_best h ON h.error_id=e.id
    ), assigned_ops_final AS (
      SELECT 'final'::text AS kind,
             COALESCE(o.access_account_id,o.logged_account_id) AS account_id,
             (extract(epoch FROM COALESCE(o.access_at,o.logged_at))*1000)::bigint AS end_ms,
             COALESCE(o.access_latency_ms,1)::bigint AS duration_ms
        FROM terminal_ops_matched o
        JOIN active a ON a.id=COALESCE(o.access_account_id,o.logged_account_id)
    ), recovered AS MATERIALIZED (
      SELECT e.*
        FROM ops_error_logs e
       WHERE e.created_at >= (SELECT lower_ts FROM bounds)
         AND e.created_at <= (SELECT upper_ts FROM bounds)
         AND COALESCE(e.status_code,0) < 400
         AND (lower(COALESCE(e.error_message,'')) LIKE 'recovered upstream error%'
              OR lower(COALESCE(e.error_message,'')) LIKE 'recovered account authentication failure%')
    ), recovered_events AS (
      SELECT 'hidden'::text AS kind, (ev.value->>'account_id')::bigint AS account_id,
             CASE WHEN ev.value->>'at_unix_ms' ~ '^[0-9]+$'
                  THEN (ev.value->>'at_unix_ms')::bigint
                  ELSE (extract(epoch FROM e.created_at)*1000)::bigint END AS end_ms,
             1::bigint AS duration_ms
        FROM recovered e
        CROSS JOIN LATERAL jsonb_array_elements(
          CASE WHEN jsonb_typeof(e.upstream_errors)='array'
               THEN e.upstream_errors ELSE '[]'::jsonb END
        ) ev(value)
        JOIN active a ON a.id=CASE
          WHEN ev.value->>'account_id' ~ '^[0-9]+$'
          THEN (ev.value->>'account_id')::bigint ELSE NULL END
       WHERE ev.value->>'account_id' ~ '^[0-9]+$'
         AND lower(COALESCE(ev.value->>'kind','')) NOT IN
             ('request_normalized','client_tool_context_corrupt')
    ), recovered_fallback AS (
      SELECT 'hidden'::text AS kind, e.account_id,
             (extract(epoch FROM e.created_at)*1000)::bigint AS end_ms,
             1::bigint AS duration_ms
        FROM recovered e JOIN active a ON a.id=e.account_id
       WHERE NOT EXISTS (
         SELECT 1
           FROM jsonb_array_elements(
             CASE WHEN jsonb_typeof(e.upstream_errors)='array'
                  THEN e.upstream_errors ELSE '[]'::jsonb END
           ) ev(value)
          WHERE ev.value->>'account_id' ~ '^[0-9]+$'
            AND lower(COALESCE(ev.value->>'kind','')) NOT IN
                ('request_normalized','client_tool_context_corrupt')
       )
    )
    SELECT * FROM assigned_access_final
    UNION ALL SELECT * FROM assigned_ops_final
    UNION ALL SELECT * FROM recovered_events
    UNION ALL SELECT * FROM recovered_fallback
    ORDER BY kind, account_id, end_ms
    """


def _event_sql(analysis_days: int, snapshot_at: str) -> str:
    return f"""
    WITH bounds AS MATERIALIZED (
      SELECT TIMESTAMPTZ '{snapshot_at}' AS upper_ts,
             TIMESTAMPTZ '{snapshot_at}'-make_interval(days=>{analysis_days}) AS lower_ts,
             (extract(epoch FROM (
               TIMESTAMPTZ '{snapshot_at}'-make_interval(days=>{analysis_days})
             ))*1000)::bigint AS lower_ms
    ), active AS MATERIALIZED (
      SELECT id FROM accounts
       WHERE deleted_at IS NULL AND status='active' AND schedulable
    ), http_all AS MATERIALIZED (
      SELECT l.id, l.account_id, l.request_id, l.client_request_id, l.created_at,
             l.extra->>'latency_ms' AS latency_raw
        FROM ops_system_logs l JOIN active a ON a.id=l.account_id
       WHERE l.created_at >= (SELECT lower_ts FROM bounds)
         AND l.created_at <= (SELECT upper_ts FROM bounds)
         AND l.component='http.access' AND l.message='http request completed'
    ), http_valid AS MATERIALIZED (
      SELECT id, account_id, request_id, client_request_id, created_at,
             (latency_raw)::bigint AS latency_ms
        FROM http_all
       WHERE CASE WHEN latency_raw ~ '^[0-9]+$'
                  THEN (latency_raw)::numeric > 0
                   AND (latency_raw)::numeric <= {MAX_INTERVAL_MS}
                  ELSE false END
    ), usage_base AS MATERIALIZED (
      SELECT u.id, u.account_id, u.request_id, u.created_at,
             u.duration_ms::bigint AS duration_ms
        FROM usage_logs u JOIN active a ON a.id=u.account_id
       WHERE u.created_at >= (SELECT lower_ts FROM bounds)-interval '1 day'
         AND u.created_at <= (SELECT upper_ts FROM bounds)
         AND u.duration_ms > 0 AND u.duration_ms <= {MAX_INTERVAL_MS}
    ), access_keys AS MATERIALIZED (
      SELECT h.id access_id, h.account_id, h.created_at access_at,
             h.latency_ms, 'client:'||h.client_request_id AS match_key,
             'client'::text AS match_kind, 1::int AS match_rank
        FROM http_valid h WHERE NULLIF(h.client_request_id,'') IS NOT NULL
      UNION ALL
      SELECT h.id, h.account_id, h.created_at, h.latency_ms,
             'local:'||h.request_id, 'local'::text, 2::int
        FROM http_valid h WHERE NULLIF(h.request_id,'') IS NOT NULL
      UNION ALL
      SELECT h.id, h.account_id, h.created_at, h.latency_ms,
             h.request_id, 'raw'::text, 3::int
        FROM http_valid h WHERE NULLIF(h.request_id,'') IS NOT NULL
    ), candidates AS MATERIALIZED (
      SELECT u.id usage_id, u.account_id, u.created_at usage_at, u.duration_ms,
             k.access_id, k.access_at, k.match_kind, k.match_rank,
             abs(extract(epoch FROM (k.access_at-u.created_at))*1000)::bigint AS lag_ms,
             count(*) OVER (PARTITION BY u.id) AS candidate_count,
             row_number() OVER (
               PARTITION BY u.id
               ORDER BY k.match_rank,
                        abs(extract(epoch FROM (k.access_at-u.created_at))), k.access_id
             ) AS rn
        FROM usage_base u JOIN access_keys k
          ON k.account_id=u.account_id AND k.match_key=u.request_id
       WHERE abs(extract(epoch FROM (k.access_at-u.created_at))) <= 86400
    ), selected AS MATERIALIZED (
      SELECT * FROM candidates WHERE rn=1
    ), matched AS MATERIALIZED (
      SELECT * FROM selected WHERE candidate_count=1
    ), http_base AS (
      SELECT 'H'::text AS source, account_id,
             (extract(epoch FROM created_at)*1000)::bigint AS end_ms,
             GREATEST(
               (extract(epoch FROM created_at)*1000)::bigint-latency_ms,
               (SELECT lower_ms FROM bounds)
             ) AS start_ms
        FROM http_valid
    ), forward_base AS (
      SELECT 'F'::text AS source, account_id,
             (extract(epoch FROM access_at)*1000)::bigint AS end_ms,
             GREATEST(
               (extract(epoch FROM access_at)*1000)::bigint-duration_ms,
               (SELECT lower_ms FROM bounds)
             ) AS start_ms
        FROM matched
    ), base AS (
      SELECT * FROM http_base UNION ALL SELECT * FROM forward_base
    ), events AS (
      SELECT source,account_id,start_ms AS ts_ms,1::bigint AS delta FROM base
      UNION ALL
      SELECT source,account_id,end_ms AS ts_ms,-1::bigint AS delta FROM base
    ), event_agg AS (
      SELECT source,account_id,ts_ms,sum(delta)::bigint AS delta
        FROM events GROUP BY source,account_id,ts_ms
    ), coverage AS (
      SELECT a.id AS account_id,
             count(u.id) FILTER (WHERE u.created_at >= (SELECT lower_ts FROM bounds)) AS usage_total,
             count(m.usage_id) FILTER (WHERE m.usage_at >= (SELECT lower_ts FROM bounds)
                                        AND m.candidate_count=1) AS usage_matched,
             (SELECT count(*) FROM http_all h
               WHERE h.account_id=a.id AND NOT CASE
                 WHEN h.latency_raw ~ '^[0-9]+$'
                 THEN (h.latency_raw)::numeric > 0
                  AND (h.latency_raw)::numeric <= {MAX_INTERVAL_MS}
                 ELSE false END) AS invalid_access_rows,
             (SELECT count(*) FROM usage_logs ux
               WHERE ux.account_id=a.id
                 AND ux.created_at >= (SELECT lower_ts FROM bounds)
                 AND ux.created_at <= (SELECT upper_ts FROM bounds)
                 AND (ux.duration_ms IS NULL OR ux.duration_ms <= 0
                      OR ux.duration_ms > {MAX_INTERVAL_MS})
             ) AS invalid_usage_rows
        FROM active a
        LEFT JOIN usage_base u ON u.account_id=a.id
        LEFT JOIN selected m ON m.usage_id=u.id
       GROUP BY a.id
    )
    SELECT 'event'::text AS row_kind, source, account_id, ts_ms, delta,
           NULL::bigint AS usage_total, NULL::bigint AS usage_matched,
           NULL::bigint AS invalid_access_rows,
           NULL::bigint AS invalid_usage_rows
      FROM event_agg
    UNION ALL
    SELECT 'coverage', NULL::text, account_id, NULL::bigint, NULL::bigint,
           usage_total, usage_matched, invalid_access_rows, invalid_usage_rows
      FROM coverage
    ORDER BY row_kind, source NULLS LAST, account_id, ts_ms NULLS LAST
    """


def _meta_sql(
    requested_days: int,
    error_retention_days: int,
    analysis_days: int,
    min_seconds: int,
    snapshot_at: str | None = None,
) -> str:
    snapshot_expression = (
        f"TIMESTAMPTZ '{snapshot_at}'"
        if snapshot_at is not None
        else f"(now()-make_interval(secs=>{SETTLEMENT_LAG_SECONDS}))"
    )
    return f"""
    WITH access_bounds AS MATERIALIZED (
      SELECT min(created_at) AS access_min
        FROM ops_system_logs
       WHERE component='http.access' AND message='http request completed'
    )
    SELECT ({snapshot_expression})::text AS snapshot_at,
           ({snapshot_expression}) <=
             (now()-make_interval(secs=>{SETTLEMENT_LAG_SECONDS})) AS snapshot_is_settled,
           (now() AT TIME ZONE 'UTC')::text AS db_now_utc,
           {requested_days}::int AS requested_days,
           {error_retention_days}::int AS error_retention_days,
           {analysis_days}::int AS analysis_days,
           {min_seconds}::int AS min_sustain_seconds,
           (access_min AT TIME ZONE 'UTC')::text AS access_min_utc,
           COALESCE(
             access_min <= ({snapshot_expression})-make_interval(days=>{analysis_days}),
             false
           ) AS access_window_complete,
           COALESCE((SELECT (value::jsonb->>'enable_sampling')::boolean
                       FROM settings WHERE key='ops_runtime_log_config' LIMIT 1), false)
             AS runtime_sampling_enabled
      FROM access_bounds
    """


def analyze_edge(
    edge: str,
    requested_days: int,
    min_seconds: int,
    snapshot_at: str | None = None,
) -> dict[str, Any]:
    retention_row = _one_row(
        """
        SELECT COALESCE(
          NULLIF((SELECT value::jsonb #>> '{data_retention,error_log_retention_days}'
                    FROM settings WHERE key='ops_advanced_settings' LIMIT 1), '')::int,
          14
        ) AS error_retention_days
        """
    )
    error_retention_days = _int(retention_row["error_retention_days"], 14)
    analysis_days = min(requested_days, error_retention_days)

    requested_snapshot = _normalize_snapshot(snapshot_at) if snapshot_at else None
    meta = _one_row(
        _meta_sql(
            requested_days,
            error_retention_days,
            analysis_days,
            min_seconds,
            requested_snapshot,
        )
    )
    if not _bool(meta.pop("snapshot_is_settled")):
        raise CapacityReportError(
            f"snapshot is newer than the {SETTLEMENT_LAG_SECONDS}s settlement watermark"
        )
    if not _bool(meta.pop("access_window_complete")):
        raise CapacityReportError(
            f"access logs do not cover the requested {analysis_days}-day safety window"
        )
    snapshot_at = _normalize_snapshot(meta["snapshot_at"])
    if requested_snapshot is not None and snapshot_at != requested_snapshot:
        raise CapacityReportError("database did not preserve the requested snapshot")
    meta = {
        **meta,
        "snapshot_at": snapshot_at,
        "settlement_lag_seconds": SETTLEMENT_LAG_SECONDS,
        "requested_days": requested_days,
        "error_retention_days": error_retention_days,
        "analysis_days": analysis_days,
        "min_sustain_seconds": min_seconds,
        "runtime_sampling_enabled": _bool(meta["runtime_sampling_enabled"]),
    }

    accounts: dict[int, dict[str, Any]] = {}
    for row in _copy_rows(
        """
        SELECT id, platform, channel_type, concurrency
          FROM accounts
         WHERE deleted_at IS NULL AND status='active' AND schedulable
         ORDER BY id
        """
    ):
        account_id = int(row["id"])
        accounts[account_id] = {
            "account_id": account_id,
            "platform": row["platform"],
            "channel_type": _int(row["channel_type"]),
            "configured_concurrency": _int(row["concurrency"]),
        }

    final_unsafe: dict[int, list[tuple[int, int]]] = defaultdict(list)
    hidden_unsafe: dict[int, list[tuple[int, int]]] = defaultdict(list)
    for row in _copy_rows(_error_sql(analysis_days, snapshot_at)):
        end_ms = int(row["end_ms"])
        interval = (end_ms - int(row["duration_ms"]), end_ms)
        account_id = int(row["account_id"])
        if row["kind"] == "final":
            final_unsafe[account_id].append(interval)
        else:
            hidden_unsafe[account_id].append(interval)

    final_unsafe = {
        account_id: _merge_ranges(ranges)
        for account_id, ranges in final_unsafe.items()
    }
    hidden_unsafe = {
        account_id: _merge_ranges(ranges)
        for account_id, ranges in hidden_unsafe.items()
    }

    event_groups: dict[tuple[str, int], list[tuple[int, int]]] = defaultdict(list)
    coverage: dict[int, dict[str, Any]] = {}
    for row in _copy_rows(_event_sql(analysis_days, snapshot_at)):
        account_id = int(row["account_id"])
        if row["row_kind"] == "event":
            event_groups[(row["source"], account_id)].append(
                (int(row["ts_ms"]), int(row["delta"]))
            )
            continue
        usage_total = _int(row["usage_total"])
        usage_matched = _int(row["usage_matched"])
        coverage[account_id] = {
            "usage_total": usage_total,
            "usage_matched": usage_matched,
            "usage_match_pct": (
                round(100 * usage_matched / usage_total, 2)
                if usage_total
                else None
            ),
            "invalid_access_rows": _int(row["invalid_access_rows"]),
            "invalid_usage_rows": _int(row["invalid_usage_rows"]),
        }

    output_accounts: list[dict[str, Any]] = []
    for account_id in sorted(accounts):
        account = accounts[account_id]
        final_ranges = final_unsafe.get(account_id, [])
        hidden_ranges = hidden_unsafe.get(account_id, [])
        pristine_ranges = _merge_ranges([*final_ranges, *hidden_ranges])
        sources: dict[str, dict[str, Any]] = {}
        for source in ("F", "H"):
            events = event_groups.get((source, account_id), [])
            sources[source] = {
                "pristine": summarize_runs(events, pristine_ranges, min_seconds),
            }

        output_accounts.append(
            {
                **account,
                "coverage": coverage.get(account_id, {}),
                "sources": {
                    "F": {"pristine": sources["F"]["pristine"]},
                    "H": {"pristine": sources["H"]["pristine"]},
                },
            }
        )

    document = {
        "schema_version": SCHEMA_VERSION,
        "edge": edge,
        "meta": meta,
        "accounts": output_accounts,
    }
    return validate_document(document, edge)


def _command_analyze(args: argparse.Namespace) -> int:
    document = analyze_edge(
        args.edge,
        args.days,
        args.min_seconds,
        getattr(args, "snapshot_at", None),
    )
    encoded = json.dumps(document, ensure_ascii=True, separators=(",", ":"))
    payload_bytes = len(encoded.encode("utf-8"))
    if payload_bytes > 22_000:
        raise CapacityReportError(
            f"probe JSON exceeds SSM stdout budget: bytes={payload_bytes} limit=22000"
        )
    sys.stdout.write(encoded + "\n")
    return 0


def _positive_int(raw: str) -> int:
    value = int(raw)
    if value <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return value


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("analyze",))
    parser.add_argument("--edge", required=True)
    parser.add_argument("--days", type=_positive_int, default=DEFAULT_DAYS)
    parser.add_argument("--min-seconds", type=_positive_int, default=DEFAULT_MIN_SECONDS)
    parser.add_argument("--snapshot-at")
    parser.set_defaults(func=_command_analyze)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    try:
        return int(args.func(args))
    except CapacityReportError as exc:
        print(f"edge_capacity_probe: ERROR: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
