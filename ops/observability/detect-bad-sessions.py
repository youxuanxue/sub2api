#!/usr/bin/env python3
"""Detect sessions whose requests repeatedly fail or stall.

The live probe joins three existing, read-only surfaces:

* ``ops_error_logs`` for final failures and provider attribution;
* ``usage_logs`` for successful requests and explicit client session ids;
* gateway JSON logs for the request-id -> session-hash correlation.

Usage (read-only, via the existing SSM transport):
    bash ops/observability/run-probe.sh --target prod --compressed-output \
        --script ops/observability/probe-bad-sessions.sh --env WINDOW_MINUTES=180

Optional env: USER_ID, API_KEY_ID, PLATFORM, MODEL, MIN_FAILURES (3),
MIN_CONSECUTIVE (3), MIN_CANCEL_LATENCY_MS (30000), MAX_ROWS (50000; max 500000).
LOG_DIR optionally reads retained sub2api*.log[.gz] on the host instead of only
the current container. For prod this is /var/lib/tokenkey/app/logs after verifying
the live mount. UNTIL_UTC fixes an ISO timestamp for reproducible multi-user scans.
REPORT_PATH optionally saves the full report to a new mode-0600 host file and
returns only summaries of the top 20 candidates, fitting SSM inline transport.
Run separately on prod or an edge; request ids are not merged across hosts.
Read deploy/hetzner/README.md before choosing a live target. The report uses UTC.
A candidate can include a later recovery; inspect its ordered request timeline.
HTTP 200 stream failures without a terminal error record are not detected.
A partial report means missing correlation or row limits affect coverage.
If any row limit is reached, no candidates are emitted: narrow the window/scope.
Uncorrelated events with known user/key break that key's failure runs conservatively.
Candidates are observed failure runs, not proof of a dead conversation or root
cause. Missing session identities and retained logs limit recall. A billed usage
row may describe partial output; a matching terminal error always takes precedence.
Investigate candidate request ids with probe-tail-gateway-logs.sh and
ops-error-triage.sh on the same target/window. Do not infer absence of response
text from 499 or a long duration. No production validation is implied.

It deliberately never prints request bodies.  The pure functions in
this file are also used by the fixture tests, so the detection policy stays
deterministic and reviewable without a live host.
"""

from __future__ import annotations

import bisect
import collections
import datetime as dt
import hashlib
import gzip
import itertools
import json
import os
import pathlib
import re
import statistics
import subprocess
import sys
import tempfile
from typing import Any, Iterable


DEFAULT_WINDOW_MINUTES = 180
DEFAULT_MIN_FAILURES = 3
DEFAULT_MIN_CONSECUTIVE = 3
DEFAULT_MIN_CANCEL_LATENCY_MS = 30_000
DEFAULT_MAX_ROWS = 50_000

_CANCEL_RE = re.compile(r"(?:cancel|canceled|cancelled|context canceled|client disconnected|broken pipe)", re.I)
_NON_ACTIONABLE_RE = re.compile(
    r"(?:invalid[_ -]?api[_ -]?key|api[_ -]?key[_ -]?required|insufficient[_ -]?(?:balance|quota)|user[_ -]?(?:inactive|not[_ -]?found)|group[_ -]?(?:deleted|disabled))",
    re.I,
)


def _text(value: Any, limit: int = 240) -> str:
    text = re.sub(r"[\x00-\x1f\x7f]+", " ", str(value or "")).strip()
    return text[:limit]


def _int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def _time(value: Any) -> dt.datetime | None:
    if isinstance(value, dt.datetime):
        return value if value.tzinfo else value.replace(tzinfo=dt.timezone.utc)
    raw = _text(value, 64)
    if not raw:
        return None
    try:
        parsed = dt.datetime.fromisoformat(raw.replace("Z", "+00:00"))
        return parsed if parsed.tzinfo else parsed.replace(tzinfo=dt.timezone.utc)
    except ValueError:
        return None


