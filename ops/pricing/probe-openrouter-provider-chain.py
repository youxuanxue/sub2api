#!/usr/bin/env python3
"""Live prod compliance probe for TokenKey OpenRouter provider seller surface.

Validates endpoints and catalog schema against:
https://openrouter.ai/docs/guides/community/for-providers

Runs locally against https://api.tokenkey.dev when keys are set, or via SSM on prod
when --via-ssm is passed (reads OR inference/monitor keys from prod DB).

Exit 0 = all checks pass; 1 = compliance failures; 2 = setup error.
"""
from __future__ import annotations

import argparse
import base64
import importlib.util
import json
import re
import sys
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
BASE_URL = "https://api.tokenkey.dev"
INFERENCE_KEY_ID = 369
MONITOR_KEY_ID = 370

VALID_INPUT_MODALITIES = {"text", "image", "file", "audio", "video"}
VALID_OUTPUT_MODALITIES = {"text", "image", "embeddings", "audio", "video", "rerank", "speech", "transcription"}
VALID_QUANT = {"int4", "int8", "fp4", "mxfp4", "nvfp4", "fp6", "fp8", "mxfp8", "fp16", "bf16", "fp32"}
VALID_SAMPLING = {
    "temperature", "top_p", "top_k", "min_p", "top_a", "frequency_penalty",
    "presence_penalty", "repetition_penalty", "stop", "seed", "max_tokens", "logit_bias",
}
VALID_FEATURES = {"tools", "json_mode", "structured_outputs", "logprobs", "web_search", "reasoning"}
PRICE_FIELDS = ("prompt", "completion", "image", "request", "input_cache_read")
USD_NUM = re.compile(r"^-?\d+(\.\d+)?$")


@dataclass
class Check:
    name: str
    ok: bool
    detail: str = ""


@dataclass
class Report:
    checks: list[Check] = field(default_factory=list)

    def add(self, name: str, ok: bool, detail: str = "") -> None:
        self.checks.append(Check(name, ok, detail))

    @property
    def passed(self) -> bool:
        return all(c.ok for c in self.checks)


def http_json(method: str, url: str, api_key: str = "", body: dict | None = None, timeout: int = 60) -> tuple[int, Any, str]:
    data = None
    headers = {"Accept": "application/json"}
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
            try:
                return resp.status, json.loads(raw), raw
            except json.JSONDecodeError:
                return resp.status, raw, raw
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace")
        try:
            return exc.code, json.loads(raw), raw
        except json.JSONDecodeError:
            return exc.code, raw, raw


def validate_model_entry(model: dict, report: Report) -> None:
    mid = model.get("id", "<missing>")
    prefix = f"catalog[{mid}]"

    for req in ("id", "name", "created", "input_modalities", "output_modalities", "quantization",
                "context_length", "max_output_length", "pricing", "supported_sampling_parameters", "is_ready"):
        report.add(f"{prefix}.required.{req}", req in model and model[req] is not None,
                   "" if req in model else "missing")

    if not str(mid).startswith("tokenkey/"):
        report.add(f"{prefix}.id_prefix", False, f"id={mid!r}")

    in_mod = model.get("input_modalities") or []
    out_mod = model.get("output_modalities") or []
    report.add(f"{prefix}.input_modalities.valid", all(m in VALID_INPUT_MODALITIES for m in in_mod), str(in_mod))
    report.add(f"{prefix}.output_modalities.valid", all(m in VALID_OUTPUT_MODALITIES for m in out_mod), str(out_mod))

    quant = model.get("quantization")
    if quant:
        report.add(f"{prefix}.quantization.valid", quant in VALID_QUANT, quant)

    pricing = model.get("pricing") or {}
    for pf in PRICE_FIELDS:
        if pf not in pricing:
            report.add(f"{prefix}.pricing.{pf}", False, "missing")
            continue
        val = pricing[pf]
        report.add(f"{prefix}.pricing.{pf}.string", isinstance(val, str), type(val).__name__)
        if isinstance(val, str) and val and val != "0":
            report.add(f"{prefix}.pricing.{pf}.numeric", bool(USD_NUM.match(val)), val)

    for sp in model.get("supported_sampling_parameters") or []:
        if sp not in VALID_SAMPLING:
            report.add(f"{prefix}.sampling.invalid", False, sp)

    for feat in model.get("supported_features") or []:
        if feat not in VALID_FEATURES:
            report.add(f"{prefix}.features.invalid", False, feat)

    for dc in model.get("datacenters") or []:
        cc = (dc or {}).get("country_code", "")
        report.add(f"{prefix}.datacenter.country", bool(re.match(r"^[A-Z]{2}$", cc)), cc)


