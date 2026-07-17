#!/usr/bin/env python3
"""Model operations planner and explicit model-surface activation entry.

This is intentionally NOT a background writer. ``plan`` automates read-only
reconciliation; ``activate`` is the only evidence-gated prod mapping write path.
The deterministic model operations include:

  * normalize upstream-discovered model ids,
  * normalize probe TSV output from ops/pricing/probe-servable-models.sh,
  * compare candidates against tk_served_models.json and tk_pricing_overlay.json,
  * compare repo intent against an optional live model_mapping snapshot,
  * compare explicit mirror-account policies from a live mapping snapshot,
  * keep the public catalog / user menu surface tied to the same servable sets.

Writes still go through the existing reviewed paths:

  * model_mapping: migrations or ops/newapi/apply-model-mapping-live.py
  * price: ops/pricing/apply-pricing-hotfix.py / tk_pricing_overlay.json
  * manifest: backend/internal/service/tk_served_models.json
  * catalog/menu allowlists: ops/pricing/refresh-servable-allowlist.py

Usage:

  python3 ops/pricing/modelops.py snapshot-sql --channel-type 17
  python3 ops/pricing/modelops.py plan \
    --upstream "$QWEN_ACCOUNT_ID":/tmp/qwen_upstream_models.json \
    --probe-results /tmp/qwen_probe.tsv \
    --live-mapping /tmp/model_mapping_snapshot.json \
    --mirror "$SOURCE_QWEN_ACCOUNT_ID":"$TARGET_QWEN_ACCOUNT_ID"

  python3 ops/pricing/modelops.py activate \
    --bundle /tmp/model-surface-bundle.json \
    --current-bundle /tmp/current-model-surface-bundle.json \
    --probe-evidence /tmp/model-activation-probe.json \
    --pricing-evidence /tmp/model-activation-pricing.json

  python3 ops/pricing/modelops.py --selftest
"""

from __future__ import annotations

import argparse
import collections
import datetime as dt
import importlib.util
import json
import re
import shlex
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any, Iterable


REPO_ROOT = Path(__file__).resolve().parents[2]
SERVICE_DIR = REPO_ROOT / "backend" / "internal" / "service"
MANIFEST_PATH = SERVICE_DIR / "tk_served_models.json"
OVERLAY_PATH = SERVICE_DIR / "tk_pricing_overlay.json"
MAPPING_MANAGER_PATH = REPO_ROOT / "ops" / "pricing" / "manage-account-model-mapping-runtime.py"

_bundle_spec = importlib.util.spec_from_file_location(
    "tk_model_surface_bundle", REPO_ROOT / "ops" / "pricing" / "model_surface_bundle.py")
_BUNDLE = importlib.util.module_from_spec(_bundle_spec)
_bundle_spec.loader.exec_module(_BUNDLE)

ACTIVATION_EVIDENCE_SCHEMA_VERSION = 1
ACTIVATION_EVIDENCE_MAX_AGE = dt.timedelta(hours=24)
ACTIVATION_CONFIRM = "yes-activate-model-surface"

MODE_FIELDS = {
    "image_generation": ("output_cost_per_image",),
    "video_generation": ("output_cost_per_second",),
    "chat": ("input_cost_per_token", "output_cost_per_token"),
}

# SQL generator registry for scripts/checks/ops-sql-coverage.py. The argparse
# command wrapper name ends in `_sql` by convention, but the real generator is
# build_snapshot_sql below.
SELF_CHECK_EXEMPT = {
    "cmd_snapshot_sql": "argparse command wrapper; build_snapshot_sql is enumerated",
}


class AccountPolicy:
    __slots__ = ("account_id", "name", "platform", "channel_type")

    def __init__(self, account_id: str, name: str, platform: str, channel_type: int) -> None:
        self.account_id = account_id
        self.name = name
        self.platform = platform
        self.channel_type = channel_type


# Historical guard tuples for curated long-tail seed accounts. This is not the
# serving-pool membership source of truth: operators should pass a live snapshot
# from snapshot-sql so name/platform/channel_type come from the runtime DB.
KNOWN_ACCOUNTS: dict[str, AccountPolicy] = {
    "7": AccountPolicy("7", "volcengine", "newapi", 45),
    "39": AccountPolicy("39", "ds-官", "newapi", 43),
    "60": AccountPolicy("60", "Qwen", "newapi", 17),
    "72": AccountPolicy("72", "Qwen-2", "newapi", 17),
}


class ManifestEntry:
    __slots__ = (
        "key",
        "platform",
        "model_id",
        "served_on",
        "channel_type",
        "price_source",
        "price_key",
        "display",
        "notes",
    )

    def __init__(
        self,
        key: str,
        platform: str,
        model_id: str,
        served_on: tuple[str, ...],
        channel_type: int,
        price_source: str,
        price_key: str,
        display: bool,
        notes: str = "",
    ) -> None:
        self.key = key
        self.platform = platform
        self.model_id = model_id
        self.served_on = served_on
        self.channel_type = channel_type
        self.price_source = price_source
        self.price_key = price_key
        self.display = display
        self.notes = notes


class Candidate:
    __slots__ = ("account_id", "model_id", "source", "upstream_pricing_status")

    def __init__(
        self,
        account_id: str,
        model_id: str,
        source: str,
        upstream_pricing_status: str | None = None,
    ) -> None:
        self.account_id = account_id
        self.model_id = model_id
        self.source = source
        self.upstream_pricing_status = upstream_pricing_status


class ProbeAggregate:
    __slots__ = ("platform", "model_id", "verdicts", "codes", "variants")

    def __init__(self, platform: str, model_id: str) -> None:
        self.platform = platform
        self.model_id = model_id
        self.verdicts: collections.Counter[str] = collections.Counter()
        self.codes: collections.Counter[str] = collections.Counter()
        self.variants: list[str] = []

    def add(self, code: str, verdict: str, variant: str | None = None) -> None:
        self.verdicts[verdict] += 1
        self.codes[code] += 1
        if variant:
            self.variants.append(variant)

    @property
    def status(self) -> str:
        if self.verdicts["servable"]:
            return "servable"
        if self.verdicts["not_allowlisted"]:
            return "mapping_gap"
        if self.verdicts["auth_error"] or self.verdicts["config_error"]:
            return "probe_error"
        if self.verdicts["inconclusive"]:
            return "inconclusive"
        if self.verdicts["unsupported"]:
            return "unsupported"
        return "unknown"


