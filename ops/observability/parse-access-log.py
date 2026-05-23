#!/usr/bin/env python3
"""parse-access-log.py — Parse TokenKey gateway "http request completed" JSON
log lines into a stable aggregation. Replaces the 30-line Python heredoc that
previously lived inside the tokenkey-online-log-troubleshooting skill §5.

Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
  - All output is a single JSON object on stdout — keys stable, field names
    embedded next to values. Downstream parsers must use json.loads, never
    grep+column.
  - Sort order is deterministic (descending by count, then ascending by key).
  - No locale-sensitive number formatting; latency is integer milliseconds.

Input modes (choose one):
  --stdin                Read raw docker logs (or any text stream) from stdin.
                         Each line is scanned for the "http request completed"
                         marker and a trailing {...} JSON blob. Lines without
                         that shape are silently skipped (we may also see
                         "sticky.scheduler_entry" etc. interleaved).
  --file PATH            Same as --stdin but reads PATH.
  --docker CONTAINER     Spawn `docker logs CONTAINER --since SINCE` and parse
                         its stdout. Requires --since.

Filters:
  --path PATH            Only count rows whose JSON `path` equals PATH.
                         (Empty = no filter.)
  --model MODEL          Only count rows whose JSON `model` equals MODEL.
  --status-min N         Only count rows whose status_code >= N (for the
                         `bad` bucket); does NOT restrict the histogram.

Aggregation knobs:
  --top-minutes N        Cap minute histogram rows (default 30).
  --top-models N         Cap (model, status) histogram rows (default 30).
  --markers a,b,c        Comma-separated substrings to count anywhere in the
                         raw line (NOT in the JSON). Default markers cover the
                         well-known anthropic/openai resiliency signals.

Output JSON shape:
  {
    "input": {...meta...},
    "totals": {"lines_seen": N, "lines_parsed": N, "lines_skipped": N},
    "status_counts": {"200": N, "503": N, ...},
    "by_minute": [{"minute_utc": "...Z", "status_code": 200, "n": N}, ...],
    "by_model_status": [{"model": "...", "status_code": 200, "n": N}, ...],
    "markers": {"GROUP_RPM_EXCEEDED": N, ...},
    "latency_ms": {"n": N, "p50": N, "p90": N, "p95": N, "p99": N, "max": N}
                  or null if no `latency_ms` field present in any parsed row
  }

Exit codes:
  0 — parse completed (even with zero parsed rows; check totals.lines_parsed)
  2 — usage / I/O failure
"""
from __future__ import annotations

import argparse
import collections
import json
import re
import subprocess
import sys
from typing import Iterable

DEFAULT_MARKERS = [
    "GROUP_RPM_EXCEEDED",
    "thinking blocks have invalid signature",
    "thinking block retry succeeded",
    "no available accounts",
    "overloaded_error",
    "rate_limit_error",
    "529",
    "timeout",
]

# Anchor the JSON blob to the END of the line — early `{...}` substrings in
# Caddy bracketed log levels would otherwise capture the wrong span.
_JSON_RE = re.compile(r"\{.*\}\s*$")
_MARKER_LINE = "http request completed"


def fail(msg: str, code: int = 2) -> None:
    print(f"[parse-access-log] ERROR: {msg}", file=sys.stderr)
    raise SystemExit(code)


def iter_lines(args: argparse.Namespace) -> Iterable[str]:
    modes = [args.stdin, bool(args.file), bool(args.docker)]
    if sum(modes) != 1:
        fail("exactly one of --stdin / --file / --docker must be set")

    if args.stdin:
        yield from sys.stdin
        return

    if args.file:
        with open(args.file, "r", encoding="utf-8", errors="replace") as fh:
            for line in fh:
                yield line
        return

    # --docker mode
    if not args.since:
        fail("--docker requires --since (e.g. --since 1h)")
    cmd = ["docker", "logs", args.docker, "--since", args.since]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        fail(f"docker logs {args.docker} failed: {proc.stderr.strip()}")
    yield from proc.stdout.splitlines()


