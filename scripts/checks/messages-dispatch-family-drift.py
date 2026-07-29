#!/usr/bin/env python3
"""messages-dispatch-family-drift.py — lockstep guard for Claude tier dispatch mappings.

Source of truth: backend/internal/service/tk_messages_dispatch_family_registry.json

Validates:
  1. Go runtime default constants match platform_defaults.openai / grok.
  2. Frontend OPENAI/GROK default constants match the same platform_defaults.
  3. Every group_defaults entry has a group_families entry and tier models match
     that family's prefix rules (no GPT copy-paste onto gemini/glm/kimi groups).
  4. group_defaults tier models exactly match group_families prefix expectations.

Usage: python3 scripts/checks/messages-dispatch-family-drift.py [--quiet] [--selftest]
Exit 0 ok, 1 drift, 2 missing dep / file.
"""

from __future__ import annotations

import json
import pathlib
import re
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
REGISTRY = REPO_ROOT / "backend/internal/service/tk_messages_dispatch_family_registry.json"
GO_DISPATCH = REPO_ROOT / "backend/internal/service/openai_messages_dispatch.go"
GO_GROK = REPO_ROOT / "backend/internal/service/openai_messages_dispatch_tk_grok.go"
FE_DISPATCH = REPO_ROOT / "frontend/src/views/admin/groupsMessagesDispatch.ts"


def _load_registry() -> dict:
    return json.loads(REGISTRY.read_text(encoding="utf-8"))


def _model_matches_family(model: str, family: str, prefixes: dict[str, list[str]]) -> bool:
    model_l = model.strip().lower()
    for prefix in prefixes.get(family, []):
        if model_l.startswith(prefix.lower()):
            return True
    return False


def _extract_go_const(path: pathlib.Path, name: str) -> str:
    text = path.read_text(encoding="utf-8")
    m = re.search(rf'{re.escape(name)}\s*=\s*"([^"]+)"', text)
    if not m:
        raise ValueError(f"missing Go const {name} in {path}")
    return m.group(1)


def _extract_fe_const(name: str) -> str:
    text = FE_DISPATCH.read_text(encoding="utf-8")
    m = re.search(rf'{re.escape(name)}:\s*"([^"]+)"', text)
    if not m:
        raise ValueError(f"missing FE const {name} in {FE_DISPATCH}")
    return m.group(1)


def check_registry_internal(doc: dict) -> list[str]:
    errors: list[str] = []
    prefixes = doc.get("family_prefixes", {})
    group_defaults = doc.get("group_defaults", {})
    group_families = doc.get("group_families", {})

    for group_name, tiers in group_defaults.items():
        family = group_families.get(group_name)
        if not family:
            errors.append(f"group_defaults[{group_name!r}] missing group_families entry")
            continue
        for field in ("opus_mapped_model", "sonnet_mapped_model", "haiku_mapped_model"):
            model = tiers.get(field, "")
            if not model:
                errors.append(f"group_defaults[{group_name!r}].{field} is empty")
                continue
            if not _model_matches_family(model, family, prefixes):
                errors.append(
                    f"group_defaults[{group_name!r}].{field}={model!r} "
                    f"does not match family {family!r} prefixes {prefixes.get(family)}"
                )
    return errors


def check_go_fe_platform_defaults(doc: dict) -> list[str]:
    errors: list[str] = []
    openai = doc["platform_defaults"]["openai"]
    grok = doc["platform_defaults"]["grok"]

    go_pairs = {
        "defaultOpenAIMessagesDispatchOpusMappedModel": openai["opus_mapped_model"],
        "defaultOpenAIMessagesDispatchSonnetMappedModel": openai["sonnet_mapped_model"],
        "defaultOpenAIMessagesDispatchHaikuMappedModel": openai["haiku_mapped_model"],
        "defaultGrokMessagesDispatchOpusMappedModel": grok["opus_mapped_model"],
        "defaultGrokMessagesDispatchSonnetMappedModel": grok["sonnet_mapped_model"],
        "defaultGrokMessagesDispatchHaikuMappedModel": grok["haiku_mapped_model"],
    }
    for const, want in go_pairs.items():
        path = GO_GROK if const.startswith("defaultGrok") else GO_DISPATCH
        got = _extract_go_const(path, const)
        if got != want:
            errors.append(f"Go {const}={got!r}, registry wants {want!r}")

    fe_pairs = {
        "OPENAI_MESSAGES_DISPATCH_DEFAULTS.opus_mapped_model": (
            "opus_mapped_model",
            openai["opus_mapped_model"],
        ),
        "OPENAI_MESSAGES_DISPATCH_DEFAULTS.sonnet_mapped_model": (
            "sonnet_mapped_model",
            openai["sonnet_mapped_model"],
        ),
        "OPENAI_MESSAGES_DISPATCH_DEFAULTS.haiku_mapped_model": (
            "haiku_mapped_model",
            openai["haiku_mapped_model"],
        ),
        "GROK_MESSAGES_DISPATCH_DEFAULTS.opus_mapped_model": (
            "opus_mapped_model",
            grok["opus_mapped_model"],
        ),
        "GROK_MESSAGES_DISPATCH_DEFAULTS.sonnet_mapped_model": (
            "sonnet_mapped_model",
            grok["sonnet_mapped_model"],
        ),
        "GROK_MESSAGES_DISPATCH_DEFAULTS.haiku_mapped_model": (
            "haiku_mapped_model",
            grok["haiku_mapped_model"],
        ),
    }
    for label, (field, want) in fe_pairs.items():
        got = _extract_fe_const(field) if "OPENAI" in label or "GROK" in label else ""
        # Both blocks use same field names; disambiguate by reading block
        block = "OPENAI_MESSAGES_DISPATCH_DEFAULTS" if "OPENAI" in label else "GROK_MESSAGES_DISPATCH_DEFAULTS"
        text = FE_DISPATCH.read_text(encoding="utf-8")
        block_m = re.search(rf"export const {block} = \{{([^}}]+)\}}", text, re.S)
        if not block_m:
            errors.append(f"missing FE block {block}")
            continue
        field_m = re.search(rf'{field}:\s*"([^"]+)"', block_m.group(1))
        if not field_m:
            errors.append(f"missing {label}")
            continue
        if field_m.group(1) != want:
            errors.append(f"FE {label}={field_m.group(1)!r}, registry wants {want!r}")
    return errors


def run_selftest() -> None:
    doc = _load_registry()
    assert check_registry_internal(doc) == []
    assert check_go_fe_platform_defaults(doc) == []
    bad = dict(doc)
    bad["group_defaults"] = dict(doc["group_defaults"])
    bad["group_defaults"]["glm"] = {
        "opus_mapped_model": "gpt-5.6-sol",
        "sonnet_mapped_model": "glm-4.7",
        "haiku_mapped_model": "glm-4.5-air",
    }
    errs = check_registry_internal(bad)
    assert any("glm" in e and "gpt-5.6-sol" in e for e in errs), errs


def main() -> int:
    if "--selftest" in sys.argv:
        run_selftest()
        return 0

    quiet = "--quiet" in sys.argv
    if not REGISTRY.is_file():
        print(f"ERROR: missing registry {REGISTRY}", file=sys.stderr)
        return 2

    doc = _load_registry()
    errors = check_registry_internal(doc) + check_go_fe_platform_defaults(doc)
    if errors:
        if not quiet:
            print("messages-dispatch-family drift:")
            for err in errors:
                print(f"  - {err}")
        return 1
    if not quiet:
        print("ok: messages dispatch family registry aligned")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