class AccountSnapshot:
    __slots__ = ("account_id", "name", "platform", "channel_type", "model_mapping")

    def __init__(
        self,
        account_id: str,
        name: str | None = None,
        platform: str | None = None,
        channel_type: int | None = None,
        model_mapping: dict[str, str] | None = None,
    ) -> None:
        self.account_id = account_id
        self.name = name
        self.platform = platform
        self.channel_type = channel_type
        self.model_mapping = model_mapping or {}


def _is_pos_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def load_manifest(path: Path = MANIFEST_PATH) -> list[ManifestEntry]:
    data = load_json(path)
    entries = data.get("entries")
    if not isinstance(entries, dict):
        raise SystemExit(f"{path}: top-level entries object missing")
    out: list[ManifestEntry] = []
    for key, raw in entries.items():
        if key.startswith("_"):
            continue
        if not isinstance(raw, dict):
            raise SystemExit(f"{path}: {key}: entry is not an object")
        out.append(ManifestEntry(
            key=key,
            platform=str(raw.get("platform", "")),
            model_id=str(raw.get("model_id", "")),
            served_on=tuple(str(x) for x in raw.get("served_on", [])),
            channel_type=int(raw.get("channel_type", 0)),
            price_source=str(raw.get("price_source", "")),
            price_key=str(raw.get("price_key", "")),
            display=bool(raw.get("display", False)),
            notes=str(raw.get("notes", "") or ""),
        ))
    return out


def load_overlay(path: Path = OVERLAY_PATH) -> dict[str, dict[str, Any]]:
    data = load_json(path)
    return {k: v for k, v in data.items() if not k.startswith("_") and isinstance(v, dict)}


def overlay_price_ok(overlay: dict[str, dict[str, Any]], model_id: str) -> bool:
    entry = overlay.get(model_id)
    if not isinstance(entry, dict):
        return False
    fields = MODE_FIELDS.get(entry.get("mode"))
    if not fields:
        return False
    return all(_is_pos_number(entry.get(field)) for field in fields)


def extract_model_items(obj: Any) -> list[tuple[str, str | None]]:
    """Return (model_id, pricing_status) pairs from common discovery shapes."""
    out: list[tuple[str, str | None]] = []

    def add_item(item: Any, fallback_key: str | None = None) -> None:
        pricing: str | None = None
        model: str | None = None
        if fallback_key and isinstance(item, str) and item in ("priced", "missing"):
            model = fallback_key.strip()
            pricing = item
        elif isinstance(item, str):
            model = item.strip()
        elif isinstance(item, dict):
            raw = item.get("id") or item.get("model_id") or item.get("model")
            if isinstance(raw, str):
                model = raw.strip()
            raw_pricing = item.get("pricing_status")
            if raw_pricing in ("priced", "missing"):
                pricing = raw_pricing
        if not model and fallback_key:
            model = fallback_key.strip()
            if isinstance(item, str) and item in ("priced", "missing"):
                pricing = item
            elif isinstance(item, dict) and item.get("pricing_status") in ("priced", "missing"):
                pricing = item.get("pricing_status")
        if model:
            out.append((model, pricing))

    if isinstance(obj, list):
        for item in obj:
            add_item(item)
    elif isinstance(obj, dict):
        if isinstance(obj.get("models"), list):
            for item in obj["models"]:
                add_item(item)
        elif isinstance(obj.get("data"), list):
            for item in obj["data"]:
                add_item(item)
        else:
            for key, value in obj.items():
                if str(key).startswith("_"):
                    continue
                add_item(value, str(key))
    return dedupe_pairs(out)


def dedupe_pairs(items: Iterable[tuple[str, str | None]]) -> list[tuple[str, str | None]]:
    seen: dict[str, str | None] = {}
    for model, pricing in items:
        model = model.strip()
        if not model:
            continue
        if model not in seen or seen[model] is None:
            seen[model] = pricing
    return [(model, seen[model]) for model in sorted(seen)]


def load_upstream_spec(spec: str, default_account: str | None = None) -> list[Candidate]:
    if ":" in spec and not Path(spec).exists():
        account, raw_path = spec.split(":", 1)
    else:
        if not default_account:
            raise SystemExit("--upstream PATH requires --account-id, or use --upstream ACCOUNT:PATH")
        account, raw_path = default_account, spec
    account = account.strip()
    path = Path(raw_path)
    text = path.read_text(encoding="utf-8")
    try:
        obj = json.loads(text)
        pairs = extract_model_items(obj)
    except ValueError:
        pairs = dedupe_pairs((line.strip(), None) for line in text.splitlines()
                             if line.strip() and not line.lstrip().startswith("#"))
    return [Candidate(account, model, str(path), pricing) for model, pricing in pairs]


_VARIANT_RE = re.compile(r"^(?P<model>.+?)\s+\((?P<variant>thinking|nonthinking)\)$")


def normalize_probe_model(raw: str) -> tuple[str, str | None]:
    raw = raw.strip()
    match = _VARIANT_RE.match(raw)
    if match:
        return match.group("model").strip(), match.group("variant")
    return raw, None


def load_probe_results(paths: list[Path]) -> dict[str, ProbeAggregate]:
    out: dict[str, ProbeAggregate] = {}
    for path in paths:
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) != 4:
                raise SystemExit(f"{path}:{lineno}: expected 4 TSV columns, got {len(parts)}")
            platform, raw_model, code, verdict = (p.strip() for p in parts)
            model, variant = normalize_probe_model(raw_model)
            if not model:
                continue
            agg = out.setdefault(model, ProbeAggregate(platform=platform, model_id=model))
            agg.add(code, verdict, variant)
    return out


def _string_mapping(raw: Any) -> dict[str, str]:
    if not isinstance(raw, dict):
        return {}
    out: dict[str, str] = {}
    for key, value in raw.items():
        if isinstance(key, str) and isinstance(value, str):
            out[key] = value
    return out


def parse_live_mapping(data: Any) -> dict[str, AccountSnapshot]:
    """Accept common JSON shapes for live account model_mapping snapshots."""
    if isinstance(data, dict) and "accounts" in data:
        data = data["accounts"]

    rows: list[Any]
    if isinstance(data, list):
        rows = data
    elif isinstance(data, dict):
        rows = []
        for account_id, value in data.items():
            if isinstance(value, dict):
                row = dict(value)
                row.setdefault("id", account_id)
                rows.append(row)
            else:
                rows.append({"id": account_id, "model_mapping": value})
    else:
        raise SystemExit("live mapping snapshot must be a JSON object or list")

    out: dict[str, AccountSnapshot] = {}
    for row in rows:
        if not isinstance(row, dict):
            continue
        account_id = str(row.get("id") or row.get("account_id") or "").strip()
        if not account_id:
            continue
        credentials = row.get("credentials") if isinstance(row.get("credentials"), dict) else {}
        mapping = (
            _string_mapping(row.get("model_mapping"))
            or _string_mapping(credentials.get("model_mapping"))
            or _string_mapping(row.get("credentials.model_mapping"))
        )
        channel_type = row.get("channel_type")
        if channel_type is None:
            channel_type = row.get("channelType")
        try:
            ct = int(channel_type) if channel_type is not None else None
        except (TypeError, ValueError):
            ct = None
        out[account_id] = AccountSnapshot(
            account_id=account_id,
            name=row.get("name") if isinstance(row.get("name"), str) else None,
            platform=row.get("platform") if isinstance(row.get("platform"), str) else None,
            channel_type=ct,
            model_mapping=mapping,
        )
    return out


