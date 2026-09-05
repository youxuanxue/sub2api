#!/usr/bin/env python3
"""Serial inference smoke for every model in the OpenRouter provider catalog.

Routes each catalog row to chat / image / video based on output_modalities.
Honors openrouter.stream_required metadata (GLM stream-only models).

    python3 ops/pricing/probe-openrouter-provider-inference-serial.py --via-ssm
    python3 ops/pricing/probe-openrouter-provider-inference-serial.py --via-ssm --sleep 1.5

Exit 0 = all models pass; 1 = one or more failures; 2 = setup error.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
CHAIN_PROBE = REPO_ROOT / "ops/pricing/probe-openrouter-provider-chain.py"
BASE_URL = "https://api.tokenkey.dev"
DEFAULT_SLEEP_S = 1.0


def load_chain_probe():
    name = "or_chain_probe"
    spec = importlib.util.spec_from_file_location(name, CHAIN_PROBE)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


@dataclass
class InferResult:
    model: str
    kind: str
    ok: bool
    http: int
    detail: str = ""
    elapsed_s: float = 0.0
    poll_http: int | None = None


@dataclass
class SerialReport:
    results: list[InferResult] = field(default_factory=list)

    @property
    def passed(self) -> bool:
        return all(r.ok for r in self.results)


def modality_types(mods) -> set[str]:
    """Accept schema 2.4 modality objects or legacy string lists."""
    out: set[str] = set()
    for m in mods or []:
        if isinstance(m, dict):
            t = m.get("type")
            if isinstance(t, str) and t:
                out.add(t)
        elif isinstance(m, str) and m:
            out.add(m)
    return out


def route_kind(output_modalities) -> str:
    outs = modality_types(output_modalities)
    if "video" in outs:
        return "video"
    if "image" in outs and "text" not in outs:
        return "image"
    return "chat"


def stream_required(model: dict) -> bool:
    or_meta = model.get("openrouter") or {}
    if str(or_meta.get("stream_required", "")).lower() == "true":
        return True
    desc = str(model.get("description") or "")
    return "stream=true" in desc.lower()


def infer_chat(base: str, api_key: str, model_id: str, *, stream: bool, http_json) -> tuple[int, bool, str]:
    code, body, detail = http_json(
        "POST",
        f"{base}/v1/chat/completions",
        api_key,
        {
            "model": model_id,
            "messages": [{"role": "user", "content": "Reply with exactly: ok"}],
            "max_tokens": 8,
            "stream": stream,
        },
        timeout=120,
    )
    if stream:
        raw = body if isinstance(body, str) else detail
        ok = code == 200 and isinstance(raw, str) and "data:" in raw
        return code, ok, raw[:200] if not ok else "stream chunks ok"
    ok = code == 200 and isinstance(body, dict) and bool((body.get("choices") or [{}])[0].get("message"))
    return code, ok, detail if not ok else "choices.message ok"


def infer_image(base: str, api_key: str, model_id: str, http_json) -> tuple[int, bool, str]:
    code, body, detail = http_json(
        "POST",
        f"{base}/openrouter/v1/images",
        api_key,
        {"model": model_id, "prompt": "a blue circle", "aspect_ratio": "1:1"},
        timeout=180,
    )
    b64 = ""
    if isinstance(body, dict):
        b64 = (((body.get("data") or [{}])[0]).get("b64_json") or "")
    ok = code == 200 and len(b64) > 20
    return code, ok, detail if not ok else f"b64_json len={len(b64)}"


def infer_video(base: str, api_key: str, model_id: str, http_json) -> tuple[int, bool, str, int | None]:
    code, body, detail = http_json(
        "POST",
        f"{base}/openrouter/v1/videos",
        api_key,
        {"model": model_id, "prompt": "a cat walking"},
        timeout=120,
    )
    task_id = poll = None
    if isinstance(body, dict):
        task_id = body.get("id") or body.get("task_id")
        poll = body.get("polling_url")
    if code not in (200, 202) or not task_id or not poll:
        return code, False, detail, None
    poll_url = poll if str(poll).startswith("http") else f"{base}/openrouter/v1/videos/{task_id}"
    poll_code, _, poll_detail = http_json("GET", poll_url, api_key, timeout=60)
    ok = poll_code == 200
    return code, ok, poll_detail if not ok else f"submit={code} poll=200 task={task_id}", poll_code


def infer_one(base: str, api_key: str, model: dict, http_json) -> InferResult:
    mid = model.get("id", "")
    kind = route_kind(model.get("output_modalities") or [])
    t0 = time.time()
    try:
        if kind == "chat":
            use_stream = stream_required(model)
            code, ok, detail = infer_chat(base, api_key, mid, stream=use_stream, http_json=http_json)
            return InferResult(model=mid, kind=kind, ok=ok, http=code, detail=detail, elapsed_s=round(time.time() - t0, 1))
        if kind == "image":
            code, ok, detail = infer_image(base, api_key, mid, http_json)
            return InferResult(model=mid, kind=kind, ok=ok, http=code, detail=detail, elapsed_s=round(time.time() - t0, 1))
        code, ok, detail, poll_code = infer_video(base, api_key, mid, http_json)
        return InferResult(
            model=mid,
            kind=kind,
            ok=ok,
            http=code,
            detail=detail,
            elapsed_s=round(time.time() - t0, 1),
            poll_http=poll_code,
        )
    except Exception as exc:  # noqa: BLE001
        return InferResult(model=mid, kind=kind, ok=False, http=0, detail=f"exception: {exc}", elapsed_s=round(time.time() - t0, 1))


def run_serial(
    base_url: str,
    api_key: str,
    *,
    sleep_s: float,
    http_json,
) -> SerialReport:
    base = base_url.rstrip("/")
    code, payload, _ = http_json("GET", f"{base}/openrouter/v1/models", api_key, timeout=60)
    if code != 200:
        raise RuntimeError(f"catalog fetch failed http={code}")
    catalog = sorted(payload.get("data") or [], key=lambda m: m.get("id", ""))
    report = SerialReport()
    total = len(catalog)
    for i, model in enumerate(catalog, 1):
        result = infer_one(base, api_key, model, http_json)
        report.results.append(result)
        mark = "OK" if result.ok else "FAIL"
        stream_note = " stream" if result.kind == "chat" and stream_required(model) else ""
        print(
            f"[{i}/{total}] {mark} {result.model} ({result.kind}{stream_note}) "
            f"http={result.http} {result.detail[:120]} ({result.elapsed_s}s)"
        )
        sys.stdout.flush()
        if i < total and sleep_s > 0:
            time.sleep(sleep_s)
    return report


def main() -> int:
    chain = load_chain_probe()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=BASE_URL)
    parser.add_argument("--api-key", default="")
    parser.add_argument("--inference-key", default="", help="alias of --api-key (compat)")
    parser.add_argument("--monitor-key", default="", help="ignored; compat")
    parser.add_argument("--via-ssm", action="store_true")
    parser.add_argument("--sleep", type=float, default=DEFAULT_SLEEP_S, help="seconds between models")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    api_key = (args.api_key or args.inference_key or args.monitor_key).strip()
    if args.via_ssm or not api_key:
        api_key = chain.fetch_keys_via_ssm()

    print(f"Serial OR inference smoke — {args.base_url} sleep={args.sleep}s")
    sys.stdout.flush()
    report = run_serial(
        args.base_url,
        api_key,
        sleep_s=args.sleep,
        http_json=chain.http_json,
    )
    failed = [r for r in report.results if not r.ok]
    passed = len(report.results) - len(failed)

    if args.json:
        print(
            json.dumps(
                {
                    "passed": report.passed,
                    "total": len(report.results),
                    "failed_count": len(failed),
                    "results": [r.__dict__ for r in report.results],
                },
                indent=2,
                ensure_ascii=False,
            )
        )
    else:
        print(f"\nSUMMARY: {passed}/{len(report.results)} passed")
        if failed:
            print("Failures:")
            for r in failed:
                print(f"  - {r.model} ({r.kind}) http={r.http} {r.detail[:200]}")

    return 0 if report.passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