def short_session_ref(value: str) -> str:
    """Return a stable, non-secret correlation reference."""
    value = _text(value, 512)
    if not value:
        return ""
    if len(value) <= 16 and re.fullmatch(r"[0-9a-fA-F-]+", value):
        return value[:16]
    return hashlib.sha256(value.encode("utf-8")).hexdigest()[:16]


def parse_gateway_log_lines(lines: Iterable[str]) -> dict[str, dict[str, Any]]:
    """Map request ids to the session hash emitted by gateway request logs."""
    by_request: dict[str, dict[str, Any]] = {}
    for line in lines:
        line = line.strip()
        if not line:
            continue
        # Docker logs may prefix a timestamp; the final JSON object is stable.
        start = line.find("{")
        if start < 0:
            continue
        try:
            event = json.loads(line[start:])
        except json.JSONDecodeError:
            continue
        if not isinstance(event, dict):
            continue
        request_id = _text(event.get("request_id"), 128)
        if not request_id:
            continue
        message = _text(event.get("msg") or event.get("message"), 96)
        item = by_request.setdefault(request_id, {})
        if not message:
            for marker in ("sticky.session_hash_generated", "http request completed"):
                if marker in line[:start]:
                    message = marker
                    break
        if message == "sticky.session_hash_generated" and event.get("session_hash"):
            item["session_hash"] = _text(event["session_hash"], 256)
        if event.get("account_id"):
            item["account_id"] = _int(event["account_id"])
        for key in ("user_id", "api_key_id"):
            if event.get(key):
                item[key] = _int(event[key])
        for key in ("platform", "model"):
            if event.get(key):
                item[key] = _text(event[key], 100)
        if message == "http request completed":
            item["access_status"] = _int(event.get("status_code"))
            item["access_latency_ms"] = _int(event.get("latency_ms"))
            item["completed_at"] = _text(event.get("completed_at"), 64)
    return by_request


def _failure_class(row: dict[str, Any]) -> str:
    status = _int(row.get("status_code"))
    upstream = _int(row.get("upstream_status_code"))
    message = " ".join(
        _text(row.get(key), 160)
        for key in ("error_type", "error_message", "upstream_error_message", "network_error_type")
    )
    if (status == 499 and upstream == 0) or (upstream == 0 and _CANCEL_RE.search(message)):
        return "client_cancel"
    if upstream >= 400 or row.get("error_owner") == "provider":
        return "upstream_failure"
    if status >= 500 or row.get("error_owner") == "platform":
        return "platform_failure"
    if row.get("error_phase") == "routing":
        return "routing_failure"
    return "request_failure"


def _actionable_failure(row: dict[str, Any], min_cancel_latency_ms: int) -> bool:
    status = _int(row.get("status_code"))
    upstream = _int(row.get("upstream_status_code"))
    if _failure_class(row) == "client_cancel":
        latency = max(_int(row.get("response_latency_ms")), _int(row.get("access_latency_ms")))
        return latency >= min_cancel_latency_ms
    # Similar messages describe both user errors and broken supplier credentials.
    if upstream >= 400 or row.get("error_owner") == "provider" or status >= 500:
        return True
    if _NON_ACTIONABLE_RE.search(" ".join(_text(row.get(k), 120) for k in ("error_type", "error_message"))):
        return False
    return status == 429


def _request_id(value: Any) -> str:
    # gateway_usage_billing.go stores server request ids with this billing prefix.
    return _text(value, 128).removeprefix("local:")


def _session_for(row: dict[str, Any], logs: dict[str, dict[str, Any]]) -> tuple[str, str]:
    request_id = _request_id(row.get("request_id"))
    log = logs.get(request_id, {})
    if log.get("session_hash"):
        return "session_hash", short_session_ref(log["session_hash"])
    if row.get("session_id"):
        return "client_session_id", short_session_ref(str(row["session_id"]))
    return "", ""