def price_status(
    model_id: str,
    candidate: Candidate | None,
    manifest_by_model: dict[str, list[ManifestEntry]],
    overlay: dict[str, dict[str, Any]],
) -> tuple[str, str]:
    if overlay_price_ok(overlay, model_id):
        return "priced", "overlay"
    if candidate and candidate.upstream_pricing_status == "priced":
        return "priced", "runtime-catalog"
    for entry in manifest_by_model.get(model_id, []):
        if entry.price_source in ("mirror", "channel"):
            return "priced", entry.price_source
    return "missing", "none"


def policy_for_account(account_id: str, snapshot: AccountSnapshot | None = None) -> AccountPolicy:
    if snapshot:
        known = KNOWN_ACCOUNTS.get(account_id)
        return AccountPolicy(
            account_id=account_id,
            name=snapshot.name or (known.name if known else f"account-{account_id}"),
            platform=snapshot.platform or (known.platform if known else "newapi"),
            channel_type=snapshot.channel_type or (known.channel_type if known else 0),
        )
    return KNOWN_ACCOUNTS.get(account_id, AccountPolicy(account_id, f"account-{account_id}", "newapi", 0))


def infer_mode(model_id: str, overlay: dict[str, dict[str, Any]]) -> str:
    mode = overlay.get(model_id, {}).get("mode")
    if mode == "image_generation":
        return "image"
    if mode == "video_generation":
        return "video"
    lower = model_id.lower()
    if "seedream" in lower or "image" in lower or "imagen" in lower:
        return "image"
    if "seedance" in lower or "video" in lower or "veo" in lower:
        return "video"
    return "chat"


def probe_env_name(
    account_id: str,
    model_id: str,
    overlay: dict[str, dict[str, Any]],
    snapshot: AccountSnapshot | None = None,
) -> str | None:
    policy = policy_for_account(account_id, snapshot)
    if policy.channel_type == 17:
        return "DASHSCOPE_CHAT_MODELS"
    if policy.channel_type == 26:
        return None
    if policy.channel_type == 45:
        mode = infer_mode(model_id, overlay)
        if mode == "image":
            return "ARK_IMAGE_MODELS"
        if mode == "video":
            return "ARK_VIDEO_MODELS"
        return "ARK_CHAT_MODELS"
    return None


def run_probe_command(env_name: str, models: list[str]) -> str:
    env_value = f"{env_name}={' '.join(models)}"
    # probe-servable-models.sh resolves every probe key through reserved
    # __tk_probe_* groups; its companion library must ride along via --with.
    return (
        "bash ops/observability/run-probe.sh --target prod "
        "--script ops/pricing/probe-servable-models.sh "
        "--with ops/pricing/probe_reserved_resources.sh "
        f"--env {shlex.quote(env_value)} --timeout-seconds 300"
    )


def apply_command(account_id: str, model_id: str, snapshot: AccountSnapshot | None = None) -> str:
    return apply_many_command(account_id, [model_id], snapshot)


def apply_many_command(account_id: str, model_ids: list[str], snapshot: AccountSnapshot | None = None) -> str:
    policy = policy_for_account(account_id, snapshot)
    adds = " ".join(f"--add-identity {shlex.quote(model_id)}" for model_id in model_ids if model_id.strip())
    return (
        "python3 ops/newapi/apply-model-mapping-live.py sync-live "
        f"--account-id {shlex.quote(policy.account_id)} "
        f"--name {shlex.quote(policy.name)} "
        f"--platform {shlex.quote(policy.platform)} "
        f"--channel-type {policy.channel_type} "
        f"{adds} --dry-run"
    )