def validate_catalog(payload: dict, report: Report, sample_limit: int = 0) -> list[dict]:
    data = payload.get("data")
    report.add("catalog.data_array", isinstance(data, list), type(data).__name__)
    if not isinstance(data, list) or not data:
        return []
    report.add("catalog.non_empty", len(data) > 0, f"count={len(data)}")
    items = data if sample_limit <= 0 else data[:sample_limit]
    for model in items:
        if isinstance(model, dict):
            validate_model_entry(model, report)
    return data


def pick_models(catalog: list[dict]) -> dict[str, str | None]:
    out: dict[str, str | None] = {"chat": None, "image": None, "imagen": None, "video": None}
    for m in catalog:
        mid = m.get("id", "")
        outs = set(m.get("output_modalities") or [])
        if out["chat"] is None and outs == {"text"} and "image" not in (m.get("input_modalities") or []):
            if "flash" in mid and "image" not in mid and "imagen" not in mid and "veo" not in mid:
                out["chat"] = mid
        if out["image"] is None and "image" in outs and "gemini" in mid and "flash-image" in mid:
            out["image"] = mid
        if out["imagen"] is None and "image" in outs and "imagen" in mid:
            out["imagen"] = mid
        if out["video"] is None and "video" in outs:
            out["video"] = mid
    if out["chat"] is None:
        for m in catalog:
            if (m.get("output_modalities") or []) == ["text"]:
                out["chat"] = m.get("id")
                break
    return out


def run_probe(base_url: str, inference_key: str, monitor_key: str, full_catalog: bool) -> Report:
    report = Report()
    base = base_url.rstrip("/")

    # Auth boundaries
    code, _, _ = http_json("GET", f"{base}/openrouter/v1/models")
    report.add("auth.catalog_no_key", code == 401, f"http={code}")

    code, _, _ = http_json("GET", f"{base}/openrouter/v1/models", monitor_key)
    report.add("auth.catalog_monitor", code == 200, f"http={code}")

    code, _, _ = http_json("GET", f"{base}/openrouter/v1/models", inference_key)
    report.add("auth.catalog_inference", code == 200, f"http={code}")

    code, alias_payload, _ = http_json("GET", f"{base}/v1/models", monitor_key)
    report.add("auth.alias_v1_models_monitor", code == 200, f"http={code}")

    code, _, raw = http_json("POST", f"{base}/v1/chat/completions", monitor_key,
                             {"model": "tokenkey/claude-sonnet-4-6", "messages": [{"role": "user", "content": "hi"}], "max_tokens": 1})
    report.add("auth.monitor_blocks_chat", code == 403, f"http={code} body={raw[:120]}")

    code, _, raw = http_json("POST", f"{base}/openrouter/v1/images", monitor_key,
                             {"model": "tokenkey/gemini-2.5-flash-image", "prompt": "x"})
    report.add("auth.monitor_blocks_images", code == 403, f"http={code}")

    # Catalog schema (full or sample)
    code, catalog_payload, _ = http_json("GET", f"{base}/openrouter/v1/models", monitor_key)
    report.add("catalog.fetch", code == 200, f"http={code}")
    catalog = validate_catalog(catalog_payload if isinstance(catalog_payload, dict) else {}, report,
                               sample_limit=0 if full_catalog else 25)

    if isinstance(alias_payload, dict) and isinstance(catalog_payload, dict):
        or_ids = {m.get("id") for m in catalog_payload.get("data", []) if isinstance(m, dict)}
        alias_ids = {m.get("id") for m in alias_payload.get("data", []) if isinstance(m, dict)}
        overlap = len(or_ids & alias_ids)
        report.add("catalog.alias_overlap", overlap >= min(len(or_ids), 1), f"overlap={overlap} or={len(or_ids)} alias={len(alias_ids)}")

    picks = pick_models(catalog)
    report.add("pick.chat_model", picks["chat"] is not None, picks["chat"] or "")
    report.add("pick.gemini_image", picks["image"] is not None, picks["image"] or "")
    report.add("pick.imagen_image", picks["imagen"] is not None, picks["imagen"] or "")
    report.add("pick.video_model", picks["video"] is not None, picks["video"] or "")

    # Chat inference with public id
    if picks["chat"]:
        code, body, raw = http_json("POST", f"{base}/v1/chat/completions", inference_key, {
            "model": picks["chat"],
            "messages": [{"role": "user", "content": "Reply with exactly: ok"}],
            "max_tokens": 8,
            "stream": False,
        }, timeout=90)
        report.add("infer.chat.status", code == 200, f"http={code} model={picks['chat']}")
        if isinstance(body, dict):
            has_choice = bool((body.get("choices") or [{}])[0].get("message"))
            report.add("infer.chat.response_shape", has_choice, raw[:200])

    # Gemini native image
    if picks["image"]:
        code, body, raw = http_json("POST", f"{base}/openrouter/v1/images", inference_key, {
            "model": picks["image"],
            "prompt": "a single red apple on white background",
            "aspect_ratio": "1:1",
        }, timeout=180)
        report.add("infer.gemini_image.status", code == 200, f"http={code} model={picks['image']}")
        if isinstance(body, dict):
            b64 = (((body.get("data") or [{}])[0]).get("b64_json") or "")
            report.add("infer.gemini_image.b64_json", code == 200 and len(b64) > 20, f"len={len(b64)}")

    # Imagen / vertex image regression
    if picks["imagen"]:
        code, body, raw = http_json("POST", f"{base}/openrouter/v1/images", inference_key, {
            "model": picks["imagen"],
            "prompt": "a blue circle",
            "aspect_ratio": "1:1",
        }, timeout=180)
        report.add("infer.imagen.status", code == 200, f"http={code} model={picks['imagen']}")
        if isinstance(body, dict):
            b64 = (((body.get("data") or [{}])[0]).get("b64_json") or "")
            report.add("infer.imagen.b64_json", code == 200 and len(b64) > 20, f"len={len(b64)}")

    # Video submit (optional — only if catalog lists video output)
    if picks["video"]:
        code, body, raw = http_json("POST", f"{base}/openrouter/v1/videos", inference_key, {
            "model": picks["video"],
            "prompt": "a cat walking",
        }, timeout=120)
        report.add("infer.video_submit.status", code in (200, 202), f"http={code} model={picks['video']}")
        if isinstance(body, dict):
            task_id = body.get("id") or body.get("task_id")
            poll = body.get("polling_url")
            report.add("infer.video_submit.shape", bool(task_id) and bool(poll), json.dumps(body)[:200])
            if task_id and poll:
                code2, body2, _ = http_json("GET", poll.replace(base, base) if poll.startswith("http") else f"{base}/openrouter/v1/videos/{task_id}",
                                            inference_key, timeout=60)
                report.add("infer.video_poll.status", code2 == 200, f"http={code2}")

    return report


