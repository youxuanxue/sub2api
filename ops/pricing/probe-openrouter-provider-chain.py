#!/usr/bin/env python3
"""Live prod compliance probe for TokenKey OpenRouter provider seller surface.

Validates endpoints and catalog schema against:
https://openrouter.ai/docs/guides/community/for-providers

Runs locally against https://api.tokenkey.dev when keys are set, or via SSM on prod
when --via-ssm is passed (reads an OR seller key from prod DB for billing user 32).

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
BILLING_USER_ID = 32
SELLER_KEY_NAME = "openrouter"

VALID_INPUT_MODALITIES = {"text", "image", "file", "audio", "video"}
VALID_OUTPUT_MODALITIES = {"text", "image", "embeddings", "audio", "video", "rerank", "speech", "transcription"}
VALID_QUANT = {"int4", "int8", "fp4", "mxfp4", "nvfp4", "fp6", "fp8", "mxfp8", "fp16", "bf16", "fp32"}
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


def modality_types(mods: Any) -> set[str]:
    if not isinstance(mods, list):
        return set()
    out: set[str] = set()
    for m in mods:
        if isinstance(m, str):
            out.add(m)
        elif isinstance(m, dict) and m.get("type"):
            out.add(str(m["type"]))
    return out


def validate_price_entries(entries: Any, report: Report, prefix: str) -> None:
    if entries is None:
        return
    report.add(f"{prefix}.pricing.list", isinstance(entries, list), type(entries).__name__)
    if not isinstance(entries, list):
        return
    for i, entry in enumerate(entries):
        ep = f"{prefix}.pricing[{i}]"
        if not isinstance(entry, dict):
            report.add(ep, False, type(entry).__name__)
            continue
        for req in ("type", "unit", "cost_usd"):
            report.add(f"{ep}.{req}", req in entry and entry[req] is not None, "")
        cost = entry.get("cost_usd")
        if isinstance(cost, str) and cost:
            report.add(f"{ep}.cost_usd.numeric", bool(USD_NUM.match(cost)), cost)


def validate_modality_list(mods: Any, valid: set[str], report: Report, prefix: str, kind: str) -> None:
    report.add(f"{prefix}.{kind}.list", isinstance(mods, list) and len(mods) > 0, type(mods).__name__)
    if not isinstance(mods, list):
        return
    for i, mod in enumerate(mods):
        mp = f"{prefix}.{kind}[{i}]"
        if not isinstance(mod, dict):
            report.add(mp, False, type(mod).__name__)
            continue
        typ = mod.get("type")
        report.add(f"{mp}.type", typ in valid, str(typ))
        validate_price_entries(mod.get("pricing"), report, mp)


def validate_model_entry(model: dict, report: Report) -> None:
    mid = model.get("id", "<missing>")
    prefix = f"catalog[{mid}]"

    for req in ("schema_version", "id", "name", "created", "input_modalities", "output_modalities",
                "quantization", "is_ready"):
        report.add(f"{prefix}.required.{req}", req in model and model[req] is not None,
                   "" if req in model else "missing")

    report.add(f"{prefix}.schema_version", model.get("schema_version") == "2.4",
               str(model.get("schema_version")))

    if not str(mid).startswith("tokenkey/"):
        report.add(f"{prefix}.id_prefix", False, f"id={mid!r}")

    # Flat legacy fields must not reappear on new integrations.
    for legacy in ("pricing", "context_length", "max_output_length",
                   "supported_sampling_parameters", "supported_features", "capacity_tpm"):
        if legacy in model:
            report.add(f"{prefix}.no_flat.{legacy}", False, "legacy flat field present")

    validate_modality_list(model.get("input_modalities"), VALID_INPUT_MODALITIES, report, prefix, "input_modalities")
    validate_modality_list(model.get("output_modalities"), VALID_OUTPUT_MODALITIES, report, prefix, "output_modalities")

    quant = model.get("quantization")
    if quant:
        report.add(f"{prefix}.quantization.valid", quant in VALID_QUANT, quant)

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
        outs = modality_types(m.get("output_modalities"))
        ins = modality_types(m.get("input_modalities"))
        if out["chat"] is None and outs == {"text"} and "image" not in ins:
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
            if modality_types(m.get("output_modalities")) == {"text"}:
                out["chat"] = m.get("id")
                break
    return out


def run_probe(base_url: str, api_key: str, full_catalog: bool) -> Report:
    report = Report()
    base = base_url.rstrip("/")

    # Auth boundaries
    code, _, _ = http_json("GET", f"{base}/openrouter/v1/models")
    report.add("auth.catalog_no_key", code == 401, f"http={code}")

    code, _, _ = http_json("GET", f"{base}/openrouter/v1/models", api_key)
    report.add("auth.catalog_seller", code == 200, f"http={code}")

    code, alias_payload, _ = http_json("GET", f"{base}/v1/models", api_key)
    report.add("auth.alias_v1_models", code == 200, f"http={code}")

    # Catalog schema (full or sample)
    code, catalog_payload, _ = http_json("GET", f"{base}/openrouter/v1/models", api_key)
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
        code, body, raw = http_json("POST", f"{base}/v1/chat/completions", api_key, {
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
        code, body, raw = http_json("POST", f"{base}/openrouter/v1/images", api_key, {
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
        code, body, raw = http_json("POST", f"{base}/openrouter/v1/images", api_key, {
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
        code, body, raw = http_json("POST", f"{base}/openrouter/v1/videos", api_key, {
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
                                            api_key, timeout=60)
                report.add("infer.video_poll.status", code2 == 200, f"http={code2}")

    return report


def fetch_keys_via_ssm() -> str:
    """Return one seller API key for billing user 32 (prefer name openrouter)."""
    spec = importlib.util.spec_from_file_location("ssm", REPO_ROOT / "ops/stage0/ssm_execution.py")
    ssm = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(ssm)
    inst = ssm.resolve_prod_instance()
    preferred = SELLER_KEY_NAME
    py = f"""