def build_plan(args: argparse.Namespace) -> dict[str, Any]:
    manifest = load_manifest()
    overlay = load_overlay()
    manifest_by_model: dict[str, list[ManifestEntry]] = collections.defaultdict(list)
    manifest_by_account: dict[str, dict[str, ManifestEntry]] = collections.defaultdict(dict)
    for entry in manifest:
        manifest_by_model[entry.model_id].append(entry)
        for account in entry.served_on:
            manifest_by_account[account][entry.model_id] = entry

    candidates: dict[tuple[str, str], Candidate] = {}
    for spec in args.upstream or []:
        for candidate in load_upstream_spec(spec, args.account_id):
            candidates[(candidate.account_id, candidate.model_id)] = candidate
    for raw in args.candidate or []:
        if ":" not in raw:
            raise SystemExit("--candidate must be ACCOUNT:MODEL")
        account, model = raw.split(":", 1)
        candidates[(account.strip(), model.strip())] = Candidate(
            account.strip(), model.strip(), "--candidate", None)

    probes = load_probe_results([Path(p) for p in (args.probe_results or [])])
    live = parse_live_mapping(load_json(Path(args.live_mapping))) if args.live_mapping else {}

    plan: dict[str, Any] = {
        "summary": {
            "manifest_entries": len(manifest),
            "candidates": len(candidates),
            "probe_models": len(probes),
            "live_accounts": len(live),
        },
        "surfaces": {
            "served_intent": "backend/internal/service/tk_served_models.json",
            "pricing": "backend/internal/service/tk_pricing_overlay.json + channel_model_pricing",
            "runtime_mapping": "accounts.credentials.model_mapping",
            "catalog_menu": "backend/internal/service/pricing_catalog_supported_models_tk.go",
            "catalog_menu_refresh": "ops/pricing/refresh-servable-allowlist.py",
        },
        "probe_needed": [],
        "ready_for_onboard": [],
        "mapping_gap_candidates": [],
        "price_missing": [],
        "unsupported": [],
        "inconclusive": [],
        "mapping_missing": [],
        "mapping_extra_review": [],
        "mirror_drift": [],
        "mirror_sync_commands": [],
        "probe_commands": [],
    }

    for (account_id, model_id), candidate in sorted(candidates.items()):
        probe = probes.get(model_id)
        probe_status = probe.status if probe else "untested"
        priced, price_source = price_status(model_id, candidate, manifest_by_model, overlay)
        in_manifest = model_id in manifest_by_account.get(account_id, {})
        item = {
            "account_id": account_id,
            "model_id": model_id,
            "source": candidate.source,
            "probe_status": probe_status,
            "price_status": priced,
            "price_source": price_source,
            "in_manifest": in_manifest,
            "upstream_pricing_status": candidate.upstream_pricing_status,
        }
        if in_manifest:
            continue
        if priced != "priced":
            plan["price_missing"].append(item)
        elif probe_status == "servable":
            plan["ready_for_onboard"].append(item)
        elif probe_status == "mapping_gap":
            plan["mapping_gap_candidates"].append(item)
        elif probe_status == "unsupported":
            plan["unsupported"].append(item)
        elif probe_status in ("inconclusive", "probe_error"):
            plan["inconclusive"].append(item)
        else:
            plan["probe_needed"].append(item)

    if live:
        for account_id, expected in sorted(manifest_by_account.items()):
            snap = live.get(account_id)
            if not snap:
                continue
            for model_id in sorted(expected):
                actual = snap.model_mapping.get(model_id)
                if actual != model_id:
                    plan["mapping_missing"].append({
                        "account_id": account_id,
                        "model_id": model_id,
                        "actual": actual,
                        "suggested_command": apply_command(account_id, model_id, snap),
                    })
            for model_id, target in sorted(snap.model_mapping.items()):
                if model_id in expected:
                    continue
                priced, price_source = price_status(model_id, None, manifest_by_model, overlay)
                probe = probes.get(model_id)
                probe_status = probe.status if probe else "untested"
                reason = ["not in manifest"]
                if priced != "priced":
                    reason.append("unpriced")
                if probe_status in ("unsupported", "mapping_gap", "probe_error", "inconclusive", "untested"):
                    reason.append(f"probe={probe_status}")
                if args.strict_manifest:
                    reason.append("strict-manifest")
                plan["mapping_extra_review"].append({
                    "account_id": account_id,
                    "model_id": model_id,
                    "target": target,
                    "price_status": priced,
                    "price_source": price_source,
                    "probe_status": probe_status,
                    "reason": "; ".join(reason),
                })

    for mirror in args.mirror or []:
        if ":" not in mirror:
            raise SystemExit("--mirror must be SOURCE:TARGET")
        source_id, target_id = (x.strip() for x in mirror.split(":", 1))
        source = live.get(source_id)
        target = live.get(target_id)
        if not source or not target:
            plan["mirror_drift"].append({
                "source": source_id,
                "target": target_id,
                "error": "source or target account missing from --live-mapping snapshot",
            })
            continue
        source_map = source.model_mapping
        target_map = target.model_mapping
        missing = sorted(k for k in source_map if k not in target_map)
        extra = sorted(k for k in target_map if k not in source_map)
        different = sorted(k for k in source_map if k in target_map and source_map[k] != target_map[k])
        plan["mirror_drift"].append({
            "source": source_id,
            "target": target_id,
            "missing_in_target": missing,
            "extra_in_target": extra,
            "value_differences": different,
            "ok": not missing and not extra and not different,
        })
        if missing and target:
            plan["mirror_sync_commands"].append({
                "source": source_id,
                "target": target_id,
                "missing_models": missing,
                "command": apply_many_command(target_id, missing, target),
            })

    probe_groups: dict[str, set[str]] = collections.defaultdict(set)
    for item in plan["probe_needed"]:
        env = probe_env_name(item["account_id"], item["model_id"], overlay, live.get(item["account_id"]))
        if env:
            probe_groups[env].add(item["model_id"])
        else:
            item["probe_env"] = None
            item["probe_note"] = "no probe family is registered for this account/channel_type"
    for env, models in sorted(probe_groups.items()):
        plan["probe_commands"].append({
            "env": env,
            "models": sorted(models),
            "command": run_probe_command(env, sorted(models)),
        })

    return plan


def print_section(title: str, rows: list[dict[str, Any]], formatter) -> None:
    print(f"\n{title}")
    if not rows:
        print("  ok: none")
        return
    for row in rows:
        print("  - " + formatter(row))


def print_plan(plan: dict[str, Any]) -> None:
    summary = plan["summary"]
    print("modelops plan")
    print(
        f"  manifest={summary['manifest_entries']} candidates={summary['candidates']} "
        f"probe_models={summary['probe_models']} live_accounts={summary['live_accounts']}"
    )
    print("  writes=none")
    print("  surfaces:")
    for name, owner in plan["surfaces"].items():
        print(f"    {name}: {owner}")

    print_section(
        "probe needed",
        plan["probe_needed"],
        lambda r: f"account {r['account_id']} {r['model_id']} "
                  f"(price={r['price_source']}; source={r['source']})",
    )
    if plan["probe_commands"]:
        print("\nprobe commands")
        for row in plan["probe_commands"]:
            print(f"  - {row['command']}")

    print_section(
        "newapi long-tail ready for onboard review",
        plan["ready_for_onboard"],
        lambda r: f"account {r['account_id']} {r['model_id']} "
                  f"(probe=servable, price={r['price_source']})",
    )
    print_section(
        "mapping gap candidates",
        plan["mapping_gap_candidates"],
        lambda r: f"account {r['account_id']} {r['model_id']} "
                  "(probe=not_allowlisted; add mapping only after human review)",
    )
    print_section(
        "price missing",
        plan["price_missing"],
        lambda r: f"account {r['account_id']} {r['model_id']} "
                  f"(probe={r['probe_status']}; upstream_price={r['upstream_pricing_status']}) "
                  f"-> python3 ops/pricing/apply-pricing-hotfix.py lookup --model {shlex.quote(r['model_id'])}",
    )
    print_section(
        "unsupported",
        plan["unsupported"],
        lambda r: f"account {r['account_id']} {r['model_id']} (probe=unsupported)",
    )
    print_section(
        "inconclusive / probe errors",
        plan["inconclusive"],
        lambda r: f"account {r['account_id']} {r['model_id']} (probe={r['probe_status']})",
    )
    print_section(
        "live mapping missing manifest intent",
        plan["mapping_missing"],
        lambda r: f"account {r['account_id']} {r['model_id']} missing; dry-run: {r['suggested_command']}",
    )
    print_section(
        "live mapping extras needing review",
        plan["mapping_extra_review"],
        lambda r: f"account {r['account_id']} {r['model_id']}->{r['target']} "
                  f"(price={r['price_status']}/{r['price_source']}, probe={r['probe_status']})",
    )

    print("\nmirror account drift")
    if not plan["mirror_drift"]:
        print("  ok: none")
    for row in plan["mirror_drift"]:
        if row.get("error"):
            print(f"  - {row['source']} -> {row['target']}: {row['error']}")
        elif row.get("ok"):
            print(f"  - {row['source']} -> {row['target']}: ok")
        else:
            print(
                f"  - {row['source']} -> {row['target']}: "
                f"missing={row['missing_in_target']} extra={row['extra_in_target']} "
                f"diff={row['value_differences']}"
            )

    print("\nmirror sync commands")
    if not plan["mirror_sync_commands"]:
        print("  ok: none")
    for row in plan["mirror_sync_commands"]:
        print(
            f"  - {row['source']} -> {row['target']}: "
            f"add {row['missing_models']} via {row['command']}"
        )