def fetch_keys_via_ssm() -> tuple[str, str]:
    spec = importlib.util.spec_from_file_location("ssm", REPO_ROOT / "ops/stage0/ssm_execution.py")
    ssm = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(ssm)
    inst = ssm.resolve_prod_instance()
    py = f"""
import subprocess, json
PSQL = ["sudo", "docker", "exec", "-i", "tokenkey-postgres",
        "psql", "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c"]
def key_for(kid):
    sql = f"SELECT key FROM api_keys WHERE id={{kid}} AND deleted_at IS NULL LIMIT 1"
    out = subprocess.check_output(PSQL + [sql], text=True).strip()
    return out
print(json.dumps({{"inference": key_for({INFERENCE_KEY_ID}), "monitor": key_for({MONITOR_KEY_ID})}}))
"""
    shell = f"python3 - <<'PY'\n{py}\nPY"
    b64 = base64.b64encode(shell.encode()).decode()
    out = ssm.run_shell_b64(inst, b64, "or-provider chain probe keys")
    for line in reversed(out.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            data = json.loads(line)
            inf = (data.get("inference") or "").strip()
            mon = (data.get("monitor") or "").strip()
            if inf and mon:
                return inf, mon
    raise RuntimeError(f"failed to fetch keys via SSM; tail={out[-800:]}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=BASE_URL)
    parser.add_argument("--inference-key", default="")
    parser.add_argument("--monitor-key", default="")
    parser.add_argument("--via-ssm", action="store_true", help="fetch OR keys from prod via SSM")
    parser.add_argument("--full-catalog", action="store_true", help="validate every catalog entry (slow)")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    inference_key = args.inference_key.strip()
    monitor_key = args.monitor_key.strip()
    if args.via_ssm or (not inference_key or not monitor_key):
        inference_key, monitor_key = fetch_keys_via_ssm()

    report = run_probe(args.base_url, inference_key, monitor_key, args.full_catalog)
    failed = [c for c in report.checks if not c.ok]

    if args.json:
        print(json.dumps({
            "passed": report.passed,
            "total": len(report.checks),
            "failed_count": len(failed),
            "checks": [{"name": c.name, "ok": c.ok, "detail": c.detail} for c in report.checks],
        }, indent=2, ensure_ascii=False))
    else:
        print(f"OpenRouter provider chain probe — {args.base_url}")
        print(f"{'PASS' if report.passed else 'FAIL'}: {len(report.checks) - len(failed)}/{len(report.checks)} checks")
        for c in report.checks:
            mark = "ok" if c.ok else "FAIL"
            suffix = f" — {c.detail}" if c.detail else ""
            print(f"  [{mark}] {c.name}{suffix}")
        if failed:
            print("\nFailed checks:")
            for c in failed:
                print(f"  - {c.name}: {c.detail}")

    return 0 if report.passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