def build_events(
    errors: Iterable[dict[str, Any]],
    successes: Iterable[dict[str, Any]],
    logs: dict[str, dict[str, Any]],
    min_cancel_latency_ms: int = DEFAULT_MIN_CANCEL_LATENCY_MS,
) -> tuple[dict[str, list[dict[str, Any]]], dict[str, int]]:
    grouped: dict[str, list[dict[str, Any]]] = collections.defaultdict(list)
    errors = list(errors)
    failed_ids = {_request_id(row.get("request_id")) for row in errors if row.get("request_id")}
    seen = set()
    gaps: dict[tuple[int, int], list[dt.datetime]] = collections.defaultdict(list)

    def identity(row):
        return (_int(row.get("user_id")), _int(row.get("api_key_id")))

    def remember_gap(row):
        tenant = identity(row)
        at = _time(row.get("created_at"))
        if min(tenant) > 0 and at is not None:
            gaps[tenant].append(at)

    def group_key(row, ref):
        # Client supplied session identifiers are not globally unique.
        return f"{_int(row.get('user_id'))}:{_int(row.get('api_key_id'))}:{ref}"

    def enrich(raw):
        rid = _request_id(raw.get("request_id"))
        log = logs.get(rid, {})
        return {**log, **{k: v for k, v in raw.items() if v is not None}, "request_id": rid}

    coverage = {"error_rows": 0, "error_rows_with_session": 0, "success_rows": 0, "success_rows_with_session": 0}
    for raw in errors:
        row = enrich(raw)
        rid = row.get("request_id")
        if rid and rid in seen:
            continue
        if rid:
            seen.add(rid)
        coverage["error_rows"] += 1
        kind, ref = _session_for(row, logs)
        if not ref or min(identity(row)) <= 0:
            remember_gap(row)
            continue
        coverage["error_rows_with_session"] += 1
        row.update(
            event_kind="failure",
            failure_class=_failure_class(row),
            actionable=_actionable_failure(row, min_cancel_latency_ms),
            session_kind=kind,
            session_ref=ref,
            at=_time(row.get("created_at")),
        )
        grouped[group_key(row, ref)].append(row)
    for raw in successes:
        row = enrich(raw)
        rid = row.get("request_id")
        if rid and (rid in failed_ids or rid in seen):
            continue
        if rid:
            seen.add(rid)
        coverage["success_rows"] += 1
        kind, ref = _session_for(row, logs)
        if not ref or min(identity(row)) <= 0:
            remember_gap(row)
            continue
        if kind == "client_session_id" and group_key(row, ref) not in grouped:
            # No evidence equates this client id to a gateway hash. Treat it as
            # a possible intervening success, rather than inventing an alias.
            remember_gap(row)
            continue
        coverage["success_rows_with_session"] += 1
        row.update(event_kind="success", actionable=False, session_kind=kind, session_ref=ref, at=_time(row.get("created_at")))
        grouped[group_key(row, ref)].append(row)
    for times in gaps.values():
        times.sort()
    for events in grouped.values():
        events.sort(key=lambda event: (event.get("at") or dt.datetime.min.replace(tzinfo=dt.timezone.utc), _text(event.get("request_id"), 128)))
        times = gaps.get(identity(events[0]), [])
        previous_epoch = 0
        for event in events:
            at = event.get("at") or dt.datetime.min.replace(tzinfo=dt.timezone.utc)
            epoch = bisect.bisect_right(times, at)
            event["break_before"] = epoch != previous_epoch or bisect.bisect_left(times, at) < epoch
            previous_epoch = epoch
    coverage["uncorrelated_breakers"] = sum(len(times) for times in gaps.values())
    return dict(grouped), coverage


def access_log_failures(logs: dict[str, dict[str, Any]]) -> list[dict[str, Any]]:
    """Turn uncaptured non-2xx access rows into conservative failure events."""
    rows = []
    for request_id, log in logs.items():
        status = _int(log.get("access_status"))
        if status < 400:
            continue
        rows.append(
            {
                "request_id": request_id,
                "status_code": status,
                "response_latency_ms": _int(log.get("access_latency_ms")),
                "created_at": log.get("completed_at"),
                "account_id": log.get("account_id"),
                "user_id": log.get("user_id"),
                "api_key_id": log.get("api_key_id"),
                "platform": log.get("platform", ""),
                "model": log.get("model", ""),
                "error_type": "access_log_failure",
                "error_message": "",
            }
        )
    return rows


