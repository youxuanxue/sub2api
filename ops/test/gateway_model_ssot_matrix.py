#!/usr/bin/env python3
"""Derive and optionally probe the universal-key model/protocol matrix.

The source is the user-visible priced catalog projection
(`/api/v1/public/pricing`), which is built from TokenKey's servable allowlists,
curated newapi manifest, and pricing sources. This tool deliberately does not
maintain a second hand-written model list.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass
from typing import Any

DEFAULT_BASE_URL = os.environ.get("TK_FULLTEST_BASE_URL", "https://api.tokenkey.dev")
DEFAULT_TIMEOUT = float(os.environ.get("TK_FULLTEST_TIMEOUT", "90"))

VENDOR_PLATFORM = {
    "anthropic": "anthropic",
    "openai": "openai",
    "azure_openai": "openai",
    "gemini": "gemini",
    "google": "gemini",
    "vertex_ai": "gemini",
    "vertex_ai-language-models": "gemini",
    "antigravity": "antigravity",
    "newapi": "newapi",
    "volcengine": "newapi",
    "deepseek": "newapi",
    "dashscope": "newapi",
    "alibaba": "newapi",
    "zhipu": "newapi",
    "bigmodel": "newapi",
    "zai": "newapi",
    "xai": "grok",
    "x-ai": "grok",
}

OPENAI_COMPAT_TEXT_PLATFORMS = {"openai", "newapi", "grok"}
GEMINI_NATIVE_PLATFORMS = {"gemini", "antigravity"}
MESSAGE_POLICY_PLATFORMS = {"openai", "newapi"}


@dataclass(frozen=True)
class MatrixRow:
    platform: str
    vendor: str
    model: str
    modality: str
    protocol: str
    method: str
    path: str
    paid: bool
    source: str
    note: str = ""


@dataclass(frozen=True)
class ExcludedRow:
    vendor: str
    model: str
    reason: str


def fetch_json(url: str, timeout: float) -> dict[str, Any]:
    req = urllib.request.Request(url, headers={"user-agent": "tokenkey-ssot-matrix/1"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:  # noqa: S310 fixed/operator URL
        return json.loads(resp.read().decode("utf-8"))


def priced(row: dict[str, Any]) -> bool:
    pricing = row.get("pricing") or {}
    tiers = pricing.get("tiers") or []
    base = tiers[0] if tiers else pricing
    return any(
        (base.get(field) or 0) > 0
        for field in (
            "input_per_1k_tokens",
            "output_per_1k_tokens",
            "output_cost_per_image",
            "output_cost_per_second",
        )
    )


def is_embedding_model(vendor: str, model: str) -> bool:
    v = vendor.lower()
    m = model.lower()
    return "embedding" in v or "embedding" in m or m.startswith("text-embedding")


def is_antigravity_image_model(model: str) -> bool:
    m = model.lower()
    return m.startswith("gemini-") and "image" in m


def modality_for(row: dict[str, Any]) -> str:
    pricing = row.get("pricing") or {}
    billing_mode = (pricing.get("billing_mode") or "").strip().lower()
    if billing_mode in {"image", "video"}:
        return billing_mode
    vendor = str(row.get("vendor") or "")
    model = str(row.get("model_id") or row.get("id") or "")
    if is_embedding_model(vendor, model):
        return "embeddings"
    return "text"


def text_protocols(platform: str) -> list[tuple[str, str, str, str]]:
    rows: list[tuple[str, str, str, str]] = [
        ("messages", "POST", "/v1/messages", ""),
        ("count_tokens", "POST", "/v1/messages/count_tokens", ""),
        ("chat", "POST", "/v1/chat/completions", ""),
        ("responses", "POST", "/v1/responses", ""),
    ]
    if platform in GEMINI_NATIVE_PLATFORMS:
        rows.append(("gemini_generate", "POST", "/v1beta/models/{model}:generateContent", "native gemini shape"))
    if platform in MESSAGE_POLICY_PLATFORMS:
        rows[0] = (rows[0][0], rows[0][1], rows[0][2], "requires group allow_messages_dispatch")
        rows[1] = (rows[1][0], rows[1][1], rows[1][2], "requires group allow_messages_dispatch")
    return rows


def rows_from_public_catalog(payload: dict[str, Any], source: str) -> tuple[list[MatrixRow], list[ExcludedRow]]:
    rows: list[MatrixRow] = []
    excluded: list[ExcludedRow] = []
    for item in payload.get("data") or []:
        model = str(item.get("model_id") or item.get("id") or "").strip()
        vendor = str(item.get("vendor") or "").strip()
        if not model:
            continue
        if not priced(item):
            excluded.append(ExcludedRow(vendor, model, "not_priced_in_public_catalog"))
            continue
        platform = VENDOR_PLATFORM.get(vendor)
        if not platform:
            excluded.append(ExcludedRow(vendor, model, "vendor_not_mapped_to_universal_platform"))
            continue

        modality = modality_for(item)
        if modality == "text":
            for protocol, method, path, note in text_protocols(platform):
                rows.append(MatrixRow(platform, vendor, model, modality, protocol, method, path, False, source, note))
        elif modality == "embeddings":
            if platform in {"openai", "newapi"}:
                rows.append(MatrixRow(platform, vendor, model, modality, "embeddings", "POST", "/v1/embeddings", False, source))
            else:
                excluded.append(ExcludedRow(vendor, model, "embeddings_not_in_universal_endpoint_candidates"))
        elif modality == "image":
            if platform == "antigravity" and is_antigravity_image_model(model):
                rows.append(
                    MatrixRow(
                        platform,
                        vendor,
                        model,
                        modality,
                        "chat_image",
                        "POST",
                        "/v1/chat/completions",
                        True,
                        source,
                        "antigravity Studio image path",
                    )
                )
            else:
                rows.append(MatrixRow(platform, vendor, model, modality, "image", "POST", "/v1/images/generations", True, source))
        elif modality == "video":
            rows.append(MatrixRow(platform, vendor, model, modality, "video", "POST", "/v1/video/generations", True, source))
        else:
            excluded.append(ExcludedRow(vendor, model, f"unknown_modality:{modality}"))
    rows.sort(key=lambda r: (r.platform, r.modality, r.model, r.protocol))
    excluded.sort(key=lambda r: (r.vendor, r.model, r.reason))
    return rows, excluded


def load_matrix(args) -> tuple[list[MatrixRow], list[ExcludedRow]]:
    if args.source != "live-pricing":
        raise SystemExit(f"unsupported source: {args.source}")
    base = args.base_url.rstrip("/")
    payload = fetch_json(f"{base}/api/v1/public/pricing", args.timeout)
    return rows_from_public_catalog(payload, f"live-pricing:{base}/api/v1/public/pricing")


def filter_rows(rows: list[MatrixRow], args) -> list[MatrixRow]:
    if args.only_platform:
        rows = [r for r in rows if r.platform == args.only_platform]
    if args.only_protocol:
        rows = [r for r in rows if r.protocol == args.only_protocol]
    wanted = requested_models(args)
    if wanted:
        rows = [r for r in rows if r.model in wanted]
    return rows


def requested_models(args) -> set[str]:
    out: set[str] = set()
    for raw in getattr(args, "model", []) or []:
        for part in re.split(r"[,\s]+", raw):
            if part:
                out.add(part)
    return out


def compact_body(protocol: str, model: str) -> dict[str, Any]:
    if protocol == "messages":
        return {"model": model, "max_tokens": 8, "messages": [{"role": "user", "content": "hi"}]}
    if protocol == "count_tokens":
        return {"model": model, "messages": [{"role": "user", "content": "hi"}]}
    if protocol == "chat":
        body: dict[str, Any] = {"model": model, "max_tokens": 8, "messages": [{"role": "user", "content": "hi"}]}
        if chat_requires_stream(model):
            body["stream"] = True
        if model in {"qwen3.7-max-preview", "qwen3.7-max-2026-05-17"}:
            body["enable_thinking"] = True
        elif re.match(r"qwen3[-.]", model):
            body["enable_thinking"] = False
        return body
    if protocol == "chat_image":
        return {
            "model": model,
            "max_tokens": 1024,
            "stream": False,
            "messages": [{"role": "user", "content": "Create a simple image of a small red circle on white."}],
            "extra_body": {"google": {"image_config": {"aspect_ratio": "1:1"}}},
        }
    if protocol == "responses":
        return {
            "model": model,
            "instructions": "You are helpful.",
            "input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Say OK"}]}],
            "stream": False,
        }
    if protocol == "gemini_generate":
        return {"contents": [{"role": "user", "parts": [{"text": "Reply with OK"}]}], "generationConfig": {"maxOutputTokens": 8}}
    if protocol == "image":
        size = "2048x2048" if "seedream" in model.lower() else "1024x1024"
        return {"model": model, "prompt": "a small red circle on white", "n": 1, "size": size}
    if protocol == "video":
        prompt = "a small red ball rolling on a table"
        if "seedance" in model.lower():
            prompt += " --resolution 480p --duration 5"
        return {"model": model, "prompt": prompt, "seconds": "4"}
    if protocol == "embeddings":
        return {"model": model, "input": "hello"}
    raise ValueError(f"unsupported protocol: {protocol}")


def chat_requires_stream(model: str) -> bool:
    return model in {
        "glm-4.5",
        "glm-4.5-air",
        "qwen3.7-max-preview",
        "qwen3.7-max-2026-05-17",
    }


def shape_ok(protocol: str, code: int, body: dict[str, Any], body_text: str = "") -> bool:
    if code != 200:
        return False
    if protocol == "messages":
        return body.get("type") == "message" or "content" in body
    if protocol == "count_tokens":
        return isinstance(body.get("input_tokens"), (int, float))
    if protocol == "chat":
        if body_text.lstrip().startswith("data:"):
            return True
        return bool((body.get("choices") or [{}])[0].get("message"))
    if protocol == "chat_image":
        return bool(body)
    if protocol == "responses":
        return bool(body.get("id") or body.get("output") or body.get("usage"))
    if protocol == "gemini_generate":
        return bool((body.get("candidates") or [{}])[0].get("content"))
    if protocol == "image":
        return bool((body.get("data") or [None])[0])
    if protocol == "video":
        return bool(body.get("id") or body.get("task_id"))
    if protocol == "embeddings":
        data = body.get("data") or []
        return bool(data and data[0].get("embedding") is not None)
    return False


def classify(code: int, body_text: str, ok: bool) -> tuple[str, str]:
    haystack = body_text.lower()
    if code == 200:
        return ("PASS", "") if ok else ("FAIL", "200 response shape mismatch")
    if code == 403:
        if re.search(r"universal_no_entitled_group|no platform in your plan|group_not_allowed|not allowed", haystack):
            return "SKIP", "not authorized for platform/group"
        if re.search(r"does not have access to responses api|accessdenied", haystack):
            return "SKIP", "model/protocol not provisioned"
        return "FAIL", "unexpected 403"
    if code == 429:
        if re.search(r"no available accounts|available accounts exhausted", haystack):
            return "SKIP", "empty schedulable pool"
        return "SKIP", "upstream throttle/transient"
    if code in {400, 404}:
        if re.search(
            r"retired|sunset|not[_ ]found|does not exist|invalid model|unknown model|model_not_found|not supported|"
            r"unsupported model|upstream rejected the request|does not support|not a valid|no endpoints|"
            r"invalid[_ ]argument|missing scopes|insufficient permission|not available on the serving account|"
            r"channel_type|requested capability|does not have access to responses api|only support stream mode|"
            r"enable_thinking parameter is restricted",
            haystack,
        ):
            return "SKIP", "model/protocol not provisioned"
        return "FAIL", f"unexpected {code}"
    if code in {500, 502, 503, 504}:
        return "SKIP", f"{code} upstream/gateway transient"
    if code == 401:
        return "FAIL", "401 auth failure"
    if code == 0:
        return "SKIP", "timeout/connection interrupted"
    return "FAIL", f"unexpected HTTP {code}"


def post_json(base_url: str, path: str, key: str, body: dict[str, Any], timeout: float) -> tuple[int, str]:
    url = base_url.rstrip("/") + path
    data = json.dumps(body, separators=(",", ":")).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers={
            "authorization": f"Bearer {key}",
            "anthropic-version": "2023-06-01",
            "content-type": "application/json",
            "user-agent": "tokenkey-ssot-matrix/1",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:  # noqa: S310 operator URL
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except Exception as e:  # noqa: BLE001
        return 0, str(e)


def render_path(row: MatrixRow) -> str:
    return row.path.replace("{model}", urllib.parse.quote(row.model, safe=""))


def row_to_record(row: MatrixRow) -> dict[str, Any]:
    return asdict(row)


def cmd_list(args) -> int:
    rows, excluded = load_matrix(args)
    rows = filter_rows(rows, args)
    if args.include_paid is False:
        rows = [r for r in rows if not r.paid]
    if args.limit:
        rows = rows[: args.limit]

    if args.format == "json":
        print(json.dumps({"rows": [row_to_record(r) for r in rows], "excluded": [asdict(e) for e in excluded]}, indent=2, sort_keys=True))
        return 0
    print("platform\tvendor\tmodel\tmodality\tprotocol\tmethod\tpath\tpaid\tnote")
    for r in rows:
        print(f"{r.platform}\t{r.vendor}\t{r.model}\t{r.modality}\t{r.protocol}\t{r.method}\t{r.path}\t{int(r.paid)}\t{r.note}")
    if args.show_excluded and excluded:
        print("\n# excluded")
        print("vendor\tmodel\treason")
        for e in excluded:
            print(f"{e.vendor}\t{e.model}\t{e.reason}")
    return 0


def cmd_run(args) -> int:
    key = args.key or os.environ.get("TK_FULLTEST_KEY", "")
    if not key:
        print("ERROR: TK_FULLTEST_KEY is required for run", file=sys.stderr)
        return 2
    rows, excluded = load_matrix(args)
    rows = filter_rows(rows, args)
    if not args.include_paid:
        rows = [r for r in rows if not r.paid]
    if args.limit:
        rows = rows[: args.limit]

    pass_count = skip_count = fail_count = 0
    print("platform\tmodality\tprotocol\tmodel\thttp\tresult\tnote")
    for row in rows:
        path = render_path(row)
        code, body_text = post_json(args.base_url, path, key, compact_body(row.protocol, row.model), args.timeout)
        try:
            body = json.loads(body_text) if body_text else {}
        except json.JSONDecodeError:
            body = {}
        result, note = classify(code, body_text, shape_ok(row.protocol, code, body, body_text))
        if result == "PASS":
            pass_count += 1
        elif result == "SKIP":
            skip_count += 1
        else:
            fail_count += 1
        print(f"{row.platform}\t{row.modality}\t{row.protocol}\t{row.model}\t{code}\t{result}\t{note}")
    print(f"PASS={pass_count} SKIP={skip_count} FAIL={fail_count} EXCLUDED={len(excluded)}")
    return 1 if fail_count else 0


def cmd_selftest(_args) -> int:
    payload = {
        "data": [
            {"vendor": "openai", "model_id": "gpt-5.1", "pricing": {"input_per_1k_tokens": 1}},
            {"vendor": "volcengine", "model_id": "doubao-seedream-5-0-260128", "pricing": {"billing_mode": "image", "output_cost_per_image": 0.03}},
            {"vendor": "volcengine", "model_id": "doubao-seedance-2-0-260128", "pricing": {"billing_mode": "video", "output_cost_per_second": 0.3}},
            {"vendor": "antigravity", "model_id": "gemini-3-pro-image", "pricing": {"billing_mode": "image", "output_cost_per_image": 0.1}},
            {"vendor": "vertex_ai-embedding-models", "model_id": "gemini-embedding-001", "pricing": {"input_per_1k_tokens": 0.1}},
            {"vendor": "bedrock", "model_id": "bedrock-x", "pricing": {"input_per_1k_tokens": 0.1}},
            {"vendor": "openai", "model_id": "free-x", "pricing": {}},
        ]
    }
    rows, excluded = rows_from_public_catalog(payload, "fixture")
    got = {(r.platform, r.model, r.protocol, r.path, r.paid) for r in rows}
    assert ("openai", "gpt-5.1", "chat", "/v1/chat/completions", False) in got
    assert ("newapi", "doubao-seedream-5-0-260128", "image", "/v1/images/generations", True) in got
    assert ("newapi", "doubao-seedance-2-0-260128", "video", "/v1/video/generations", True) in got
    assert ("antigravity", "gemini-3-pro-image", "chat_image", "/v1/chat/completions", True) in got
    assert any(e.model == "gemini-embedding-001" and e.reason == "vendor_not_mapped_to_universal_platform" for e in excluded)
    assert any(e.model == "bedrock-x" and e.reason == "vendor_not_mapped_to_universal_platform" for e in excluded)
    assert any(e.model == "free-x" and e.reason == "not_priced_in_public_catalog" for e in excluded)
    assert compact_body("chat", "qwen3-8b")["enable_thinking"] is False
    assert compact_body("chat", "qwen3.7-max-preview")["stream"] is True
    assert compact_body("chat", "qwen3.7-max-preview")["enable_thinking"] is True
    assert compact_body("chat", "glm-4.5")["stream"] is True
    assert classify(429, '{"error":"No available accounts"}', False)[0] == "SKIP"
    assert classify(403, '{"error":"universal_no_entitled_group"}', False)[0] == "SKIP"
    assert classify(403, '{"error":{"message":"does not have access to responses api"}}', False)[0] == "SKIP"
    assert classify(400, '{"error":{"message":"Upstream rejected the request"}}', False)[0] == "SKIP"
    assert classify(400, '{"error":{"message":"This model only support stream mode"}}', False)[0] == "SKIP"
    assert shape_ok("chat", 200, {}, "data: {\"choices\":[]}\n\ndata: [DONE]\n")
    print("gateway_model_ssot_matrix selftest: PASS")
    return 0


def add_source_args(p: argparse.ArgumentParser) -> None:
    p.add_argument("--source", choices=["live-pricing"], default="live-pricing")
    p.add_argument("--base-url", default=DEFAULT_BASE_URL)
    p.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    p.add_argument("--only-platform", choices=["anthropic", "openai", "gemini", "antigravity", "newapi", "grok"])
    p.add_argument(
        "--only-protocol",
        choices=["messages", "count_tokens", "chat", "responses", "gemini_generate", "chat_image", "image", "video", "embeddings"],
    )
    p.add_argument("--model", action="append", default=[], help="model id to include; may be repeated or comma/space separated")
    p.add_argument("--limit", type=int, default=0)


def main() -> int:
    try:
        sys.stdout.reconfigure(line_buffering=True)
    except AttributeError:
        pass

    parser = argparse.ArgumentParser(description="TokenKey universal model/protocol matrix from the public pricing SSOT projection")
    sub = parser.add_subparsers(dest="cmd", required=True)

    list_p = sub.add_parser("list", help="print the derived matrix without sending model requests")
    add_source_args(list_p)
    list_p.add_argument("--format", choices=["tsv", "json"], default="tsv")
    list_p.add_argument("--include-paid", action="store_true", help="include image/video rows in the listing")
    list_p.add_argument("--show-excluded", action="store_true", help="show priced catalog rows excluded from the universal matrix")
    list_p.set_defaults(func=cmd_list)

    run_p = sub.add_parser("run", help="probe universal key rows derived from the matrix")
    add_source_args(run_p)
    run_p.add_argument("--key", default="")
    run_p.add_argument("--include-paid", action="store_true", help="actually send image/video requests")
    run_p.set_defaults(func=cmd_run)

    selftest_p = sub.add_parser("selftest", help="run offline unit tests")
    selftest_p.set_defaults(func=cmd_selftest)

    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
