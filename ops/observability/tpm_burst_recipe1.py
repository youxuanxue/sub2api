#!/usr/bin/env python3
"""Recipe-1 planner: pool perf measurement and TPM burst.

Live load is never started unless APPROVE=yes-formal-tpm-loadtest.
Default entrance is https://api.galaxywaveai.com. Do not use
https://api-cn.tokenkey.dev — that name points at Tencent USW prod.

Examples:
  # Plan only. No traffic.
  python3 ops/observability/tpm_burst_recipe1.py perf --dry-run

  # After human approval. Key must be bound to china_deployed.
  APPROVE=yes-formal-tpm-loadtest \\
    python3 ops/observability/tpm_burst_recipe1.py perf

  # Pure math from measured shape (no network)
  python3 ops/observability/tpm_burst_recipe1.py estimate \\
      --tok-per-req 40000 --duration-s 45 --pool-slots 252 --target-tpm 20000000
"""

from __future__ import annotations

import argparse
import gzip
import hashlib
import ipaddress
import json
import math
import os
import statistics
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any, Sequence


FORMAL_APPROVE_TOKEN = "yes-formal-tpm-loadtest"
CALIBRATE_MAX_N = 9
DEFAULT_TARGET_TPM = 20_000_000
# Snapshot only — live cn-prod account×concurrency changes. Prefer
# TPM_BURST_POOL_SLOTS (or POOL_SLOTS) = accounts * per-account concurrency.
_LEGACY_POOL_SLOTS_SNAPSHOT = 21 * 12
DEFAULT_FAILURE_OVERHEAD = 1.15
DEFAULT_SAFETY_TPM_MARGIN = 1.05
FORMAL_WALL_SLACK_S = 75.0
LOADTEST_BASE_URL = "https://api.galaxywaveai.com"
# Customer chat bodies. Not inlined into an SSM document; copy the gzip to the host first.
DEFAULT_CASES_PATH = "/tmp/tk-rt-cases-200-bodies.jsonl.gz"
PERF_PROMPT_TOKENS_TARGET = 50_000
PERF_MAX_TOKENS = 1_000
PERF_STEPS_DEFAULT = (4, 8, 16, 32)
PERF_HOLD_S = 300.0
PERF_SUCCESS_MIN = 0.98
PERF_USER_AGENT = "tokenkey-pool-perf/1.0"
PERF_METRIC_FIELDS = (
    "concurrency_peak",
    "concurrency_mean",
    "rpm_peak_rolling_60s",
    "rpm_mean",
    "success_rate",
    "ttft_s_p50",
    "ttft_s_p90",
    "tpot_s_p90",
    "tokens_per_second_p50",
    "latency_s_p50",
    "latency_s_p90",
    "latency_p90_over_p50",
    "cache_token_hit_rate",
    "cache_request_hit_rate",
    "content_rate",
    "ttft_reasoning_s_p50",
)
_DEFAULT_CN_HOST_HEADER = "api-cn.tokenkey.dev"


def _prod_target_path_for(here: Path) -> Path | None:
    """Repo checkout only. /tmp/file.py has no parents[2]."""
    if len(here.parents) <= 2:
        return None
    return here.parents[2] / "deploy/domestic/prod-target.json"


def _prod_target_path() -> Path | None:
    return _prod_target_path_for(Path(__file__).resolve())


def _load_prod_target() -> dict[str, Any]:
    path = _prod_target_path()
    if path is None:
        return {}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError, TypeError):
        return {}
    return data if isinstance(data, dict) else {}


def cn_prod_host_header_default() -> str:
    """Caddy vhost name from prod-target fallback_domain (not for TLS SNI)."""
    domain = str(_load_prod_target().get("fallback_domain") or "").strip()
    return domain or _DEFAULT_CN_HOST_HEADER


def loadtest_base_url() -> str:
    """Public load-test entrance. TOKENKEY_BASE_URL overrides the default domain."""
    raw = os.environ.get("TOKENKEY_BASE_URL", "").strip()
    if raw:
        return raw.rstrip("/")
    return LOADTEST_BASE_URL


def cn_prod_public_origin() -> str:
    """cn-prod public_origin from deploy/domestic/prod-target.json (not DNS)."""
    raw = os.environ.get("TOKENKEY_BASE_URL", "").strip()
    if raw:
        return raw.rstrip("/")
    origin = str(_load_prod_target().get("public_origin") or "").strip().rstrip("/")
    if origin:
        return origin
    path = _prod_target_path() or Path("deploy/domestic/prod-target.json")
    raise FileNotFoundError(
        f"missing public_origin in {path}; set TOKENKEY_BASE_URL"
    )


def _hostname(url: str) -> str:
    return (urllib.parse.urlparse(url).hostname or "").strip().lower()


def _is_ip_host(host: str) -> bool:
    if not host:
        return False
    try:
        ipaddress.ip_address(host)
        return True
    except ValueError:
        return False


def resolve_host_header(base_url: str, explicit: str | None = None) -> str | None:
    """When base_url is a bare IP, send Host for Caddy vhost (HTTP only; TLS uses IP SAN)."""
    if explicit is not None and str(explicit).strip():
        return str(explicit).strip()
    env = os.environ.get("TOKENKEY_HOST_HEADER", "").strip()
    if env:
        return env
    host = _hostname(base_url)
    if _is_ip_host(host):
        return cn_prod_host_header_default()
    return None


def refuse_aws_cutover_domain(base_url: str) -> str | None:
    """api-cn.tokenkey.dev resolves to Tencent USW prod — not cn-prod (Tencent Beijing)."""
    host = _hostname(base_url)
    cutover = cn_prod_host_header_default().lower()
    if host == cutover:
        return (
            f"base_url host {host!r} resolves to overseas prod (Tencent USW), not cn-prod; "
            f"use the cn-prod public_origin from deploy/domestic/prod-target.json "
            f"(public_origin) plus Host: {cutover}"
        )
    return None


def default_pool_slots() -> int:
    """Resolve pool slots from env; fall back to a dated snapshot (not live SSOT)."""
    for key in ("TPM_BURST_POOL_SLOTS", "POOL_SLOTS"):
        raw = os.environ.get(key, "").strip()
        if not raw:
            continue
        try:
            n = int(raw)
        except ValueError as e:
            raise ValueError(f"{key} must be a positive int, got {raw!r}") from e
        if n <= 0:
            raise ValueError(f"{key} must be positive, got {n}")
        return n
    return _LEGACY_POOL_SLOTS_SNAPSHOT


# Back-compat alias for tests/importers; prefer default_pool_slots() at call time.
DEFAULT_POOL_SLOTS = _LEGACY_POOL_SLOTS_SNAPSHOT


def resolve_pool_slots(cli_value: int | None) -> int:
    """CLI --pool-slots wins; else env; else legacy snapshot."""
    if cli_value is not None and int(cli_value) > 0:
        return int(cli_value)
    return default_pool_slots()


@dataclass(frozen=True)
class Sample:
    ok: bool
    prompt_tokens: int
    completion_tokens: int
    total_tokens: int
    duration_s: float
    status_code: int
    error: str = ""
    ttft_s: float | None = None
    tpot_s: float | None = None
    tokens_per_second: float | None = None
    cached_tokens: int | None = None
    cache_field_present: bool = False
    visible_tokens: int | None = None
    finish_reason: str = ""
    content_emitted: bool = False
    ttft_reasoning_s: float | None = None


@dataclass(frozen=True)
class Estimate:
    target_tpm: int
    tok_per_req: float
    tok_per_req_p10: float
    tok_per_req_p90: float
    duration_s: float
    duration_s_p90: float
    pool_slots: int
    n_min_success: int
    n_plan_total: int
    threads: int
    per_thread: int
    rpm_needed: float
    rpm_ceiling: float
    tpm_ceiling: float
    tpm_ceiling_conservative: float
    feasible: bool
    feasibility_reason: str
    accuracy_notes: list[str]
    calibrate_n: int


def _percentile(sorted_vals: Sequence[float], p: float) -> float:
    if not sorted_vals:
        return 0.0
    if len(sorted_vals) == 1:
        return float(sorted_vals[0])
    # Inclusive rank percentile (same spirit as numpy default linear).
    k = (len(sorted_vals) - 1) * p
    f = math.floor(k)
    c = math.ceil(k)
    if f == c:
        return float(sorted_vals[int(k)])
    d0 = sorted_vals[f] * (c - k)
    d1 = sorted_vals[c] * (k - f)
    return float(d0 + d1)


