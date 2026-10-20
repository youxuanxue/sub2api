#!/usr/bin/env python3
"""Recipe-1 TPM burst planner: calibrate (≤9 req) → estimate → plan.

Formal load is never started unless APPROVE=yes-formal-tpm-loadtest.

Examples:
  # Pure math from measured shape (no network)
  python3 ops/observability/tpm_burst_recipe1.py estimate \\
      --tok-per-req 40000 --duration-s 45 --pool-slots 252 --target-tpm 20000000

  # ≤9 live calibrations against UCloud cn-prod (writes JSON report).
  # Do NOT use https://api-cn.tokenkey.dev — that DNS is cut over to AWS Tokyo.
  python3 ops/observability/tpm_burst_recipe1.py calibrate \\
      --api-key "$KEY" \\
      --model kimi-k3 --prompt-tokens-target 35000 --max-tokens 5000 --n 5 \\
      --out /tmp/tpm-calibrate.json

  # Build formal plan from calibration JSON (still does NOT run load)
  python3 ops/observability/tpm_burst_recipe1.py plan --from /tmp/tpm-calibrate.json

  # Formal load (blocked unless APPROVE=yes-formal-tpm-loadtest)
  python3 ops/observability/tpm_burst_recipe1.py formal --from /tmp/tpm-calibrate.json
"""

from __future__ import annotations

import argparse
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
_DEFAULT_CN_HOST_HEADER = "api-cn.tokenkey.dev"
_PROD_TARGET_JSON = Path(__file__).resolve().parents[2] / "deploy/domestic/prod-target.json"


def _load_prod_target() -> dict[str, Any]:
    try:
        data = json.loads(_PROD_TARGET_JSON.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError, TypeError):
        return {}
    return data if isinstance(data, dict) else {}


def cn_prod_host_header_default() -> str:
    """Caddy vhost name from prod-target fallback_domain (not for TLS SNI)."""
    domain = str(_load_prod_target().get("fallback_domain") or "").strip()
    return domain or _DEFAULT_CN_HOST_HEADER


def cn_prod_public_origin() -> str:
    """UCloud static-IP origin from deploy/domestic/prod-target.json (not DNS)."""
    raw = os.environ.get("TOKENKEY_BASE_URL", "").strip()
    if raw:
        return raw.rstrip("/")
    origin = str(_load_prod_target().get("public_origin") or "").strip().rstrip("/")
    if origin:
        return origin
    raise FileNotFoundError(
        f"missing public_origin in {_PROD_TARGET_JSON}; set TOKENKEY_BASE_URL"
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
    """api-cn.tokenkey.dev currently resolves to Tokyo AWS — not UCloud cn-prod."""
    host = _hostname(base_url)
    cutover = cn_prod_host_header_default().lower()
    if host == cutover:
        return (
            f"base_url host {host!r} is cut over to AWS Tokyo DNS; "
            f"use UCloud static IP from deploy/domestic/prod-target.json "
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
) -> Sample:
    url = base_url.rstrip("/") + "/v1/chat/completions"
    body = {
        "model": model,
        "messages": messages,
        "max_tokens": max_tokens,
        "stream": False,
        "temperature": temperature,
    }
    data = json.dumps(body).encode("utf-8")
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
        "User-Agent": "tokenkey-tpm-burst-recipe1/1.0",
    }
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
    try:
        with urllib.request.urlopen(req, timeout=timeout_s) as resp:
            raw = resp.read()
            status = int(resp.status)
    except urllib.error.HTTPError as e:
        duration = time.perf_counter() - t0
        err_body = e.read().decode("utf-8", errors="replace")[:300]
        return Sample(
            ok=False,
            prompt_tokens=0,
            completion_tokens=0,
            total_tokens=0,
            duration_s=duration,
            status_code=int(e.code),
            error=err_body,
        )
    except Exception as e:  # noqa: BLE001 — surface calibrate errors as sample
        duration = time.perf_counter() - t0
        return Sample(
            ok=False,
            prompt_tokens=0,
            completion_tokens=0,
            total_tokens=0,
            duration_s=duration,
            status_code=0,
            error=str(e)[:300],
        )

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
        error="" if ok else str(payload.get("error") or "")[:300],
    )


def cmd_calibrate(args: argparse.Namespace) -> int:
    n = int(args.n)
    if n < 1 or n > CALIBRATE_MAX_N:
        print(f"calibrate n must be 1..{CALIBRATE_MAX_N}", file=sys.stderr)
        return 2
    api_key = args.api_key or os.environ.get("TOKENKEY_API_KEY") or os.environ.get("OPENAI_API_KEY")
    if not api_key:
        print("missing --api-key or TOKENKEY_API_KEY/OPENAI_API_KEY", file=sys.stderr)
        return 2
    try:
        base_url = str(args.base_url or cn_prod_public_origin())
    except FileNotFoundError as e:
        print(json.dumps({"phase": "calibrate_refused", "error": str(e)}, ensure_ascii=False), file=sys.stderr)
        return 2
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

    try:
        base_url = str(args.base_url or cn_prod_public_origin())
    except FileNotFoundError as e:
        print(json.dumps({"phase": "formal_refused_wrong_origin", "error": str(e)}, ensure_ascii=False), file=sys.stderr)
        return 2
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


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="Recipe-1 TPM burst calibrate/estimate/plan")
    sub = p.add_subparsers(dest="cmd", required=True)

    c = sub.add_parser("calibrate", help=f"live calibrate with 1..{CALIBRATE_MAX_N} requests")
    c.add_argument(
        "--base-url",
        default="",
        help="default: TOKENKEY_BASE_URL or deploy/domestic/prod-target.json public_origin (UCloud IP)",
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
        help="default: TOKENKEY_BASE_URL or UCloud public_origin; refuses api-cn.tokenkey.dev DNS",
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

    return p


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