def cmd_plan(args: argparse.Namespace) -> int:
    plan = build_plan(args)
    if args.format == "json":
        print(json.dumps(plan, ensure_ascii=False, indent=2, sort_keys=True))
    else:
        print_plan(plan)
    return 0


def build_snapshot_sql(
    accounts: list[str] | None = None,
    channel_type: int | None = None,
) -> str:
    if bool(accounts) == (channel_type is not None):
        raise SystemExit("choose exactly one of --accounts or --channel-type")
    if accounts:
        ids = ", ".join(a for a in accounts if a.isdigit())
        if not ids or len(ids.split(", ")) != len(accounts):
            raise SystemExit("--accounts must be a comma-separated list of numeric account ids")
        where = f"id IN ({ids})"
    else:
        if channel_type is None or channel_type < 0:
            raise SystemExit("--channel-type must be a non-negative integer")
        where = f"platform = 'newapi' AND channel_type = {channel_type}"
    return (
        "SELECT jsonb_object_agg(id::text, jsonb_build_object(\n"
        "  'id', id,\n"
        "  'name', name,\n"
        "  'platform', platform,\n"
        "  'channel_type', channel_type,\n"
        "  'model_mapping', COALESCE(credentials->'model_mapping', '{}'::jsonb)\n"
        "))\n"
        "FROM accounts\n"
        f"WHERE {where} AND deleted_at IS NULL;"
    )


def iter_self_check_sql() -> list[tuple[str, str]]:
    return [
        ("build_snapshot_sql", build_snapshot_sql(accounts=["60", "72"])),
        ("build_snapshot_sql_channel_type", build_snapshot_sql(channel_type=17)),
    ]


def cmd_snapshot_sql(args: argparse.Namespace) -> int:
    accounts = [a.strip() for a in args.accounts.split(",") if a.strip()] if args.accounts else None
    print(build_snapshot_sql(accounts=accounts, channel_type=args.channel_type))
    return 0


class ActivationError(RuntimeError):
    pass


def _parse_evidence_time(raw: Any, label: str) -> dt.datetime:
    if not isinstance(raw, str) or not raw.strip():
        raise ActivationError(f"{label}: observed_at must be an RFC3339 UTC timestamp")
    value = raw.strip()
    if value.endswith("Z"):
        value = value[:-1] + "+00:00"
    try:
        parsed = dt.datetime.fromisoformat(value)
    except ValueError as e:
        raise ActivationError(f"{label}: invalid observed_at {raw!r}") from e
    if parsed.tzinfo is None:
        raise ActivationError(f"{label}: observed_at must include a UTC offset")
    return parsed.astimezone(dt.timezone.utc)


def _bundle_mapping_scopes(bundle: dict[str, Any]) -> dict[str, dict[str, str]]:
    floor = bundle["account_model_mapping"]
    scopes = {
        str(scope): dict(mapping)
        for scope, mapping in (floor.get("platforms") or {}).items()
        if isinstance(mapping, dict)
    }
    for channel_type, mapping in (floor.get("newapi_channel_types") or {}).items():
        if isinstance(mapping, dict):
            scopes[f"newapi_channel_type:{channel_type}"] = dict(mapping)
    return scopes


def _activation_delta(current: dict[str, Any], target: dict[str, Any]) -> dict[str, Any]:
    current_scopes = _bundle_mapping_scopes(current)
    target_scopes = _bundle_mapping_scopes(target)
    activated: list[dict[str, Any]] = []
    removed: list[dict[str, Any]] = []
    for scope, mapping in sorted(target_scopes.items()):
        previous = current_scopes.get(scope, {})
        for model_id, target_id in sorted(mapping.items()):
            previous_target = previous.get(model_id)
            if previous_target == target_id:
                continue
            activated.append({
                "scope": scope,
                "model_id": model_id,
                "target": target_id,
                "change": "added" if previous_target is None else "retargeted",
                "previous_target": previous_target,
            })
    for scope, mapping in sorted(current_scopes.items()):
        target_mapping = target_scopes.get(scope, {})
        for model_id, previous_target in sorted(mapping.items()):
            if model_id not in target_mapping:
                removed.append({
                    "scope": scope,
                    "model_id": model_id,
                    "previous_target": previous_target,
                })

    current_floor = current["account_model_mapping"]
    target_floor = target["account_model_mapping"]

    def policy_additions(field: str) -> list[dict[str, str]]:
        before = current_floor.get(field) or {}
        after = target_floor.get(field) or {}
        rows: list[dict[str, str]] = []
        for scope, values in sorted(after.items()):
            old = set(before.get(scope) or [])
            for value in sorted(set(values or []) - old):
                rows.append({"scope": str(scope), "value": str(value)})
        return rows

    current_scopes_list = sorted(set(current_floor.get("antigravity_group_scopes") or []))
    target_scopes_list = sorted(set(target_floor.get("antigravity_group_scopes") or []))
    return {
        "activated": activated,
        "removed_required": removed,
        "forbidden_keys_added": policy_additions("forbidden_model_mapping_keys"),
        "forbidden_prefixes_added": policy_additions("forbidden_model_mapping_prefixes"),
        "antigravity_group_scopes_changed": current_scopes_list != target_scopes_list,
        "current_antigravity_group_scopes": current_scopes_list,
        "target_antigravity_group_scopes": target_scopes_list,
    }