import subprocess, json
PSQL = ["sudo", "docker", "exec", "-i", "tokenkey-postgres",
        "psql", "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c"]
USER_ID = {BILLING_USER_ID}
PREFERRED = {preferred!r}

def key_for(name):
    sql = (
        "SELECT key FROM api_keys WHERE user_id=%d AND name='%s' AND deleted_at IS NULL "
        "ORDER BY id LIMIT 1" % (USER_ID, name.replace("'", "''"))
    )
    return subprocess.check_output(PSQL + [sql], text=True).strip()

def any_key():
    sql = (
        "SELECT key FROM api_keys WHERE user_id=%d AND deleted_at IS NULL "
        "ORDER BY id LIMIT 1" % USER_ID
    )
    return subprocess.check_output(PSQL + [sql], text=True).strip()

key = key_for(PREFERRED) or any_key()
print(json.dumps({{"key": key}}))
"""
    shell = f"python3 - <<'PY'\n{py}\nPY"
    b64 = base64.b64encode(shell.encode()).decode()
    out = ssm.run_shell_b64(inst, b64, "or-provider chain probe keys")
    for line in reversed(out.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            data = json.loads(line)
            key = (data.get("key") or "").strip()
            if key:
                return key
    raise RuntimeError(f"failed to fetch seller key via SSM; tail={out[-800:]}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=BASE_URL)
    parser.add_argument("--api-key", default="", help="OR seller API key (billing user)")
    parser.add_argument("--inference-key", default="", help="alias of --api-key (compat)")
    parser.add_argument("--monitor-key", default="", help="ignored; compat alias")
    parser.add_argument("--via-ssm", action="store_true", help="fetch OR seller key from prod via SSM")
    parser.add_argument("--full-catalog", action="store_true", help="validate every catalog entry (slow)")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    api_key = (args.api_key or args.inference_key or args.monitor_key).strip()
    if args.via_ssm or not api_key:
        api_key = fetch_keys_via_ssm()

    report = run_probe(args.base_url, api_key, args.full_catalog)
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
