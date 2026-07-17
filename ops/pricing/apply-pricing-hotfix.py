#!/usr/bin/env python3
"""apply-pricing-hotfix.py — 缺价模型定价热更新（飞书「模型缺价」告警的配套 runbook 脚本）。

背景：catch-all 账号会把任意模型名转发到上游；缺价模型按零成本记账（不拒绝服务），
由 PricingMissingNotifier 发飞书提醒运营。本脚本把既有的人肉止血路径机械化
（deepseek-v4 先例：手工配渠道定价止血 → tk_pricing_overlay.json 固化）：

  热更（立即生效，无需发版）：渠道定价 DB 凌驾一切定价来源，经 prod admin API
  （x-api-key，参考 settings.admin_api_key）upsert —— 与 TLS 指纹/tiers 的
  「repo 基线 + 脚本推运行时」热更新模式同构。
  固化（随下次发版生效）：fill-only 条目写入 backend/internal/service/
  tk_pricing_overlay.json（litellm 镜像补上后自动让位），提 PR。

子命令：
  lookup        从 litellm 上游全量源（含被裁剪镜像丢掉的带前缀键）查某模型价格，
                输出建议的 overlay 条目 + 渠道定价 payload。
  channels      列出渠道（id / 名称 / 各定价条目的平台与模型数），帮运营选 --channel-id。
  apply         GET 渠道 → upsert 该模型的定价条目 → PUT 回写（默认 dry-run，--yes 才真写）。
  stage-overlay 把条目以文本追加方式写入 tk_pricing_overlay.json（保持既有格式零搅动）。
  selftest      离线自检（纯函数全覆盖，无网络）。

环境变量：TOKENKEY_BASE_URL（默认 https://api.tokenkey.dev）、TOKENKEY_ADMIN_API_KEY。
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from decimal import Decimal
from pathlib import Path

LITELLM_URL_DEFAULT = (
    "https://raw.githubusercontent.com/BerriAI/litellm/main/"
    "model_prices_and_context_window.json"
)
BASE_URL_DEFAULT = os.environ.get("TOKENKEY_BASE_URL", "https://api.tokenkey.dev")
REPO_ROOT = Path(__file__).resolve().parents[2]
OVERLAY_PATH = REPO_ROOT / "backend" / "internal" / "service" / "tk_pricing_overlay.json"

# overlay 允许携带的 litellm 字段（与 pricing_service_tk_overlay.go 的解析面对齐）。
OVERLAY_FIELDS = (
    "litellm_provider",
    "mode",
    "input_cost_per_token",
    "output_cost_per_token",
    "cache_creation_input_token_cost",
    "cache_creation_input_token_cost_above_1hr",
    "cache_read_input_token_cost",
    "output_cost_per_image",
    "output_cost_per_image_token",
    "output_cost_per_video_per_second",
    "max_input_tokens",
    "max_output_tokens",
    "max_tokens",
    "supports_prompt_caching",
    "supports_function_calling",
    "supports_tool_choice",
    "supports_vision",
    "supports_pdf_input",
    "supports_reasoning",
)


# ---------------------------------------------------------------------------
# 纯函数（selftest 全覆盖）
# ---------------------------------------------------------------------------

def litellm_candidates(pricing: dict, model: str) -> dict:
    """返回 litellm 全量源里匹配 model 的条目：裸名精确匹配 + 任意 provider 前缀键。

    被裁剪镜像丢掉的就是带前缀键（如 vertex_ai/imagen-4.0-generate-001），
    这里必须把它们找回来。
    """
    out = {}
    lower = model.strip().lower()
    for key, entry in pricing.items():
        if not isinstance(entry, dict):
            continue
        k = key.lower()
        if k == lower or k.endswith("/" + lower):
            out[key] = entry
    return out


def pick_litellm_candidate(found: dict, model: str, explicit_key: str | None = None):
    """从多候选里确定性选一个条目。

    apply --yes 直接写计费配置，不允许猜：裸名精确键优先（litellm 的规范键）；
    只剩一个候选时取它；多个 provider 前缀键并存（价格可能不同）则拒绝，
    要求 --litellm-key 显式指定。返回 (key, entry)。
    """
    if explicit_key:
        if explicit_key not in found:
            raise SystemExit(f"--litellm-key {explicit_key!r} not among matches: "
                             f"{', '.join(sorted(found))}")
        return explicit_key, found[explicit_key]
    lower = model.strip().lower()
    for key in found:
        if key.lower() == lower:
            return key, found[key]
    if len(found) == 1:
        key = next(iter(found))
        return key, found[key]
    raise SystemExit(
        f"ambiguous: {len(found)} litellm keys match {model!r} with no bare-name key: "
        f"{', '.join(sorted(found))} — prices may differ per provider; "
        "pass --litellm-key to choose explicitly")


def synthesize_overlay_entry(litellm_entry: dict, source_note: str) -> dict:
    """从 litellm 条目裁出 overlay 条目（只保留 overlay 解析面认识的字段）。"""
    out = {}
    for field in OVERLAY_FIELDS:
        if field in litellm_entry and litellm_entry[field] is not None:
            out[field] = litellm_entry[field]
    if source_note:
        out["source"] = source_note
    return out


def synthesize_channel_pricing(model: str, platform: str, entry: dict) -> dict:
    """从 litellm 条目合成渠道定价 request 条目（admin PUT /channels/:id 的
    model_pricing 元素，字段名对齐 channelModelPricingRequest）。"""
    out = {"platform": platform, "models": [model]}
    mode = (entry.get("mode") or "chat").lower()
    if entry.get("output_cost_per_image") is not None:
        out["billing_mode"] = "per_request"
        out["per_request_price"] = entry["output_cost_per_image"]
        return out
    if mode in ("image_generation", "image"):
        out["billing_mode"] = "per_request"
        if entry.get("output_cost_per_image") is not None:
            out["per_request_price"] = entry["output_cost_per_image"]
        return out
    out["billing_mode"] = "token"
    mapping = (
        ("input_cost_per_token", "input_price"),
        ("output_cost_per_token", "output_price"),
        ("cache_creation_input_token_cost", "cache_write_price"),
        ("cache_read_input_token_cost", "cache_read_price"),
    )
    for src, dst in mapping:
        if entry.get(src) is not None:
            out[dst] = entry[src]
    return out


def strip_pricing_response_fields(pricing_list: list) -> list:
    """把 GET 响应里的 model_pricing 还原成 PUT request 形态（剥掉 id 等响应字段）。"""
    cleaned = []
    for p in pricing_list or []:
        q = {k: v for k, v in p.items() if k not in ("id",) and v is not None}
        intervals = []
        for itv in q.get("intervals") or []:
            intervals.append({k: v for k, v in itv.items()
                              if k not in ("id", "pricing_id") and v is not None})
        if intervals:
            q["intervals"] = intervals
        else:
            q.pop("intervals", None)
        cleaned.append(q)
    return cleaned


def upsert_model_pricing(existing: list, new_entry: dict) -> list:
    """把 new_entry（单模型条目）upsert 进渠道定价列表。

    确定性规则：先把该模型从所有既有条目的 models 里摘除（同平台才摘；条目因此
    变空则整条删除），再把 new_entry 追加到尾部。多模型共享条目不被整条覆盖。
    """
    model = new_entry["models"][0].strip().lower()
    platform = (new_entry.get("platform") or "").strip().lower()
    out = []
    for p in existing or []:
        p_platform = (p.get("platform") or "").strip().lower()
        if p_platform != platform:
            out.append(p)
            continue
        models = [m for m in (p.get("models") or []) if m.strip().lower() != model]
        if not models:
            continue  # 条目只剩该模型 → 整条让位给 new_entry
        if len(models) != len(p.get("models") or []):
            p = dict(p)
            p["models"] = models
        out.append(p)
    out.append(new_entry)
    return out


def format_number(v) -> str:
    """价格以普通十进制输出（不出现 1e-05 这类科学计数法），与 overlay 文件既有风格一致。"""
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, int):
        return str(v)
    d = v if isinstance(v, Decimal) else Decimal(str(v))
    s = format(d, "f")
    if "." in s:
        s = s.rstrip("0").rstrip(".")
    return s or "0"


def render_overlay_block(model: str, entry: dict) -> str:
    """手工序列化 overlay 条目（2 空格缩进、价格普通十进制），供文本追加。"""
    lines = [f'  {json.dumps(model, ensure_ascii=False)}: {{']
    items = list(entry.items())
    for i, (k, v) in enumerate(items):
        comma = "," if i < len(items) - 1 else ""
        if isinstance(v, str):
            rendered = json.dumps(v, ensure_ascii=False)
        else:
            rendered = format_number(v)
        lines.append(f'    {json.dumps(k, ensure_ascii=False)}: {rendered}{comma}')
    lines.append("  }")
    return "\n".join(lines)


def insert_overlay_entry(text: str, model: str, entry: dict) -> str:
    """把条目以文本方式追加到 overlay JSON 末尾（既有内容一个字节不动）。"""
    data = json.loads(text)
    if model in data:
        raise ValueError(
            f"model {model!r} already present in overlay — edit it by hand "
            "(textual append cannot replace in place)")
    trimmed = text.rstrip("\n")
    if not trimmed.endswith("}"):
        raise ValueError("overlay file does not end with '}'")
    body = trimmed[:-1].rstrip()
    if not body.endswith("}"):
        raise ValueError("overlay file has no trailing entry to append after")
    new_text = body + ",\n" + render_overlay_block(model, entry) + "\n}\n"
    parsed = json.loads(new_text)  # 追加后必须仍是合法 JSON
    if model not in parsed:
        raise ValueError("post-insert validation failed")
    return new_text


# ---------------------------------------------------------------------------
# 网络面（admin API / litellm 源）
# ---------------------------------------------------------------------------

def http_json(url: str, method: str = "GET", payload: dict | None = None,
              api_key: str | None = None, timeout: int = 30) -> dict:
    headers = {"Accept": "application/json"}
    body = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        body = json.dumps(payload).encode()
    if api_key:
        headers["x-api-key"] = api_key
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        detail = e.read().decode(errors="replace")[:500]
        raise SystemExit(f"HTTP {e.code} {method} {url}: {detail}") from e


def admin_api_key() -> str:
    key = os.environ.get("TOKENKEY_ADMIN_API_KEY", "").strip()
    if not key:
        raise SystemExit(
            "TOKENKEY_ADMIN_API_KEY not set (settings.admin_api_key; "
            "see memory/runbook: prod admin API via caddy x-api-key)")
    return key


def fetch_litellm(url: str) -> dict:
    print(f"fetching litellm source: {url}", file=sys.stderr)
    return http_json(url)


def unwrap_data(resp: dict):
    if isinstance(resp, dict) and "data" in resp:
        return resp["data"]
    return resp


# ---------------------------------------------------------------------------
# 子命令
# ---------------------------------------------------------------------------

def cmd_lookup(args) -> int:
    pricing = fetch_litellm(args.litellm_url)
    found = litellm_candidates(pricing, args.model)
    if not found:
        print(f"no litellm entry for {args.model!r} (bare or provider-prefixed). "
              "Price it from the provider's official list, then use "
              "`apply --input-price/--output-price` and `stage-overlay --entry-json`.")
        return 1
    for key, entry in found.items():
        print(f"\n=== litellm key: {key}")
        print(json.dumps(entry, indent=2, ensure_ascii=False))
    picked_key, pick = pick_litellm_candidate(found, args.model, args.litellm_key)
    note = f"litellm {picked_key} (captured via apply-pricing-hotfix.py lookup)"
    print(f"\n=== suggested overlay entry (from {picked_key}; "
          "stage-overlay --from-litellm uses this):")
    print(render_overlay_block(args.model, synthesize_overlay_entry(pick, note)))
    print("\n=== suggested channel pricing payload element (apply --from-litellm uses this):")
    print(json.dumps(synthesize_channel_pricing(args.model, args.platform, pick),
                     indent=2, ensure_ascii=False))
    return 0


def cmd_channels(args) -> int:
    resp = http_json(f"{args.base_url}/api/v1/admin/channels", api_key=admin_api_key())
    data = unwrap_data(resp)
    items = data.get("items") if isinstance(data, dict) else data
    if items is None:
        items = data if isinstance(data, list) else []
    for ch in items:
        pricing = ch.get("model_pricing") or []
        plats = sorted({(p.get("platform") or "?") for p in pricing})
        nmodels = sum(len(p.get("models") or []) for p in pricing)
        print(f"#{ch.get('id')}\t{ch.get('name')}\tstatus={ch.get('status')}\t"
              f"pricing: {nmodels} models on {','.join(plats) or '-'}")
    return 0


def cmd_apply(args) -> int:
    if args.from_litellm:
        pricing = fetch_litellm(args.litellm_url)
        found = litellm_candidates(pricing, args.model)
        if not found:
            raise SystemExit(f"--from-litellm: no litellm entry for {args.model!r}; "
                             "pass explicit --input-price/--output-price instead")
        _, picked = pick_litellm_candidate(found, args.model, args.litellm_key)
        new_entry = synthesize_channel_pricing(args.model, args.platform, picked)
    else:
        new_entry = {"platform": args.platform, "models": [args.model]}
        if args.per_request_price is not None:
            new_entry["billing_mode"] = "per_request"
            new_entry["per_request_price"] = args.per_request_price
        else:
            if args.input_price is None or args.output_price is None:
                raise SystemExit("apply needs --from-litellm, or --per-request-price, "
                                 "or both --input-price and --output-price")
            new_entry["billing_mode"] = "token"
            new_entry["input_price"] = args.input_price
            new_entry["output_price"] = args.output_price
            if args.cache_read_price is not None:
                new_entry["cache_read_price"] = args.cache_read_price
            if args.cache_write_price is not None:
                new_entry["cache_write_price"] = args.cache_write_price

    key = admin_api_key()
    url = f"{args.base_url}/api/v1/admin/channels/{args.channel_id}"
    channel = unwrap_data(http_json(url, api_key=key))
    existing = strip_pricing_response_fields(channel.get("model_pricing") or [])
    merged = upsert_model_pricing(existing, new_entry)
    payload = {"model_pricing": merged}

    print(f"channel #{args.channel_id} ({channel.get('name')}): "
          f"{len(existing)} pricing entries -> {len(merged)}")
    print("new/updated entry:")
    print(json.dumps(new_entry, indent=2, ensure_ascii=False))
    if not args.yes:
        print("\nDRY-RUN（未写入）。确认无误后追加 --yes 真正 PUT。")
        return 0
    http_json(url, method="PUT", payload=payload, api_key=key)
    print("PUT ok — 渠道定价缓存即时失效，下一请求生效。")
    print("别忘了固化：stage-overlay（或确认 litellm 镜像已收录该裸名键）。")
    return 0


def cmd_stage_overlay(args) -> int:
    if args.entry_json:
        raw = sys.stdin.read() if args.entry_json == "-" else Path(args.entry_json).read_text()
        entry = json.loads(raw)
        if not isinstance(entry, dict) or not entry:
            raise SystemExit("--entry-json must be a non-empty JSON object")
        if args.source:
            entry["source"] = args.source
    else:
        pricing = fetch_litellm(args.litellm_url)
        found = litellm_candidates(pricing, args.model)
        if not found:
            raise SystemExit(f"no litellm entry for {args.model!r}; "
                             "provide --entry-json with provider official prices")
        picked_key, picked = pick_litellm_candidate(found, args.model, args.litellm_key)
        note = args.source or (f"litellm {picked_key} "
                               "(captured via apply-pricing-hotfix.py)")
        entry = synthesize_overlay_entry(picked, note)

    text = OVERLAY_PATH.read_text()
    new_text = insert_overlay_entry(text, args.model, entry)
    if args.dry_run:
        print(render_overlay_block(args.model, entry))
        print(f"\nDRY-RUN（未写入 {OVERLAY_PATH}）。")
        return 0
    OVERLAY_PATH.write_text(new_text)
    print(f"appended {args.model!r} to {OVERLAY_PATH}")
    print("next: scripts/checks/pricing-overlay.py + scripts/preflight.sh, 然后提 PR 固化。")
    return 0


# ---------------------------------------------------------------------------
# selftest（离线）
# ---------------------------------------------------------------------------

def cmd_selftest(_args) -> int:
    fixture = {
        "sample_spec": "doc",
        "gpt-x": {"mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06},
        "vertex_ai/imagen-9.0-generate-001": {
            "mode": "image_generation", "litellm_provider": "vertex_ai-image-models",
            "output_cost_per_image": 0.04},
        "deepseek/deepseek-v9": {
            "mode": "chat", "litellm_provider": "deepseek",
            "input_cost_per_token": 1.4e-07, "output_cost_per_token": 2.8e-07,
            "cache_read_input_token_cost": 2.8e-09, "supports_prompt_caching": True},
    }

    # litellm_candidates: 裸名 + 带前缀键都要命中；大小写不敏感。
    assert list(litellm_candidates(fixture, "gpt-x")) == ["gpt-x"]
    assert list(litellm_candidates(fixture, "imagen-9.0-generate-001")) == \
        ["vertex_ai/imagen-9.0-generate-001"]
    assert list(litellm_candidates(fixture, "DeepSeek-V9")) == ["deepseek/deepseek-v9"]
    assert litellm_candidates(fixture, "nope") == {}

    # pick_litellm_candidate: 裸名优先；单候选直取；多前缀键无裸名 → 拒绝（防止
    # 不同 provider 价格不同时静默取错）；--litellm-key 显式指定必须存在于候选。
    multi = {
        "openrouter/foo-1": {"input_cost_per_token": 9e-06},
        "vertex_ai/foo-1": {"input_cost_per_token": 1e-06},
    }
    k, _ = pick_litellm_candidate(dict(multi, **{"foo-1": {"input_cost_per_token": 2e-06}}), "foo-1")
    assert k == "foo-1"  # 裸名优先
    k, _ = pick_litellm_candidate({"vertex_ai/foo-1": multi["vertex_ai/foo-1"]}, "foo-1")
    assert k == "vertex_ai/foo-1"  # 单候选直取
    try:
        pick_litellm_candidate(multi, "foo-1")
        raise AssertionError("multi-candidate without bare key must be rejected")
    except SystemExit:
        pass
    k, _ = pick_litellm_candidate(multi, "foo-1", "vertex_ai/foo-1")
    assert k == "vertex_ai/foo-1"  # 显式指定
    try:
        pick_litellm_candidate(multi, "foo-1", "nope/foo-1")
        raise AssertionError("unknown --litellm-key must be rejected")
    except SystemExit:
        pass

    # synthesize_overlay_entry: 只保留白名单字段 + source。
    ov = synthesize_overlay_entry(fixture["deepseek/deepseek-v9"], "note")
    assert ov["litellm_provider"] == "deepseek" and ov["source"] == "note"
    assert "mode" in ov and ov["supports_prompt_caching"] is True

    # synthesize_channel_pricing: chat → token 模式；image → per_request。
    cp = synthesize_channel_pricing("deepseek-v9", "anthropic", fixture["deepseek/deepseek-v9"])
    assert cp["billing_mode"] == "token" and cp["input_price"] == 1.4e-07
    assert cp["cache_read_price"] == 2.8e-09 and cp["models"] == ["deepseek-v9"]
    ci = synthesize_channel_pricing("imagen-9.0-generate-001", "gemini",
                                    fixture["vertex_ai/imagen-9.0-generate-001"])
    assert ci["billing_mode"] == "per_request" and ci["per_request_price"] == 0.04

    # upsert: 新模型追加；同平台同模型替换；多模型共享条目只摘除该模型；跨平台不动。
    existing = [
        {"platform": "anthropic", "models": ["glm-5", "qwen-9"], "billing_mode": "token",
         "input_price": 1e-06, "output_price": 2e-06},
        {"platform": "gemini", "models": ["deepseek-v9"], "billing_mode": "token"},
    ]
    new = {"platform": "anthropic", "models": ["deepseek-v9"], "billing_mode": "token",
           "input_price": 1.4e-07, "output_price": 2.8e-07}
    merged = upsert_model_pricing(existing, new)
    assert merged[-1] == new and len(merged) == 3  # gemini 条目不受影响
    replaced = upsert_model_pricing(merged, dict(new, input_price=9e-07))
    assert len(replaced) == 3 and replaced[-1]["input_price"] == 9e-07
    shared = upsert_model_pricing(
        [{"platform": "anthropic", "models": ["a", "b"], "billing_mode": "token"}],
        {"platform": "anthropic", "models": ["a"], "billing_mode": "token"})
    assert shared[0]["models"] == ["b"] and shared[1]["models"] == ["a"]

    # format_number: 永不输出科学计数法。
    assert format_number(1e-05) == "0.00001"
    assert format_number(0.04) == "0.04"
    assert format_number(True) == "true" and format_number(7) == "7"

    # insert_overlay_entry: 既有文本零搅动 + 追加后合法；重复键拒绝。
    base = '{\n  "_meta": {\n    "note": "x"\n  },\n  "m1": {\n    "mode": "chat"\n  }\n}\n'
    inserted = insert_overlay_entry(base, "m2", {"mode": "chat", "input_cost_per_token": 1e-05})
    assert inserted.startswith(base[:-3])  # 原文本（除收尾）原样保留
    parsed = json.loads(inserted)
    assert parsed["m2"]["input_cost_per_token"] == 1e-05
    assert "1e-05" not in inserted  # 普通十进制
    try:
        insert_overlay_entry(base, "m1", {"mode": "chat"})
        raise AssertionError("duplicate key must be rejected")
    except ValueError:
        pass

    # 真实 overlay 文件:只读校验可被追加(不落盘)。
    real = OVERLAY_PATH.read_text()
    out = insert_overlay_entry(real, "tk-selftest-sentinel-model", {"mode": "chat"})
    assert json.loads(out)["tk-selftest-sentinel-model"]["mode"] == "chat"

    # strip_pricing_response_fields: 剥 id / 空值 / interval 内嵌 id。
    stripped = strip_pricing_response_fields([
        {"id": 3, "platform": "anthropic", "models": ["x"], "input_price": None,
         "billing_mode": "token", "output_price": 1e-06,
         "intervals": [{"id": 9, "pricing_id": 3, "min_tokens": 0, "input_price": 1e-06}]},
    ])
    assert "id" not in stripped[0] and "input_price" not in stripped[0]
    assert stripped[0]["intervals"] == [{"min_tokens": 0, "input_price": 1e-06}]

    print("selftest: OK")
    return 0


# ---------------------------------------------------------------------------

def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("lookup", help="query litellm full source for a model's price")
    p.add_argument("--model", required=True)
    p.add_argument("--platform", default="anthropic",
                   help="platform used in the suggested channel payload (default anthropic)")
    p.add_argument("--litellm-url", default=LITELLM_URL_DEFAULT)
    p.add_argument("--litellm-key", help="exact litellm key to use when several match")
    p.set_defaults(fn=cmd_lookup)

    p = sub.add_parser("channels", help="list channels to pick --channel-id")
    p.add_argument("--base-url", default=BASE_URL_DEFAULT)
    p.set_defaults(fn=cmd_channels)

    p = sub.add_parser("apply", help="hot-apply channel pricing via admin API (dry-run by default)")
    p.add_argument("--model", required=True)
    p.add_argument("--channel-id", type=int, required=True)
    p.add_argument("--platform", required=True,
                   help="group platform the pricing entry binds to (anthropic/openai/gemini/newapi/...)")
    p.add_argument("--from-litellm", action="store_true")
    p.add_argument("--litellm-url", default=LITELLM_URL_DEFAULT)
    p.add_argument("--litellm-key", help="exact litellm key to use when several match")
    p.add_argument("--input-price", type=float, help="USD per input token")
    p.add_argument("--output-price", type=float, help="USD per output token")
    p.add_argument("--cache-read-price", type=float)
    p.add_argument("--cache-write-price", type=float)
    p.add_argument("--per-request-price", type=float, help="USD per request (image etc.)")
    p.add_argument("--base-url", default=BASE_URL_DEFAULT)
    p.add_argument("--yes", action="store_true", help="actually PUT (default is dry-run)")
    p.set_defaults(fn=cmd_apply)

    p = sub.add_parser("stage-overlay", help="append a fill-only entry to tk_pricing_overlay.json")
    p.add_argument("--model", required=True)
    p.add_argument("--from-litellm", action="store_true")
    p.add_argument("--litellm-url", default=LITELLM_URL_DEFAULT)
    p.add_argument("--litellm-key", help="exact litellm key to use when several match")
    p.add_argument("--entry-json", help="path to a JSON object (or '-' for stdin) "
                                        "with overlay fields, for models litellm lacks")
    p.add_argument("--source", help="provenance note stored in the entry's source field")
    p.add_argument("--dry-run", action="store_true")
    p.set_defaults(fn=cmd_stage_overlay)

    p = sub.add_parser("selftest", help="offline self-checks")
    p.set_defaults(fn=cmd_selftest)

    args = ap.parse_args()
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