def _load_activation_evidence(
    path: Path,
    *,
    kind: str,
    current_sha256: str,
    target_sha256: str,
    now: dt.datetime,
) -> tuple[dict[str, Any], dict[tuple[str, str, str], dict[str, Any]]]:
    label = str(path)
    try:
        data = load_json(path)
    except (OSError, json.JSONDecodeError) as e:
        raise ActivationError(f"{label}: cannot load evidence: {e}") from e
    if not isinstance(data, dict):
        raise ActivationError(f"{label}: evidence must be a JSON object")
    if data.get("schema_version") != ACTIVATION_EVIDENCE_SCHEMA_VERSION:
        raise ActivationError(
            f"{label}: unsupported evidence schema {data.get('schema_version')!r}; "
            f"expected {ACTIVATION_EVIDENCE_SCHEMA_VERSION}"
        )
    if data.get("kind") != kind:
        raise ActivationError(f"{label}: evidence kind must be {kind!r}")
    if data.get("current_floor_sha256") != current_sha256:
        raise ActivationError(f"{label}: current_floor_sha256 does not match --current-bundle")
    if data.get("target_floor_sha256") != target_sha256:
        raise ActivationError(f"{label}: target_floor_sha256 does not match --bundle")
    observed_at = _parse_evidence_time(data.get("observed_at"), label)
    now = now.astimezone(dt.timezone.utc)
    if observed_at > now:
        raise ActivationError(f"{label}: observed_at is in the future")
    if now - observed_at > ACTIVATION_EVIDENCE_MAX_AGE:
        raise ActivationError(f"{label}: evidence is stale (older than 24 hours)")
    rows = data.get("models")
    if not isinstance(rows, list):
        raise ActivationError(f"{label}: models must be an array")
    index: dict[tuple[str, str, str], dict[str, Any]] = {}
    for position, row in enumerate(rows):
        row_label = f"{label}:models[{position}]"
        if not isinstance(row, dict):
            raise ActivationError(f"{row_label}: row must be an object")
        values: dict[str, str] = {}
        for field in ("scope", "model_id", "target", "verdict", "source"):
            value = row.get(field)
            if not isinstance(value, str) or not value.strip():
                raise ActivationError(f"{row_label}: {field} must be a non-empty string")
            values[field] = value.strip()
        if kind == "model_activation_probe":
            account_id = row.get("account_id")
            if not isinstance(account_id, str) or not account_id.strip():
                raise ActivationError(f"{row_label}: probe evidence requires account_id")
        key = (values["scope"], values["model_id"], values["target"])
        if key in index:
            raise ActivationError(f"{row_label}: duplicate evidence for {key}")
        index[key] = {**row, **values}
    return data, index


def build_activation_context(
    *,
    bundle_path: Path,
    current_bundle_path: Path,
    probe_evidence_path: Path,
    pricing_evidence_path: Path,
    now: dt.datetime | None = None,
) -> dict[str, Any]:
    try:
        target = _BUNDLE.load_bundle(bundle_path)
        current = _BUNDLE.load_bundle(current_bundle_path)
    except RuntimeError as e:
        raise ActivationError(str(e)) from e
    target_sha256 = target["floor_sha256"]
    current_sha256 = current["floor_sha256"]
    if target_sha256 == current_sha256:
        raise ActivationError("target and current bundles have the same floor_sha256")
    delta = _activation_delta(current, target)
    if not delta["activated"]:
        raise ActivationError("bundle delta has no added or retargeted required model mappings")

    checked_at = now or dt.datetime.now(dt.timezone.utc)
    probe, probe_index = _load_activation_evidence(
        probe_evidence_path,
        kind="model_activation_probe",
        current_sha256=current_sha256,
        target_sha256=target_sha256,
        now=checked_at,
    )
    pricing, pricing_index = _load_activation_evidence(
        pricing_evidence_path,
        kind="model_activation_pricing",
        current_sha256=current_sha256,
        target_sha256=target_sha256,
        now=checked_at,
    )
    missing_probe: list[str] = []
    missing_pricing: list[str] = []
    shared_sources: list[str] = []
    for row in delta["activated"]:
        key = (row["scope"], row["model_id"], row["target"])
        probe_row = probe_index.get(key)
        if not probe_row or probe_row.get("verdict") != "servable":
            missing_probe.append("/".join(key))
        pricing_row = pricing_index.get(key)
        if not pricing_row or pricing_row.get("verdict") != "priced":
            missing_pricing.append("/".join(key))
        if probe_row and pricing_row and probe_row.get("source") == pricing_row.get("source"):
            shared_sources.append("/".join(key))
    if missing_probe:
        raise ActivationError("probe evidence missing servable verdicts: " + ", ".join(missing_probe))
    if missing_pricing:
        raise ActivationError("pricing evidence missing priced verdicts: " + ", ".join(missing_pricing))
    if shared_sources:
        raise ActivationError(
            "probe and pricing evidence must use independent sources: " + ", ".join(shared_sources))
    return {
        "current_bundle": str(current_bundle_path.expanduser().resolve()),
        "current_floor_sha256": current_sha256,
        "target_bundle": str(bundle_path.expanduser().resolve()),
        "target_floor_sha256": target_sha256,
        "delta": delta,
        "evidence": {
            "probe": {"path": str(probe_evidence_path), "observed_at": probe["observed_at"]},
            "pricing": {"path": str(pricing_evidence_path), "observed_at": pricing["observed_at"]},
        },
    }


def _mapping_manager_command(
    action: str,
    bundle_path: Path,
    *,
    prod_instance_id: str | None = None,
    activation_floor_sha256: str | None = None,
) -> list[str]:
    command = ["python3", str(MAPPING_MANAGER_PATH.relative_to(REPO_ROOT)), action]
    if action == "apply-accounts-dry-run":
        command[2:] = ["apply-accounts", "--target", "prod", "--dry-run"]
    elif action == "apply-accounts":
        command[2:] = [
            "apply-accounts", "--target", "prod", "--confirm", "yes-apply-account-model-mapping",
        ]
    elif action == "release-gate":
        command.extend(["--json"])
    else:
        raise ValueError(f"unsupported mapping manager action {action!r}")
    if prod_instance_id:
        command.extend(["--prod-instance-id", prod_instance_id])
    if activation_floor_sha256:
        if action not in {"apply-accounts-dry-run", "apply-accounts"}:
            raise ValueError("activation floor guard is only valid for account apply")
        command.extend(["--activation-floor-sha256", activation_floor_sha256])
    command.extend(["--bundle", str(bundle_path.expanduser().resolve())])
    return command