def estimate_plan(
    *,
    samples_tok: Sequence[float],
    samples_duration_s: Sequence[float],
    target_tpm: int = DEFAULT_TARGET_TPM,
    pool_slots: int = DEFAULT_POOL_SLOTS,
    failure_overhead: float = DEFAULT_FAILURE_OVERHEAD,
    safety_tpm_margin: float = DEFAULT_SAFETY_TPM_MARGIN,
) -> Estimate:
    if not samples_tok or not samples_duration_s:
        raise ValueError("need at least one token and duration sample")
    if target_tpm <= 0 or pool_slots <= 0:
        raise ValueError("target_tpm and pool_slots must be positive")
    if failure_overhead < 1.0:
        raise ValueError("failure_overhead must be >= 1.0")

    tok_sorted = sorted(float(x) for x in samples_tok)
    dur_sorted = sorted(float(x) for x in samples_duration_s)
    tok_mean = statistics.fmean(tok_sorted)
    dur_mean = statistics.fmean(dur_sorted)
    tok_p10 = _percentile(tok_sorted, 0.10)
    tok_p90 = _percentile(tok_sorted, 0.90)
    dur_p90 = _percentile(dur_sorted, 0.90)

    # Size the burst with pessimistic tokens (need more successes if tok is low).
    tok_for_n = max(tok_p10, 1.0)
    n_min = int(math.ceil(target_tpm / tok_for_n))
    n_plan = int(math.ceil(n_min * failure_overhead))

    # Ceiling uses pessimistic duration (slower → lower RPM/TPM ceiling).
    dur_for_ceiling = max(dur_p90, 0.001)
    rpm_ceiling = pool_slots * 60.0 / dur_for_ceiling
    tpm_ceiling = rpm_ceiling * tok_mean
    tpm_ceiling_cons = (pool_slots * 60.0 / dur_for_ceiling) * tok_p10

    rpm_needed = n_min  # successes concentrated into one rolling 60s window
    threads = max(1, min(pool_slots, int(math.ceil(n_plan * dur_mean / 60.0))))
    # Prefer filling the pipe: if needed RPM is high, threads ≈ pool_slots.
    threads_for_rpm = max(1, min(pool_slots, int(math.ceil(rpm_needed * dur_mean / 60.0))))
    threads = max(threads, threads_for_rpm)
    per_thread = int(math.ceil(n_plan / threads))

    feasible = tpm_ceiling_cons >= target_tpm * safety_tpm_margin
    if feasible:
        reason = (
            f"conservative TPM ceiling {tpm_ceiling_cons:,.0f} "
            f">= target×{safety_tpm_margin:g} ({target_tpm * safety_tpm_margin:,.0f})"
        )
    else:
        reason = (
            f"conservative TPM ceiling {tpm_ceiling_cons:,.0f} "
            f"< target×{safety_tpm_margin:g} ({target_tpm * safety_tpm_margin:,.0f}); "
            "raise tok/req, shorten duration, or grow pool_slots before formal run"
        )

    notes = [
        "Rolling-60s peak TPM needs ≈N successes whose completions fall in one 60s window.",
        "N uses tok p10 (more requests if calibration under-shoots tokens).",
        "Ceiling uses duration p90 × tok p10 (pessimistic capacity).",
        f"calibrate_n={len(tok_sorted)}: with n<10, percentiles are unstable; treat as directional.",
        "Do not exceed pool_slots in-flight; extra threads only buy InflightBatchsizeExceeded.",
        "Formal run requires APPROVE=yes-formal-tpm-loadtest.",
        (
            "pool_slots must be live accounts×concurrency "
            f"(env TPM_BURST_POOL_SLOTS); default snapshot={_LEGACY_POOL_SLOTS_SNAPSHOT} is not SSOT."
        ),
    ]
    if len(tok_sorted) < 5:
        notes.append("calibrate_n<5: prefer re-calibrating to 5–9 before approving formal.")
    # Target-TPM sizing often uses << full pipe; that cannot measure pool/per-account ceilings.
    if pool_slots > 0 and threads < int(math.ceil(pool_slots * 0.9)):
        notes.append(
            f"threads={threads} fills only {threads / pool_slots:.0%} of pool_slots={pool_slots}; "
            "this plan is a target-TPM burst, not full-pool saturation or per-account TPM ceilings. "
            "To probe pool ceiling set max_inflight≈pool_slots and total_requests≳rpm_ceiling; "
            "per-account ceilings need pin-to-account traffic or per-account usage_logs."
        )

    return Estimate(
        target_tpm=target_tpm,
        tok_per_req=tok_mean,
        tok_per_req_p10=tok_p10,
        tok_per_req_p90=tok_p90,
        duration_s=dur_mean,
        duration_s_p90=dur_p90,
        pool_slots=pool_slots,
        n_min_success=n_min,
        n_plan_total=n_plan,
        threads=threads,
        per_thread=per_thread,
        rpm_needed=float(rpm_needed),
        rpm_ceiling=rpm_ceiling,
        tpm_ceiling=tpm_ceiling,
        tpm_ceiling_conservative=tpm_ceiling_cons,
        feasible=feasible,
        feasibility_reason=reason,
        accuracy_notes=notes,
        calibrate_n=len(tok_sorted),
    )


