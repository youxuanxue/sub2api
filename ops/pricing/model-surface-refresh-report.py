#!/usr/bin/env python3
"""Build an offline, read-only model-surface evidence report.

The report deliberately does not discover models, call a provider, query TokenKey,
or write repository/runtime state. Existing platform-specific tools remain the owners
of those operations; this file only normalizes their saved evidence and computes
fail-closed deltas.
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import importlib.util
import json
import sys
import tempfile
from collections import defaultdict
from pathlib import Path
from typing import Any, Iterable

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_BUNDLE = REPO_ROOT / "ops" / "pricing" / "model-surface-bundle.json"
DEFAULT_OVERLAY = REPO_ROOT / "backend" / "internal" / "service" / "tk_pricing_overlay.json"
DEFAULT_LEDGER = REPO_ROOT / "ops" / "pricing" / "servable-reprobe-ledger.json"
REPORT_SCHEMA_VERSION = 1
EVIDENCE_MAX_AGE = dt.timedelta(hours=24)
LIST_MAX_AGE = dt.timedelta(days=7)
PAID_MODALITIES = {"image", "video"}
LOCAL_FLOOR_VERDICTS = {"gateway_rejected", "local_floor_blocked", "not_allowlisted"}
INCONCLUSIVE_VERDICTS = {
    "auth_error", "config_error", "inconclusive", "setup_error", "transport_error",
    "uncorrelated_success", "wrong_account",
}
FAMILY_SCOPE = {
    "anthropic": "anthropic",
    "openai_chat": "openai",
    "openai_responses": "openai",
    "openai_image": "openai",
    "gemini_chat": "gemini",
    "gemini_chat_image": "gemini",
    "gemini_image": "gemini",
    "gemini_video": "gemini",
    "grok": "grok",
}
FAMILY_MODALITY = {
    "openai_image": "image",
    "gemini_chat_image": "image",
    "gemini_image": "image",
    "gemini_video": "video",
}

_bundle_spec = importlib.util.spec_from_file_location(
    "tk_model_surface_bundle_report", REPO_ROOT / "ops" / "pricing" / "model_surface_bundle.py")
_BUNDLE = importlib.util.module_from_spec(_bundle_spec)
_bundle_spec.loader.exec_module(_BUNDLE)

# This module owns no SQL. These names document that property for ops SQL coverage.
SELF_CHECK_EXEMPT: dict[str, str] = {}


def iter_self_check_sql() -> list[tuple[str, str]]:
    return []


def _load_json(path: Path) -> Any:
    try:
        return json.loads(path.expanduser().read_text(encoding="utf-8"))
    except OSError as exc:
        raise RuntimeError(f"cannot read {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"invalid JSON in {path}: {exc}") from exc


def _parse_time(raw: Any, label: str) -> dt.datetime | None:
    if raw in (None, ""):
        return None
    if not isinstance(raw, str):
        raise RuntimeError(f"{label} must be an ISO-8601 string")
    value = raw.strip()
    if value.endswith("Z"):
        value = value[:-1] + "+00:00"
    try:
        parsed = dt.datetime.fromisoformat(value)
    except ValueError as exc:
        raise RuntimeError(f"{label} must be an ISO-8601 timestamp") from exc
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    return parsed.astimezone(dt.timezone.utc)


def _parse_as_of(raw: str | None) -> dt.datetime:
    if raw:
        parsed = _parse_time(raw, "--as-of")
        assert parsed is not None
        return parsed
    return dt.datetime.now(dt.timezone.utc)


def _clean_model(raw: Any) -> str:
    return raw.strip() if isinstance(raw, str) else ""


def _clean_scope(raw: Any) -> str:
    return raw.strip().lower() if isinstance(raw, str) else ""


def _sorted(values: Iterable[str]) -> list[str]:
    return sorted(set(values))


def _scope_floor(bundle: dict[str, Any]) -> dict[str, set[str]]:
    floor = bundle["account_model_mapping"]
    out = {
        scope: set(mapping)
        for scope, mapping in floor["platforms"].items()
    }
    out.update({
        f"newapi_channel_type:{channel_type}": set(mapping)
        for channel_type, mapping in floor["newapi_channel_types"].items()
    })
    for override in floor.get("account_overrides") or []:
        out[_BUNDLE.account_override_scope(override)] = set(override["model_mapping"])
    return out


def _mapping_covers_model(mapping_keys: set[str], model: str) -> bool:
    if model in mapping_keys:
        return True
    return any(key.endswith("*") and model.startswith(key[:-1]) for key in mapping_keys)


def _normalize_base_url(raw: Any) -> str:
    return _BUNDLE.normalize_account_override_base_url(raw)


def _account_shared_scope(row: dict[str, Any], floor: dict[str, Any]) -> str:
    platform = _clean_scope(row.get("platform"))
    account_type = _clean_scope(row.get("type"))
    base_url = _normalize_base_url(row.get("base_url"))
    channel_type = str(row.get("channel_type") or "").strip()

    for override in floor.get("account_overrides") or []:
        if platform != _clean_scope(override.get("platform")):
            continue
        expected_channel = override.get("channel_type")
        if expected_channel is not None and channel_type != str(expected_channel):
            continue
        if base_url != _normalize_base_url(override.get("base_url")):
            continue
        return _BUNDLE.account_override_scope(override)

    if platform == "openai" and account_type == "apikey":
        if base_url in {"https://api.ainzy.net", "https://api.ainzy.net/v1"}:
            return "openai_ainzy_relay"
        if base_url == "https://agent.tokensea.ai":
            return "openai_tokensea_relay"
        if base_url in {"https://api.cloudwise.ai/api", "https://api-us.cloudwise.ai/api"}:
            return "openai_cloudwise_relay"
    if platform == "anthropic" and account_type == "apikey":
        if base_url == "https://agent.tokensea.ai":
            return "anthropic_tokensea_relay"
        if _clean_scope(row.get("mirror_platform")) == "kiro":
            return "kiro"
    if platform == "anthropic" and account_type == "bedrock":
        return "bedrock"
    if platform == "newapi":
        return f"newapi_channel_type:{channel_type or '0'}"
    return platform


def _active_accounts(inventory: Any, floor: dict[str, Any]) -> tuple[list[dict[str, Any]], list[str]]:
    rows = inventory.get("accounts") if isinstance(inventory, dict) else None
    if not isinstance(rows, list):
        raise RuntimeError("inventory must contain an accounts array")
    accounts: list[dict[str, Any]] = []
    warnings: list[str] = []
    for raw in rows:
        if not isinstance(raw, dict):
            continue
        if _clean_scope(raw.get("status") or "active") != "active":
            continue
        if raw.get("schedulable") is False:
            continue
        row = dict(raw)
        row["account_id"] = str(raw.get("id") or raw.get("account_id") or "").strip()
        if not row["account_id"]:
            warnings.append("inventory row without account id was ignored")
            continue
        row["shared_scope"] = _account_shared_scope(row, floor)
        if row["shared_scope"] == "newapi_channel_type:41":
            row["vertex_capability_profile"] = _clean_scope(
                row.get("vertex_capability_profile"))
        accounts.append(row)
    return accounts, warnings


def _normalize_candidates(raw: Any) -> tuple[dict[str, dict[str, str]], dict[str, Any]]:
    """Return scope -> model -> modality and candidate-list metadata.

    Preferred shape:
      {"observed_at": "...", "authoritative": true,
       "scopes": {"anthropic": [{"model": "...", "modality": "chat"}]},
       "account_lists": {"grok": {"65": ["grok-4.6"], "79": ["grok-4.6"]}}}

    ``account_lists`` preserves per-account listing differences. Those lists
    remain candidate evidence, not capability proof, but any difference outside
    Vertex channel_type 41 blocks shared-scope promotion.

    The existing refresh-servable-allowlist family map is accepted as a safe
    compatibility input, but is non-authoritative unless wrapped with metadata.
    """
    if not isinstance(raw, dict):
        raise RuntimeError("candidate discovery must be a JSON object")
    authoritative = raw.get("authoritative") is True
    observed_at = raw.get("observed_at")
    scopes_raw = raw.get("scopes")
    if scopes_raw is None:
        scopes_raw = raw
    if not isinstance(scopes_raw, dict):
        raise RuntimeError("candidate discovery scopes must be an object")
    out: dict[str, dict[str, str]] = defaultdict(dict)
    for source_scope, items in scopes_raw.items():
        if str(source_scope).startswith("_") or source_scope in {
            "account_lists", "authoritative", "observed_at", "source", "scopes",
        }:
            continue
        scope = FAMILY_SCOPE.get(str(source_scope), _clean_scope(source_scope))
        if not scope or not isinstance(items, list):
            continue
        default_modality = FAMILY_MODALITY.get(str(source_scope), "chat")
        for item in items:
            if isinstance(item, str):
                model = _clean_model(item)
                modality = default_modality
            elif isinstance(item, dict):
                model = _clean_model(item.get("model") or item.get("model_id") or item.get("id"))
                modality = _clean_scope(item.get("modality")) or default_modality
            else:
                continue
            if model:
                out[scope][model] = modality
    account_lists: dict[str, dict[str, set[str]]] = defaultdict(dict)
    account_lists_raw = raw.get("account_lists")
    if account_lists_raw is not None:
        if not isinstance(account_lists_raw, dict):
            raise RuntimeError("candidate discovery account_lists must be an object")
        for source_scope, accounts_raw in account_lists_raw.items():
            scope = FAMILY_SCOPE.get(str(source_scope), _clean_scope(source_scope))
            if not scope or not isinstance(accounts_raw, dict):
                continue
            for account_id_raw, models_raw in accounts_raw.items():
                account_id = str(account_id_raw).strip()
                if not account_id or not isinstance(models_raw, list):
                    continue
                account_lists[scope][account_id] = {
                    model for item in models_raw
                    if (model := _clean_model(
                        item.get("model") or item.get("model_id") or item.get("id")
                        if isinstance(item, dict) else item
                    ))
                }
    return dict(out), {
        "authoritative": authoritative,
        "observed_at": observed_at,
        "source": raw.get("source") if isinstance(raw.get("source"), str) else "",
        "account_lists": {
            scope: {account_id: set(models) for account_id, models in accounts.items()}
            for scope, accounts in account_lists.items()
        },
    }


def _decode_json_values(text: str) -> list[Any]:
    decoder = json.JSONDecoder()
    values: list[Any] = []
    offset = 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset >= len(text):
            break
        value, offset = decoder.raw_decode(text, offset)
        values.append(value)
    return values


def _normalize_evidence_row(row: dict[str, Any], observed_at: str) -> dict[str, Any]:
    normalized = dict(row)
    probe = row.get("probe") if isinstance(row.get("probe"), dict) else {}
    target = row.get("target_account") if isinstance(row.get("target_account"), dict) else {}
    usage = row.get("usage_match") if isinstance(row.get("usage_match"), dict) else {}
    normalized.setdefault("model", probe.get("model"))
    normalized.setdefault("account_id", target.get("id"))
    normalized.setdefault("platform", probe.get("platform") or target.get("platform"))
    normalized.setdefault(
        "scope",
        row.get("account_scope") or target.get("scope"),
    )
    normalized.setdefault("channel_type", target.get("channel_type"))
    normalized.setdefault("status", row.get("http_status") or row.get("http_code"))
    normalized.setdefault("usage_match_account_id", usage.get("account_id"))
    normalized.setdefault("modality", probe.get("endpoint"))
    normalized.setdefault("observed_at", probe.get("probe_started_at_utc") or observed_at)
    return normalized


def _read_evidence(paths: list[Path], kind: str) -> tuple[list[dict[str, Any]], list[str]]:
    rows: list[dict[str, Any]] = []
    warnings: list[str] = []
    for path in paths:
        expanded = path.expanduser()
        text = expanded.read_text(encoding="utf-8")
        file_observed_at = dt.datetime.fromtimestamp(
            expanded.stat().st_mtime, tz=dt.timezone.utc).isoformat()
        try:
            decoded = _decode_json_values(text)
        except json.JSONDecodeError:
            decoded = []
        if decoded:
            for raw in decoded:
                default_observed_at = raw.get("observed_at") if isinstance(raw, dict) else None
                values = raw.get("results") if isinstance(raw, dict) and "results" in raw else raw
                if isinstance(values, dict):
                    values = [values]
                elif not isinstance(values, list):
                    raise RuntimeError(
                        f"{path}: evidence JSON values must be objects, arrays, or contain results arrays")
                for value in values:
                    if isinstance(value, dict):
                        rows.append(_normalize_evidence_row(
                            value, default_observed_at or file_observed_at))
            continue
        for lineno, line in enumerate(text.splitlines(), 1):
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            parts = line.split("\t")
            if kind == "traffic" and len(parts) == 3:
                scope, model, hits = (part.strip() for part in parts)
                rows.append({
                    "scope": scope, "model": model, "hits": hits,
                    "observed_at": file_observed_at,
                })
            elif kind != "traffic" and len(parts) == 4:
                scope, model, status, verdict = (part.strip() for part in parts)
                rows.append({
                    "scope": scope, "model": model, "status": status,
                    "verdict": verdict, "observed_at": file_observed_at,
                })
            else:
                warnings.append(f"{path}:{lineno}: ignored malformed evidence row")
    return rows, warnings


def _evidence_class(row: dict[str, Any], kind: str) -> str:
    if kind == "traffic":
        return "traffic-proven"
    verdict = _clean_scope(row.get("verdict"))
    status_raw = str(
        row.get("status") or row.get("http_status") or row.get("code") or ""
    ).strip()
    try:
        status = int(status_raw)
    except ValueError:
        status = 0
    if status == 429 or status >= 500:
        return "inconclusive"
    if verdict in {"unsupported", "unsupported_or_denied", "upstream_rejected"} and 400 <= status < 500:
        return "unsupported"
    if verdict in INCONCLUSIVE_VERDICTS:
        return "inconclusive"
    if kind == "gateway" and verdict in LOCAL_FLOOR_VERDICTS:
        return "local-floor-blocked"
    if verdict == "servable" and 200 <= status < 300:
        if kind == "gateway":
            expected = str(row.get("account_id") or "").strip()
            actual = str(row.get("usage_account_id") or row.get("usage_match_account_id") or "").strip()
            if not expected or actual != expected:
                return "inconclusive"
        return "servable"
    return "inconclusive"


def _fresh_rows(
    rows: list[dict[str, Any]], kind: str, as_of: dt.datetime,
) -> tuple[list[dict[str, Any]], list[str]]:
    fresh: list[dict[str, Any]] = []
    warnings: list[str] = []
    for row in rows:
        observed = _parse_time(row.get("observed_at"), f"{kind} evidence observed_at")
        label = (
            f"{_clean_scope(row.get('scope') or row.get('platform'))}/"
            f"{_clean_model(row.get('model'))}"
        )
        if observed is None:
            warnings.append(f"undated {kind} evidence ignored: {label}")
            continue
        if as_of - observed > EVIDENCE_MAX_AGE or observed > as_of + dt.timedelta(minutes=5):
            warnings.append(f"stale {kind} evidence ignored: {label}")
            continue
        fresh.append(row)
    return fresh, warnings


def _normalize_row_scope(row: dict[str, Any], account_by_id: dict[str, dict[str, Any]]) -> str:
    scope = _clean_scope(row.get("scope") or row.get("platform"))
    account_id = str(row.get("account_id") or "").strip()
    if account_id and account_id in account_by_id:
        account = account_by_id[account_id]
        account_scope = account["shared_scope"]
        family_scope = FAMILY_SCOPE.get(scope, scope)
        account_platform = _clean_scope(account.get("platform"))
        if not scope or scope == "gemini" or family_scope == account_platform:
            return account_scope
    if scope == "newapi" and row.get("channel_type") is not None:
        return f"newapi_channel_type:{row.get('channel_type')}"
    return FAMILY_SCOPE.get(scope, scope)


def _price_entry(overlay: dict[str, Any], model: str) -> Any:
    entry = overlay.get(model)
    if entry is not None:
        return entry
    normalized = model.lower()
    return overlay.get(normalized) if normalized != model else None


def _positive_price(entry: Any) -> bool:
    if not isinstance(entry, dict):
        return False
    mode = entry.get("mode")
    fields = {
        "chat": ("input_cost_per_token", "output_cost_per_token"),
        "embedding": ("input_cost_per_token",),
        "image_generation": ("output_cost_per_image",),
        "video_generation": ("output_cost_per_second",),
    }.get(mode)
    if not fields:
        return False
    return all(
        isinstance(entry.get(field), (int, float))
        and not isinstance(entry.get(field), bool)
        and entry[field] > 0
        for field in fields
    )


def _watchlist_status(ledger: Any, as_of: dt.datetime) -> dict[str, Any]:
    watchlist = ledger.get("watchlist") if isinstance(ledger, dict) else []
    skiplist = ledger.get("skiplist") if isinstance(ledger, dict) else []
    deadlist = ledger.get("deadlist") if isinstance(ledger, dict) else []
    stale: list[str] = []
    for row in watchlist if isinstance(watchlist, list) else []:
        if not isinstance(row, dict):
            continue
        last = row.get("last_probe")
        days = row.get("freshness_days")
        if not isinstance(last, str) or not isinstance(days, int):
            stale.append(f"{row.get('platform', '')}/{row.get('model', '')}")
            continue
        try:
            expires = dt.date.fromisoformat(last) + dt.timedelta(days=days)
        except ValueError:
            stale.append(f"{row.get('platform', '')}/{row.get('model', '')}")
            continue
        if expires < as_of.date():
            stale.append(f"{row.get('platform', '')}/{row.get('model', '')}")
    return {
        "watchlist_count": len(watchlist) if isinstance(watchlist, list) else 0,
        "stale": sorted(stale),
        "skiplist_count": len(skiplist) if isinstance(skiplist, list) else 0,
        "deadlist_count": len(deadlist) if isinstance(deadlist, list) else 0,
    }


def _capability_clusters(
    account_ids: list[str], raw_by_account: dict[str, dict[str, set[str]]],
    profiles: dict[str, dict[str, str]], candidate_models: set[str],
) -> dict[str, Any]:
    sets: dict[str, set[str]] = {
        account_id: set(raw_by_account.get(account_id, {}).get("servable", set()))
        for account_id in account_ids
    }
    observed_by_account = {
        account_id: set().union(*raw_by_account.get(account_id, {}).values())
        if raw_by_account.get(account_id) else set()
        for account_id in account_ids
    }
    complete = bool(account_ids) and bool(candidate_models) and all(
        candidate_models <= observed_by_account[account_id]
        for account_id in account_ids
    )
    intersection = set.intersection(*sets.values()) if complete and sets else set()
    union = set().union(*sets.values()) if sets else set()
    profile_sets = {profile: set(mapping) for profile, mapping in profiles.items()}
    grouped: dict[tuple[str, ...], list[str]] = defaultdict(list)
    if complete:
        for account_id, models in sets.items():
            grouped[tuple(sorted(models))].append(account_id)
    suggestions: list[dict[str, Any]] = []
    for shape, members in sorted(grouped.items()):
        models = set(shape)
        known = next((name for name, expected in profile_sets.items() if expected == models), "")
        digest = hashlib.sha256("\n".join(shape).encode("utf-8")).hexdigest()[:10]
        suggestions.append({
            "profile": known or f"candidate-{digest}",
            "existing_profile": bool(known),
            "account_ids": sorted(members),
            "models": list(shape),
        })
    return {
        "complete_account_coverage": complete,
        "account_capabilities": {key: _sorted(value) for key, value in sorted(sets.items())},
        "strict_intersection": _sorted(intersection),
        "public_union": _sorted(union),
        "profile_suggestions": suggestions,
    }


def build_report(
    *,
    bundle: dict[str, Any],
    overlay: dict[str, Any],
    ledger: dict[str, Any],
    inventory: dict[str, Any],
    candidates_raw: dict[str, Any],
    raw_rows: list[dict[str, Any]],
    gateway_rows: list[dict[str, Any]],
    traffic_rows: list[dict[str, Any]],
    as_of: dt.datetime,
) -> dict[str, Any]:
    floor = bundle["account_model_mapping"]
    floors = _scope_floor(bundle)
    accounts, warnings = _active_accounts(inventory, floor)
    account_by_id = {row["account_id"]: row for row in accounts}
    accounts_by_scope: dict[str, list[str]] = defaultdict(list)
    for row in accounts:
        accounts_by_scope[row["shared_scope"]].append(row["account_id"])

    candidates, candidate_meta = _normalize_candidates(candidates_raw)
    observed = _parse_time(candidate_meta.get("observed_at"), "candidate discovery observed_at")
    list_fresh = observed is not None and dt.timedelta(0) <= as_of - observed <= LIST_MAX_AGE
    authoritative_list = bool(candidate_meta["authoritative"] and list_fresh)
    if not candidate_meta["authoritative"]:
        warnings.append("candidate discovery is not marked authoritative; proposed additions are blocked")
    elif not list_fresh:
        warnings.append("candidate discovery is missing, stale, or future-dated; proposed additions are blocked")

    fresh_raw, row_warnings = _fresh_rows(raw_rows, "raw", as_of)
    warnings.extend(row_warnings)
    fresh_gateway, row_warnings = _fresh_rows(gateway_rows, "gateway", as_of)
    warnings.extend(row_warnings)
    fresh_traffic, row_warnings = _fresh_rows(traffic_rows, "traffic", as_of)
    warnings.extend(row_warnings)

    evidence: dict[str, dict[str, dict[str, set[str]]]] = {
        "raw": defaultdict(lambda: defaultdict(set)),
        "gateway": defaultdict(lambda: defaultdict(set)),
        "traffic": defaultdict(lambda: defaultdict(set)),
    }
    raw_by_account: dict[str, dict[str, set[str]]] = defaultdict(lambda: defaultdict(set))
    modality: dict[tuple[str, str], str] = {}
    paid_approval: dict[tuple[str, str], bool] = {}
    for kind, rows in (("raw", fresh_raw), ("gateway", fresh_gateway), ("traffic", fresh_traffic)):
        for row in rows:
            model = _clean_model(row.get("model") or row.get("model_id"))
            scope = _normalize_row_scope(row, account_by_id)
            if not model or not scope:
                warnings.append(f"{kind} evidence without scope/model was ignored")
                continue
            klass = _evidence_class(row, kind)
            evidence[kind][scope][klass].add(model)
            row_modality = _clean_scope(row.get("modality"))
            if row_modality:
                modality[(scope, model)] = row_modality
            if row.get("paid_probe_approved") is True:
                paid_approval[(scope, model)] = True
            account_id = str(row.get("account_id") or "").strip()
            if kind == "raw" and account_id:
                raw_by_account[account_id][klass].add(model)

    all_scopes = set(floors) | set(candidates) | set(accounts_by_scope)
    for kind in evidence.values():
        all_scopes.update(kind)
    scope_docs: dict[str, Any] = {}
    violations: list[str] = []
    for scope in sorted(all_scopes):
        listed = set(candidates.get(scope, {}))
        current = set(floors.get(scope, set()))
        current_coverage = set(current)
        if scope == "newapi_channel_type:41":
            for mapping in (floor.get("vertex_capability_profiles") or {}).values():
                current_coverage.update(mapping)
        traffic = set(evidence["traffic"][scope].get("traffic-proven", set()))
        raw_capable = set(evidence["raw"][scope].get("servable", set()))
        gateway_served = set(evidence["gateway"][scope].get("servable", set()))
        unsupported = (
            set(evidence["raw"][scope].get("unsupported", set()))
            | set(evidence["gateway"][scope].get("unsupported", set()))
        ) - raw_capable - gateway_served
        inconclusive = (
            set(evidence["raw"][scope].get("inconclusive", set()))
            | set(evidence["gateway"][scope].get("inconclusive", set()))
        ) - raw_capable - gateway_served
        local_blocked = set(evidence["gateway"][scope].get("local-floor-blocked", set()))
        candidate_universe = listed | raw_capable | gateway_served | traffic
        price_missing = {
            model for model in candidate_universe
            if not _positive_price(_price_entry(overlay, model))
        }

        divergence: list[dict[str, Any]] = []
        if scope != "newapi_channel_type:41":
            listed_by_account = candidate_meta["account_lists"].get(scope, {})
            if len(listed_by_account) > 1:
                listed_universe = set().union(*listed_by_account.values())
                for model in sorted(listed_universe):
                    present = sorted(
                        account_id for account_id, models in listed_by_account.items()
                        if model in models
                    )
                    absent = sorted(set(listed_by_account) - set(present))
                    if present and absent:
                        divergence.append({
                            "model": model,
                            "kind": "account-list",
                            "listed_account_ids": present,
                            "missing_account_ids": absent,
                        })

            conclusive: dict[str, dict[str, str]] = defaultdict(dict)
            for account_id in accounts_by_scope.get(scope, []):
                for klass in ("servable", "unsupported"):
                    for model in raw_by_account.get(account_id, {}).get(klass, set()):
                        conclusive[model][account_id] = klass
            for model, verdicts in sorted(conclusive.items()):
                if len(set(verdicts.values())) > 1:
                    divergence.append({
                        "model": model,
                        "kind": "capability",
                        "account_verdicts": dict(sorted(verdicts.items())),
                    })
            if divergence:
                violations.append(
                    f"{scope}: account discovery/capability divergence is not allowed outside Vertex channel_type 41")

        already_required = {
            model for model in listed | raw_capable | gateway_served
            if _mapping_covers_model(current_coverage, model)
        }
        proposed = (listed & raw_capable & gateway_served) - already_required - price_missing
        paid_pending = set()
        for model in list(proposed):
            model_modality = modality.get((scope, model), candidates.get(scope, {}).get(model, "chat"))
            if model_modality in PAID_MODALITIES and not paid_approval.get((scope, model), False):
                proposed.remove(model)
                paid_pending.add(model)
        if not authoritative_list or divergence:
            proposed.clear()
        scope_docs[scope] = {
            "account_ids": sorted(accounts_by_scope.get(scope, [])),
            "current_required": _sorted(current),
            "already_required": _sorted(already_required),
            "listed": _sorted(listed),
            "traffic_proven": _sorted(traffic),
            "raw_capable": _sorted(raw_capable),
            "gateway_served": _sorted(gateway_served),
            "unsupported": _sorted(unsupported),
            "inconclusive": _sorted(inconclusive),
            "local_floor_blocked": _sorted(local_blocked),
            "price_missing": _sorted(price_missing),
            "paid_probe_approval_missing": _sorted(paid_pending),
            "proposed_add": _sorted(proposed),
            "account_divergence": divergence,
        }

    vertex_scope = "newapi_channel_type:41"
    vertex_account_ids = sorted(accounts_by_scope.get(vertex_scope, []))
    vertex = _capability_clusters(
        vertex_account_ids,
        raw_by_account,
        floor.get("vertex_capability_profiles") or {},
        set(candidates.get(vertex_scope, {})),
    )
    if vertex_account_ids and not vertex["complete_account_coverage"]:
        violations.append("newapi_channel_type:41: raw evidence does not cover every active schedulable account")

    watchlist = _watchlist_status(ledger, as_of)
    if watchlist["stale"]:
        warnings.append(f"{len(watchlist['stale'])} reprobe watchlist entries are stale")
    return {
        "schema_version": REPORT_SCHEMA_VERSION,
        "as_of": as_of.isoformat().replace("+00:00", "Z"),
        "bundle": {
            "schema_version": bundle["schema_version"],
            "floor_sha256": bundle["floor_sha256"],
        },
        "candidate_discovery": {
            **{key: value for key, value in candidate_meta.items() if key != "account_lists"},
            "account_list_scope_count": len(candidate_meta["account_lists"]),
            "fresh": list_fresh,
            "usable_for_additions": authoritative_list,
            "scope_count": len(candidates),
            "model_count": sum(len(models) for models in candidates.values()),
        },
        "inventory": {
            "active_schedulable_account_count": len(accounts),
            "accounts_by_scope": {scope: sorted(ids) for scope, ids in sorted(accounts_by_scope.items())},
        },
        "scopes": scope_docs,
        "vertex_channel_type_41": vertex,
        "reprobe_ledger": watchlist,
        "violations": sorted(set(violations)),
        "warnings": sorted(set(warnings)),
        "commands": {
            "writes": [],
            "note": "report is offline/read-only; review evidence before using existing platform probe or modelops activation commands",
        },
    }


def _render_text(report: dict[str, Any]) -> str:
    lines = [
        f"model-surface refresh report {report['as_of']}",
        f"bundle {report['bundle']['floor_sha256']}",
    ]
    for scope, row in report["scopes"].items():
        lines.append(
            f"{scope}: listed={len(row['listed'])} raw={len(row['raw_capable'])} "
            f"gateway={len(row['gateway_served'])} price_missing={len(row['price_missing'])} "
            f"proposed_add={len(row['proposed_add'])}")
        if row["proposed_add"]:
            lines.append("  proposed_add: " + ", ".join(row["proposed_add"]))
        if row["account_divergence"]:
            lines.append("  BLOCKED: shared-scope account divergence")
    vertex = report["vertex_channel_type_41"]
    lines.append(
        "vertex ch41: intersection=" + ", ".join(vertex["strict_intersection"])
        + " | union=" + ", ".join(vertex["public_union"]))
    for violation in report["violations"]:
        lines.append("VIOLATION: " + violation)
    for warning in report["warnings"]:
        lines.append("WARNING: " + warning)
    return "\n".join(lines) + "\n"


def _write_fixture(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value), encoding="utf-8")


def _selftest() -> int:
    as_of = dt.datetime(2026, 8, 14, 12, tzinfo=dt.timezone.utc)
    shared = {"shared": "shared"}
    bundle_floor = {
        "platforms": {"openai": shared},
        "newapi_channel_types": {"41": shared},
        "account_overrides": [],
        "vertex_capability_profiles": {
            "core-pro": {"shared": "shared", "pro": "pro"},
            "core-image": {"shared": "shared", "image": "image"},
        },
        "antigravity_group_scopes": ["claude"],
        "forbidden_model_mapping_keys": {},
        "forbidden_model_mapping_prefixes": {},
    }
    bundle = {
        "schema_version": 4,
        "floor_sha256": _BUNDLE.floor_sha256(bundle_floor),
        "account_model_mapping": bundle_floor,
    }
    overlay = {
        "embedding": {"mode": "embedding", "input_cost_per_token": 1, "output_cost_per_token": 0},
        "new-text": {"mode": "chat", "input_cost_per_token": 1, "output_cost_per_token": 1},
        "image": {"mode": "image_generation", "output_cost_per_image": 1},
        "pro": {"mode": "chat", "input_cost_per_token": 1, "output_cost_per_token": 1},
        "shared": {"mode": "chat", "input_cost_per_token": 1, "output_cost_per_token": 1},
    }
    assert _positive_price(overlay["embedding"])
    overlay["lowercase-price"] = overlay["new-text"]
    assert _positive_price(_price_entry(overlay, "LOWERCASE-PRICE"))
    inventory = {"accounts": [
        {"id": 1, "platform": "openai", "status": "active", "schedulable": True},
        {"id": 2, "platform": "openai", "status": "active", "schedulable": True},
        {
            "id": 3, "platform": "openai", "type": "apikey",
            "base_url": "https://api.cloudwise.ai/api",
            "status": "active", "schedulable": True,
        },
        {"id": 47, "platform": "newapi", "channel_type": 41, "status": "active", "schedulable": True},
        {"id": 57, "platform": "newapi", "channel_type": 41, "status": "active", "schedulable": True},
    ]}
    candidates = {
        "observed_at": "2026-08-14T10:00:00Z", "authoritative": True,
        "scopes": {
            "openai": ["new-text", "unpriced", {"model": "paid-image", "modality": "image"}],
            "newapi_channel_type:41": ["shared", "pro", {"model": "image", "modality": "image"}],
        },
    }
    observed = "2026-08-14T11:00:00Z"
    raw = [
        {"scope": "openai", "account_id": 1, "model": "new-text", "status": 200, "verdict": "servable", "observed_at": observed},
        {"scope": "openai", "account_id": 2, "model": "new-text", "status": 200, "verdict": "servable", "observed_at": observed},
        {"scope": "openai", "account_id": 1, "model": "unpriced", "status": 429, "verdict": "unsupported", "observed_at": observed},
        {"scope": "newapi", "account_id": 47, "model": "shared", "status": 200, "verdict": "servable", "observed_at": observed},
        {"scope": "newapi", "account_id": 47, "model": "pro", "status": 200, "verdict": "servable", "observed_at": observed},
        {"scope": "newapi", "account_id": 47, "model": "image", "status": 404, "verdict": "unsupported", "modality": "image", "observed_at": observed},
        {"scope": "newapi", "account_id": 57, "model": "shared", "status": 200, "verdict": "servable", "observed_at": observed},
        {"scope": "newapi", "account_id": 57, "model": "pro", "status": 404, "verdict": "unsupported", "observed_at": observed},
        {"scope": "newapi", "account_id": 57, "model": "image", "status": 200, "verdict": "servable", "modality": "image", "paid_probe_approved": True, "observed_at": observed},
    ]
    gateway = [
        {"scope": "openai", "account_id": 1, "usage_account_id": 1, "model": "new-text", "status": 200, "verdict": "servable", "observed_at": observed},
        {"scope": "openai", "model": "unpriced", "status": 503, "verdict": "unsupported", "observed_at": observed},
        {"scope": "openai", "model": "paid-image", "status": 400, "verdict": "gateway_rejected", "observed_at": observed},
        {"scope": "newapi", "account_id": 47, "usage_account_id": 47, "model": "pro", "status": 200, "verdict": "servable", "observed_at": observed},
        {"scope": "newapi", "account_id": 57, "usage_account_id": 57, "model": "image", "status": 200, "verdict": "servable", "modality": "image", "observed_at": observed},
    ]
    ledger = {"watchlist": [{
        "platform": "openai", "model": "old", "last_probe": "2026-01-01", "freshness_days": 30,
    }], "skiplist": [], "deadlist": []}
    report = build_report(
        bundle=bundle, overlay=overlay, ledger=ledger, inventory=inventory,
        candidates_raw=candidates, raw_rows=raw, gateway_rows=gateway, traffic_rows=[], as_of=as_of)
    assert report["scopes"]["openai"]["proposed_add"] == ["new-text"]
    assert report["scopes"]["openai"]["inconclusive"] == ["unpriced"]
    assert _evidence_class(
        {"status": 401, "verdict": "unsupported_or_denied"}, "raw",
    ) == "unsupported"

    report_accounts, _ = _active_accounts(inventory, bundle_floor)
    account_by_id = {row["account_id"]: row for row in report_accounts}
    assert _normalize_row_scope(
        {"scope": "openai", "account_id": 3}, account_by_id,
    ) == "openai_cloudwise_relay"
    assert _normalize_row_scope(
        {"scope": "openai_chat", "account_id": 3}, account_by_id,
    ) == "openai_cloudwise_relay"
    assert _normalize_row_scope(
        {"scope": "different-scope", "account_id": 3}, account_by_id,
    ) == "different-scope"

    wildcard_floor = dict(bundle_floor)
    wildcard_floor["platforms"] = {"openai": {"glm-*": "glm-*"}}
    wildcard_bundle = {
        "schema_version": 4,
        "floor_sha256": _BUNDLE.floor_sha256(wildcard_floor),
        "account_model_mapping": wildcard_floor,
    }
    wildcard_candidates = {
        "observed_at": "2026-08-14T10:00:00Z", "authoritative": True,
        "scopes": {"openai": ["glm-5.2"]},
    }
    wildcard_raw = [
        {"scope": "openai", "account_id": 1, "model": "glm-5.2", "status": 200, "verdict": "servable", "observed_at": observed},
        {"scope": "openai", "account_id": 2, "model": "glm-5.2", "status": 200, "verdict": "servable", "observed_at": observed},
    ]
    wildcard_gateway = [{
        "scope": "openai", "account_id": 1, "usage_account_id": 1,
        "model": "glm-5.2", "status": 200, "verdict": "servable", "observed_at": observed,
    }]
    wildcard = build_report(
        bundle=wildcard_bundle, overlay={"glm-5.2": overlay["new-text"]}, ledger={},
        inventory=inventory, candidates_raw=wildcard_candidates, raw_rows=wildcard_raw,
        gateway_rows=wildcard_gateway, traffic_rows=[], as_of=as_of)
    assert wildcard["scopes"]["openai"]["already_required"] == ["glm-5.2"]
    assert wildcard["scopes"]["openai"]["proposed_add"] == []
    assert report["scopes"]["openai"]["local_floor_blocked"] == ["paid-image"]
    assert report["reprobe_ledger"]["stale"] == ["openai/old"]
    assert report["scopes"]["newapi_channel_type:41"]["already_required"] == [
        "image", "pro", "shared",
    ]
    assert report["scopes"]["newapi_channel_type:41"]["proposed_add"] == []
    vertex = report["vertex_channel_type_41"]
    assert vertex["strict_intersection"] == ["shared"]
    assert vertex["public_union"] == ["image", "pro", "shared"]
    assert {row["profile"] for row in vertex["profile_suggestions"]} == {"core-image", "core-pro"}

    partial_vertex_raw = [
        row for row in raw
        if not (row.get("account_id") == 57 and row.get("model") == "image")
    ]
    partial_vertex = build_report(
        bundle=bundle, overlay=overlay, ledger={}, inventory=inventory,
        candidates_raw=candidates, raw_rows=partial_vertex_raw,
        gateway_rows=gateway, traffic_rows=[], as_of=as_of)
    assert partial_vertex["vertex_channel_type_41"]["complete_account_coverage"] is False
    assert partial_vertex["vertex_channel_type_41"]["strict_intersection"] == []
    assert partial_vertex["vertex_channel_type_41"]["profile_suggestions"] == []
    assert any(
        violation.startswith("newapi_channel_type:41: raw evidence does not cover")
        for violation in partial_vertex["violations"]
    )

    no_authority = dict(candidates)
    no_authority["authoritative"] = False
    blocked = build_report(
        bundle=bundle, overlay=overlay, ledger={}, inventory=inventory,
        candidates_raw=no_authority, raw_rows=raw, gateway_rows=gateway, traffic_rows=[], as_of=as_of)
    assert blocked["scopes"]["openai"]["proposed_add"] == []

    divergent_raw = raw + [
        {"scope": "openai", "account_id": 2, "model": "split", "status": 404, "verdict": "unsupported", "observed_at": observed},
        {"scope": "openai", "account_id": 1, "model": "split", "status": 200, "verdict": "servable", "observed_at": observed},
    ]
    divergent = build_report(
        bundle=bundle, overlay=overlay, ledger={}, inventory=inventory,
        candidates_raw=candidates, raw_rows=divergent_raw, gateway_rows=gateway, traffic_rows=[], as_of=as_of)
    assert divergent["scopes"]["openai"]["account_divergence"]
    assert divergent["scopes"]["openai"]["proposed_add"] == []

    listed_divergent_candidates = dict(candidates)
    listed_divergent_candidates["account_lists"] = {
        "openai": {
            "1": ["shared-list", "account-one-only"],
            "2": ["shared-list", "account-two-only"],
        },
    }
    listed_divergent = build_report(
        bundle=bundle, overlay=overlay, ledger={}, inventory=inventory,
        candidates_raw=listed_divergent_candidates, raw_rows=raw,
        gateway_rows=gateway, traffic_rows=[], as_of=as_of)
    listed_divergence = listed_divergent["scopes"]["openai"]["account_divergence"]
    assert {row["model"] for row in listed_divergence} == {
        "account-one-only", "account-two-only",
    }
    assert all(row["kind"] == "account-list" for row in listed_divergence)
    assert any(
        violation.startswith("openai: account discovery/capability divergence")
        for violation in listed_divergent["violations"]
    )

    paid_candidates = {
        "observed_at": "2026-08-14T10:00:00Z", "authoritative": True,
        "scopes": {"openai": [{"model": "paid-image", "modality": "image"}]},
    }
    paid_raw = [{"scope": "openai", "model": "paid-image", "modality": "image", "status": 200, "verdict": "servable", "observed_at": observed}]
    paid_gateway = [{
        "scope": "openai", "account_id": 1, "usage_account_id": 1,
        "model": "paid-image", "modality": "image", "status": 200,
        "verdict": "servable", "observed_at": observed,
    }]
    paid_overlay = dict(overlay)
    paid_overlay["paid-image"] = {"mode": "image_generation", "output_cost_per_image": 1}
    paid = build_report(
        bundle=bundle, overlay=paid_overlay, ledger={}, inventory=inventory,
        candidates_raw=paid_candidates, raw_rows=paid_raw, gateway_rows=paid_gateway,
        traffic_rows=[], as_of=as_of)
    assert paid["scopes"]["openai"]["paid_probe_approval_missing"] == ["paid-image"]
    assert paid["scopes"]["openai"]["proposed_add"] == []

    empty = build_report(
        bundle=bundle, overlay=overlay, ledger={}, inventory={"accounts": []},
        candidates_raw={"observed_at": "2026-08-14T10:00:00Z", "authoritative": True, "scopes": {}},
        raw_rows=[], gateway_rows=[], traffic_rows=[], as_of=as_of)
    assert empty["candidate_discovery"]["model_count"] == 0
    assert empty["violations"] == []

    stale = build_report(
        bundle=bundle, overlay=overlay, ledger={}, inventory=inventory,
        candidates_raw={"observed_at": "2026-07-01T00:00:00Z", "authoritative": True, "scopes": {"openai": ["new-text"]}},
        raw_rows=raw, gateway_rows=gateway, traffic_rows=[], as_of=as_of)
    assert stale["candidate_discovery"]["usable_for_additions"] is False
    assert stale["scopes"]["openai"]["proposed_add"] == []

    undated = build_report(
        bundle=bundle, overlay=overlay, ledger={}, inventory=inventory,
        candidates_raw=candidates,
        raw_rows=[{"scope": "openai", "account_id": 1, "model": "new-text", "status": 200, "verdict": "servable"}],
        gateway_rows=[], traffic_rows=[], as_of=as_of)
    assert undated["scopes"]["openai"]["raw_capable"] == []
    assert any(warning.startswith("undated raw evidence ignored") for warning in undated["warnings"])

    with tempfile.TemporaryDirectory() as temp_dir:
        evidence_path = Path(temp_dir) / "gateway.json"
        nested = {
            "http_code": "200",
            "probe": {
                "model": "new-text", "platform": "openai", "endpoint": "responses",
                "probe_started_at_utc": observed,
            },
            "target_account": {"id": 1, "platform": "openai"},
            "usage_match": {"account_id": 1},
            "verdict": "servable",
        }
        blocked_nested = {
            "http_code": "400",
            "probe": {
                "model": "blocked", "platform": "openai", "endpoint": "chat",
                "probe_started_at_utc": observed,
            },
            "target_account": {"id": 1, "platform": "openai"},
            "usage_match": None,
            "verdict": "gateway_rejected",
        }
        evidence_path.write_text(
            json.dumps(nested) + "\n" + json.dumps(blocked_nested), encoding="utf-8")
        nested_rows, nested_warnings = _read_evidence([evidence_path], "gateway")
        assert nested_warnings == []
        assert len(nested_rows) == 2
        assert nested_rows[0]["model"] == "new-text"
        assert str(nested_rows[0]["account_id"]) == "1"
        assert str(nested_rows[0]["usage_match_account_id"]) == "1"
        assert nested_rows[0]["status"] == "200"
        assert nested_rows[0]["modality"] == "responses"
        assert _evidence_class(nested_rows[0], "gateway") == "servable"
        assert _evidence_class(nested_rows[1], "gateway") == "local-floor-blocked"

        output = Path(temp_dir) / "report.json"
        output.write_text(json.dumps(report, sort_keys=True), encoding="utf-8")
        assert json.loads(output.read_text(encoding="utf-8"))["schema_version"] == REPORT_SCHEMA_VERSION
    print("selftest ok")
    return 0


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--selftest", action="store_true")
    subparsers = parser.add_subparsers(dest="command")
    generate = subparsers.add_parser("generate", help="build an offline evidence report")
    generate.add_argument("--bundle", type=Path, default=DEFAULT_BUNDLE)
    generate.add_argument("--overlay", type=Path, default=DEFAULT_OVERLAY)
    generate.add_argument("--reprobe-ledger", type=Path, default=DEFAULT_LEDGER)
    generate.add_argument("--inventory", type=Path, required=True)
    generate.add_argument("--candidates", type=Path, required=True)
    generate.add_argument("--raw-probe", type=Path, action="append", default=[])
    generate.add_argument("--gateway-probe", type=Path, action="append", default=[])
    generate.add_argument("--traffic-evidence", type=Path, action="append", default=[])
    generate.add_argument("--as-of", help="ISO-8601 clock for deterministic freshness checks")
    generate.add_argument("--format", choices=("json", "text"), default="json")
    return parser


def main() -> int:
    args = _build_parser().parse_args()
    if args.selftest:
        return _selftest()
    if args.command != "generate":
        _build_parser().print_help(sys.stderr)
        return 2
    bundle = _BUNDLE.load_bundle(args.bundle)
    overlay = _load_json(args.overlay)
    ledger = _load_json(args.reprobe_ledger)
    inventory = _load_json(args.inventory)
    candidates = _load_json(args.candidates)
    raw_rows, warnings = _read_evidence(args.raw_probe, "raw")
    gateway_rows, more = _read_evidence(args.gateway_probe, "gateway")
    warnings.extend(more)
    traffic_rows, more = _read_evidence(args.traffic_evidence, "traffic")
    warnings.extend(more)
    report = build_report(
        bundle=bundle, overlay=overlay, ledger=ledger, inventory=inventory,
        candidates_raw=candidates, raw_rows=raw_rows, gateway_rows=gateway_rows,
        traffic_rows=traffic_rows, as_of=_parse_as_of(args.as_of))
    report["warnings"] = sorted(set(report["warnings"] + warnings))
    if args.format == "json":
        print(json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True))
    else:
        print(_render_text(report), end="")
    return 1 if report["violations"] else 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RuntimeError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        raise SystemExit(2) from exc