def _run_json_command(command: list[str], allowed_returncodes: set[int]) -> tuple[int, dict[str, Any]]:
    proc = subprocess.run(
        command,
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    output = proc.stdout.strip()
    try:
        data = json.loads(output) if output else {}
    except json.JSONDecodeError as e:
        raise ActivationError(
            f"command emitted invalid JSON: {' '.join(shlex.quote(p) for p in command)}: {e}"
        ) from e
    if proc.returncode not in allowed_returncodes:
        detail = (proc.stderr or output or "no output").strip()[:1600]
        raise ActivationError(
            f"command failed rc={proc.returncode}: {' '.join(shlex.quote(p) for p in command)}: {detail}"
        )
    return proc.returncode, data


def _require_unshadowed_activation_bundle(gate: dict[str, Any]) -> None:
    targets = gate.get("runtime_setting_targets") or []
    if targets:
        raise ActivationError(
            "target bundle is shadowed by tk_account_model_mapping_runtime on: "
            + ", ".join(str(target) for target in targets)
            + "; fold the runtime scope into the bundle or clear it before activation"
        )


def _resolved_prod_instance_id(gate: dict[str, Any]) -> str:
    rows = gate.get("resolved_targets")
    if not isinstance(rows, list):
        raise ActivationError("release gate did not report its resolved prod target")
    matches = [row for row in rows if isinstance(row, dict) and row.get("target") == "prod"]
    if len(matches) != 1:
        raise ActivationError("release gate did not resolve exactly one prod target")
    instance_id = matches[0].get("instance_id")
    if not isinstance(instance_id, str) or not re.fullmatch(r"i-[0-9a-f]{17}", instance_id):
        raise ActivationError("release gate reported an invalid prod instance id")
    return instance_id


def _activation_confirm_command(args: argparse.Namespace) -> str:
    command = [
        "python3", "ops/pricing/modelops.py", "activate",
        "--bundle", str(args.bundle),
        "--current-bundle", str(args.current_bundle),
        "--probe-evidence", str(args.probe_evidence),
        "--pricing-evidence", str(args.pricing_evidence),
    ]
    if args.prod_instance_id:
        command.extend(["--prod-instance-id", args.prod_instance_id])
    command.extend(["--confirm", ACTIVATION_CONFIRM])
    return " ".join(shlex.quote(part) for part in command)


def _print_activation_result(result: dict[str, Any]) -> None:
    context = result["context"]
    delta = context["delta"]
    plan = result["mapping_plan"]
    print(f"modelops activation {result['status']}")
    print(f"  current_floor_sha256={context['current_floor_sha256']}")
    print(f"  target_floor_sha256={context['target_floor_sha256']}")
    print(f"  activated_mappings={len(delta['activated'])}")
    print(
        f"  prod_plan=accounts:{plan.get('account_change_count', 0)} "
        f"groups:{plan.get('group_change_count', 0)}"
    )
    print(f"  pre_activation_gate={result['pre_activation_gate'].get('status', 'unknown')}")
    if result["status"] == "dry_run":
        print("  writes=none")
        print(f"  apply={result['confirm_command']}")
    else:
        print(f"  post_activation_gate={result['post_activation_gate'].get('status', 'unknown')}")


def cmd_activate(args: argparse.Namespace) -> int:
    if args.confirm is not None and args.confirm != ACTIVATION_CONFIRM:
        print(f"ERROR: activate requires --confirm {ACTIVATION_CONFIRM}", file=sys.stderr)
        return 2
    try:
        context = build_activation_context(
            bundle_path=args.bundle,
            current_bundle_path=args.current_bundle,
            probe_evidence_path=args.probe_evidence,
            pricing_evidence_path=args.pricing_evidence,
        )
        _, pre_gate = _run_json_command(
            _mapping_manager_command(
                "release-gate", args.bundle, prod_instance_id=args.prod_instance_id),
            {0, 1},
        )
        _require_unshadowed_activation_bundle(pre_gate)
        prod_instance_id = _resolved_prod_instance_id(pre_gate)
        _, mapping_plan = _run_json_command(
            _mapping_manager_command(
                "apply-accounts-dry-run",
                args.bundle,
                prod_instance_id=prod_instance_id,
                activation_floor_sha256=context["target_floor_sha256"],
            ),
            {0},
        )
        result: dict[str, Any] = {
            "status": "dry_run" if args.confirm is None else "applied",
            "context": context,
            "mapping_plan": mapping_plan,
            "pre_activation_gate": pre_gate,
        }
        if args.confirm is None:
            result["confirm_command"] = _activation_confirm_command(args)
        else:
            _, applied = _run_json_command(
                _mapping_manager_command(
                    "apply-accounts",
                    args.bundle,
                    prod_instance_id=prod_instance_id,
                    activation_floor_sha256=context["target_floor_sha256"],
                ),
                {0},
            )
            _, post_gate = _run_json_command(
                _mapping_manager_command(
                    "release-gate", args.bundle, prod_instance_id=prod_instance_id),
                {0},
            )
            _require_unshadowed_activation_bundle(post_gate)
            result["mapping_apply"] = applied
            result["post_activation_gate"] = post_gate
    except ActivationError as e:
        if args.format == "json":
            print(json.dumps({"status": "error", "error": str(e)}, ensure_ascii=False, indent=2))
        else:
            print(f"ERROR: {e}", file=sys.stderr)
        return 2
    if args.format == "json":
        print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
    else:
        _print_activation_result(result)
    return 0


def _selftest() -> int:
    failures: list[str] = []

    pairs = extract_model_items({"models": [
        {"id": "qwen-a", "pricing_status": "priced"},
        "qwen-b",
        {"model_id": "qwen-a", "pricing_status": "missing"},
    ]})
    if pairs != [("qwen-a", "priced"), ("qwen-b", None)]:
        failures.append(f"extract_model_items unexpected: {pairs}")
    status_map_pairs = extract_model_items({"qwen-c": "priced", "qwen-d": "missing"})
    if status_map_pairs != [("qwen-c", "priced"), ("qwen-d", "missing")]:
        failures.append(f"extract_model_items status map unexpected: {status_map_pairs}")

    model, variant = normalize_probe_model("qwen3-8b (thinking)")
    if (model, variant) != ("qwen3-8b", "thinking"):
        failures.append("normalize_probe_model failed for qwen variant")

    agg = ProbeAggregate("newapi", "qwen3-8b")
    agg.add("429", "not_allowlisted")
    if agg.status != "mapping_gap":
        failures.append(f"expected mapping_gap, got {agg.status}")
    agg.add("200", "servable", "thinking")
    if agg.status != "servable":
        failures.append(f"expected servable to dominate, got {agg.status}")

    overlay = {
        "qwen-new": {
            "mode": "chat",
            "input_cost_per_token": 0.1,
            "output_cost_per_token": 0.2,
        },
        "seedream-x": {
            "mode": "image_generation",
            "output_cost_per_image": 0.2,
        },
    }
    if not overlay_price_ok(overlay, "qwen-new") or overlay_price_ok(overlay, "missing"):
        failures.append("overlay_price_ok failed")
    if infer_mode("seedream-x", overlay) != "image":
        failures.append("infer_mode failed for image")
    if probe_env_name("60", "qwen-new", overlay) != "DASHSCOPE_CHAT_MODELS":
        failures.append("probe env failed for qwen")
    dynamic_qwen = AccountSnapshot("17001", "Qwen runtime member", "newapi", 17, {})
    if probe_env_name("17001", "qwen-new", overlay, dynamic_qwen) != "DASHSCOPE_CHAT_MODELS":
        failures.append("probe env failed for runtime qwen snapshot")
    if probe_env_name("67", "glm-5-turbo", overlay) is not None:
        failures.append("removed GLM direct account must not emit zhipu probe env")
    if probe_env_name("7", "seedream-x", overlay) != "ARK_IMAGE_MODELS":
        failures.append("probe env failed for ark image")

    live = parse_live_mapping({
        "60": {
            "name": "Qwen",
            "platform": "newapi",
            "channel_type": 17,
            "model_mapping": {"qwen-a": "qwen-a"},
        },
        "72": {"model_mapping": {"qwen-a": "qwen-a", "qwen-extra": "qwen-extra"}},
    })
    if live["60"].model_mapping != {"qwen-a": "qwen-a"}:
        failures.append("parse_live_mapping failed direct shape")

    if "--add-identity qwen-new --dry-run" not in apply_command("60", "qwen-new", live["60"]):
        failures.append("apply_command shape changed")

    with tempfile.TemporaryDirectory() as td:
        tmp = Path(td)
        upstream = tmp / "upstream.json"
        upstream.write_text(json.dumps({
            "models": [
                {"id": "qwen-new", "pricing_status": "priced"},
                {"id": "qwen-missing-price", "pricing_status": "missing"},
                {"id": "qwen-unprobed", "pricing_status": "priced"},
            ]
        }), encoding="utf-8")
        probe = tmp / "probe.tsv"
        probe.write_text(
            "newapi\tqwen-new (thinking)\t200\tservable\n"
            "newapi\tqwen-new (nonthinking)\t200\tservable\n"
            "newapi\tqwen-missing-price\t429\tnot_allowlisted\n",
            encoding="utf-8",
        )
        live_path = tmp / "live.json"
        live_path.write_text(json.dumps({
            "60": {
                "name": "Qwen",
                "platform": "newapi",
                "channel_type": 17,
                "model_mapping": {"qwen-turbo": "qwen-turbo", "qwen-extra": "qwen-extra"},
            },
            "72": {
                "name": "Qwen-2",
                "platform": "newapi",
                "channel_type": 17,
                "model_mapping": {"qwen-turbo": "qwen-turbo"},
            },
        }), encoding="utf-8")
        args = argparse.Namespace(
            upstream=[f"60:{upstream}"],
            account_id=None,
            candidate=[],
            probe_results=[str(probe)],
            live_mapping=str(live_path),
            mirror=["60:72"],
            strict_manifest=False,
            format="json",
        )
        plan = build_plan(args)
        if [x["model_id"] for x in plan["ready_for_onboard"]] != ["qwen-new"]:
            failures.append(f"plan ready_for_onboard wrong: {plan['ready_for_onboard']}")
        if [x["model_id"] for x in plan["price_missing"]] != ["qwen-missing-price"]:
            failures.append(f"plan price_missing wrong: {plan['price_missing']}")
        if [x["model_id"] for x in plan["probe_needed"]] != ["qwen-unprobed"]:
            failures.append(f"plan probe_needed wrong: {plan['probe_needed']}")
        mirror_row = plan["mirror_drift"][0]
        if mirror_row.get("missing_in_target") != ["qwen-extra"]:
            failures.append(f"mirror diff wrong: {mirror_row}")
        mirror_sync = plan["mirror_sync_commands"][0]
        if "--add-identity qwen-extra" not in mirror_sync["command"]:
            failures.append(f"mirror sync command wrong: {mirror_sync}")

    if failures:
        print("SELFTEST FAILED:")
        for failure in failures:
            print(f"  - {failure}")
        return 1
    print("selftest ok: modelops planner parsing/probe/mapping helpers behave")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="TokenKey modelops planner and explicit activation entry")
    parser.add_argument("--selftest", action="store_true", help="run offline selftest")
    sub = parser.add_subparsers(dest="command")

    plan = sub.add_parser("plan", help="build a read-only model operations plan")
    plan.add_argument("--account-id", help="default account id for --upstream PATH")
    plan.add_argument("--upstream", action="append", default=[],
                      help="ACCOUNT:PATH or PATH (with --account-id). JSON list/object or newline model list.")
    plan.add_argument("--candidate", action="append", default=[],
                      help="ACCOUNT:MODEL ad hoc candidate; can repeat")
    plan.add_argument("--probe-results", action="append", default=[],
                      help="TSV output from ops/pricing/probe-servable-models.sh; can repeat")
    plan.add_argument("--live-mapping", help="JSON snapshot of live account model_mapping")
    plan.add_argument("--mirror", action="append", default=[],
                      help="SOURCE:TARGET mirror policy to diff from the live snapshot")
    plan.add_argument("--strict-manifest", action="store_true",
                      help="flag every live mapping key absent from manifest for removal review")
    plan.add_argument("--format", choices=("text", "json"), default="text")
    plan.set_defaults(func=cmd_plan)

    activate = sub.add_parser(
        "activate",
        help="validate independent evidence, plan prod mapping, and explicitly activate a model-surface bundle",
    )
    activate.add_argument(
        "--bundle", type=Path, default=_BUNDLE.DEFAULT_BUNDLE_PATH,
        help="target generated model-surface bundle",
    )
    activate.add_argument("--current-bundle", type=Path, required=True,
                          help="currently active model-surface bundle")
    activate.add_argument("--probe-evidence", type=Path, required=True,
                          help="fresh independent model_activation_probe JSON")
    activate.add_argument("--pricing-evidence", type=Path, required=True,
                          help="fresh independent model_activation_pricing JSON")
    activate.add_argument("--prod-instance-id",
                          help="pin the full prod activation chain to this EC2 instance id")
    activate.add_argument("--confirm", help=f"write confirmation phrase: {ACTIVATION_CONFIRM}")
    activate.add_argument("--format", choices=("text", "json"), default="text")
    activate.set_defaults(func=cmd_activate)

    snap = sub.add_parser("snapshot-sql", help="print read-only SQL for a live mapping snapshot")
    snap_filter = snap.add_mutually_exclusive_group(required=True)
    snap_filter.add_argument("--accounts", help="comma-separated account ids for a point lookup")
    snap_filter.add_argument("--channel-type", type=int,
                             help="snapshot all active newapi accounts with this channel_type, e.g. 17 for DashScope/Qwen")
    snap.set_defaults(func=cmd_snapshot_sql)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.selftest:
        return _selftest()
    if not hasattr(args, "func"):
        parser.print_help()
        return 2
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