def _pad_prompt(target_tokens: int) -> str:
    # Rough ASCII filler; Volc/Kimi tokenizers vary — calibrate measures truth.
    unit = "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda "
    need_chars = max(64, int(target_tokens * 4))
    return (unit * (need_chars // len(unit) + 1))[:need_chars]


def _build_messages(
    *,
    prompt_tokens_target: int,
    max_tokens: int,
    force_completion: bool,
    prompt_file: str = "",
) -> list[dict[str, str]]:
    """Build chat messages.

    prompt_file: use a pre-packed coding prompt (e.g. pack_repo_prompt.py output).
    force_completion: filler essay mode that tries to fill max_tokens (not coding-like).
    """
    if prompt_file:
        text = Path(prompt_file).read_text(encoding="utf-8")
        if not text.strip():
            raise ValueError(f"prompt file empty: {prompt_file}")
        return [{"role": "user", "content": text}]

    context = _pad_prompt(prompt_tokens_target)
    if not force_completion:
        return [{"role": "user", "content": context}]
    # Slightly oversized vs "约1000": caller should pass max_tokens≈1100–1300.
    instruction = (
        f"You are in a load-test calibration. Ignore the filler below except as context bulk. "
        f"Write a continuous English essay of at least {max_tokens} tokens. "
        f"Do not stop early. Do not use bullet lists. Do not apologize. "
        f"Continue with dense paragraphs until the output length limit is reached.\n\n"
        f"FILLER_CONTEXT_START\n{context}\nFILLER_CONTEXT_END\n\n"
        f"Begin the long essay now:"
    )
    return [{"role": "user", "content": instruction}]


def synthetic_context_case(*, prompt_tokens_target: int, max_tokens: int) -> dict[str, Any]:
    """One owned filler context. The prompt text stays in the request body only."""
    if prompt_tokens_target <= 0 or max_tokens <= 0:
        raise ValueError("prompt_tokens_target and max_tokens must be positive")
    messages = _build_messages(
        prompt_tokens_target=prompt_tokens_target,
        max_tokens=max_tokens,
        force_completion=True,
    )
    body = {
        "model": "kimi-k3",
        "messages": messages,
        "max_tokens": max_tokens,
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    identity = json.dumps(
        {"prompt_tokens_target": prompt_tokens_target, "max_tokens": max_tokens, "kind": "synthetic-context"},
        sort_keys=True,
    )
    return {
        "sha256": hashlib.sha256(identity.encode("utf-8")).hexdigest(),
        "redacted_marker": False,
        "body": body,
    }


def _redact(text: str, secret: str) -> str:
    if secret and secret in text:
        return text.replace(secret, "<redacted>")
    return text


def _pct_optional(values: Sequence[float | None], p: float) -> float | None:
    xs = sorted(float(v) for v in values if v is not None)
    if not xs or (len(xs) < 2 and p > 0.5):
        return None
    return _percentile(xs, p)


def _time_weighted_mean(trace: Sequence[tuple[float, int]]) -> float | None:
    if len(trace) < 2:
        return None
    acc = 0.0
    for (t0, level), (t1, _) in zip(trace, trace[1:]):
        dt = t1 - t0
        if dt > 0:
            acc += level * dt
    span = trace[-1][0] - trace[0][0]
    if span <= 0:
        return float(trace[-1][1])
    return acc / span


def _rolling_peak_count(times: Sequence[float], window_s: float = 60.0) -> int:
    if not times:
        return 0
    ordered = sorted(float(t) for t in times)
    peak = 0
    left = 0
    for right, t in enumerate(ordered):
        while ordered[left] < t - window_s:
            left += 1
        peak = max(peak, right - left + 1)
    return peak


def sample_from_stream_events(
    events: Sequence[tuple[float, dict[str, Any]]],
    *,
    t0: float,
    status: int,
    done: bool,
    duration_s: float,
    transport_error: str = "",
) -> Sample:
    """Turn streamed chat chunks into one measured sample.

    Transport success does not require visible content. ttft_s is the first
    non-empty content delta. ttft_reasoning_s is the first non-empty reasoning
    delta. TPOT uses visible completion tokens, subtracting reasoning tokens
    when the usage payload reports them.
    """
    first: float | None = None
    last: float | None = None
    first_reasoning: float | None = None
    finish = ""
    usage: dict[str, Any] = {}
    error = transport_error
    reasoning_events = 0
    for now, obj in events:
        if obj.get("error"):
            error = error or json.dumps(obj["error"], ensure_ascii=False)[:300]
            break
        raw_usage = obj.get("usage")
        if isinstance(raw_usage, dict):
            usage = raw_usage
        for choice in obj.get("choices") or []:
            if not isinstance(choice, dict):
                continue
            delta = choice.get("delta") if isinstance(choice.get("delta"), dict) else {}
            reasoning = delta.get("reasoning_content")
            if reasoning is None:
                reasoning = delta.get("reasoning")
            if isinstance(reasoning, str) and reasoning:
                reasoning_events += 1
                if first_reasoning is None:
                    first_reasoning = now
            elif reasoning:
                reasoning_events += 1
                if first_reasoning is None:
                    first_reasoning = now
            content = delta.get("content")
            if isinstance(content, str) and content:
                if first is None:
                    first = now
                last = now
            if choice.get("finish_reason"):
                finish = str(choice["finish_reason"])

    prompt_tokens = int(usage.get("prompt_tokens") or 0)
    completion_tokens = int(usage.get("completion_tokens") or 0)
    total = int(usage.get("total_tokens") or (prompt_tokens + completion_tokens))
    details = usage.get("prompt_tokens_details") if isinstance(usage.get("prompt_tokens_details"), dict) else {}
    cache_present = "cached_tokens" in details
    cached = int(details.get("cached_tokens") or 0) if cache_present else None
    comp_details = (
        usage.get("completion_tokens_details")
        if isinstance(usage.get("completion_tokens_details"), dict)
        else {}
    )
    reasoning_known = "reasoning_tokens" in comp_details
    reasoning_tokens = int(comp_details.get("reasoning_tokens") or 0) if reasoning_known else 0
    visible: int | None
    if completion_tokens and (not reasoning_events or reasoning_known):
        visible = completion_tokens - reasoning_tokens
    else:
        visible = None
    ttft = (first - t0) if first is not None else None
    ttft_reasoning = (first_reasoning - t0) if first_reasoning is not None else None
    generation = (last - first) if first is not None and last is not None else None
    tpot = None
    tps = None
    if visible is not None and visible > 1 and generation is not None and generation > 0:
        tpot = generation / (visible - 1)
        tps = 1.0 / tpot
    content_emitted = first is not None
    ok = (
        200 <= status < 300
        and finish in ("stop", "length")
        and done
        and not error
        and total > 0
    )
    if not ok and not error:
        error = "incomplete_stream_or_missing_usage"
    return Sample(
        ok=ok,
        prompt_tokens=prompt_tokens,
        completion_tokens=completion_tokens,
        total_tokens=total,
        duration_s=duration_s,
        status_code=status,
        error="" if ok else error[:300],
        ttft_s=ttft,
        tpot_s=tpot,
        tokens_per_second=tps,
        cached_tokens=cached,
        cache_field_present=cache_present,
        visible_tokens=visible,
        finish_reason=finish,
        content_emitted=content_emitted,
        ttft_reasoning_s=ttft_reasoning,
    )


def request_record_from_sample(
    sample: Sample,
    *,
    account_id: int | None,
    model: str,
    step: str,
    inflight: int,
    seq: int,
    started_unix: float,
    request_id: str | None = None,
    case_sha256: str | None = None,
    reasoning_effort: str | None = None,
    output_limit: int | None = None,
    output_limit_field: str | None = None,
    redacted_marker: bool | None = None,
) -> dict[str, Any]:
    """One request, all measured fields. Later reports aggregate this row; do not drop fields."""

    def rnd(value: float | None, digits: int = 3) -> float | None:
        return None if value is None else round(value, digits)

    return {
        "phase": "request",
        "account_id": account_id,
        "model": model,
        "step": step,
        "inflight": inflight,
        "seq": seq,
        "started_unix": round(started_unix, 3),
        "latency_s": rnd(sample.duration_s),
        "ttft_s": rnd(sample.ttft_s),
        "ttft_reasoning_s": rnd(sample.ttft_reasoning_s),
        "tpot_s": rnd(sample.tpot_s),
        "tokens_per_second": rnd(sample.tokens_per_second),
        "ok": sample.ok,
        "content_emitted": sample.content_emitted,
        "status_code": sample.status_code,
        "prompt_tokens": sample.prompt_tokens,
        "completion_tokens": sample.completion_tokens,
        "total_tokens": sample.total_tokens,
        "cached_tokens": sample.cached_tokens,
        "cache_field_present": sample.cache_field_present,
        "visible_tokens": sample.visible_tokens,
        "finish_reason": sample.finish_reason or None,
        "request_id": request_id,
        "case_sha256": case_sha256,
        "reasoning_effort": reasoning_effort,
        "output_limit": output_limit,
        "output_limit_field": output_limit_field,
        "redacted_marker": redacted_marker,
        "error": sample.error or None,
    }


def summarize_perf(
    samples: Sequence[Sample],
    *,
    wall_s: float,
    inflight_trace: Sequence[tuple[float, int]],
    success_completion_times: Sequence[float],
    inflight: int,
) -> dict[str, Any]:
    """Pool-step metrics. Missing cache fields stay absent; they are not zeros."""
    n = len(samples)
    ok = [s for s in samples if s.ok]
    success_n = len(ok)
    wall = max(float(wall_s), 0.0)
    lat_p50 = _pct_optional([s.duration_s for s in ok], 0.50)
    lat_p90 = _pct_optional([s.duration_s for s in ok], 0.90)
    ratio = None
    if lat_p50 and lat_p90 is not None and lat_p50 > 0:
        ratio = lat_p90 / lat_p50
    known = [s for s in ok if s.cache_field_present]
    prompt_sum = sum(s.prompt_tokens for s in known)
    cached_sum = sum(int(s.cached_tokens or 0) for s in known)
    cache_token_rate = (cached_sum / prompt_sum) if prompt_sum else None
    cache_request_rate = (
        sum(1 for s in known if int(s.cached_tokens or 0) > 0) / len(known) if known else None
    )
    from collections import Counter

    peak_level = max((level for _, level in inflight_trace), default=0)
    mean_level = _time_weighted_mean(inflight_trace)
    note = None
    if wall < 60:
        note = (
            "wall shorter than 60s; rpm_peak_rolling_60s is the success count "
            "inside this step, not a sustained per-minute peak"
        )
    return {
        "inflight": inflight,
        "n": n,
        "wall_s": round(wall, 3),
        "success_n": success_n,
        "fail_n": n - success_n,
        "success_rate": round(success_n / n, 4) if n else 0.0,
        "concurrency_peak": peak_level,
        "concurrency_mean": None if mean_level is None else round(mean_level, 3),
        "rpm_mean": round(success_n / wall * 60.0, 2) if wall > 0 else 0.0,
        "rpm_peak_rolling_60s": _rolling_peak_count(success_completion_times, 60.0),
        "rpm_note": note,
        "ttft_s_p50": _pct_optional([s.ttft_s for s in ok], 0.50),
        "ttft_s_p90": _pct_optional([s.ttft_s for s in ok], 0.90),
        "tpot_s_p90": _pct_optional([s.tpot_s for s in ok], 0.90),
        "tokens_per_second_p50": _pct_optional([s.tokens_per_second for s in ok], 0.50),
        "latency_s_p50": lat_p50,
        "latency_s_p90": lat_p90,
        "latency_p90_over_p50": None if ratio is None else round(ratio, 4),
        "cache_token_hit_rate": None if cache_token_rate is None else round(cache_token_rate, 4),
        "cache_request_hit_rate": None if cache_request_rate is None else round(cache_request_rate, 4),
        "cache_known_n": len(known),
        "content_emitted_n": sum(1 for s in samples if s.content_emitted),
        "content_rate": round(sum(1 for s in samples if s.content_emitted) / n, 4) if n else 0.0,
        "ttft_reasoning_s_p50": _pct_optional([s.ttft_reasoning_s for s in ok], 0.50),
        "ttft_reasoning_s_p90": _pct_optional([s.ttft_reasoning_s for s in ok], 0.90),
        "prompt_tokens_mean": round(statistics.fmean(s.prompt_tokens for s in ok), 1) if ok else None,
        "completion_tokens_mean": round(statistics.fmean(s.completion_tokens for s in ok), 1) if ok else None,
        "fail_status_hist": dict(Counter(str(s.status_code) for s in samples if not s.ok)),
    }


def select_operating_point(steps: Sequence[dict[str, Any]], success_min: float) -> dict[str, Any] | None:
    """Last step whose success rate stays at or above the bar. The first miss stops the walk."""
    chosen: dict[str, Any] | None = None
    for step in steps:
        if float(step.get("success_rate") or 0) + 1e-12 < success_min:
            break
        chosen = dict(step)
    return chosen


def parse_perf_steps(raw: str) -> list[int]:
    steps: list[int] = []
    for part in raw.split(","):
        part = part.strip()
        if not part:
            continue
        n = int(part)
        if n <= 0:
            raise ValueError(f"perf step must be positive, got {n}")
        steps.append(n)
    if not steps:
        raise ValueError("perf steps empty")
    return steps


def perf_steps_for_pool(steps: Sequence[int], pool_slots: int | None) -> list[int]:
    if pool_slots is None:
        return [s for s in steps if s > 0]
    return [s for s in steps if 0 < s <= pool_slots]


def cases_path_from_env(explicit: str | None = None) -> str:
    raw = (explicit or "").strip()
    if raw:
        return raw
    for key in ("REQUEST_POOL", "CASES_PATH"):
        env = os.environ.get(key, "").strip()
        if env:
            return env
    return DEFAULT_CASES_PATH


def _open_jsonl(path: Path):
    if path.name.endswith(".gz"):
        return gzip.open(path, "rt", encoding="utf-8")
    return path.open("r", encoding="utf-8")


def load_chat_case_pool(path: str) -> list[dict[str, Any]]:
    """Replayable chat bodies. Skips truncated rows. Does not log message text.

    When index.jsonl.gz sits beside bodies.jsonl.gz, case sha and redacted_marker
    come from that index, aligned by line number.
    """
    body_path = Path(path)
    if not body_path.is_file():
        raise FileNotFoundError(path)
    index_path = body_path.with_name("index.jsonl.gz")
    index_rows: list[Any] = []
    if index_path.is_file():
        with _open_jsonl(index_path) as handle:
            for line in handle:
                if not line.strip():
                    index_rows.append(None)
                    continue
                try:
                    index_rows.append(json.loads(line))
                except json.JSONDecodeError:
                    index_rows.append(None)
    cases: list[dict[str, Any]] = []
    with _open_jsonl(body_path) as handle:
        for i, line in enumerate(handle):
            if not line.strip():
                continue
            try:
                obj = json.loads(line)
                if isinstance(obj, str):
                    obj = json.loads(obj)
            except json.JSONDecodeError:
                continue
            if not isinstance(obj, dict) or not isinstance(obj.get("messages"), list):
                continue
            meta = index_rows[i] if i < len(index_rows) and isinstance(index_rows[i], dict) else {}
            sha = str(meta.get("request_sha256") or "").strip()
            if not sha:
                sha = hashlib.sha256(line.encode("utf-8")).hexdigest()
            cases.append(
                {
                    "sha256": sha,
                    "redacted_marker": bool(meta.get("redacted_marker")),
                    "body": obj,
                }
            )
    return cases


def prepare_replay_body(body: dict[str, Any], *, model: str | None = None) -> dict[str, Any]:
    """Customer body with stream and include_usage forced. Output limits stay as sent."""
    out = dict(body)
    if model:
        out["model"] = model
    out["stream"] = True
    opts = dict(out.get("stream_options") or {})
    opts["include_usage"] = True
    out["stream_options"] = opts
    return out


def case_replay_fields(case: dict[str, Any]) -> dict[str, Any]:
    body = case.get("body") if isinstance(case.get("body"), dict) else {}
    field: str | None = None
    limit: int | None = None
    if isinstance(body.get("max_completion_tokens"), int) and not isinstance(body.get("max_completion_tokens"), bool):
        field, limit = "max_completion_tokens", int(body["max_completion_tokens"])
    elif isinstance(body.get("max_tokens"), int) and not isinstance(body.get("max_tokens"), bool):
        field, limit = "max_tokens", int(body["max_tokens"])
    effort = body.get("reasoning_effort")
    return {
        "case_sha256": case.get("sha256"),
        "reasoning_effort": effort if isinstance(effort, str) else None,
        "output_limit": limit,
        "output_limit_field": field,
        "redacted_marker": bool(case.get("redacted_marker")),
    }


def summarize_case_pool(cases: Sequence[dict[str, Any]]) -> dict[str, Any]:
    """Counts only. Never includes message text."""
    effort_max = 0
    limit_le_1000 = 0
    for case in cases:
        fields = case_replay_fields(case)
        if fields["reasoning_effort"] == "max":
            effort_max += 1
        limit = fields["output_limit"]
        if isinstance(limit, int) and limit <= 1000:
            limit_le_1000 += 1
    return {
        "replayable_n": len(cases),
        "reasoning_effort_max_n": effort_max,
        "output_limit_le_1000_n": limit_le_1000,
    }


def build_perf_plan(
    *,
    base_url: str,
    model: str,
    steps: Sequence[int],
    hold_s: float,
    success_min: float,
    pool_slots: int | None,
    cases_path: str | None = None,
    prompt_tokens_target: int | None = None,
    max_tokens: int | None = None,
    case_summary: dict[str, Any] | None = None,
) -> dict[str, Any]:
    if cases_path:
        shape: dict[str, Any] = {
            "source": "customer_cases",
            "cases_path": cases_path,
            "replay": (
                "customer chat bodies; stream and include_usage are forced; "
                "reasoning_effort and output limits are kept"
            ),
            "stream": True,
            "cases": case_summary,
        }
    else:
        shape = {
            "source": "synthetic_context",
            "prompt_tokens_target": prompt_tokens_target,
            "max_tokens": max_tokens,
            "stream": True,
            "replay": "owned filler context; max_tokens is the output cap",
        }
    return {
        "phase": "perf_plan",
        "will_not_send_traffic": True,
        "approval_required": True,
        "approve_env": f"APPROVE={FORMAL_APPROVE_TOKEN}",
        "base_url": base_url,
        "model": model,
        "group_requirement": (
            "TOKENKEY_API_KEY must be bound to china_deployed; "
            "this client does not select a group"
        ),
        "shape": shape,
        "steps": list(steps),
        "hold_s": hold_s,
        "success_min": success_min,
        "pool_slots": pool_slots,
        "hold_rule": (
            "after a concurrency step starts, keep that inflight filled until "
            f"hold_s={hold_s:g}s has elapsed, then drain in-flight work; "
            "a fixed request count does not end the step"
        ),
        "stop_rule": (
            "stop at the first step whose success_rate is below success_min; "
            "operating_point is the last step at or above it"
        ),
        "metrics": list(PERF_METRIC_FIELDS),
    }


def _emit_approval_block(phase: str) -> int:
    print(
        json.dumps(
            {
                "phase": phase,
                "error": "load test requires human approval",
                "need": f"APPROVE={FORMAL_APPROVE_TOKEN}",
                "will_not_send_traffic": True,
            },
            ensure_ascii=False,
            indent=2,
        ),
        file=sys.stderr,
    )
    return 10


def _loadtest_opener() -> urllib.request.OpenerDirector:
    """Open load-test requests directly.

    urllib.request.urlopen follows the macOS system proxy. Pool perf runs on
    the operator machine, and a local proxy dial timeout (ClashX DIRECT is 5s)
    would be recorded as a gateway transport failure. An empty ProxyHandler
    replaces the default one inside this opener only.
    """
    return urllib.request.build_opener(urllib.request.ProxyHandler({}))


def _chat_completion(
    *,
    base_url: str,
    api_key: str,
    model: str,
    messages: list[dict[str, str]],
    max_tokens: int,
    timeout_s: float,
    temperature: float = 0.7,
    host_header: str | None = None,
    stream: bool = False,
    include_temperature: bool = True,
    user_agent: str = "tokenkey-tpm-burst-recipe1/1.0",
    payload: dict[str, Any] | None = None,
) -> Sample:
    url = base_url.rstrip("/") + "/v1/chat/completions"
    if payload is not None:
        body = payload
    else:
        body = {
            "model": model,
            "messages": messages,
            "max_tokens": max_tokens,
            "stream": stream,
        }
        if stream:
            body["stream_options"] = {"include_usage": True}
        if include_temperature:
            body["temperature"] = temperature
    data = json.dumps(body).encode("utf-8")
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
        "User-Agent": user_agent,
    }
    if stream:
        headers["Accept"] = "text/event-stream"
    hh = resolve_host_header(base_url, host_header)
    if hh:
        headers["Host"] = hh
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers=headers,
    )
    t0 = time.perf_counter()

    def failed(status: int, err: str) -> Sample:
        return Sample(
            ok=False,
            prompt_tokens=0,
            completion_tokens=0,
            total_tokens=0,
            duration_s=time.perf_counter() - t0,
            status_code=status,
            error=_redact(err, api_key)[:300],
        )

    try:
        with _loadtest_opener().open(req, timeout=timeout_s) as resp:
            status = int(resp.status)
            if not stream:
                raw = resp.read()
            else:
                events: list[tuple[float, dict[str, Any]]] = []
                done = False
                transport_error = ""
                for raw_line in resp:
                    now = time.perf_counter()
                    if now - t0 > timeout_s:
                        transport_error = "request wall time exceeded timeout"
                        break
                    line = raw_line.decode("utf-8", errors="replace").strip()
                    if not line.startswith("data:"):
                        continue
                    chunk = line[5:].strip()
                    if chunk == "[DONE]":
                        done = True
                        break
                    try:
                        obj = json.loads(chunk)
                    except json.JSONDecodeError:
                        transport_error = "non-json sse"
                        break
                    if isinstance(obj, dict):
                        events.append((now, obj))
                return sample_from_stream_events(
                    events,
                    t0=t0,
                    status=status,
                    done=done,
                    duration_s=time.perf_counter() - t0,
                    transport_error=transport_error,
                )
    except urllib.error.HTTPError as e:
        err_body = e.read().decode("utf-8", errors="replace")
        return failed(int(e.code), err_body)
    except Exception as e:  # noqa: BLE001 — surface transport errors as a sample
        return failed(0, str(e))

    duration = time.perf_counter() - t0
    try:
        payload = json.loads(raw.decode("utf-8"))
    except json.JSONDecodeError:
        return Sample(False, 0, 0, 0, duration, status, "non-json response")

    usage = payload.get("usage") or {}
    prompt_tokens = int(usage.get("prompt_tokens") or 0)
    completion_tokens = int(usage.get("completion_tokens") or 0)
    total = int(usage.get("total_tokens") or (prompt_tokens + completion_tokens))
    ok = 200 <= status < 300 and total > 0
    return Sample(
        ok=ok,
        prompt_tokens=prompt_tokens,
        completion_tokens=completion_tokens,
        total_tokens=total,
        duration_s=duration,
        status_code=status,
        error="" if ok else _redact(str(payload.get("error") or ""), api_key)[:300],
    )


def cmd_calibrate(args: argparse.Namespace) -> int:
    n = int(args.n)
    if n < 1 or n > CALIBRATE_MAX_N:
        print(f"calibrate n must be 1..{CALIBRATE_MAX_N}", file=sys.stderr)
        return 2
    if os.environ.get("APPROVE") != FORMAL_APPROVE_TOKEN:
        return _emit_approval_block("calibrate_blocked")
    api_key = args.api_key or os.environ.get("TOKENKEY_API_KEY") or os.environ.get("OPENAI_API_KEY")
    if not api_key:
        print("missing --api-key or TOKENKEY_API_KEY/OPENAI_API_KEY", file=sys.stderr)
        return 2
    base_url = str(args.base_url or loadtest_base_url())
    bad = refuse_aws_cutover_domain(base_url)
    if bad:
        print(json.dumps({"phase": "calibrate_refused", "error": bad}, ensure_ascii=False), file=sys.stderr)
        return 2
    host_header = resolve_host_header(base_url, getattr(args, "host_header", None))

    force_completion = not bool(args.no_force_completion)
    prompt_file = str(args.prompt_file or "")
    if prompt_file:
        # Coding-shaped packed prompts should not use filler force-completion.
        force_completion = False
    messages = _build_messages(
        prompt_tokens_target=int(args.prompt_tokens_target),
        max_tokens=int(args.max_tokens),
        force_completion=force_completion,
        prompt_file=prompt_file,
    )
    samples: list[Sample] = []
    print(
        json.dumps(
            {
                "phase": "calibrate_start",
                "n": n,
                "model": args.model,
                "prompt_tokens_target": args.prompt_tokens_target,
                "max_tokens": args.max_tokens,
                "force_completion": force_completion,
                "prompt_file": prompt_file or None,
                "prompt_chars": len(messages[0]["content"]),
                "base_url": base_url,
                "host_header": host_header,
            },
            ensure_ascii=False,
        ),
        flush=True,
    )
    for i in range(n):
        s = _chat_completion(
            base_url=base_url,
            api_key=api_key,
            model=args.model,
            messages=messages,
            max_tokens=int(args.max_tokens),
            timeout_s=float(args.timeout_s),
            temperature=0.2 if prompt_file else 0.7,
            host_header=host_header,
        )
        samples.append(s)
        print(json.dumps({"phase": "calibrate_sample", "i": i + 1, **asdict(s)}, ensure_ascii=False), flush=True)
        if i + 1 < n and args.sleep_s > 0:
            time.sleep(float(args.sleep_s))

    ok_samples = [s for s in samples if s.ok]
    report: dict[str, Any] = {
        "phase": "calibrate_report",
        "recipe": "1",
        "model": args.model,
        "base_url": base_url,
        "host_header": host_header,
        "prompt_tokens_target": args.prompt_tokens_target,
        "max_tokens": args.max_tokens,
        "force_completion": force_completion,
        "prompt_file": prompt_file or None,
        "prompt_chars": len(messages[0]["content"]),
        "shape": "repo_explain" if prompt_file else ("force_essay" if force_completion else "filler"),
        "n_requested": n,
        "n_ok": len(ok_samples),
        "samples": [asdict(s) for s in samples],
    }
    if ok_samples:
        est = estimate_plan(
            samples_tok=[s.total_tokens for s in ok_samples],
            samples_duration_s=[s.duration_s for s in ok_samples],
            target_tpm=int(args.target_tpm),
            pool_slots=resolve_pool_slots(args.pool_slots),
            failure_overhead=float(args.failure_overhead),
        )
        report["estimate"] = asdict(est)
        report["verdict"] = "feasible" if est.feasible else "not_feasible_with_current_shape"
    else:
        report["estimate"] = None
        report["verdict"] = "calibrate_failed_no_success"

    text = json.dumps(report, ensure_ascii=False, indent=2)
    if args.out:
        with open(args.out, "w", encoding="utf-8") as f:
            f.write(text + "\n")
        print(json.dumps({"phase": "wrote", "path": args.out, "verdict": report["verdict"]}, ensure_ascii=False))
    else:
        print(text)
    return 0 if ok_samples else 3


def cmd_estimate(args: argparse.Namespace) -> int:
    if args.tok_per_req is not None:
        toks = [float(args.tok_per_req)]
        durs = [float(args.duration_s)]
    elif args.from_path:
        with open(args.from_path, encoding="utf-8") as f:
            report = json.load(f)
        ok = [s for s in report.get("samples", []) if s.get("ok")]
        if not ok:
            print("no ok samples in --from report", file=sys.stderr)
            return 2
        toks = [float(s["total_tokens"]) for s in ok]
        durs = [float(s["duration_s"]) for s in ok]
    else:
        print("provide --tok-per-req/--duration-s or --from", file=sys.stderr)
        return 2

    est = estimate_plan(
        samples_tok=toks,
        samples_duration_s=durs,
        target_tpm=int(args.target_tpm),
        pool_slots=resolve_pool_slots(args.pool_slots),
        failure_overhead=float(args.failure_overhead),
    )
    print(json.dumps(asdict(est), ensure_ascii=False, indent=2))
    return 0 if est.feasible else 4


def _estimate_from_report(
    report: dict[str, Any],
    *,
    target_tpm: int,
    pool_slots: int,
    failure_overhead: float,
) -> Estimate:
    """Rebuild Estimate from ok samples so CLI pool/target overrides always apply."""
    ok = [s for s in report.get("samples", []) if s.get("ok")]
    if ok:
        return estimate_plan(
            samples_tok=[float(s["total_tokens"]) for s in ok],
            samples_duration_s=[float(s["duration_s"]) for s in ok],
            target_tpm=target_tpm,
            pool_slots=pool_slots,
            failure_overhead=failure_overhead,
        )
    est_raw = report.get("estimate")
    if not est_raw:
        raise ValueError("calibration report has no estimate/ok samples")
    # No samples: reuse embedded estimate but force CLI capacity knobs.
    return estimate_plan(
        samples_tok=[float(est_raw["tok_per_req"])],
        samples_duration_s=[float(est_raw["duration_s"])],
        target_tpm=target_tpm,
        pool_slots=pool_slots,
        failure_overhead=failure_overhead,
    )


def cmd_plan(args: argparse.Namespace) -> int:
    with open(args.from_path, encoding="utf-8") as f:
        report = json.load(f)
    embedded = report.get("estimate") or {}
    target_tpm = int(
        args.target_tpm
        or embedded.get("target_tpm")
        or DEFAULT_TARGET_TPM
    )
    pool_slots = resolve_pool_slots(args.pool_slots)
    try:
        est = _estimate_from_report(
            report,
            target_tpm=target_tpm,
            pool_slots=pool_slots,
            failure_overhead=float(args.failure_overhead),
        )
    except ValueError as e:
        print(str(e), file=sys.stderr)
        return 2

    plan = {
        "phase": "formal_plan_only",
        "approval_required": True,
        "approve_env": f"APPROVE={FORMAL_APPROVE_TOKEN}",
        "will_not_run": True,
        "recipe": "1",
        "model": report.get("model"),
        "shape": {
            "prompt_tokens_target": report.get("prompt_tokens_target"),
            "max_tokens": report.get("max_tokens"),
        },
        "estimate": asdict(est),
        "client_recipe": {
            "threads": est.threads,
            "per_thread_ceil": est.per_thread,
            "total_requests": est.n_plan_total,
            "scheduling_note": (
                f"cap total starts at n_plan_total={est.n_plan_total}; "
                f"use ≤{est.threads} in-flight (pool_slots), not threads×per_thread_ceil"
            ),
            "stop_when": (
                f"n_plan_total starts issued OR wall_clock > duration_p90 + {FORMAL_WALL_SLACK_S:g}s "
                "(formal runner stops new submits at wall limit; in-flight finish)"
            ),
            "metric": "rolling_60s_success_TPM from usage_logs / client success completions",
        },
        "accuracy": {
            "calibrate_n": est.calibrate_n,
            "notes": est.accuracy_notes,
            "expected_peak_tpm_band": [
                round(est.tpm_ceiling_conservative),
                round(est.tpm_ceiling),
            ],
        },
    }
    print(json.dumps(plan, ensure_ascii=False, indent=2))
    return 0 if est.feasible else 4


def _run_formal_load(
    *,
    base_url: str,
    api_key: str,
    model: str,
    prompt_file: str,
    max_tokens: int,
    total_requests: int,
    max_inflight: int,
    timeout_s: float,
    wall_limit_s: float,
    host_header: str | None = None,
) -> dict[str, Any]:
    from collections import Counter
    from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait

    messages = _build_messages(
        prompt_tokens_target=0,
        max_tokens=max_tokens,
        force_completion=False,
        prompt_file=prompt_file,
    )
    t0 = time.time()
    results: list[Sample] = []
    print(
        json.dumps(
            {
                "phase": "formal_start",
                "model": model,
                "base_url": base_url,
                "host_header": host_header,
                "prompt_file": prompt_file,
                "prompt_chars": len(messages[0]["content"]),
                "max_tokens": max_tokens,
                "total_requests": total_requests,
                "max_inflight": max_inflight,
                "wall_limit_s": wall_limit_s,
            },
            ensure_ascii=False,
        ),
        flush=True,
    )

    def one(i: int) -> Sample:
        s = _chat_completion(
            base_url=base_url,
            api_key=api_key,
            model=model,
            messages=messages,
            max_tokens=max_tokens,
            timeout_s=timeout_s,
            temperature=0.2,
            host_header=host_header,
        )
        print(
            json.dumps({"phase": "formal_sample", "i": i, **asdict(s)}, ensure_ascii=False),
            flush=True,
        )
        return s

    workers = max(1, max_inflight)
    planned = total_requests
    submit_cap = planned
    submitted = 0
    stopped_early = False
    stop_reason = ""
    with ThreadPoolExecutor(max_workers=workers) as pool:
        pending: set[Any] = set()
        while submitted < submit_cap or pending:
            # Stop issuing new work past wall limit; let in-flight finish.
            if (
                not stopped_early
                and submitted < submit_cap
                and (time.time() - t0) >= wall_limit_s
            ):
                stopped_early = True
                stop_reason = f"wall_limit_s={wall_limit_s}"
                submit_cap = submitted
            while submitted < submit_cap and len(pending) < workers:
                pending.add(pool.submit(one, submitted + 1))
                submitted += 1
            if not pending:
                break
            done, pending = wait(pending, return_when=FIRST_COMPLETED)
            for fut in done:
                results.append(fut.result())

    ok = [s for s in results if s.ok]
    wall = time.time() - t0
    # Rolling peak TPM approximation: whole-run average only — use usage_logs for peaks.
    success_n = len(ok)
    tok_sum = sum(s.total_tokens for s in ok)
    rpm = success_n / wall * 60.0 if wall > 0 else 0.0
    tpm = tok_sum / wall * 60.0 if wall > 0 else 0.0
    fail_hist = Counter(str(s.status_code) for s in results if not s.ok)
    return {
        "phase": "formal_summary",
        "wall_s": round(wall, 2),
        "wall_limit_s": wall_limit_s,
        "total_requests_planned": planned,
        "submitted_n": submitted,
        "stopped_early": stopped_early,
        "stop_reason": stop_reason or None,
        "success_n": success_n,
        "fail_n": len(results) - success_n,
        "tok_sum_success": tok_sum,
        "approx_success_rpm": round(rpm, 2),
        "approx_success_tpm": round(tpm, 2),
        "note": (
            "approx_* are whole-run averages, not rolling-60s peaks; "
            "use cn-prod usage_logs for rolling peak TPM after the run"
        ),
        "fail_status_hist": dict(fail_hist),
    }


def cmd_formal(args: argparse.Namespace) -> int:
    dry = bool(getattr(args, "dry_run", False))
    approve = os.environ.get("APPROVE", "")
    if not dry and approve != FORMAL_APPROVE_TOKEN:
        print(
            json.dumps(
                {
                    "phase": "formal_blocked",
                    "error": "formal loadtest requires human approval",
                    "need": f"APPROVE={FORMAL_APPROVE_TOKEN}",
                    "hint": "use ops/observability/tpm_burst_bundle.sh prepare|dry-run; set APPROVE only when ready",
                },
                ensure_ascii=False,
                indent=2,
            ),
            file=sys.stderr,
        )
        return 10

    with open(args.from_path, encoding="utf-8") as f:
        report = json.load(f)
    embedded = report.get("estimate") or {}
    if not embedded and not any(s.get("ok") for s in report.get("samples", [])):
        print("calibration report missing estimate/ok samples", file=sys.stderr)
        return 2
    prompt_file = args.prompt_file or report.get("prompt_file") or ""
    if not prompt_file:
        print("--prompt-file required for formal (repo explain pack)", file=sys.stderr)
        return 2
    if not Path(prompt_file).is_file():
        print(f"prompt file not found: {prompt_file}", file=sys.stderr)
        return 2

    base_url = str(args.base_url or loadtest_base_url())
    bad = refuse_aws_cutover_domain(base_url)
    if bad:
        print(json.dumps({"phase": "formal_refused_wrong_origin", "error": bad}, ensure_ascii=False), file=sys.stderr)
        return 2
    host_header = resolve_host_header(base_url, getattr(args, "host_header", None))

    # Recompute with live pool slots so stale calibrate JSON (e.g. 21×12) cannot pin capacity.
    pool_slots = resolve_pool_slots(getattr(args, "pool_slots", None))
    target_tpm = int(embedded.get("target_tpm") or DEFAULT_TARGET_TPM)
    try:
        est = _estimate_from_report(
            report,
            target_tpm=target_tpm,
            pool_slots=pool_slots,
            failure_overhead=float(
                getattr(args, "failure_overhead", None) or DEFAULT_FAILURE_OVERHEAD
            ),
        )
    except ValueError as e:
        print(str(e), file=sys.stderr)
        return 2

    api_key = args.api_key or os.environ.get("TOKENKEY_API_KEY") or os.environ.get("OPENAI_API_KEY")
    total = int(args.total_requests or est.n_plan_total)
    inflight = int(args.max_inflight or est.threads)
    max_tokens = int(args.max_tokens or report.get("max_tokens") or 1200)
    wall_limit_s = float(est.duration_s_p90) + FORMAL_WALL_SLACK_S
    plan_preview = {
        "phase": "formal_plan_preview",
        "dry_run": dry,
        "approve_present": approve == FORMAL_APPROVE_TOKEN,
        "model": args.model or report.get("model") or "kimi-k3",
        "base_url": base_url,
        "host_header": host_header,
        "prompt_file": prompt_file,
        "max_tokens": max_tokens,
        "total_requests": total,
        "max_inflight": inflight,
        "pool_slots": pool_slots,
        "wall_limit_s": wall_limit_s,
        "estimate_feasible": est.feasible,
        "n_min_success": est.n_min_success,
        "tpm_ceiling_conservative": est.tpm_ceiling_conservative,
    }
    print(json.dumps(plan_preview, ensure_ascii=False, indent=2), flush=True)
    if dry:
        print(json.dumps({"phase": "formal_dry_run_ok", "will_not_send_traffic": True}, ensure_ascii=False))
        return 0
    if not api_key:
        print("missing --api-key or TOKENKEY_API_KEY", file=sys.stderr)
        return 2
    if not est.feasible and not args.allow_infeasible:
        print(
            json.dumps(
                {
                    "phase": "formal_refused_infeasible",
                    "reason": est.feasibility_reason,
                    "hint": "pass --allow-infeasible to override after explicit review",
                },
                ensure_ascii=False,
            ),
            file=sys.stderr,
        )
        return 4

    summary = _run_formal_load(
        base_url=base_url,
        api_key=api_key,
        model=str(args.model or report.get("model") or "kimi-k3"),
        prompt_file=prompt_file,
        max_tokens=max_tokens,
        total_requests=total,
        max_inflight=inflight,
        timeout_s=float(args.timeout_s),
        wall_limit_s=wall_limit_s,
        host_header=host_header,
    )
    if args.out:
        Path(args.out).write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        summary["wrote"] = args.out
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0 if summary.get("fail_n", 1) == 0 else 5


def _run_perf_step(
    *,
    base_url: str,
    api_key: str,
    model: str,
    messages: list[dict[str, str]],
    max_tokens: int,
    inflight: int,
    hold_s: float,
    timeout_s: float,
    host_header: str | None,
    requests_out: str | None = None,
    cases: Sequence[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    """Keep `inflight` slots busy until `hold_s` elapses, then drain."""
    import threading
    from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait

    if hold_s <= 0:
        raise ValueError("hold_s must be positive")
    lock = threading.Lock()
    open_n = 0
    started = time.time()
    deadline = started + hold_s
    trace: list[tuple[float, int]] = [(started, 0)]
    results: list[tuple[Sample, float | None]] = []
    submitted = 0
    seq = 0
    case_i = 0
    request_fp = open(requests_out, "a", encoding="utf-8") if requests_out else None

    def one() -> tuple[Sample, float | None]:
        nonlocal open_n, seq, case_i
        with lock:
            open_n += 1
            seq += 1
            this_seq = seq
            case = None
            if cases:
                case = cases[case_i % len(cases)]
                case_i += 1
            trace.append((time.time(), open_n))
        started_unix = time.time()
        replay = prepare_replay_body(case["body"], model=model) if case else None
        fields = case_replay_fields(case) if case else {}
        try:
            sample = _chat_completion(
                base_url=base_url,
                api_key=api_key,
                model=model,
                messages=messages,
                max_tokens=max_tokens,
                timeout_s=timeout_s,
                host_header=host_header,
                stream=True,
                include_temperature=False,
                user_agent=PERF_USER_AGENT,
                payload=replay,
            )
            if request_fp is not None:
                record = request_record_from_sample(
                    sample,
                    account_id=None,
                    model=model,
                    step="perf",
                    inflight=inflight,
                    seq=this_seq,
                    started_unix=started_unix,
                    case_sha256=fields.get("case_sha256"),
                    reasoning_effort=fields.get("reasoning_effort"),
                    output_limit=fields.get("output_limit"),
                    output_limit_field=fields.get("output_limit_field"),
                    redacted_marker=fields.get("redacted_marker"),
                )
                line = json.dumps(record, ensure_ascii=False)
                with lock:
                    request_fp.write(line + "\n")
                    request_fp.flush()
            finished = time.time() if sample.ok else None
            return sample, finished
        finally:
            with lock:
                open_n -= 1
                trace.append((time.time(), open_n))

    workers = max(1, inflight)
    with ThreadPoolExecutor(max_workers=workers) as pool:
        pending: set[Any] = set()
        while True:
            now = time.time()
            if now < deadline and len(pending) < workers:
                pending.add(pool.submit(one))
                submitted += 1
                continue
            if not pending:
                break
            timeout = None
            if now < deadline:
                timeout = max(0.01, deadline - now)
            done, pending = wait(pending, timeout=timeout, return_when=FIRST_COMPLETED)
            for fut in done:
                results.append(fut.result())
    if request_fp is not None:
        request_fp.close()
    ended = time.time()
    trace.append((ended, 0))
    samples = [sample for sample, _ in results]
    completions = [ts for _, ts in results if ts is not None]
    summary = summarize_perf(
        samples,
        wall_s=ended - started,
        inflight_trace=trace,
        success_completion_times=completions,
        inflight=inflight,
    )
    summary["phase"] = "perf_step"
    summary["hold_s"] = hold_s
    summary["submitted_n"] = submitted
    summary["hold_filled"] = (ended - started) + 1e-6 >= hold_s
    summary["error_samples"] = [
        {"status": s.status_code, "error": (s.error or "")[:160]}
        for s in samples
        if not s.ok
    ][:5]
    return summary


def cmd_perf(args: argparse.Namespace) -> int:
    dry = bool(args.dry_run)
    pool_cap: int | None
    if args.pool_slots is not None and int(args.pool_slots) > 0:
        pool_cap = int(args.pool_slots)
    else:
        raw_pool = os.environ.get("TPM_BURST_POOL_SLOTS", "").strip() or os.environ.get("POOL_SLOTS", "").strip()
        try:
            pool_cap = int(raw_pool) if raw_pool else None
        except ValueError:
            print("TPM_BURST_POOL_SLOTS/POOL_SLOTS must be a positive int", file=sys.stderr)
            return 2
        if pool_cap is not None and pool_cap <= 0:
            print("pool slots must be positive", file=sys.stderr)
            return 2
    try:
        steps = perf_steps_for_pool(parse_perf_steps(args.steps), pool_cap)
    except ValueError as e:
        print(str(e), file=sys.stderr)
        return 2
    if not steps:
        print("no perf steps at or below pool_slots", file=sys.stderr)
        return 2
    hold_s = float(args.hold_s)
    if hold_s <= 0:
        print("hold_s must be positive", file=sys.stderr)
        return 2
    base_url = str(args.base_url or loadtest_base_url())
    bad = refuse_aws_cutover_domain(base_url)
    if bad:
        print(json.dumps({"phase": "perf_refused_wrong_origin", "error": bad}, ensure_ascii=False), file=sys.stderr)
        return 2
    host_header = resolve_host_header(base_url, args.host_header)
    cases_path = (args.cases or "").strip()
    prompt_tokens_target = int(args.prompt_tokens_target)
    max_tokens = int(args.max_tokens)
    if prompt_tokens_target <= 0 or max_tokens <= 0:
        print("prompt-tokens-target and max-tokens must be positive", file=sys.stderr)
        return 2
    case_summary = None
    if cases_path and Path(cases_path).is_file():
        try:
            case_summary = summarize_case_pool(load_chat_case_pool(cases_path))
        except (OSError, json.JSONDecodeError):
            case_summary = None
    plan = build_perf_plan(
        base_url=base_url,
        model=args.model,
        steps=steps,
        hold_s=hold_s,
        success_min=float(args.success_min),
        pool_slots=pool_cap,
        cases_path=cases_path or None,
        prompt_tokens_target=prompt_tokens_target,
        max_tokens=max_tokens,
        case_summary=case_summary,
    )
    plan["host_header"] = host_header
    plan["success_rate_means"] = "transport success: HTTP 2xx, finish stop|length, usage, completed stream"
    if dry:
        plan["phase"] = "perf_dry_run_ok"
    print(json.dumps(plan, ensure_ascii=False, indent=2), flush=True)
    if dry:
        return 0
    if os.environ.get("APPROVE") != FORMAL_APPROVE_TOKEN:
        return _emit_approval_block("perf_blocked")
    api_key = args.api_key or os.environ.get("TOKENKEY_API_KEY") or os.environ.get("OPENAI_API_KEY")
    if not api_key:
        print("missing --api-key or TOKENKEY_API_KEY", file=sys.stderr)
        return 2
    if cases_path:
        try:
            cases = load_chat_case_pool(cases_path)
        except (OSError, json.JSONDecodeError) as exc:
            print(json.dumps({"phase": "perf_cases_unreadable", "path": cases_path, "error": str(exc)}), file=sys.stderr)
            return 2
        if not cases:
            print(json.dumps({"phase": "perf_cases_empty", "path": cases_path}), file=sys.stderr)
            return 2
        start_shape = {"source": "customer_cases", "cases_path": cases_path, "replayable_n": len(cases)}
    else:
        cases = [
            synthetic_context_case(
                prompt_tokens_target=prompt_tokens_target,
                max_tokens=max_tokens,
            )
        ]
        start_shape = {
            "source": "synthetic_context",
            "prompt_tokens_target": prompt_tokens_target,
            "max_tokens": max_tokens,
            "replayable_n": 1,
        }
    executed: list[dict[str, Any]] = []
    if args.requests_out:
        Path(args.requests_out).write_text("", encoding="utf-8")
    print(
        json.dumps(
            {
                "phase": "perf_start",
                "base_url": base_url,
                "model": args.model,
                "steps": steps,
                "hold_s": hold_s,
                **start_shape,
            },
            ensure_ascii=False,
        ),
        flush=True,
    )
    for inflight in steps:
        step = _run_perf_step(
            base_url=base_url,
            api_key=api_key,
            model=args.model,
            messages=[],
            max_tokens=0,
            inflight=inflight,
            hold_s=hold_s,
            timeout_s=float(args.timeout_s),
            host_header=host_header,
            requests_out=args.requests_out or None,
            cases=cases,
        )
        executed.append(step)
        print(json.dumps(step, ensure_ascii=False), flush=True)
        if float(step["success_rate"]) + 1e-12 < float(args.success_min):
            break
    operating = select_operating_point(executed, float(args.success_min))
    summary = {
        "phase": "perf_summary",
        "base_url": base_url,
        "model": args.model,
        "success_min": float(args.success_min),
        "hold_s": hold_s,
        "operating_point": operating,
        "steps": executed,
    }
    if args.out:
        Path(args.out).write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        summary["wrote"] = args.out
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0 if operating is not None else 5


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="Recipe-1 TPM burst calibrate/estimate/plan")
    sub = p.add_subparsers(dest="cmd", required=True)

    c = sub.add_parser("calibrate", help=f"live calibrate with 1..{CALIBRATE_MAX_N} requests")
    c.add_argument(
        "--base-url",
        default="",
        help="default: TOKENKEY_BASE_URL or https://api.galaxywaveai.com; refuses api-cn.tokenkey.dev",
    )
    c.add_argument("--host-header", default="", help="Host header when base-url is an IP")
    c.add_argument("--api-key", default="")
    c.add_argument("--model", default="kimi-k3")
    c.add_argument("--prompt-tokens-target", type=int, default=35000)
    c.add_argument("--max-tokens", type=int, default=1200,
                   help="completion cap; for ~1000 output use 1100–1300 with force_completion")
    c.add_argument("--no-force-completion", action="store_true",
                   help="disable long-essay instruction (legacy filler-only prompt)")
    c.add_argument("--prompt-file", default="",
                   help="use packed coding prompt from pack_repo_prompt.py (implies no force fill)")
    c.add_argument("--n", type=int, default=5)
    c.add_argument("--sleep-s", type=float, default=1.0)
    c.add_argument("--timeout-s", type=float, default=600.0)
    c.add_argument("--target-tpm", type=int, default=DEFAULT_TARGET_TPM)
    c.add_argument(
        "--pool-slots",
        type=int,
        default=None,
        help="accounts×concurrency; default env TPM_BURST_POOL_SLOTS/POOL_SLOTS or legacy 21×12 snapshot",
    )
    c.add_argument("--failure-overhead", type=float, default=DEFAULT_FAILURE_OVERHEAD)
    c.add_argument("--out", default="")
    c.set_defaults(func=cmd_calibrate)

    e = sub.add_parser("estimate", help="pure estimate from numbers or calibrate JSON")
    e.add_argument("--tok-per-req", type=float, default=None)
    e.add_argument("--duration-s", type=float, default=33.0)
    e.add_argument("--from", dest="from_path", default="")
    e.add_argument("--target-tpm", type=int, default=DEFAULT_TARGET_TPM)
    e.add_argument("--pool-slots", type=int, default=None)
    e.add_argument("--failure-overhead", type=float, default=DEFAULT_FAILURE_OVERHEAD)
    e.set_defaults(func=cmd_estimate)

    pl = sub.add_parser("plan", help="print formal plan from calibrate JSON; does not run load")
    pl.add_argument("--from", dest="from_path", required=True)
    pl.add_argument("--target-tpm", type=int, default=0)
    pl.add_argument("--pool-slots", type=int, default=None)
    pl.add_argument("--failure-overhead", type=float, default=DEFAULT_FAILURE_OVERHEAD)
    pl.set_defaults(func=cmd_plan)

    f = sub.add_parser("formal", help="formal load (blocked without APPROVE; --dry-run ok)")
    f.add_argument("--from", dest="from_path", required=True)
    f.add_argument("--prompt-file", default="")
    f.add_argument(
        "--base-url",
        default="",
        help="default: TOKENKEY_BASE_URL or https://api.galaxywaveai.com; refuses api-cn.tokenkey.dev",
    )
    f.add_argument("--host-header", default="", help="Host header when base-url is an IP")
    f.add_argument("--api-key", default="")
    f.add_argument("--model", default="")
    f.add_argument("--max-tokens", type=int, default=0)
    f.add_argument("--total-requests", type=int, default=0)
    f.add_argument("--max-inflight", type=int, default=0)
    f.add_argument("--pool-slots", type=int, default=None)
    f.add_argument("--failure-overhead", type=float, default=DEFAULT_FAILURE_OVERHEAD)
    f.add_argument("--timeout-s", type=float, default=600.0)
    f.add_argument("--out", default="")
    f.add_argument("--dry-run", action="store_true")
    f.add_argument("--allow-infeasible", action="store_true")
    f.set_defaults(func=cmd_formal)

    perf = sub.add_parser(
        "perf",
        help="pool streaming perf at ~100% success (blocked without APPROVE; --dry-run sends nothing)",
    )
    perf.add_argument("--base-url", default="", help=f"default: TOKENKEY_BASE_URL or {LOADTEST_BASE_URL}")
    perf.add_argument("--host-header", default="", help="Host header when base-url is an IP")
    perf.add_argument("--api-key", default="")
    perf.add_argument("--model", default="kimi-k3")
    perf.add_argument("--prompt-tokens-target", type=int, default=PERF_PROMPT_TOKENS_TARGET)
    perf.add_argument("--max-tokens", type=int, default=PERF_MAX_TOKENS)
    perf.add_argument(
        "--cases",
        default="",
        help="when set, replay this customer jsonl instead of the owned filler context",
    )
    perf.add_argument("--steps", default=",".join(str(s) for s in PERF_STEPS_DEFAULT))
    perf.add_argument(
        "--hold-s",
        type=float,
        default=PERF_HOLD_S,
        help="seconds to keep each concurrency step full (default 300)",
    )
    perf.add_argument("--success-min", type=float, default=PERF_SUCCESS_MIN)
    perf.add_argument("--pool-slots", type=int, default=None)
    perf.add_argument("--timeout-s", type=float, default=600.0)
    perf.add_argument("--out", default="")
    perf.add_argument("--requests-out", default="", help="JSONL path for one row per request")
    perf.add_argument("--dry-run", action="store_true")
    perf.set_defaults(func=cmd_perf)

    return p


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