def percentile(sorted_values: list[int], p: float) -> int:
    if not sorted_values:
        return 0
    idx = min(len(sorted_values) - 1, int((len(sorted_values) - 1) * p))
    return sorted_values[idx]


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Parse TokenKey gateway 'http request completed' logs into a stable JSON aggregation.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    src = parser.add_argument_group("input mode (choose one)")
    src.add_argument("--stdin", action="store_true", help="read raw log lines from stdin")
    src.add_argument("--file", default="", help="read raw log lines from PATH")
    src.add_argument("--docker", default="", help="spawn docker logs <CONTAINER>")
    src.add_argument("--since", default="", help="docker logs --since window (e.g. 1h, 30m)")

    filt = parser.add_argument_group("filters")
    filt.add_argument("--path", default="", help="exact JSON `path` filter")
    filt.add_argument("--model", default="", help="exact JSON `model` filter")
    filt.add_argument("--status-min", type=int, default=400, help="bad-bucket threshold")

    agg = parser.add_argument_group("aggregation knobs")
    agg.add_argument("--top-minutes", type=int, default=30)
    agg.add_argument("--top-models", type=int, default=30)
    agg.add_argument("--markers", default=",".join(DEFAULT_MARKERS))

    args = parser.parse_args()

    markers = [m.strip() for m in args.markers.split(",") if m.strip()]
    status_counts: collections.Counter = collections.Counter()
    minute_status: collections.Counter = collections.Counter()
    model_status: collections.Counter = collections.Counter()
    marker_counts: collections.Counter = collections.Counter()
    latencies: list[int] = []

    lines_seen = 0
    lines_parsed = 0
    bad_count = 0

    for line in iter_lines(args):
        lines_seen += 1
        # Marker count is line-level (deliberately broad — operator wants
        # "did this signature appear anywhere", not "in the JSON specifically").
        for marker in markers:
            if marker in line:
                marker_counts[marker] += 1

        if _MARKER_LINE not in line:
            continue
        m = _JSON_RE.search(line)
        if not m:
            continue
        try:
            obj = json.loads(m.group(0))
        except json.JSONDecodeError:
            continue

        if args.path and obj.get("path") != args.path:
            continue
        if args.model and obj.get("model") != args.model:
            continue

        sc = obj.get("status_code")
        if sc is None:
            continue
        try:
            sc_int = int(sc)
        except (TypeError, ValueError):
            continue

        lines_parsed += 1
        status_counts[sc_int] += 1
        if sc_int >= args.status_min:
            bad_count += 1

        ts = str(obj.get("completed_at") or "")
        # "2026-05-21T01:09:42.123Z" -> "2026-05-21T01:09:00Z" minute bucket
        if ts and len(ts) >= 16:
            minute = ts[:16] + ":00Z"
            minute_status[(minute, sc_int)] += 1

        model_name = str(obj.get("model") or "")
        model_status[(model_name, sc_int)] += 1

        lat = obj.get("latency_ms")
        if isinstance(lat, (int, float)):
            latencies.append(int(lat))

    latencies.sort()
    latency_block: dict | None
    if latencies:
        latency_block = {
            "n": len(latencies),
            "p50": percentile(latencies, 0.50),
            "p90": percentile(latencies, 0.90),
            "p95": percentile(latencies, 0.95),
            "p99": percentile(latencies, 0.99),
            "max": latencies[-1],
        }
    else:
        latency_block = None

    by_minute = [
        {"minute_utc": minute, "status_code": sc, "n": n}
        for (minute, sc), n in sorted(
            minute_status.items(), key=lambda kv: (-kv[1], kv[0])
        )
    ][: args.top_minutes]

    by_model_status = [
        {"model": model, "status_code": sc, "n": n}
        for (model, sc), n in sorted(
            model_status.items(), key=lambda kv: (-kv[1], kv[0])
        )
    ][: args.top_models]

    output = {
        "input": {
            "mode": "stdin" if args.stdin else ("file" if args.file else "docker"),
            "container": args.docker or None,
            "since": args.since or None,
            "path_filter": args.path or None,
            "model_filter": args.model or None,
            "status_min": args.status_min,
            "markers": markers,
        },
        "totals": {
            "lines_seen": lines_seen,
            "lines_parsed": lines_parsed,
            "lines_skipped": lines_seen - lines_parsed,
            "bad_count": bad_count,
        },
        "status_counts": {str(k): v for k, v in sorted(status_counts.items())},
        "by_minute": by_minute,
        "by_model_status": by_model_status,
        "markers": dict(marker_counts),
        "latency_ms": latency_block,
    }

    json.dump(output, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