def _hypotheses(events: list[dict[str, Any]]) -> list[str]:
    failures = [event for event in events if event.get("event_kind") == "failure" and event.get("actionable")]
    accounts = {_int(event.get("account_id")) for event in failures if _int(event.get("account_id")) > 0}
    classes = {event.get("failure_class") for event in failures}
    hints: list[str] = []
    if classes == {"client_cancel"}:
        hints.append("upstream_or_gateway_stall_before_client_disconnect")
    elif "upstream_failure" in classes:
        hints.append("provider_or_account_failure")
    if len(accounts) == 1:
        hints.append("same_account_repeatedly_selected")
    elif len(accounts) > 1:
        hints.append("failures_span_multiple_accounts")
    if len(accounts) == 0:
        hints.append("account_attribution_missing")
    return hints


def detect_candidates(
    grouped: dict[str, list[dict[str, Any]]],
    min_failures: int = DEFAULT_MIN_FAILURES,
    min_consecutive: int = DEFAULT_MIN_CONSECUTIVE,
) -> list[dict[str, Any]]:
    candidates: list[dict[str, Any]] = []
    for events in grouped.values():
        run = longest = 0
        for event in events:
            if event.get("break_before"):
                run = 0
            if event.get("event_kind") == "failure" and event.get("actionable"):
                run += 1
                longest = max(longest, run)
            else:
                run = 0
        failures = [event for event in events if event.get("event_kind") == "failure" and event.get("actionable")]
        if len(failures) < min_failures or longest < min_consecutive:
            continue
        latencies = [
            max(_int(event.get("response_latency_ms")), _int(event.get("access_latency_ms")))
            for event in failures
            if max(_int(event.get("response_latency_ms")), _int(event.get("access_latency_ms"))) > 0
        ]
        classes = collections.Counter(event.get("failure_class", "unknown") for event in failures)
        accounts = sorted({_int(event.get("account_id")) for event in failures if _int(event.get("account_id")) > 0})
        candidate = {
            "session_ref": events[0]["session_ref"],
            "user_id": _int(events[0].get("user_id")) or None,
            "api_key_id": _int(events[0].get("api_key_id")) or None,
            "session_kind": next((event.get("session_kind") for event in events if event.get("session_kind")), "unknown"),
            "request_count": len(events),
            "failure_count": len(failures),
            "max_consecutive_failures": longest,
            "failure_classes": dict(classes),
            "accounts": accounts,
            "platforms": sorted({_text(event.get("platform"), 48) for event in failures if _text(event.get("platform"), 48)}),
            "models": sorted({_text(event.get("model"), 100) for event in failures if _text(event.get("model"), 100)}),
            "first_at": (events[0].get("at").isoformat() if events and events[0].get("at") else None),
            "last_at": (events[-1].get("at").isoformat() if events and events[-1].get("at") else None),
            "median_latency_ms": int(statistics.median(latencies)) if latencies else 0,
            "hypotheses": _hypotheses(events),
            "requests": [
                {
                    "at": event.get("at").isoformat() if event.get("at") else None,
                    "request_id": _text(event.get("request_id"), 128),
                    "status_code": _int(event.get("status_code")) if event.get("event_kind") == "failure" else 200,
                    "failure_class": event.get("failure_class") if event.get("event_kind") == "failure" else "success",
                    "account_id": _int(event.get("account_id")) or None,
                    "model": _text(event.get("model"), 100),
                    "latency_ms": max(_int(event.get("response_latency_ms")), _int(event.get("duration_ms")), _int(event.get("access_latency_ms"))) or None,
                    "error_type": _text(event.get("error_type"), 96) if event.get("event_kind") == "failure" else "",
                    "actionable": bool(event.get("actionable")),
                    "correlation_gap_before": bool(event.get("break_before")),
                }
                for event in events
            ],
        }
        candidates.append(candidate)
    candidates.sort(key=lambda item: (-item["max_consecutive_failures"], -item["failure_count"], item["first_at"] or ""))
    return candidates


