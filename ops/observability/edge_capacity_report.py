#!/usr/bin/env python3
"""Collect, analyze, and render TokenKey edge concurrency evidence."""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import json
import os
import re
import subprocess
import sys
import tempfile
import time
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any, Iterable, Iterator, Sequence


SCHEMA_VERSION = 1
MAX_INTERVAL_MS = 86_400_000
DEFAULT_DAYS = 60
DEFAULT_MIN_SECONDS = 60
DEFAULT_REPORT_PATH = "docs/ops/edge-capacity-report-20260720-c1.md"


class CapacityReportError(RuntimeError):
    """Raised when collection or rendering cannot produce trustworthy output."""


def _repo_root() -> Path:
    return Path(__file__).resolve().parents[2]


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


def _iso_time(timestamp_ms: int | None) -> str | None:
    if timestamp_ms is None:
        return None
    value = dt.datetime.fromtimestamp(timestamp_ms / 1000, tz=dt.timezone.utc)
    return value.isoformat().replace("+00:00", "Z")


def _normalize_snapshot(raw: str) -> str:
    normalized = re.sub(r"([+-]\d{2})$", r"\1:00", raw.replace("Z", "+00:00"))
    try:
        value = dt.datetime.fromisoformat(normalized)
    except ValueError as exc:
        raise CapacityReportError(f"invalid database snapshot timestamp: {raw!r}") from exc
    if value.tzinfo is None:
        raise CapacityReportError(f"database snapshot timestamp has no timezone: {raw!r}")
    return value.astimezone(dt.timezone.utc).isoformat(timespec="microseconds")


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
        "observed_episode_seconds": 0.0,
        "observed_episode_start_utc": None,
        "observed_episode_end_utc": None,
        "peak_clean_episode_seconds": 0.0,
        "repeat_episode_count": 0,
        "repeat_distinct_days": 0,
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
            peak = max(peak, concurrency)
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

    best_start: int | None = None
    best_end: int | None = None
    if observed:
        best_start, best_end = max(
            qualifying[observed], key=lambda run: run[1] - run[0]
        )
    peak_clean_seconds = max(
        ((end_ms - start_ms) / 1000 for start_ms, end_ms in all_runs.get(peak, [])),
        default=0.0,
    )
    repeated_runs = qualifying.get(repeated, [])

    return {
        "peak": peak,
        "observed": observed,
        "repeated": repeated,
        "cross_day": cross_day,
        "observed_episode_seconds": round(
            ((best_end - best_start) / 1000)
            if best_start is not None and best_end is not None
            else 0.0,
            3,
        ),
        "observed_episode_start_utc": _iso_time(best_start),
        "observed_episode_end_utc": _iso_time(best_end),
        "peak_clean_episode_seconds": round(peak_clean_seconds, 3),
        "repeat_episode_count": len(repeated_runs),
        "repeat_distinct_days": len(
            {_day_key(start_ms) for start_ms, _ in repeated_runs}
        ),
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
      SELECT l.id, l.account_id, l.platform, l.model, l.request_id,
             l.client_request_id, l.created_at,
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
             COALESCE(NULLIF(h.platform,''),'<unknown>') AS platform,
             COALESCE(NULLIF(h.model,''),'<unknown>') AS model,
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
      SELECT h.id AS access_id, h.account_id, h.platform, h.model, h.created_at,
             h.latency_ms, 'client'::text AS key_kind,
             h.client_request_id AS correlation_key, 1::int AS match_rank
        FROM access h WHERE NULLIF(h.client_request_id,'') IS NOT NULL
      UNION ALL
      SELECT h.id, h.account_id, h.platform, h.model, h.created_at,
             h.latency_ms, 'request'::text, h.request_id, 2::int
        FROM access h WHERE NULLIF(h.request_id,'') IS NOT NULL
    ), terminal_ops_keys AS MATERIALIZED (
      SELECT e.id AS error_id, e.created_at AS error_at,
             'client'::text AS key_kind, e.client_request_id AS correlation_key
        FROM terminal_ops_base e WHERE NULLIF(e.client_request_id,'') IS NOT NULL
      UNION ALL
      SELECT e.id, e.created_at, 'request'::text, e.request_id
        FROM terminal_ops_base e WHERE NULLIF(e.request_id,'') IS NOT NULL
    ), terminal_ops_candidate_pairs AS MATERIALIZED (
      SELECT DISTINCT ON (e.error_id,h.access_id)
             e.error_id, h.access_id, h.account_id, h.platform, h.model,
             h.created_at, h.latency_ms, h.match_rank,
             abs(extract(epoch FROM (h.created_at-e.error_at))) AS distance_s
        FROM terminal_ops_keys e JOIN access_error_keys h
          ON h.key_kind=e.key_kind AND h.correlation_key=e.correlation_key
       WHERE abs(extract(epoch FROM (h.created_at-e.error_at))) <= 86400
       ORDER BY e.error_id,h.access_id,h.match_rank,distance_s
    ), terminal_ops_candidates AS MATERIALIZED (
      SELECT p.*,
             count(*) OVER (PARTITION BY p.error_id) AS candidate_count,
             row_number() OVER (
               PARTITION BY p.error_id
               ORDER BY p.match_rank,p.distance_s,p.access_id
             ) AS rn
        FROM terminal_ops_candidate_pairs p
    ), terminal_ops_best AS MATERIALIZED (
      SELECT * FROM terminal_ops_candidates WHERE rn=1 AND candidate_count=1
    ), terminal_ops_matched AS (
      SELECT e.id, e.account_id AS logged_account_id, e.platform AS logged_platform,
             e.model AS logged_model, e.created_at AS logged_at,
             h.account_id AS access_account_id, h.platform AS access_platform,
             h.model AS access_model, h.created_at AS access_at,
             h.latency_ms AS access_latency_ms
        FROM terminal_ops_base e
        LEFT JOIN terminal_ops_best h ON h.error_id=e.id
    ), assigned_ops_final AS (
      SELECT 'final'::text AS kind,
             COALESCE(o.access_account_id,o.logged_account_id) AS account_id,
             COALESCE(NULLIF(o.access_platform,''),NULLIF(o.logged_platform,''),'<unknown>') AS platform,
             COALESCE(NULLIF(o.access_model,''),NULLIF(o.logged_model,''),'<unknown>') AS model,
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
             COALESCE(NULLIF(ev.value->>'platform',''),NULLIF(e.platform,''),'<unknown>') AS platform,
             COALESCE(NULLIF(e.model,''),'<unknown>') AS model,
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
             COALESCE(NULLIF(e.platform,''),'<unknown>') AS platform,
             COALESCE(NULLIF(e.model,''),'<unknown>') AS model,
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
    ), pool_terminal AS (
      SELECT 'pool'::text AS kind, NULL::bigint AS account_id,
             COALESCE(NULLIF(o.logged_platform,''),'<unknown>') AS platform,
             COALESCE(NULLIF(o.logged_model,''),'<unknown>') AS model,
             (extract(epoch FROM o.logged_at)*1000)::bigint AS end_ms,
             1::bigint AS duration_ms
        FROM terminal_ops_matched o
       WHERE COALESCE(o.access_account_id,o.logged_account_id) IS NULL
    )
    SELECT * FROM assigned_access_final
    UNION ALL SELECT * FROM assigned_ops_final
    UNION ALL SELECT * FROM recovered_events
    UNION ALL SELECT * FROM recovered_fallback
    UNION ALL SELECT * FROM pool_terminal
    ORDER BY kind, account_id NULLS FIRST, end_ms
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
             count(m.usage_id) FILTER (WHERE m.usage_at >= (SELECT lower_ts FROM bounds)
                                        AND m.candidate_count=1 AND m.match_kind='client') AS client_matches,
             count(m.usage_id) FILTER (WHERE m.usage_at >= (SELECT lower_ts FROM bounds)
                                        AND m.candidate_count=1 AND m.match_kind='local') AS local_matches,
             count(m.usage_id) FILTER (WHERE m.usage_at >= (SELECT lower_ts FROM bounds)
                                        AND m.candidate_count=1 AND m.match_kind='raw') AS raw_matches,
             count(m.usage_id) FILTER (WHERE m.usage_at >= (SELECT lower_ts FROM bounds)
                                        AND m.candidate_count>1) AS ambiguous_usage,
             (SELECT count(*) FROM http_valid h WHERE h.account_id=a.id) AS access_total,
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
           NULL::bigint AS client_matches, NULL::bigint AS local_matches,
           NULL::bigint AS raw_matches, NULL::bigint AS ambiguous_usage,
           NULL::bigint AS access_total, NULL::bigint AS invalid_access_rows,
           NULL::bigint AS invalid_usage_rows
      FROM event_agg
    UNION ALL
    SELECT 'coverage', NULL::text, account_id, NULL::bigint, NULL::bigint,
           usage_total, usage_matched, client_matches, local_matches, raw_matches,
           ambiguous_usage, access_total, invalid_access_rows, invalid_usage_rows
      FROM coverage
    ORDER BY row_kind, source NULLS LAST, account_id, ts_ms NULLS LAST
    """


def _account_recommendation(
    metric: dict[str, Any], configured_concurrency: int
) -> tuple[int | None, str]:
    cross_day = _int(metric.get("cross_day"))
    if cross_day > 0:
        if configured_concurrency > 0:
            cross_day = min(cross_day, configured_concurrency)
        return cross_day, "cross-day-pristine"
    if _int(metric.get("repeated")) > 0:
        return None, "same-day-repeat-only"
    if _int(metric.get("observed")) > 0:
        return None, "single-episode-only"
    return None, "none"


def analyze_edge(edge: str, requested_days: int, min_seconds: int) -> dict[str, Any]:
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

    meta = _one_row(
        f"""
        SELECT now()::text AS snapshot_at,
               (now() AT TIME ZONE 'UTC')::text AS db_now_utc,
               {requested_days}::int AS requested_days,
               {error_retention_days}::int AS error_retention_days,
               {analysis_days}::int AS analysis_days,
               {min_seconds}::int AS min_sustain_seconds,
               (SELECT min(created_at) AT TIME ZONE 'UTC' FROM usage_logs)::text AS usage_min_utc,
               (SELECT max(created_at) AT TIME ZONE 'UTC' FROM usage_logs)::text AS usage_max_utc,
               (SELECT min(created_at) AT TIME ZONE 'UTC' FROM ops_system_logs
                 WHERE component='http.access'
                   AND message='http request completed')::text AS access_min_utc,
               (SELECT max(created_at) AT TIME ZONE 'UTC' FROM ops_system_logs
                 WHERE component='http.access'
                   AND message='http request completed')::text AS access_max_utc,
               COALESCE((SELECT (value::jsonb->>'enable_sampling')::boolean
                           FROM settings WHERE key='ops_runtime_log_config' LIMIT 1), false)
                 AS runtime_sampling_enabled,
               COALESCE((SELECT NULLIF(value::jsonb->>'sampling_initial','')::int
                           FROM settings WHERE key='ops_runtime_log_config' LIMIT 1), 100)
                 AS runtime_sampling_initial,
               COALESCE((SELECT NULLIF(value::jsonb->>'sampling_thereafter','')::int
                           FROM settings WHERE key='ops_runtime_log_config' LIMIT 1), 100)
                 AS runtime_sampling_thereafter
        """
    )
    snapshot_at = _normalize_snapshot(meta["snapshot_at"])
    meta = {
        **meta,
        "snapshot_at": snapshot_at,
        "requested_days": requested_days,
        "error_retention_days": error_retention_days,
        "analysis_days": analysis_days,
        "min_sustain_seconds": min_seconds,
        "runtime_sampling_enabled": _bool(meta["runtime_sampling_enabled"]),
        "runtime_sampling_initial": _int(meta["runtime_sampling_initial"], 100),
        "runtime_sampling_thereafter": _int(
            meta["runtime_sampling_thereafter"], 100
        ),
    }

    accounts: dict[int, dict[str, Any]] = {}
    for row in _copy_rows(
        """
        SELECT id, name, platform, type, channel_type, concurrency,
               CASE WHEN jsonb_typeof(credentials->'model_mapping')='object'
                    THEN COALESCE((
                      SELECT jsonb_agg(key ORDER BY key)
                        FROM jsonb_object_keys(credentials->'model_mapping') AS key
                    ), '[]'::jsonb)::text
                    ELSE '[]' END AS model_mapping_keys
          FROM accounts
         WHERE deleted_at IS NULL AND status='active' AND schedulable
         ORDER BY id
        """
    ):
        account_id = int(row["id"])
        accounts[account_id] = {
            "account_id": account_id,
            "account_name": row["name"],
            "platform": row["platform"],
            "account_type": row["type"],
            "channel_type": _int(row["channel_type"]),
            "configured_concurrency": _int(row["concurrency"]),
            "model_mapping_keys": json.loads(row["model_mapping_keys"] or "[]"),
        }

    final_unsafe: dict[int, list[tuple[int, int]]] = defaultdict(list)
    hidden_unsafe: dict[int, list[tuple[int, int]]] = defaultdict(list)
    pool_context: Counter[tuple[str, str]] = Counter()
    for row in _copy_rows(_error_sql(analysis_days, snapshot_at)):
        if row["kind"] == "pool":
            pool_context[(row["platform"], row["model"])] += 1
            continue
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
            "client_matches": _int(row["client_matches"]),
            "local_matches": _int(row["local_matches"]),
            "raw_matches": _int(row["raw_matches"]),
            "ambiguous_usage": _int(row["ambiguous_usage"]),
            "access_total": _int(row["access_total"]),
            "invalid_access_rows": _int(row["invalid_access_rows"]),
            "invalid_usage_rows": _int(row["invalid_usage_rows"]),
        }

    model_counts: dict[int, list[dict[str, Any]]] = defaultdict(list)
    for row in _copy_rows(
        f"""
        SELECT u.account_id,
               COALESCE(NULLIF(u.model,''),'<unknown>') AS model,
               count(*)::bigint AS requests
          FROM usage_logs u JOIN accounts a ON a.id=u.account_id
         WHERE u.created_at >= TIMESTAMPTZ '{snapshot_at}'-make_interval(days=>{requested_days})
           AND u.created_at <= TIMESTAMPTZ '{snapshot_at}'
           AND a.deleted_at IS NULL AND a.status='active' AND a.schedulable
         GROUP BY u.account_id, COALESCE(NULLIF(u.model,''),'<unknown>')
         ORDER BY u.account_id, requests DESC, model
        """
    ):
        model_counts[int(row["account_id"])].append(
            {"model": row["model"], "requests": int(row["requests"])}
        )

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

        f_pristine = sources["F"]["pristine"]
        recommendation, evidence = _account_recommendation(
            f_pristine, account["configured_concurrency"]
        )

        output_accounts.append(
            {
                **account,
                "final_error_intervals": len(final_ranges),
                "hidden_error_intervals": len(hidden_ranges),
                "recommended": recommendation,
                "recommendation_evidence": evidence,
                "coverage": coverage.get(account_id, {}),
                "models": model_counts.get(account_id, [])[:16],
                "sources": {
                    "F": {"pristine": sources["F"]["pristine"]},
                    "H": {"pristine": sources["H"]["pristine"]},
                },
            }
        )

    pool_by_platform: dict[str, Counter[str]] = defaultdict(Counter)
    for (platform, model), count in pool_context.items():
        pool_by_platform[platform][model] += count

    return {
        "schema_version": SCHEMA_VERSION,
        "edge": edge,
        "meta": meta,
        "accounts": output_accounts,
        "pool_context": [
            {
                "platform": platform,
                "terminal_error_count": sum(models.values()),
                "top_models": [
                    {"model": model, "terminal_error_count": count}
                    for model, count in models.most_common(5)
                ],
            }
            for platform, models in sorted(pool_by_platform.items())
        ],
    }


def _flatten_accounts(edge_documents: Sequence[dict[str, Any]]) -> list[dict[str, Any]]:
    flattened: list[dict[str, Any]] = []
    for document in edge_documents:
        edge = document["edge"]
        for account in document["accounts"]:
            flattened.append({**account, "edge": edge})
    return flattened


def aggregate_type_groups(
    edge_documents: Sequence[dict[str, Any]],
) -> list[dict[str, Any]]:
    grouped: dict[tuple[str, int], list[dict[str, Any]]] = defaultdict(list)
    for account in _flatten_accounts(edge_documents):
        grouped[(account["platform"], int(account["channel_type"]))].append(account)

    output: list[dict[str, Any]] = []
    for (platform, channel_type), accounts in sorted(grouped.items()):
        values = [
            int(account["sources"]["F"]["pristine"]["cross_day"])
            for account in accounts
        ]
        required_accounts = min(3, len(accounts))
        all_edges = {account["edge"] for account in accounts}
        required_edges = 1 if len(accounts) == 1 else 2
        recommendation = 0
        supporters: list[dict[str, Any]] = []
        for candidate in sorted({value for value in values if value > 0}, reverse=True):
            candidate_supporters = [
                account
                for account, value in zip(accounts, values)
                if value >= candidate
            ]
            if len(candidate_supporters) < required_accounts:
                continue
            if len({account["edge"] for account in candidate_supporters}) < required_edges:
                continue
            recommendation = candidate
            supporters = candidate_supporters
            break

        current_caps = [
            int(account["configured_concurrency"])
            for account in accounts
            if int(account["configured_concurrency"]) > 0
        ]
        if recommendation and current_caps:
            recommendation = min(recommendation, min(current_caps))

        model_counter: Counter[str] = Counter()
        mapping_keys: set[str] = set()
        for account in accounts:
            mapping_keys.update(account.get("model_mapping_keys", []))
            for model in account.get("models", []):
                model_counter[model["model"]] += int(model["requests"])

        if len(accounts) >= 3 and len({a["edge"] for a in supporters}) >= 2:
            confidence = "高"
        elif len(accounts) == 2 and len(all_edges) >= 2:
            confidence = "中"
        else:
            confidence = "暂定"

        output.append(
            {
                "platform": platform,
                "channel_type": channel_type,
                "account_count": len(accounts),
                "edge_count": len(all_edges),
                "values": sorted(values),
                "required_accounts": required_accounts,
                "required_edges": required_edges,
                "recommended": recommendation or None,
                "supporter_count": len(supporters),
                "supporter_edge_count": len({a["edge"] for a in supporters}),
                "supporters": [
                    {
                        "edge": account["edge"],
                        "account_id": account["account_id"],
                        "value": account["sources"]["F"]["pristine"]["cross_day"],
                    }
                    for account in sorted(
                        supporters,
                        key=lambda item: (item["edge"], item["account_id"]),
                    )
                ],
                "confidence": confidence,
                "model_mapping_keys": sorted(mapping_keys),
                "models": [
                    {"model": model, "requests": count}
                    for model, count in model_counter.most_common()
                ],
            }
        )
    return output


def _metric_label(min_seconds: int) -> str:
    return "C1" if min_seconds == 60 else f"C{min_seconds}s"


def _fmt_value(value: Any) -> str:
    return str(value) if value not in (None, "") else "-"


def _fmt_pct(value: Any) -> str:
    if value in (None, ""):
        return "-"
    numeric = float(value)
    return f"{numeric:.2f}%".replace(".00%", "%")


def _fmt_code_list(values: Sequence[Any], limit: int | None = None) -> str:
    selected = list(values[:limit] if limit is not None else values)
    if not selected:
        return "-"
    rendered = ", ".join(f"`{value}`" for value in selected)
    if limit is not None and len(values) > limit:
        rendered += f"，另有 {len(values) - limit} 个"
    return rendered


def render_report(edge_documents: Sequence[dict[str, Any]]) -> str:
    if not edge_documents:
        raise CapacityReportError("cannot render an empty edge document set")
    edge_ids = [str(document.get("edge", "")) for document in edge_documents]
    duplicate_edges = sorted(
        edge for edge, count in Counter(edge_ids).items() if count > 1
    )
    if duplicate_edges:
        raise CapacityReportError(f"duplicate edge documents: {duplicate_edges}")
    for document in edge_documents:
        if document.get("schema_version") != SCHEMA_VERSION:
            raise CapacityReportError(
                f"unsupported schema_version for edge={document.get('edge')}"
            )

    edge_documents = sorted(edge_documents, key=lambda item: item["edge"])
    accounts = sorted(
        _flatten_accounts(edge_documents),
        key=lambda item: (item["edge"], int(item["account_id"])),
    )
    groups = aggregate_type_groups(edge_documents)
    min_seconds_values = {
        int(document["meta"]["min_sustain_seconds"])
        for document in edge_documents
    }
    if len(min_seconds_values) != 1:
        raise CapacityReportError("edge documents use different min_sustain_seconds")
    min_seconds = min_seconds_values.pop()
    label = _metric_label(min_seconds)
    requested_days = min(int(doc["meta"]["requested_days"]) for doc in edge_documents)
    analysis_days = min(int(doc["meta"]["analysis_days"]) for doc in edge_documents)
    generated_utc = max(str(doc["meta"]["db_now_utc"]) for doc in edge_documents)

    lines = [
        "<!-- 由 ops/observability/edge_capacity_report.py 生成；请勿手工修改。 -->",
        "",
        f"# Edge 同类型账号持续 {min_seconds} 秒并发评估",
        "",
        f"生成时间（UTC）：`{generated_utc}`。请求回看 `{requested_days}` 天；无错认证窗口为各 Edge access/error 的共同留存下界，当前为 `{analysis_days}` 天。",
        "",
        "## 同类型建议",
        "",
        "账号按 `(platform, channel_type)` 合并。建议值表示该类型**单个账号**的默认并发起点，不是把多个账号容量相加。",
        "",
        f"| 平台 | channel_type | 样本 | 账号级 F 跨天 {label}（升序） | 独立支持 | 同类型建议 | 置信度 |",
        "|---|---:|---:|---|---|---:|---|",
    ]
    for group in groups:
        values = ", ".join(str(value) for value in group["values"])
        recommended = _fmt_value(group["recommended"])
        support = (
            f"{group['supporter_count']} 个账号 / "
            f"{group['supporter_edge_count']} 个 Edge"
        )
        confidence = group["confidence"]
        if group["account_count"] == 2:
            confidence += "：仅 2 个账号"
        lines.append(
            f"| {group['platform']} | {group['channel_type']} | "
            f"{group['account_count']} 个账号 / {group['edge_count']} 个 Edge | "
            f"`{values}` | {support} | **{recommended}** | {confidence} |"
        )

    lines.extend(
        [
            "",
            "合并规则：样本不少于 3 个时，取至少 3 个独立账号证明过的最高 `F pristine Cross-day` 值，并要求支持账号覆盖至少 2 个 Edge；只有 2 个账号时取两者共同证明值；只有 1 个账号时仅给暂定值。最终结果不超过该类型当前账号 cap 的最小值。这个规则避免低流量账号把结果拖到最小值，也避免单个高样本直接代表整类能力。",
            "",
            "## F/H 解释",
            "",
            "- `F`（Forward）区间为 `[access 完成时间 - usage.duration_ms, access 完成时间)`，近似请求真正转发给上游、占用最终账号执行槽的时间。usage 只通过稳定 request id 精确关联到 access，关联失败不做最近时间猜配。F 是保守下界，也是推荐依据。",
            "- `H`（HTTP lifecycle）区间为 `[access 完成时间 - access.latency_ms, access 完成时间)`，还包含鉴权、路由、排队、重试、failover 和响应收尾，是上界参考，不能直接当上游执行并发。",
            "",
        ]
    )

    example = max(
        accounts,
        key=lambda account: (
            int(account["sources"]["H"]["pristine"]["observed"])
            - int(account["sources"]["F"]["pristine"]["observed"]),
            int(account["sources"]["H"]["pristine"]["observed"]),
        ),
    )
    example_f = example["sources"]["F"]["pristine"]["observed"]
    example_h = example["sources"]["H"]["pristine"]["observed"]
    lines.extend(
        [
            f"示例：`{example['edge']}/id={example['account_id']}` 的单次 {label} 为 `F/H = {example_f}/{example_h}`。这表示历史上分别至少有一个 {min_seconds} 秒区间保持 F={example_f}，以及至少有一个 {min_seconds} 秒区间保持 H={example_h}；两个最大值不保证发生在同一时段，不能直接相减。推荐仍从 F 取值。",
            "",
            "## 指标口径",
            "",
            f"- `Peak`：瞬时重建峰值，可能只持续毫秒或数秒。",
            f"- `Observed {label}`：至少 1 个 pristine 区间在并发 `>=N` 下连续保持 {min_seconds} 秒。",
            f"- `Repeated {label}`：至少 3 个独立最大连续区间分别保持 {min_seconds} 秒；一段长区间仍只算 1 次。",
            f"- `Cross-day {label}`：满足 Repeated，且区间开始日期覆盖至少 2 个 UTC 日期。",
            "- `Pristine`：既无归属于该账号的最终对客失败，也无被 retry/failover 隐藏的上游或账号鉴权失败。",
            "- 错误先用稳定 request id 关联最终 access；关联后仍无法归属账号的池级错误只作平台/模型背景，不摊给账号，也不打断账号 pristine 区间。",
            "",
            "## 账号级证据",
            "",
            f"| Edge | 账号 | 平台 / channel_type | 当前 cap | Peak F/H | 单次 {label} F/H | 三次复现 F/H | 跨天 F/H | 账号级建议 | F 关联率 |",
            "|---|---|---|---:|---:|---:|---:|---:|---:|---:|",
        ]
    )
    for account in accounts:
        f_metric = account["sources"]["F"]["pristine"]
        h_metric = account["sources"]["H"]["pristine"]
        lines.append(
            f"| {account['edge']} | id={account['account_id']} | "
            f"{account['platform']} / {account['channel_type']} | "
            f"{account['configured_concurrency']} | "
            f"{f_metric['peak']} / {h_metric['peak']} | "
            f"{f_metric['observed']} / {h_metric['observed']} | "
            f"{f_metric['repeated']} / {h_metric['repeated']} | "
            f"{f_metric['cross_day']} / {h_metric['cross_day']} | "
            f"**{_fmt_value(account['recommended'])}** | "
            f"{_fmt_pct(account.get('coverage', {}).get('usage_match_pct'))} |"
        )

    kiro_groups = {
        int(group["channel_type"]): group
        for group in groups
        if group["platform"] == "kiro"
    }
    kiro_peaks = [
        account
        for account in accounts
        if account["platform"] == "kiro"
        and int(account["sources"]["F"]["pristine"]["peak"])
        >= int(account["configured_concurrency"])
    ]
    if kiro_peaks:
        kiro_peaks.sort(
            key=lambda account: (
                float(
                    account["sources"]["F"]["pristine"][
                        "peak_clean_episode_seconds"
                    ]
                ),
                account["edge"],
                account["account_id"],
            ),
            reverse=True,
        )
        lines.extend(
            [
                "",
                "## Kiro 峰值与持续值",
                "",
                "Kiro 到过当前 cap 说明瞬时观察成立，但只有持续时间达到门槛才进入 C1。下面列出峰值 pristine 持续时间最长的样本：",
                "",
                f"| Edge / 账号 | Peak F | 峰值最长 pristine 持续 | 单次 {label} | 跨天 {label} | 同类型建议 |",
                "|---|---:|---:|---:|---:|---:|",
            ]
        )
        for account in kiro_peaks[:5]:
            metric = account["sources"]["F"]["pristine"]
            group = kiro_groups.get(int(account["channel_type"]), {})
            lines.append(
                f"| {account['edge']} / id={account['account_id']} | "
                f"{metric['peak']} | {metric['peak_clean_episode_seconds']:.3f} 秒 | "
                f"{metric['observed']} | {metric['cross_day']} | "
                f"**{_fmt_value(group.get('recommended'))}** |"
            )

    lines.extend(
        [
            "",
            "## 模型证据",
            "",
            "配置映射来自当前账号快照；经验模型来自请求回看窗口内的成功 usage。原生透传账号映射为空时，经验模型不是有限白名单，未出现也不等于不支持。",
            "",
            "| 平台 / channel_type | 当前配置映射键 | 成功请求高频模型 |",
            "|---|---|---|",
        ]
    )
    for group in groups:
        top_models = [item["model"] for item in group["models"]]
        lines.append(
            f"| {group['platform']} / {group['channel_type']} | "
            f"{_fmt_code_list(group['model_mapping_keys'], 12)} | "
            f"{_fmt_code_list(top_models, 12)} |"
        )

    lines.extend(
        [
            "",
            "## 覆盖与限制",
            "",
            "| Edge | access 留存起点（UTC） | F 关联率范围 | 日志采样 | 无效 access / usage 行 |",
            "|---|---|---:|---|---:|",
        ]
    )
    for document in edge_documents:
        account_coverages = [account.get("coverage", {}) for account in document["accounts"]]
        match_values = [
            float(coverage["usage_match_pct"])
            for coverage in account_coverages
            if coverage.get("usage_match_pct") is not None
        ]
        match_range = (
            f"{_fmt_pct(min(match_values))}-{_fmt_pct(max(match_values))}"
            if match_values
            else "-"
        )
        invalid_access = sum(_int(item.get("invalid_access_rows")) for item in account_coverages)
        invalid_usage = sum(_int(item.get("invalid_usage_rows")) for item in account_coverages)
        sampling = "开启" if document["meta"]["runtime_sampling_enabled"] else "关闭"
        lines.append(
            f"| {document['edge']} | {document['meta']['access_min_utc']} | "
            f"{match_range} | {sampling} | {invalid_access} / {invalid_usage} |"
        )

    lines.extend(
        [
            "",
            "- 无错结论只能覆盖 access/error 的共同留存窗口；更早 usage 可用于模型成功证据，不能认证无错。",
            "- access 只记录最终选中账号；更早失败 hop 有时间和账号归属，但没有完整尝试时长。",
            "- 当前 cap、账号类型、channel_type、可调度状态和模型映射是生成时快照，不是历史版本。",
            "- 公开报告只用 `Edge + account_id` 标识账号，不输出账号名或邮箱；完整账号快照只保留在被 git ignore 的本地 raw JSON。",
            "- 异步日志 sink 理论上可能在队列压力下丢行，且没有历史 drop 计数证明绝对完整。",
            "- 同一类型仍可能有 tier、配额、地区、模型组合和请求时长差异；类型建议是默认起点，不替代真实请求分档升压。",
            "",
            "本报告只读生成，没有修改任何线上账号或并发配置。",
            "",
        ]
    )
    return "\n".join(lines)


def _validate_document(document: dict[str, Any], expected_edge: str | None = None) -> None:
    if document.get("schema_version") != SCHEMA_VERSION:
        raise CapacityReportError("probe returned unsupported schema_version")
    if expected_edge is not None and document.get("edge") != expected_edge:
        raise CapacityReportError(
            f"probe edge mismatch: expected={expected_edge} got={document.get('edge')}"
        )
    if not isinstance(document.get("accounts"), list):
        raise CapacityReportError("probe result has no accounts array")


def _resolve_edges(repo_root: Path, raw_edges: str) -> list[str]:
    if raw_edges != "auto":
        edges = [item.strip() for item in raw_edges.split(",") if item.strip()]
    else:
        proc = subprocess.run(
            [
                "python3",
                str(repo_root / "deploy/aws/stage0/resolve-edge-target.py"),
                "--list-deployable",
            ],
            cwd=repo_root,
            check=True,
            capture_output=True,
            text=True,
        )
        edges = [line.strip() for line in proc.stdout.splitlines() if line.strip()]
    if not edges:
        raise CapacityReportError("no deployable edges resolved")
    invalid = [edge for edge in edges if not re.fullmatch(r"[a-z]{2,4}[0-9]+", edge)]
    if invalid:
        raise CapacityReportError(f"invalid edge ids: {invalid}")
    return sorted(set(edges))


def _poll_ssm_invocation(
    region: str,
    instance_id: str,
    command_id: str,
    timeout_seconds: int,
) -> str:
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        proc = subprocess.run(
            [
                "aws",
                "ssm",
                "get-command-invocation",
                "--region",
                region,
                "--command-id",
                command_id,
                "--instance-id",
                instance_id,
                "--output",
                "json",
            ],
            check=True,
            capture_output=True,
            text=True,
        )
        payload = json.loads(proc.stdout)
        status = payload.get("Status")
        if status == "Success":
            return payload.get("StandardOutputContent", "")
        if status not in {"Pending", "InProgress", "Delayed"}:
            stderr = payload.get("StandardErrorContent", "")
            raise CapacityReportError(
                f"SSM command failed status={status}: {stderr[-2000:]}"
            )
        time.sleep(5)
    raise CapacityReportError(f"SSM command timed out command_id={command_id}")


def _ssm_command_is_resumable(stderr: str) -> bool:
    return re.search(r"\bstatus=(?:Pending|InProgress|Delayed)\b", stderr) is not None


def _collect_edge(
    repo_root: Path,
    edge: str,
    days: int,
    min_seconds: int,
    timeout_seconds: int,
) -> dict[str, Any]:
    run_probe = repo_root / "ops/observability/run-probe.sh"
    probe = repo_root / "ops/observability/probe-edge-capacity.sh"
    analyzer = Path(__file__).resolve()
    cmd = [
        "bash",
        str(run_probe),
        "--target",
        f"edge:{edge}",
        "--script",
        str(probe),
        "--with",
        str(analyzer),
        "--env",
        f"EDGE_ID={edge}",
        "--env",
        f"DAYS={days}",
        "--env",
        f"MIN_SECONDS={min_seconds}",
        "--timeout-seconds",
        str(timeout_seconds),
        "--comment",
        f"read-only edge capacity report {edge}",
    ]
    proc = subprocess.run(
        cmd,
        cwd=repo_root,
        capture_output=True,
        text=True,
        timeout=max(180, timeout_seconds + 30),
    )
    stdout = proc.stdout
    if proc.returncode != 0:
        resolved = re.search(
            r"resolved region=(\S+) instance_id=(\S+)", proc.stderr
        )
        command = re.search(r"command_id=([0-9a-f-]+)", proc.stderr)
        if resolved and command and _ssm_command_is_resumable(proc.stderr):
            stdout = _poll_ssm_invocation(
                resolved.group(1),
                resolved.group(2),
                command.group(1),
                timeout_seconds,
            )
        else:
            raise CapacityReportError(
                f"edge collection failed edge={edge} rc={proc.returncode}: "
                f"{proc.stderr[-4000:]}"
            )
    try:
        document = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise CapacityReportError(
            f"edge={edge} returned invalid JSON: {stdout[-2000:]}"
        ) from exc
    _validate_document(document, edge)
    return document


def _write_text_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(
        "w", encoding="utf-8", dir=path.parent, delete=False
    ) as handle:
        handle.write(content)
        temporary = Path(handle.name)
    os.replace(temporary, path)


def _command_analyze(args: argparse.Namespace) -> int:
    document = analyze_edge(args.edge, args.days, args.min_seconds)
    encoded = json.dumps(document, ensure_ascii=True, separators=(",", ":"))
    payload_bytes = len(encoded.encode("utf-8"))
    if payload_bytes > 22_000:
        raise CapacityReportError(
            f"probe JSON exceeds SSM stdout budget: bytes={payload_bytes} limit=22000"
        )
    sys.stdout.write(encoded + "\n")
    return 0


def _command_collect(args: argparse.Namespace) -> int:
    repo_root = _repo_root()
    edges = _resolve_edges(repo_root, args.edges)
    documents: list[dict[str, Any]] = []
    raw_dir = Path(args.raw_dir).resolve() if args.raw_dir else None
    if raw_dir:
        raw_dir.mkdir(parents=True, exist_ok=True)
    for edge in edges:
        print(f"collect edge={edge} status=starting", file=sys.stderr)
        document = _collect_edge(
            repo_root,
            edge,
            args.days,
            args.min_seconds,
            args.timeout_seconds,
        )
        documents.append(document)
        if raw_dir:
            _write_text_atomic(
                raw_dir / f"{edge}.json",
                json.dumps(document, ensure_ascii=False, indent=2) + "\n",
            )
        print(
            f"collect edge={edge} status=success accounts={len(document['accounts'])}",
            file=sys.stderr,
        )

    report = render_report(documents)
    output = Path(args.output)
    if not output.is_absolute():
        output = repo_root / output
    _write_text_atomic(output, report)
    for group in aggregate_type_groups(documents):
        print(
            "type_recommendation "
            f"platform={group['platform']} channel_type={group['channel_type']} "
            f"recommended={_fmt_value(group['recommended'])} "
            f"supporters={group['supporter_count']} edges={group['supporter_edge_count']}"
        )
    print(f"report_path={output}")
    return 0


def _parse_inputs(values: Sequence[str]) -> list[dict[str, Any]]:
    documents: list[dict[str, Any]] = []
    for value in values:
        if "=" not in value:
            raise CapacityReportError(f"--input must be EDGE=PATH, got: {value}")
        edge, raw_path = value.split("=", 1)
        with Path(raw_path).open(encoding="utf-8") as handle:
            document = json.load(handle)
        _validate_document(document, edge)
        documents.append(document)
    return documents


def _command_render(args: argparse.Namespace) -> int:
    documents = _parse_inputs(args.input)
    output = Path(args.output)
    if not output.is_absolute():
        output = _repo_root() / output
    _write_text_atomic(output, render_report(documents))
    print(f"report_path={output}")
    return 0


def _positive_int(raw: str) -> int:
    value = int(raw)
    if value <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return value


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    analyze = subparsers.add_parser("analyze", help="analyze one edge on the remote host")
    analyze.add_argument("--edge", required=True)
    analyze.add_argument("--days", type=_positive_int, default=DEFAULT_DAYS)
    analyze.add_argument(
        "--min-seconds", type=_positive_int, default=DEFAULT_MIN_SECONDS
    )
    analyze.set_defaults(func=_command_analyze)

    collect = subparsers.add_parser(
        "collect", help="collect all selected edges and update the Markdown report"
    )
    collect.add_argument(
        "--edges",
        default="auto",
        help="comma-separated edge ids or auto for all deployable edges",
    )
    collect.add_argument("--days", type=_positive_int, default=DEFAULT_DAYS)
    collect.add_argument(
        "--min-seconds", type=_positive_int, default=DEFAULT_MIN_SECONDS
    )
    collect.add_argument("--output", default=DEFAULT_REPORT_PATH)
    collect.add_argument("--raw-dir")
    collect.add_argument("--timeout-seconds", type=_positive_int, default=600)
    collect.set_defaults(func=_command_collect)

    render = subparsers.add_parser(
        "render", help="render a report from previously collected edge JSON"
    )
    render.add_argument(
        "--input", action="append", required=True, metavar="EDGE=PATH"
    )
    render.add_argument("--output", default=DEFAULT_REPORT_PATH)
    render.set_defaults(func=_command_render)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    try:
        return int(args.func(args))
    except CapacityReportError as exc:
        print(f"edge_capacity_report: ERROR: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
