#!/usr/bin/env python3
"""Unified Anthropic prompt-surface probe for TokenKey.

Reads mitm JSONL (probe_cc_geo_stego_mitm.py) and/or registry fixtures; surfaces:
  - geo_stego_date_line (system[] / system-reminder / date_change)
  - system_identity_anchor
  - system_billing_prefix

Gateway coverage: Go test TestTkProbePromptSurfaceGatewayCoverageJSONL via --check-gateway.
Registry: ops/anthropic/prompt_surface_registry.json
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import os
import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
BACKEND_DIR = REPO_ROOT / "backend"
REGISTRY_PATH = Path(__file__).resolve().parent / "prompt_surface_registry.json"
GEO_PROBE_PATH = Path(__file__).resolve().parent / "probe_cc_geo_stego.py"
GO_IDENTITY_PREFIXES_PATH = (
    REPO_ROOT / "backend/internal/service/gateway_request_tk_prompt_fingerprint.go"
)


def _load_geo_module():
    spec = importlib.util.spec_from_file_location("probe_cc_geo_stego", GEO_PROBE_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {GEO_PROBE_PATH}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def load_registry(path: Path | None = None) -> dict:
    p = path or REGISTRY_PATH
    return json.loads(p.read_text(encoding="utf-8"))


def registry_identity_prefix_pairs(registry: dict) -> list[tuple[str, str]]:
    pairs: list[tuple[str, str]] = []
    for surf in registry.get("surfaces") or []:
        if surf.get("id") != "system_identity_anchor":
            continue
        for item in surf.get("identity_prefixes") or []:
            pairs.append((str(item.get("id") or ""), str(item.get("prefix") or "")))
    return pairs


def go_gateway_identity_prefix_pairs() -> list[tuple[str, str]]:
    text = GO_IDENTITY_PREFIXES_PATH.read_text(encoding="utf-8")
    start = text.find("var tkPromptSurfaceIdentityPrefixes")
    if start < 0:
        raise RuntimeError("tkPromptSurfaceIdentityPrefixes not found in Go gateway fingerprint")
    chunk = text[start : start + 2000]
    return re.findall(r'\{"([^"]+)",\s*"([^"]+)"\}', chunk)


def validate_registry(registry: dict) -> list[str]:
    errors: list[str] = []
    if not registry.get("surfaces"):
        errors.append("registry.surfaces empty")
    ids = set()
    for surf in registry.get("surfaces") or []:
        sid = surf.get("id")
        if not sid:
            errors.append("surface missing id")
            continue
        if sid in ids:
            errors.append(f"duplicate surface id {sid}")
        ids.add(sid)
        if surf.get("probe_module") == "system_anchor":
            prefixes = surf.get("identity_prefixes") or []
            if not prefixes:
                errors.append(f"{sid}: identity_prefixes empty")
    cc_path = REPO_ROOT / "scripts" / "sentinels" / "cc-system-prompt.json"
    if cc_path.is_file():
        cc = json.loads(cc_path.read_text(encoding="utf-8"))
        cc_prefixes = cc.get("capture_anchors", {}).get("identity_prefixes") or []
        reg_prefixes = [prefix for _, prefix in registry_identity_prefix_pairs(registry)]
        if cc_prefixes != reg_prefixes:
            errors.append("system_identity_anchor prefixes drift from cc-system-prompt.json")
    if GO_IDENTITY_PREFIXES_PATH.is_file():
        try:
            go_pairs = go_gateway_identity_prefix_pairs()
        except RuntimeError as exc:
            errors.append(str(exc))
        else:
            reg_pairs = registry_identity_prefix_pairs(registry)
            if go_pairs != reg_pairs:
                errors.append(
                    "Go tkPromptSurfaceIdentityPrefixes drift from registry system_identity_anchor"
                )
    return errors


def identity_prefixes(registry: dict) -> list[dict]:
    for surf in registry.get("surfaces") or []:
        if surf.get("id") == "system_identity_anchor":
            return list(surf.get("identity_prefixes") or [])
    return []


def billing_prefix(registry: dict) -> str:
    for surf in registry.get("surfaces") or []:
        if surf.get("id") == "system_billing_prefix":
            return str(surf.get("match_prefix") or "")
    return "x-anthropic-billing-header"


def analyze_system_surfaces(registry: dict, system_texts: list[str]) -> dict:
    prefixes = identity_prefixes(registry)
    bill = billing_prefix(registry)
    identity_id = "absent"
    billing_present = False
    saw_system_text = False
    for text in system_texts:
        if not text or not str(text).strip():
            continue
        saw_system_text = True
        if bill in text:
            billing_present = True
        for line in str(text).splitlines():
            trimmed = line.strip()
            if not trimmed:
                continue
            for item in prefixes:
                prefix = str(item.get("prefix") or "")
                if trimmed.startswith(prefix):
                    identity_id = str(item.get("id") or "unknown")
                    break
            if identity_id != "absent":
                break
    if identity_id == "absent" and saw_system_text:
        identity_id = "unknown"
    return {
        "identity_anchor_id": identity_id,
        "billing_prefix_present": billing_present,
    }


def analyze_tool_continuation_shape(messages, system_texts: list[str], message_texts: list[str]) -> dict:
    has_tool_result = False
    has_assistant_tool_use = False
    violation = ""
    if isinstance(messages, list):
        for i, msg in enumerate(messages):
            if not isinstance(msg, dict):
                continue
            role = str(msg.get("role") or "")
            content = msg.get("content")
            if not isinstance(content, list):
                continue
            if role == "user":
                result_ids, result_violation = tool_result_ids_from_user_content(content)
                if result_violation and not violation:
                    violation = result_violation
                if result_ids:
                    has_tool_result = True
                    if not violation:
                        violation = validate_previous_assistant_tool_use(messages, i, result_ids)
            for block in content:
                if not isinstance(block, dict):
                    continue
                block_type = str(block.get("type") or "")
                if block_type == "tool_result":
                    has_tool_result = True
                elif block_type == "tool_use" and role == "assistant":
                    has_assistant_tool_use = True
    unknown: list[str] = []
    has_system_reminder = any("<system-reminder>" in text for text in message_texts)
    if violation:
        unknown.append(violation)
    if has_tool_result and not system_texts and not has_system_reminder:
        unknown.append("tool_result_without_system_surface")
    return {
        "has_tool_result": has_tool_result,
        "has_assistant_tool_use": has_assistant_tool_use,
        "tool_continuation_violation": violation,
        "tool_continuation_unknown_surfaces": unknown,
    }


def tool_result_ids_from_user_content(content: list) -> tuple[list[str], str]:
    result_ids: list[str] = []
    seen_non_tool_result = False
    for block in content:
        if not isinstance(block, dict):
            seen_non_tool_result = True
            continue
        if str(block.get("type") or "") != "tool_result":
            seen_non_tool_result = True
            continue
        if seen_non_tool_result:
            return [], "tool_result_not_leading"
        tool_use_id = str(block.get("tool_use_id") or "").strip()
        if not tool_use_id:
            return [], "tool_result_missing_tool_use_id"
        result_ids.append(tool_use_id)
    return result_ids, ""


def validate_previous_assistant_tool_use(messages: list, index: int, result_ids: list[str]) -> str:
    if index == 0:
        return "orphan_tool_result_context"
    previous = messages[index - 1]
    if not isinstance(previous, dict) or str(previous.get("role") or "") != "assistant":
        return "orphan_tool_result_context"
    tool_use_ids: set[str] = set()
    previous_content = previous.get("content")
    if isinstance(previous_content, list):
        for block in previous_content:
            if not isinstance(block, dict) or str(block.get("type") or "") != "tool_use":
                continue
            tool_use_id = str(block.get("id") or "").strip()
            if tool_use_id:
                tool_use_ids.add(tool_use_id)
    if not tool_use_ids:
        return "orphan_tool_result_context"
    result_set = set(result_ids)
    for tool_use_id in result_ids:
        if tool_use_id not in tool_use_ids:
            return "orphan_tool_result_context"
    for tool_use_id in tool_use_ids:
        if tool_use_id not in result_set:
            return "missing_tool_result_for_tool_use"
    return ""


def analyze_record(registry: dict, geo_mod, rec: dict) -> dict:
    geo_row = geo_mod.analyze_record(rec)
    wire = geo_mod.body_wire_from_record(rec)
    system_texts: list[str] = []
    message_texts: list[str] = []
    raw_messages = None
    if wire is not None:
        system_texts = geo_mod.extract_system_texts(wire.get("system"))
        raw_messages = wire.get("messages")
        for msg in raw_messages or []:
            if not isinstance(msg, dict):
                continue
            content = msg.get("content")
            if isinstance(content, str):
                message_texts.append(content)
            elif isinstance(content, list):
                for block in content:
                    if isinstance(block, dict) and block.get("type") == "text":
                        message_texts.append(str(block.get("text") or ""))
    else:
        body = rec.get("body") or {}
        system_texts = geo_mod.extract_system_texts(body.get("system"))
        raw_messages = body.get("messages")
    sys_row = analyze_system_surfaces(registry, system_texts)
    tool_row = analyze_tool_continuation_shape(raw_messages, system_texts, message_texts)
    unknown: list[str] = []
    if geo_row.get("needs_normalize"):
        unknown.append("geo_stego_date_line")
    if sys_row["identity_anchor_id"] == "unknown" and system_texts:
        unknown.append("system_identity_anchor")
    combined_text = "\n".join(system_texts + message_texts)
    if "# Environment" in combined_text or "TZ=Asia/Shanghai" in combined_text or "TZ=Asia/Urumqi" in combined_text:
        unknown.append("cc_environment_section")
    if "The user's email address is" in combined_text:
        unknown.append("cc_user_email")
    unknown.extend(tool_row["tool_continuation_unknown_surfaces"])
    return {
        **geo_row,
        **sys_row,
        **tool_row,
        "unknown_surfaces": unknown,
        "needs_attention": bool(unknown),
    }


def load_records(jsonl: Path) -> list[dict]:
    records: list[dict] = []
    for line in jsonl.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        records.append(json.loads(line))
    return records


def run_gateway_coverage(jsonl: Path, registry: dict) -> int:
    env = os.environ.copy()
    env["TOKENKEY_PROMPT_SURFACE_PROBE_JSONL"] = str(jsonl.resolve())
    env["TOKENKEY_CC_GEO_PROBE_JSONL"] = str(jsonl.resolve())
    test_name = registry.get("gateway_coverage_test") or "TestTkProbePromptSurfaceGatewayCoverageJSONL"
    proc = subprocess.run(
        ["go", "test", "-tags=unit", "./internal/service", "-run", f"^{test_name}$", "-count=1"],
        cwd=str(BACKEND_DIR),
        env=env,
        capture_output=True,
        text=True,
    )
    if proc.stdout:
        print(proc.stdout, end="")
    if proc.stderr:
        print(proc.stderr, end="", file=sys.stderr)
    return proc.returncode


def run_fixture_gateway_check(registry: dict) -> int:
    rel = (registry.get("fixtures") or {}).get("gateway_coverage_jsonl")
    if not rel:
        return 0
    path = REPO_ROOT / rel
    if not path.is_file():
        print(f"fixture missing: {path}", file=sys.stderr)
        return 1
    return run_gateway_coverage(path, registry)


def auto_fix_gateway_gaps(jsonl: Path, registry: dict, geo_mod) -> bool:
    if run_gateway_coverage(jsonl, registry) == 0:
        return False
    return bool(geo_mod.auto_fix_gateway_gaps(jsonl))


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("jsonl", type=Path, nargs="?")
    ap.add_argument("--registry", type=Path, default=REGISTRY_PATH)
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--check-registry", action="store_true")
    ap.add_argument("--check-fixture-gateway", action="store_true")
    ap.add_argument(
        "--check",
        action="store_true",
        help="exit 1 if captured client wire shows non-canonical prompt surfaces",
    )
    ap.add_argument(
        "--check-gateway",
        action="store_true",
        help="exit 1 if gateway normalize does not canonicalize captured bodies",
    )
    ap.add_argument("--fix", action="store_true")
    args = ap.parse_args()

    registry = load_registry(args.registry)
    if args.check_registry:
        errors = validate_registry(registry)
        if errors:
            for err in errors:
                print(f"registry error: {err}", file=sys.stderr)
            return 1
        print("registry ok")
        return 0

    if args.check_fixture_gateway:
        return run_fixture_gateway_check(registry)

    if args.jsonl is None:
        ap.error("jsonl path required unless --check-registry or --check-fixture-gateway")

    geo_mod = _load_geo_module()
    rows = [analyze_record(registry, geo_mod, rec) for rec in load_records(args.jsonl)]

    if args.json:
        print(json.dumps(rows, ensure_ascii=False, indent=2))
    elif not args.check_gateway:
        for row in rows:
            print(
                f"=== scenario={row.get('scenario')} needs_attention={row.get('needs_attention')} "
                f"identity={row.get('identity_anchor_id')} billing={row.get('billing_prefix_present')} ==="
            )
            for dl in row.get("date_lines") or []:
                print(
                    f"  [{dl.get('surface')}] apostrophe={dl.get('apostrophe')} "
                    f"format={dl['date']['format']}"
                )
            if row.get("unknown_surfaces"):
                print(f"  unknown_surfaces={row['unknown_surfaces']}")
            print()

    if args.check and any(r.get("needs_attention") for r in rows):
        return 1

    if args.check_gateway or args.fix:
        code = run_gateway_coverage(args.jsonl, registry)
        if code == 0:
            return 0
        if not args.fix:
            return code
        if auto_fix_gateway_gaps(args.jsonl, registry, geo_mod):
            return run_gateway_coverage(args.jsonl, registry)
        return code

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