def _sql_literal(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def _run_json_query(sql: str, container: str) -> list[dict[str, Any]]:
    command = ["docker", "exec", container, "psql", "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", "SET default_transaction_read_only=on; SET statement_timeout=\'30s\'; " + sql]
    result = subprocess.run(command, capture_output=True, text=True, check=False, timeout=40)
    if result.returncode:
        raise RuntimeError(_text(result.stderr or result.stdout, 500))
    rows: list[dict[str, Any]] = []
    for line in result.stdout.splitlines():
        line = line.strip()
        if not line or line == "SET":
            continue
        value = json.loads(line)
        if isinstance(value, dict):
            rows.append(value)
    return rows


def _resolver_module():
    resolver = os.environ.get("TK_RESOLVER_PY", "/tmp/resolve_app_container.py")
    if not os.path.isfile(resolver):
        local = os.path.join(os.path.dirname(__file__), "..", "lib", "resolve_app_container.py")
        if os.path.isfile(local):
            resolver = local
    if os.path.isfile(resolver):
        import importlib.util

        spec = importlib.util.spec_from_file_location("tk_resolve_app_container", resolver)
        if spec and spec.loader:
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            return module
    return None


def _resolve_container(value: str) -> str:
    module = _resolver_module()
    if module is not None:
        container, _ = module.resolve(value or "auto")
        if container:
            return container
        raise RuntimeError("app container unresolved; refusing to guess")
    if value and value != "auto":
        return value
    raise RuntimeError("canonical app-container resolver not found")


def _resolve_postgres_container(value: str) -> str:
    module = _resolver_module()
    if module is None:
        raise RuntimeError("canonical container resolver not found")
    container = module.resolve_postgres(value or "auto")
    if container:
        return container
    raise RuntimeError("postgres container unresolved; refusing to guess")


def _docker_log_lines(container: str, since: dt.datetime, until: dt.datetime) -> Iterable[str]:
    # Spool privately instead of keeping all raw gateway traffic in memory.
    with tempfile.TemporaryFile(mode="w+t", encoding="utf-8") as output:
        result = subprocess.run(
            ["docker", "logs", container, "--since", since.isoformat(), "--until", until.isoformat()],
            stdout=output, stderr=subprocess.STDOUT, check=False, timeout=90,
        )
        if result.returncode:
            raise RuntimeError("docker logs failed; check the selected container")
        output.seek(0)
        yield from output


_LOG_TIME_RE = re.compile(r"20\d{2}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})")


def _retained_log_lines(directory: str, since: dt.datetime, until: dt.datetime, coverage: dict) -> Iterable[str]:
    paths = sorted(pathlib.Path(directory).glob("sub2api*.log*"))
    paths = [p for p in paths if p.is_file() and (p.name.endswith(".log") or p.name.endswith(".log.gz"))]
    if not paths:
        raise RuntimeError("LOG_DIR has no retained sub2api logs")
    first_at = None
    files_read = 0
    for path in paths:
        opener = gzip.open if path.name.endswith(".gz") else open
        with opener(path, "rt", encoding="utf-8", errors="replace") as source:
            # The first timestamp describes retention, including files starting
            # before the requested window. Never return raw lines in the report.
            first = source.readline()
            match = _LOG_TIME_RE.search(first)
            at = _time(match.group(0)) if match else None
            if at is not None:
                first_at = min(first_at, at) if first_at else at
            if at is not None and at >= until:
                continue
            if dt.datetime.fromtimestamp(path.stat().st_mtime, dt.timezone.utc) < since:
                continue
            files_read += 1
            for line in itertools.chain((first,), source):
                if "sticky.session_hash_generated" not in line and "http request completed" not in line:
                    continue
                match = _LOG_TIME_RE.search(line)
                at = _time(match.group(0)) if match else None
                if at is not None and since <= at < until:
                    yield line
    coverage.update(log_source="retained_files", log_files_read=files_read,
                    retained_logs_since_utc=first_at.isoformat() if first_at else None,
                    log_window_incomplete=first_at is None or first_at > since)


def live_report() -> dict[str, Any]:
    window = max(1, min(_int(os.environ.get("WINDOW_MINUTES", DEFAULT_WINDOW_MINUTES)), 10_080))
    min_failures = max(1, _int(os.environ.get("MIN_FAILURES", DEFAULT_MIN_FAILURES)))
    min_consecutive = max(1, _int(os.environ.get("MIN_CONSECUTIVE", DEFAULT_MIN_CONSECUTIVE)))
    cancel_latency = max(0, _int(os.environ.get("MIN_CANCEL_LATENCY_MS", DEFAULT_MIN_CANCEL_LATENCY_MS)))
    max_rows = max(100, min(_int(os.environ.get("MAX_ROWS", DEFAULT_MAX_ROWS)), 500_000))
    container = None if os.environ.get("LOG_DIR") else _resolve_container(os.environ.get("CONTAINER", "auto"))
    until_at = dt.datetime.now(dt.timezone.utc)
    if os.environ.get("UNTIL_UTC"):
        requested_until = _time(os.environ["UNTIL_UTC"])
        if requested_until is None or requested_until > until_at:
            raise ValueError("UNTIL_UTC must be a valid ISO timestamp at or before now")
        until_at = requested_until
    since_at = until_at - dt.timedelta(minutes=window)
    since = _sql_literal(since_at.isoformat()) + "::timestamptz"
    until = _sql_literal(until_at.isoformat()) + "::timestamptz"
    for name in ("USER_ID", "API_KEY_ID"):
        if os.environ.get(name) and (not os.environ[name].isdigit() or int(os.environ[name]) <= 0):
            raise ValueError(f"{name} must be a positive integer")
    filters = []
    if os.environ.get("USER_ID", "").isdigit():
        filters.append(f"user_id = {int(os.environ['USER_ID'])}")
    if os.environ.get("API_KEY_ID", "").isdigit():
        filters.append(f"api_key_id = {int(os.environ['API_KEY_ID'])}")
    for name, column in (("PLATFORM", "platform"), ("MODEL", "model")):
        if os.environ.get(name, "").strip():
            filters.append(f"{column} = {_sql_literal(os.environ[name].strip())}")
    scope = (" AND " + " AND ".join(filters)) if filters else ""
    usage_filters = []
    for item in filters:
        if item.startswith("platform = "):
            usage_filters.append("COALESCE(a.platform, '') = " + item.split("=", 1)[1].strip())
        else:
            usage_filters.append("ul." + item)
    usage_scope = (" AND " + " AND ".join(usage_filters)) if usage_filters else ""
    error_sql = f"""
SELECT row_to_json(t)::text FROM (
 SELECT id, request_id, user_id, api_key_id, account_id, platform, model,
        status_code, upstream_status_code, error_owner, error_phase, error_type,
        error_message, upstream_error_message, response_latency_ms,
        created_at
 FROM ops_error_logs
 WHERE created_at >= {since} AND created_at < {until} AND COALESCE(status_code, 0) >= 400 {scope}
 ORDER BY created_at ASC LIMIT {max_rows + 1}
) t;
"""
    success_sql = f"""
SELECT row_to_json(t)::text FROM (
 SELECT ul.request_id, ul.user_id, ul.api_key_id, ul.account_id, ul.model, ul.session_id,
        ul.duration_ms, ul.created_at
 FROM usage_logs ul
 LEFT JOIN accounts a ON a.id = ul.account_id AND a.deleted_at IS NULL
 WHERE ul.created_at >= {since} AND ul.created_at < {until} {usage_scope}
 ORDER BY ul.created_at ASC LIMIT {max_rows + 1}
) t;
"""
    postgres = _resolve_postgres_container(os.environ.get("POSTGRES_CONTAINER", "auto"))
    errors = _run_json_query(error_sql, postgres)
    successes = _run_json_query(success_sql, postgres)
    log_coverage: dict[str, Any] = {"log_source": "docker"}
    if os.environ.get("LOG_DIR"):
        log_lines = _retained_log_lines(os.environ["LOG_DIR"], since_at, until_at, log_coverage)
    else:
        log_lines = _docker_log_lines(container, since_at, until_at)
    logs = parse_gateway_log_lines(log_lines)
    def in_scope(log):
        at = _time(log.get("completed_at"))
        if not at or not since_at <= at < until_at:
            return False
        for env, column in (("USER_ID", "user_id"), ("API_KEY_ID", "api_key_id"), ("PLATFORM", "platform"), ("MODEL", "model")):
            if os.environ.get(env) and str(log.get(column, "")) != os.environ[env].strip():
                return False
        return True

    scoped_logs = {rid: log for rid, log in logs.items() if in_scope(log)}
    error_request_ids = {_request_id(row.get("request_id")) for row in errors}
    access_rows = [row for row in access_log_failures(scoped_logs) if row["request_id"] not in error_request_ids]
    errors.extend(access_rows)
    # A wire 2xx without a recorded failure breaks a run conservatively. It is
    # not proof that a stream completed or that the user received useful output.
    usage_ids = {_request_id(row.get("request_id")) for row in successes}
    for rid, log in scoped_logs.items():
        if 200 <= _int(log.get("access_status")) < 300 and rid not in error_request_ids and rid not in usage_ids:
            successes.append({**log, "request_id": rid, "created_at": log.get("completed_at")})
    grouped, coverage = build_events(errors[:max_rows], successes[:max_rows], logs, cancel_latency)
    truncated = len(errors) > max_rows or len(successes) > max_rows
    # Dropping a successful request can fabricate a consecutive failure run.
    candidates = [] if truncated else detect_candidates(grouped, min_failures, min_consecutive)
    return {
        "schema_version": 1,
        "window_minutes": window,
        "since_utc": since_at.isoformat(),
        "until_utc": until_at.isoformat(),
        "thresholds": {"min_failures": min_failures, "min_consecutive": min_consecutive, "min_cancel_latency_ms": cancel_latency},
        "scope": {key.lower(): _text(os.environ.get(key), 120) for key in ("USER_ID", "API_KEY_ID", "PLATFORM", "MODEL") if os.environ.get(key)},
        "coverage": coverage | log_coverage | {"gateway_log_request_ids": len(logs), "access_only_failures": len(access_rows), "error_rows_truncated": len(errors) > max_rows, "success_rows_truncated": len(successes) > max_rows},
        "status": "partial" if log_coverage.get("log_window_incomplete") or len(errors) > max_rows or len(successes) > max_rows or (coverage["error_rows_with_session"] < coverage["error_rows"] or coverage["success_rows_with_session"] < coverage["success_rows"]) else ("candidates_found" if candidates else "no_candidates"),
        "candidate_count": len(candidates),
        "candidates": candidates,
    }


def main() -> int:
    try:
        report = live_report()
        if os.environ.get("REPORT_PATH"):
            report_path = os.environ["REPORT_PATH"]
            fd = os.open(report_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as output:
                json.dump(report, output, ensure_ascii=False)
            report = {
                **report,
                "report_path": report_path,
                "candidates_omitted": max(0, len(report["candidates"]) - 20),
                "candidates": [{k: v for k, v in candidate.items() if k != "requests"}
                               for candidate in report["candidates"][:20]],
            }
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 0
    except Exception as exc:  # probe failures must be visible and non-zero
        print(json.dumps({"schema_version": 1, "status": "error", "error": _text(exc, 500)}, ensure_ascii=False), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
