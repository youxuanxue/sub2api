#!/usr/bin/env python3
"""Manage the TK account model_mapping runtime layer and explicit account apply.

The compiled floor lives in Go:
  - native platforms: supported*CatalogModels + pricing/display gates
  - newapi: tk_served_models.json display projection
  - aliases: DefaultAntigravityModelMapping / xai.DefaultModelMapping

This tool writes an optional runtime replacement layer to settings key
``tk_account_model_mapping_runtime`` across prod/deployable edges. A present
scope REPLACES the compiled floor for that platform or newapi channel_type
when generating apply-accounts / check-accounts desired mappings; absent
scopes keep the compiled floor. Writing the setting does not mutate
accounts.

Empty-mapping Antigravity accounts (typical edge OAuth rows) honor
``platforms.antigravity`` as a serving overlay on compiled
``DefaultAntigravityModelMapping`` after settings fan-out. That is the
hot-add path for group 21 / edge AG: ``sync-runtime`` is enough. Non-empty
account mappings stay authoritative and still need ``apply-accounts``.
Use ``check-accounts`` to diff live accounts against a generated model
surface bundle, then
``apply-accounts --confirm ...`` when an operator has reviewed the diff and
wants to overwrite persisted mappings.

``release-gate`` is an explicit, **prod-only** modelops/model-activation floor
check. It compares live prod account mappings to the selected release bundle's
required floor and fails closed when prod is behind that model surface. It is not a
generic binary deploy or rollback prerequisite. Live prod may be ahead for
preheating or rollback, but forbidden keys/prefixes from the selected bundle
still fail the check. Edge accounts keep empty ``model_mapping`` and Antigravity
empty rows serve the compiled default plus the runtime overlay. Do not
bulk-apply prod floors to edges. Edge-specific troubleshooting belongs to
``check-accounts --include-edges``; ``release-gate`` always checks prod only.
See ``docs/global/agent-reference.md`` § Model serving SSOT.

Compiled ``account_overrides`` take precedence over platform and newapi
channel-type floors when an account's platform, channel type, and normalized
base URL all match. They are immutable bundle scopes and are not shadowed by
the shared-scope runtime setting.

Runtime JSON shape:

{
  "platforms": {
    "grok": {"grok": "grok-4.3"},
    "antigravity": {"claude-sonnet-4-6": "claude-sonnet-4-6"}
  },
  "newapi_channel_types": {
    "41": {"imagen-4.0-generate-001": "imagen-4.0-generate-001"}
  }
}
"""
from __future__ import annotations

import argparse
import base64
import copy
import contextlib
import gzip
import hashlib
import importlib.util
import io
import json
import re
import shlex
import subprocess
import sys
import tempfile
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Any, NoReturn

REPO_ROOT = Path(__file__).resolve().parents[2]
SETTING_KEY = "tk_account_model_mapping_runtime"
MANAGED_PLATFORMS = ("anthropic", "openai", "gemini", "antigravity", "newapi", "kiro", "grok")

_bundle_spec = importlib.util.spec_from_file_location(
    "tk_model_surface_bundle", REPO_ROOT / "ops" / "pricing" / "model_surface_bundle.py")
_BUNDLE = importlib.util.module_from_spec(_bundle_spec)
_bundle_spec.loader.exec_module(_BUNDLE)

PSQL = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1"
REDISCLI = "env -u REDISCLI_AUTH sudo docker exec tokenkey-redis redis-cli"
APPLY_CONFIRM = "yes-apply-account-model-mapping"
VERTEX_PROFILE_ASSIGN_CONFIRM = "yes-assign-vertex-capability-profiles"
VERTEX_PROFILE_CREDENTIAL_KEY = "vertex_capability_profile"
DEFAULT_BUNDLE_PATH = _BUNDLE.DEFAULT_BUNDLE_PATH
BUNDLE_SCHEMA_VERSION = _BUNDLE.SCHEMA_VERSION
DEFAULT_RUNTIME_TARGET = "all-deployable-and-prod"

ACCOUNT_MODEL_MAPPING_CHECK_SQL = """
SELECT jsonb_build_object(
  'accounts', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', a.id,
      'name', a.name,
      'platform', a.platform,
      'type', a.type,
      'channel_type', a.channel_type,
      'status', a.status,
      'schedulable', a.schedulable,
      'model_mapping', a.credentials->'model_mapping',
      'mirror_platform', a.credentials->>'mirror_platform',
      'base_url', a.credentials->>'base_url',
      'auth_mode', a.credentials->>'auth_mode',
      'vertex_capability_profile', a.credentials->>'vertex_capability_profile'
    ) ORDER BY a.platform, a.id)
    FROM accounts a
    WHERE a.deleted_at IS NULL
      AND a.status = 'active'
      AND a.platform IN ('anthropic','openai','gemini','antigravity','newapi','kiro','grok')
  ), '[]'::jsonb),
  'antigravity_groups', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', g.id,
      'name', g.name,
      'scopes', g.supported_model_scopes
    ) ORDER BY g.id)
    FROM groups g
    WHERE g.deleted_at IS NULL
      AND g.status = 'active'
      AND g.platform = 'antigravity'
  ), '[]'::jsonb),
  'runtime_setting', (
    SELECT value FROM settings WHERE key = 'tk_account_model_mapping_runtime'
  )
)::text;
""".strip()

SELF_CHECK_EXEMPT: dict[str, str] = {}


def iter_self_check_sql() -> list[tuple[str, str]]:
    """(label, rendered_sql) for the ops-sql-coverage real-Postgres self-check."""
    return [("ACCOUNT_MODEL_MAPPING_CHECK_SQL", ACCOUNT_MODEL_MAPPING_CHECK_SQL)]

_ssm_spec = importlib.util.spec_from_file_location(
    "tk_ssm_execution", REPO_ROOT / "ops" / "stage0" / "ssm_execution.py")
_SSM = importlib.util.module_from_spec(_ssm_spec)
_ssm_spec.loader.exec_module(_SSM)

_edge_ssm_spec = importlib.util.spec_from_file_location(
    "tk_edge_ssm_execution", REPO_ROOT / "ops" / "stage0" / "edge_ssm_execution.py")
_EDGE_SSM = importlib.util.module_from_spec(_edge_ssm_spec)
sys.modules.setdefault(_edge_ssm_spec.name, _EDGE_SSM)
_edge_ssm_spec.loader.exec_module(_EDGE_SSM)

_routing_spec = importlib.util.spec_from_file_location(
    "tk_edge_routing_matrix", REPO_ROOT / "ops" / "stage0" / "edge_routing_matrix.py")
_ROUTING = importlib.util.module_from_spec(_routing_spec)
sys.modules.setdefault(_routing_spec.name, _ROUTING)
_routing_spec.loader.exec_module(_ROUTING)


def fail(msg: str) -> NoReturn:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(2)


def _normalize_mapping(label: str, mapping) -> dict[str, str]:
    if not isinstance(mapping, dict) or not mapping:
        fail(f"{label}: model_mapping must be a non-empty object")
    out: dict[str, str] = {}
    for k, v in mapping.items():
        if not isinstance(k, str) or not isinstance(v, str):
            fail(f"{label}: keys and values must be strings")
        key = k.strip()
        val = v.strip()
        if not key or not val:
            fail(f"{label}: empty key/value is not allowed")
        out[key] = val
    return dict(sorted(out.items()))


def normalize_platform_key(platform: str) -> str:
    key = platform.strip().lower()
    if key == "claude":
        return "anthropic"
    if key == "xai":
        return "grok"
    return key


def normalize_runtime_doc(doc) -> dict:
    if not isinstance(doc, dict):
        fail("runtime document must be a JSON object")
    out: dict[str, dict] = {}
    platforms = doc.get("platforms", {})
    if platforms is None:
        platforms = {}
    if not isinstance(platforms, dict):
        fail("platforms must be an object")
    clean_platforms: dict[str, dict[str, str]] = {}
    for platform, mapping in platforms.items():
        if not isinstance(platform, str) or not platform.strip():
            fail("platforms contains an empty platform key")
        key = normalize_platform_key(platform)
        if key in clean_platforms:
            fail(f"platforms.{platform}: duplicate normalized platform key {key!r}")
        clean_platforms[key] = _normalize_mapping(f"platforms.{platform}", mapping)
    if clean_platforms:
        out["platforms"] = dict(sorted(clean_platforms.items()))

    channel_types = doc.get("newapi_channel_types", {})
    if channel_types is None:
        channel_types = {}
    if not isinstance(channel_types, dict):
        fail("newapi_channel_types must be an object")
    clean_ct: dict[str, dict[str, str]] = {}
    for raw_ct, mapping in channel_types.items():
        key = str(raw_ct).strip()
        if not key.isdigit() or int(key) <= 0:
            fail(f"invalid newapi channel_type {raw_ct!r}")
        clean_ct[key] = _normalize_mapping(f"newapi_channel_types.{key}", mapping)
    if clean_ct:
        out["newapi_channel_types"] = dict(sorted(clean_ct.items(), key=lambda kv: int(kv[0])))

    if not out:
        fail("runtime document has no replacement scopes")
    return out


def canonical_json(doc: dict) -> str:
    return _BUNDLE.canonical_json(doc)


def load_doc(path: Path) -> dict:
    try:
        return normalize_runtime_doc(json.loads(path.read_text()))
    except OSError as e:
        fail(f"cannot read {path}: {e}")
    except json.JSONDecodeError as e:
        fail(f"invalid JSON {path}: {e}")


def _decode_runtime_value(out: str) -> dict:
    out = out.strip()
    if not out:
        return {}
    raw = gzip.decompress(base64.b64decode(out)).decode("utf-8").strip()
    if not raw:
        return {}
    return json.loads(raw)


def read_runtime_blob(region: str, instance_id: str) -> dict:
    shell = (
        f"{PSQL} -c \"SELECT value FROM settings WHERE key='{SETTING_KEY}';\""
        " | gzip -c | base64 | tr -d '\\n'"
    )
    out = _ssm_run_shell_b64_region(
        region,
        instance_id,
        base64.b64encode(shell.encode("utf-8")).decode("ascii"),
        "account model_mapping runtime: read settings",
    )
    try:
        raw = _decode_runtime_value(out)
    except (OSError, ValueError) as e:
        fail(f"runtime settings blob decode failed: {e}")
    if not raw:
        return {}
    return normalize_runtime_doc(raw)


def _ssm_run_shell_b64_region(region: str, instance_id: str, shell_b64: str, comment: str) -> str:
    command = (
        "set -uo pipefail\n"
        f"echo {shell_b64} | base64 -d > /tmp/.tk_ssm_$$.sh\n"
        "bash /tmp/.tk_ssm_$$.sh; rc=$?\n"
        "rm -f /tmp/.tk_ssm_$$.sh\n"
        "exit $rc"
    )
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            [
                "aws", "ssm", "send-command", "--region", region,
                "--instance-ids", instance_id, "--document-name", "AWS-RunShellScript",
                "--comment", comment, "--parameters", params,
                "--query", "Command.CommandId", "--output", "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        raise RuntimeError(f"ssm send-command failed ({comment}): {e}") from e
    subprocess.run(
        ["aws", "ssm", "wait", "command-executed", "--region", region,
         "--command-id", cid, "--instance-id", instance_id],
        check=False,
    )
    try:
        inv = json.loads(subprocess.check_output(
            ["aws", "ssm", "get-command-invocation", "--region", region,
             "--command-id", cid, "--instance-id", instance_id, "--output", "json"],
            text=True,
        ))
    except (subprocess.CalledProcessError, ValueError) as e:
        raise RuntimeError(f"ssm get-command-invocation failed ({comment}): {e}") from e
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:1600]
        out = (inv.get("StandardOutputContent") or "").strip()[:600]
        raise RuntimeError(
            f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')} "
            f"({comment})\n  stderr: {err}\n  stdout: {out}"
        )
    return (inv.get("StandardOutputContent") or "").strip()


def _run_check_sql_json(region: str, instance_id: str, label: str) -> dict[str, Any]:
    sql_b64 = base64.b64encode(ACCOUNT_MODEL_MAPPING_CHECK_SQL.encode("utf-8")).decode("ascii")
    shell = (
        "set -euo pipefail\n"
        f"PSQL='{PSQL}'\n"
        f"echo {sql_b64} | base64 -d | $PSQL | gzip -c | base64 | tr -d '\\n'\n"
    )
    out = _ssm_run_shell_b64_region(
        region,
        instance_id,
        base64.b64encode(shell.encode("utf-8")).decode("ascii"),
        f"account model_mapping check {label}",
    )
    try:
        raw = gzip.decompress(base64.b64decode(out.strip())).decode("utf-8").strip()
        return json.loads(raw or "{}")
    except (OSError, ValueError, json.JSONDecodeError) as e:
        raise RuntimeError(f"{label}: failed to decode SQL JSON bundle: {e}") from e


def _normalize_instance_id(raw: str | None, label: str) -> str | None:
    if raw is None or str(raw).strip() == "":
        return None
    instance_id = str(raw).strip()
    if not re.match(r"^i-[0-9a-f]{17}$", instance_id):
        fail(f"{label}: invalid EC2 instance id {instance_id!r}")
    return instance_id


def _resolve_check_targets(
    skip_prod: bool,
    include_edges: bool = False,
    prod_instance_id: str | None = None,
) -> list[tuple[str, str, str]]:
    targets: list[tuple[str, str, str]] = []
    if not skip_prod:
        targets.append(("prod", _SSM.PROD_REGION, prod_instance_id or _SSM.resolve_prod_instance()))
    if skip_prod or include_edges:
        ec2_matrix = _ROUTING.load_matrix(REPO_ROOT / "deploy/aws/stage0/edge-targets.json")
        ls_targets = _ROUTING.load_lightsail_targets(REPO_ROOT)
        for eid in _ROUTING.iter_effective_deployable_edge_ids(ec2_matrix, ls_targets):
            ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, eid)
            targets.append((f"edge:{eid}", ident.region, ident.instance_id))
    return targets


def _resolve_single_edge_target(edge_id: str) -> tuple[str, str, str]:
    ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, edge_id)
    return (f"edge:{edge_id}", ident.region, ident.instance_id)


def _resolve_apply_targets(
    target: str,
    prod_instance_id: str | None = None,
) -> list[tuple[str, str, str]]:
    target = target.strip().lower()
    pinned_prod_instance = _normalize_instance_id(prod_instance_id, "--prod-instance-id")
    if target == "prod":
        return [("prod", _SSM.PROD_REGION, pinned_prod_instance or _SSM.resolve_prod_instance())]
    if pinned_prod_instance:
        fail("--prod-instance-id is only valid with --target prod")
    if target.startswith("edge:"):
        edge_id = target.split(":", 1)[1].strip()
        if not edge_id:
            fail("--target edge:<id> requires an edge id")
        return [_resolve_single_edge_target(edge_id)]
    if target in {"all", "all-deployable-and-prod"}:
        return _resolve_check_targets(skip_prod=False, include_edges=True)
    fail("--target must be prod, edge:<id>, or all-deployable-and-prod")


def _runtime_scope_summary(doc: dict) -> dict[str, list[str]]:
    return {
        "platforms": sorted((doc.get("platforms") or {}).keys()),
        "newapi_channel_types": sorted((doc.get("newapi_channel_types") or {}).keys(), key=lambda v: int(v)),
    }


_BUNDLE_PATH = DEFAULT_BUNDLE_PATH


def _set_bundle_path(raw: str | None) -> Path:
    global _BUNDLE_PATH
    if raw is None or not str(raw).strip():
        return _BUNDLE_PATH
    path = Path(str(raw)).expanduser().resolve()
    if not path.is_file():
        fail(f"--bundle {path}: file not found")
    _BUNDLE_PATH = path
    return _BUNDLE_PATH


def _runtime_cache_key(raw: Any) -> str:
    if raw is None or str(raw).strip() == "":
        return ""
    try:
        doc = normalize_runtime_doc(json.loads(str(raw)))
    except SystemExit as e:
        raise ValueError(f"invalid {SETTING_KEY} document (exit {e.code})") from e
    return canonical_json(doc)


def _load_bundle() -> dict[str, Any]:
    return _BUNDLE.load_bundle(_BUNDLE_PATH)


def _load_effective_floor(runtime_raw: Any) -> dict[str, Any]:
    key = _runtime_cache_key(runtime_raw)
    floor = copy.deepcopy(_load_bundle()["account_model_mapping"])
    if key:
        runtime = json.loads(key)
        for platform, mapping in (runtime.get("platforms") or {}).items():
            floor["platforms"][platform] = mapping
        for channel_type, mapping in (runtime.get("newapi_channel_types") or {}).items():
            floor["newapi_channel_types"][channel_type] = mapping
    return floor


def _model_mapping(row: dict[str, Any]) -> tuple[dict[str, str], str | None]:
    raw = row.get("model_mapping")
    if not isinstance(raw, dict) or not raw:
        return {}, "empty/missing model_mapping"
    out: dict[str, str] = {}
    for k, v in raw.items():
        if not isinstance(k, str) or not isinstance(v, str):
            return {}, "model_mapping contains non-string key/value"
        key = k.strip()
        val = v.strip()
        if not key or not val:
            return {}, "model_mapping contains empty key/value"
        out[key] = val
    if not out:
        return {}, "model_mapping contains no string entries"
    return out, None


def _is_kiro_scope(row: dict[str, Any]) -> bool:
    platform = str(row.get("platform") or "").strip().lower()
    if platform == "kiro":
        return True
    if platform != "anthropic" or str(row.get("type") or "") != "apikey":
        return False
    if str(row.get("mirror_platform") or "").strip().lower() == "kiro":
        return True
    name = str(row.get("name") or "").strip().lower()
    base_url = str(row.get("base_url") or "").strip().lower().rstrip("/")
    return (
        name.startswith("kiro-")
        and base_url.startswith("https://api-")
        and base_url.endswith(".tokenkey.dev")
    )


def _is_openai_ainzy_relay(row: dict[str, Any]) -> bool:
    if str(row.get("platform") or "").strip().lower() != "openai":
        return False
    if str(row.get("type") or "") != "apikey":
        return False
    base = str(row.get("base_url") or "").strip().lower().rstrip("/")
    return base in {"https://api.ainzy.net/v1", "https://api.ainzy.net"}


def _is_openai_tokensea_relay(row: dict[str, Any]) -> bool:
    if str(row.get("platform") or "").strip().lower() != "openai":
        return False
    if str(row.get("type") or "") != "apikey":
        return False
    base = str(row.get("base_url") or "").strip().lower().rstrip("/")
    return base == "https://agent.tokensea.ai"


def _is_openai_cloudwise_relay(row: dict[str, Any]) -> bool:
    if str(row.get("platform") or "").strip().lower() != "openai":
        return False
    if str(row.get("type") or "") != "apikey":
        return False
    base = str(row.get("base_url") or "").strip().lower().rstrip("/")
    return base in {"https://api.cloudwise.ai/api", "https://api-us.cloudwise.ai/api"}


def _is_anthropic_tokensea_relay(row: dict[str, Any]) -> bool:
    if str(row.get("platform") or "").strip().lower() != "anthropic":
        return False
    if str(row.get("type") or "") != "apikey":
        return False
    base = str(row.get("base_url") or "").strip().lower().rstrip("/")
    return base == "https://agent.tokensea.ai"


def _account_scope(row: dict[str, Any]) -> str:
    if _is_openai_ainzy_relay(row):
        return "openai_ainzy_relay"
    if _is_openai_tokensea_relay(row):
        return "openai_tokensea_relay"
    if _is_openai_cloudwise_relay(row):
        return "openai_cloudwise_relay"
    if _is_anthropic_tokensea_relay(row):
        return "anthropic_tokensea_relay"
    if _is_kiro_scope(row):
        return "kiro"
    platform = str(row.get("platform") or "").strip().lower()
    if platform == "anthropic" and str(row.get("type") or "") == "bedrock":
        return "bedrock"
    return platform


def _mapping_policy_violations_for_scope(
    scope: str,
    mapping: dict[str, str],
    floor: dict[str, Any],
) -> list[str]:
    reasons: list[str] = []

    forbidden_by_scope = floor.get("forbidden_model_mapping_keys") or {}
    forbidden = set(forbidden_by_scope.get(scope) or [])
    forbidden_keys = sorted(k for k in mapping if k in forbidden)
    if forbidden_keys:
        reasons.append("contains forbidden model_mapping keys from bundle: " + ", ".join(forbidden_keys))

    forbidden_prefix_by_scope = floor.get("forbidden_model_mapping_prefixes") or {}
    prefixes = [str(p) for p in (forbidden_prefix_by_scope.get(scope) or []) if str(p)]
    prefixed = sorted(k for k in mapping if any(k.startswith(p) for p in prefixes))
    if prefixed:
        reasons.append("contains forbidden model_mapping prefixes from bundle: " + ", ".join(prefixed))

    return reasons


def _forbidden_mapping_entries(
    scope: str,
    mapping: dict[str, str],
    floor: dict[str, Any],
) -> list[str]:
    if scope.startswith("newapi_channel_type:") or scope.startswith("newapi_vertex_profile:"):
        scope = "newapi"
    forbidden_by_scope = floor.get("forbidden_model_mapping_keys") or {}
    forbidden = set(forbidden_by_scope.get(scope) or [])
    forbidden_prefix_by_scope = floor.get("forbidden_model_mapping_prefixes") or {}
    prefixes = [str(p) for p in (forbidden_prefix_by_scope.get(scope) or []) if str(p)]
    return sorted(
        key for key in mapping
        if key in forbidden or any(key.startswith(prefix) for prefix in prefixes)
    )


def _mapping_policy_violations(row: dict[str, Any], floor: dict[str, Any]) -> list[str]:
    scope = _account_scope(row)
    mm, err = _model_mapping(row)
    if err:
        return [f"{err} (scope={scope})"]
    return _mapping_policy_violations_for_scope(scope, mm, floor)


def _desired_mapping_for_account(
    row: dict[str, Any],
    floor: dict[str, Any],
) -> tuple[dict[str, str] | None, str]:
    actual_platform = str(row.get("platform") or "").strip().lower()
    try:
        actual_channel_type = int(row.get("channel_type") or 0)
    except (TypeError, ValueError):
        actual_channel_type = 0
    actual_base_url = _BUNDLE.normalize_account_override_base_url(row.get("base_url"))
    for override in floor.get("account_overrides") or []:
        if not isinstance(override, dict):
            continue
        expected_platform = str(override.get("platform") or "").strip().lower()
        expected_channel_type = override.get("channel_type")
        if (
            actual_platform != expected_platform
            or (expected_channel_type is not None and actual_channel_type != expected_channel_type)
            or (
                actual_base_url
                != _BUNDLE.normalize_account_override_base_url(override.get("base_url"))
            )
        ):
            continue
        mapping = override.get("model_mapping")
        if isinstance(mapping, dict):
            return mapping, _BUNDLE.account_override_scope(override)

    scope = _account_scope(row)
    if scope == "newapi":
        ct = str(row.get("channel_type") or "").strip()
        if ct == "41":
            profile = str(row.get("vertex_capability_profile") or "").strip().lower()
            profiles = floor.get("vertex_capability_profiles") or {}
            mapping = profiles.get(profile) if profile else None
            if isinstance(mapping, dict):
                return mapping, f"newapi_vertex_profile:{profile}"
        mapping = (floor.get("newapi_channel_types") or {}).get(ct)
        return mapping if isinstance(mapping, dict) else None, f"newapi_channel_type:{ct or '0'}"
    mapping = (floor.get("platforms") or {}).get(scope)
    return mapping if isinstance(mapping, dict) else None, scope


def _mapping_diff(
    policy_scope: str,
    got: dict[str, str],
    want: dict[str, str],
    floor: dict[str, Any],
) -> dict[str, Any]:
    missing = sorted(k for k in want if k not in got)
    bad = sorted(k for k in want if k in got and got[k] != want[k])
    forbidden = _forbidden_mapping_entries(policy_scope, got, floor)
    return {
        "missing_keys": missing,
        "forbidden_keys": forbidden,
        "compatible_extra_keys": sorted(k for k in got if k not in want and k not in forbidden),
        "bad_targets": [
            {"key": k, "got": got[k], "want": want[k]}
            for k in bad
        ],
        "current_count": len(got),
        "desired_count": len(want),
    }


def _has_mapping_diff(diff: dict[str, Any]) -> bool:
    return bool(diff["missing_keys"] or diff["forbidden_keys"] or diff["bad_targets"])


def _short_list(values: list[Any], limit: int = 8) -> str:
    if len(values) <= limit:
        return ", ".join(str(v) for v in values)
    return ", ".join(str(v) for v in values[:limit]) + f", ... (+{len(values) - limit})"


def _format_mapping_diff_reason(scope: str, diff: dict[str, Any]) -> str:
    parts = [f"model_mapping differs from SSOT (scope={scope})"]
    if diff["missing_keys"]:
        parts.append("missing: " + _short_list(diff["missing_keys"]))
    if diff["forbidden_keys"]:
        parts.append("forbidden: " + _short_list(diff["forbidden_keys"]))
    if diff["bad_targets"]:
        bad = [f"{b['key']}->{b['got']!r} want {b['want']!r}" for b in diff["bad_targets"]]
        parts.append("bad_targets: " + _short_list(bad))
    parts.append(f"count current={diff['current_count']} desired={diff['desired_count']}")
    if diff["compatible_extra_keys"]:
        parts.append("preserved_extras: " + _short_list(diff["compatible_extra_keys"]))
    return "; ".join(parts)


def _vertex_profile_issue(row: dict[str, Any], floor: dict[str, Any]) -> str | None:
    if str(row.get("platform") or "").strip().lower() != "newapi":
        return None
    try:
        channel_type = int(row.get("channel_type") or 0)
    except (TypeError, ValueError):
        return None
    if channel_type != 41:
        return None
    profile = str(row.get("vertex_capability_profile") or "").strip().lower()
    profiles = floor.get("vertex_capability_profiles") or {}
    if not profile:
        return "missing vertex_capability_profile; fail-safe shared channel_type 41 floor applies"
    if profile not in profiles:
        return (
            f"unknown vertex_capability_profile {profile!r}; "
            "fail-safe shared channel_type 41 floor applies"
        )
    return None


def _account_plan(
    row: dict[str, Any],
    floor: dict[str, Any],
) -> dict[str, Any] | None:
    want, scope = _desired_mapping_for_account(row, floor)
    if not want:
        return None
    got, err = _model_mapping(row)
    if err:
        got = {}
        diff = {
            "missing_keys": sorted(want),
            "forbidden_keys": [],
            "compatible_extra_keys": [],
            "bad_targets": [],
            "current_count": 0,
            "desired_count": len(want),
        }
        reason = f"{err}; will replace with SSOT (scope={scope}, desired_count={len(want)})"
    else:
        diff = _mapping_diff(_account_scope(row), got, want, floor)
        if not _has_mapping_diff(diff):
            return None
        reason = _format_mapping_diff_reason(scope, diff)
    reconciled = {
        key: value
        for key, value in got.items()
        if key not in set(diff["forbidden_keys"])
    }
    reconciled.update(want)
    return {
        "kind": "account",
        "id": row.get("id"),
        "name": row.get("name"),
        "platform": row.get("platform"),
        "type": row.get("type"),
        "scope": scope,
        "reason": reason,
        "diff": diff,
        "desired_model_mapping": dict(sorted(reconciled.items())),
    }


def _desired_antigravity_group_scopes(floor: dict[str, Any]) -> list[str]:
    scopes = floor.get("antigravity_group_scopes")
    if not isinstance(scopes, list) or not scopes:
        raise RuntimeError("model surface bundle omitted antigravity_group_scopes")
    out: list[str] = []
    seen: set[str] = set()
    for raw in scopes:
        scope = str(raw).strip()
        if scope and scope not in seen:
            out.append(scope)
            seen.add(scope)
    if not out:
        raise RuntimeError("model surface bundle emitted empty antigravity_group_scopes")
    return out


def _group_violation(row: dict[str, Any], floor: dict[str, Any]) -> str | None:
    desired_scopes = set(_desired_antigravity_group_scopes(floor))
    scopes = row.get("scopes")
    if not scopes or not isinstance(scopes, list):
        return "empty/missing supported_model_scopes"
    got = {str(s).strip() for s in scopes}
    if got == desired_scopes:
        return None
    parts: list[str] = []
    extra = sorted(got - desired_scopes)
    missing = sorted(desired_scopes - got)
    if extra:
        parts.append("unexpected: " + ", ".join(extra))
    if missing:
        parts.append("missing: " + ", ".join(missing))
    return "scopes not canonical (" + "; ".join(parts) + ")"


def _runtime_policy_conflict(doc: dict[str, Any], floor: dict[str, Any]) -> str | None:
    conflicts: list[str] = []
    effective_platforms = floor.get("platforms") or {}
    for platform in (doc.get("platforms") or {}):
        desired = effective_platforms.get(platform)
        if not isinstance(desired, dict):
            continue
        reasons = _mapping_policy_violations_for_scope(platform, desired, floor)
        if reasons:
            conflicts.append(f"platforms.{platform}: " + "; ".join(reasons))

    effective_channel_types = floor.get("newapi_channel_types") or {}
    for channel_type in (doc.get("newapi_channel_types") or {}):
        if channel_type == "41":
            conflicts.append(
                "newapi_channel_types.41: ch41 is owned by the shared/profile capability contract; "
                "runtime channel replacement is not allowed"
            )
            continue
        desired = effective_channel_types.get(channel_type)
        if not isinstance(desired, dict):
            continue
        reasons = _mapping_policy_violations_for_scope("newapi", desired, floor)
        if reasons:
            conflicts.append(f"newapi_channel_types.{channel_type}: " + "; ".join(reasons))

    if not conflicts:
        return None
    return f"policy conflict in {SETTING_KEY}: " + " | ".join(conflicts)


def _runtime_setting_violation(raw: Any) -> str | None:
    if raw is None or str(raw).strip() == "":
        return None
    try:
        doc = json.loads(str(raw))
    except json.JSONDecodeError as e:
        return f"invalid JSON in {SETTING_KEY}: {e}"
    stderr = io.StringIO()
    with contextlib.redirect_stderr(stderr):
        try:
            clean = normalize_runtime_doc(doc)
        except SystemExit:
            return (stderr.getvalue() or f"invalid {SETTING_KEY} document").strip()
    floor = _load_effective_floor(canonical_json(clean))
    return _runtime_policy_conflict(clean, floor)


def _check_target(
    label: str,
    region: str,
    instance_id: str,
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], bool]:
    bundle = _run_check_sql_json(region, instance_id, label)
    violations: list[dict[str, Any]] = []
    runtime_raw = bundle.get("runtime_setting")
    runtime_present = runtime_raw is not None and str(runtime_raw).strip() != ""
    runtime_reason = _runtime_setting_violation(runtime_raw)
    if runtime_reason:
        violations.append({
            "target": label,
            "kind": "runtime_setting",
            "id": SETTING_KEY,
            "name": SETTING_KEY,
            "reason": runtime_reason,
        })
        return violations, [], runtime_present
    floor = _load_effective_floor(runtime_raw)
    for row in bundle.get("accounts") or []:
        plan = _account_plan(row, floor)
        if plan:
            plan["target"] = label
            violations.append(plan)
        profile_issue = _vertex_profile_issue(row, floor)
        if profile_issue:
            violations.append({
                "target": label,
                "kind": "vertex_capability_profile",
                "id": row.get("id"),
                "name": row.get("name"),
                "platform": row.get("platform"),
                "scope": "newapi_channel_type:41",
                "reason": profile_issue,
            })
    for row in bundle.get("antigravity_groups") or []:
        reason = _group_violation(row, floor)
        if reason:
            violations.append({
                "target": label,
                "kind": "group",
                "id": row.get("id"),
                "name": row.get("name"),
                "platform": "antigravity",
                "reason": reason,
            })
    return violations, [], runtime_present


def _canonicalize_observed_mapping(row: dict[str, Any]) -> Any:
    """Return observed model_mapping for CAS comparison.

    - absent key or JSON null -> None
    - empty dict {} -> {} (preserved for CAS)
    - non-empty dict -> sorted keys dict
    - other JSON values (string, array, etc.) -> preserved as-is
    """
    if "model_mapping" not in row:
        return None
    raw = row["model_mapping"]
    if raw is None:
        return None
    if isinstance(raw, dict):
        if not raw:
            return {}
        return dict(sorted((str(k), str(v)) for k, v in raw.items()))
    # Preserve non-dict JSON values (string, array, number) for accurate CAS
    return raw


def _collect_apply_plan(
    label: str,
    region: str,
    instance_id: str,
    activation_floor_sha256: str | None = None,
    account_ids: list[int] | None = None,
) -> dict[str, Any]:
    bundle = _run_check_sql_json(region, instance_id, label)
    runtime_raw = bundle.get("runtime_setting")
    if activation_floor_sha256 and runtime_raw is not None and str(runtime_raw).strip():
        raise RuntimeError(
            f"activation bundle {activation_floor_sha256} is shadowed by {SETTING_KEY} on {label}"
        )
    runtime_reason = _runtime_setting_violation(runtime_raw)
    if runtime_reason:
        raise RuntimeError(runtime_reason)
    floor = _load_effective_floor(runtime_raw)
    floor_sha = hashlib.sha256(canonical_json(floor).encode("utf-8")).hexdigest()

    all_rows = bundle.get("accounts") or []

    # When targeted by account_ids, select rows before planning.
    if account_ids is not None:
        rows_by_id = {int(r.get("id") or 0): r for r in all_rows}
        found_ids = sorted(aid for aid in account_ids if aid in rows_by_id)
        missing_ids = sorted(aid for aid in account_ids if aid not in rows_by_id)
        selected_rows = [rows_by_id[aid] for aid in found_ids]
    else:
        selected_rows = all_rows
        found_ids = None
        missing_ids = None

    account_changes: list[dict[str, Any]] = []
    already_compliant_ids: list[int] = []
    skipped_ids: list[int] = []
    for row in selected_rows:
        plan = _account_plan(row, floor)
        if plan and plan.get("desired_model_mapping"):
            plan["target"] = label
            plan["observed_model_mapping"] = _canonicalize_observed_mapping(row)
            account_changes.append(plan)
        elif account_ids is not None:
            # Distinguish skipped (no desired mapping / no scope match) from already-compliant.
            want, _scope = _desired_mapping_for_account(row, floor)
            if want:
                already_compliant_ids.append(int(row.get("id") or 0))
            else:
                skipped_ids.append(int(row.get("id") or 0))

    # Targeted mode: group_changes always empty.
    group_changes: list[dict[str, Any]] = []
    if account_ids is None:
        desired_scopes = _desired_antigravity_group_scopes(floor)
        for row in bundle.get("antigravity_groups") or []:
            reason = _group_violation(row, floor)
            if not reason:
                continue
            group_changes.append({
                "target": label,
                "kind": "group",
                "id": row.get("id"),
                "name": row.get("name"),
                "platform": "antigravity",
                "reason": reason,
                "desired_supported_model_scopes": desired_scopes,
            })

    result = {
        "target": label,
        "region": region,
        "instance_id": instance_id,
        "floor_sha256": floor_sha,
        "account_changes": sorted(account_changes, key=lambda p: int(p.get("id") or 0)),
        "group_changes": sorted(group_changes, key=lambda p: int(p.get("id") or 0)),
    }
    if activation_floor_sha256:
        result["activation_floor_sha256"] = activation_floor_sha256
    if account_ids is not None:
        result["targeted"] = True
        result["requested_ids"] = account_ids
        result["found_ids"] = found_ids
        result["missing_ids"] = missing_ids
        result["already_compliant_ids"] = sorted(already_compliant_ids)
        result["skipped_ids"] = sorted(skipped_ids)
        result["changed_ids"] = sorted(int(c.get("id") or 0) for c in account_changes)
        result["counts"] = {
            "requested": len(account_ids),
            "found": len(found_ids),
            "missing": len(missing_ids),
            "already_compliant": len(already_compliant_ids),
            "changed": len(account_changes),
            "skipped": len(skipped_ids),
        }
    return result


def _parse_account_ids(raw: str) -> list[int]:
    """Parse comma-separated positive integer account IDs, dedup and sort."""
    if not raw or not raw.strip():
        raise ValueError("--account-ids must not be empty")
    parts = [p.strip() for p in raw.split(",") if p.strip()]
    ids: set[int] = set()
    for part in parts:
        try:
            val = int(part)
        except ValueError:
            raise ValueError(f"--account-ids: {part!r} is not an integer")
        if val <= 0:
            raise ValueError(f"--account-ids: {val} is not a positive integer")
        ids.add(val)
    if not ids:
        raise ValueError("--account-ids must contain at least one positive integer")
    return sorted(ids)


def _ids_sql(ids: list[int]) -> str:
    clean = sorted({int(i) for i in ids if int(i) > 0})
    if not clean:
        raise ValueError("empty id list")
    return ",".join(str(i) for i in clean)


def _json_b64(doc: Any) -> str:
    raw = json.dumps(doc, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return base64.b64encode(raw).decode("ascii")


def _compute_plan_sha256(plan: dict[str, Any]) -> str:
    """Compute a deterministic digest over the targeted plan.

    Covers: target, region, instance_id, floor_sha256 (always present from bundle),
    requested/found/missing/already_compliant IDs, per-account observed+desired+diff,
    and empty group write-set.
    """
    # floor_sha256: prefer activation_floor_sha256 if present, fall back to floor_sha256
    floor_sha = plan.get("activation_floor_sha256") or plan.get("floor_sha256") or ""
    digest_input: dict[str, Any] = {
        "target": plan.get("target"),
        "region": plan.get("region"),
        "instance_id": plan.get("instance_id"),
        "floor_sha256": floor_sha,
        "requested_ids": plan.get("requested_ids") or [],
        "found_ids": plan.get("found_ids") or [],
        "missing_ids": plan.get("missing_ids") or [],
        "already_compliant_ids": plan.get("already_compliant_ids") or [],
        "account_changes": [
            {
                "id": int(c.get("id") or 0),
                "observed_model_mapping": c.get("observed_model_mapping"),
                "desired_model_mapping": c.get("desired_model_mapping"),
                "diff": c.get("diff"),
            }
            for c in sorted(plan.get("account_changes") or [], key=lambda c: int(c.get("id") or 0))
        ],
        "group_changes": [],
    }
    raw = canonical_json(digest_input)
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


def _render_targeted_apply_sql(plan: dict[str, Any]) -> str:
    """Render per-account CAS updates within a transaction for targeted apply.

    Each account row uses COALESCE(credentials->'model_mapping', 'null'::jsonb) = observed
    as a CAS guard. PL/pgSQL checks ROW_COUNT=1 per row. One merged outbox event.
    Rollback on any miss.
    """
    changes = plan.get("account_changes") or []
    if not changes:
        return ""
    lines = ["BEGIN;", "SET LOCAL statement_timeout = '30s';"]
    changed_ids: list[int] = []
    for change in sorted(changes, key=lambda c: int(c.get("id") or 0)):
        account_id = int(change["id"])
        changed_ids.append(account_id)
        desired = change.get("desired_model_mapping")
        observed = change.get("observed_model_mapping")
        # Encode desired model_mapping as the credential merge payload
        payload_b64 = _json_b64({"model_mapping": desired})
        # CAS: observed model_mapping (None canonicalized to JSON null literal)
        observed_json = canonical_json(observed) if observed is not None else "null"
        observed_b64 = base64.b64encode(observed_json.encode("utf-8")).decode("ascii")
        lines.extend([
            f"DO $tk_targeted_{account_id}$ DECLARE changed bigint; BEGIN ",
            "UPDATE accounts SET credentials = COALESCE(credentials, '{}'::jsonb) "
            f"|| convert_from(decode('{payload_b64}', 'base64'), 'UTF8')::jsonb, "
            "updated_at = NOW() "
            f"WHERE id = {account_id} AND deleted_at IS NULL AND status = 'active' "
            f"AND COALESCE(credentials->'model_mapping','null'::jsonb) = "
            f"convert_from(decode('{observed_b64}', 'base64'), 'UTF8')::jsonb; ",
            "GET DIAGNOSTICS changed = ROW_COUNT; "
            f"IF changed <> 1 THEN RAISE EXCEPTION 'account {account_id} CAS failed: observed model_mapping changed'; "
            f"END IF; END $tk_targeted_{account_id}$;",
        ])
    # One merged outbox event for all changed accounts. Encode the payload like
    # account updates so future payload fields cannot introduce SQL quoting hazards.
    outbox_payload_b64 = _json_b64({"account_ids": sorted(changed_ids)})
    lines.append(
        "INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload) "
        "VALUES ('account_bulk_changed', NULL, NULL, "
        f"convert_from(decode('{outbox_payload_b64}', 'base64'), 'UTF8')::jsonb);"
    )
    lines.extend([
        "COMMIT;",
        "SELECT 'TARGETED_APPLY_OK' AS status;",
    ])
    return "\n".join(lines) + "\n"


def _apply_targeted_plan_remote(plan: dict[str, Any]) -> str:
    sql = _render_targeted_apply_sql(plan)
    sql_b64 = base64.b64encode(sql.encode("utf-8")).decode("ascii")
    shell = (
        "set -euo pipefail\n"
        f"PSQL='{PSQL}'\n"
        f"echo {sql_b64} | base64 -d | $PSQL\n"
        "echo TARGETED_APPLY_OK\n"
    )
    return _ssm_run_shell_b64_region(
        plan["region"],
        plan["instance_id"],
        base64.b64encode(shell.encode("utf-8")).decode("ascii"),
        f"account model_mapping targeted apply {plan['target']}",
    )


def _load_vertex_profile_assignments(path: Path, floor: dict[str, Any]) -> list[dict[str, Any]]:
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except OSError as e:
        fail(f"cannot read {path}: {e}")
    except json.JSONDecodeError as e:
        fail(f"invalid JSON {path}: {e}")
    if not isinstance(raw, dict) or set(raw) != {"assignments"}:
        fail("profile assignment file must contain only an assignments array")
    assignments = raw.get("assignments")
    if not isinstance(assignments, list) or not assignments:
        fail("profile assignment file assignments must be a non-empty array")
    profiles = floor.get("vertex_capability_profiles") or {}
    out: list[dict[str, Any]] = []
    seen: set[int] = set()
    for index, assignment in enumerate(assignments):
        label = f"assignments[{index}]"
        if not isinstance(assignment, dict):
            fail(f"{label} must be an object")
        expected_fields = {"id", "name", "platform", "channel_type", "current_profile", "profile"}
        if set(assignment) != expected_fields:
            fail(f"{label} must contain exactly: " + ", ".join(sorted(expected_fields)))
        try:
            account_id = int(assignment.get("id"))
            channel_type = int(assignment.get("channel_type"))
        except (TypeError, ValueError):
            fail(f"{label}: id and channel_type must be integers")
        if account_id <= 0 or account_id in seen:
            fail(f"{label}: id must be unique and positive")
        seen.add(account_id)
        name = str(assignment.get("name") or "").strip()
        platform = str(assignment.get("platform") or "").strip().lower()
        current_profile = str(assignment.get("current_profile") or "").strip().lower()
        profile = str(assignment.get("profile") or "").strip().lower()
        if not name:
            fail(f"{label}: name must be non-empty")
        if platform != "newapi" or channel_type != 41:
            fail(f"{label}: selector must be platform=newapi and channel_type=41")
        if profile not in profiles:
            fail(f"{label}: unknown Vertex capability profile {profile!r}")
        if current_profile and current_profile not in profiles:
            fail(f"{label}: current_profile {current_profile!r} is not a known profile")
        out.append({
            "id": account_id,
            "name": name,
            "platform": platform,
            "channel_type": channel_type,
            "current_profile": current_profile,
            "profile": profile,
        })
    return sorted(out, key=lambda row: row["id"])


def _collect_vertex_profile_assignment_plan(
    label: str,
    region: str,
    instance_id: str,
    assignments: list[dict[str, Any]],
) -> dict[str, Any]:
    live = _run_check_sql_json(region, instance_id, label)
    rows = {int(row.get("id") or 0): row for row in (live.get("accounts") or [])}
    changes: list[dict[str, Any]] = []
    for assignment in assignments:
        account_id = assignment["id"]
        row = rows.get(account_id)
        if row is None:
            raise RuntimeError(f"account {account_id}: not found in active managed prod inventory")
        actual_name = str(row.get("name") or "")
        actual_platform = str(row.get("platform") or "").strip().lower()
        try:
            actual_channel_type = int(row.get("channel_type") or 0)
        except (TypeError, ValueError):
            actual_channel_type = 0
        actual_profile = str(row.get(VERTEX_PROFILE_CREDENTIAL_KEY) or "").strip().lower()
        expected_selector = (
            assignment["name"], assignment["platform"], assignment["channel_type"],
            assignment["current_profile"],
        )
        actual_selector = (actual_name, actual_platform, actual_channel_type, actual_profile)
        if actual_selector != expected_selector:
            raise RuntimeError(
                f"account {account_id}: selector drift; expected "
                f"name={assignment['name']!r} platform={assignment['platform']} "
                f"channel_type={assignment['channel_type']} current_profile={assignment['current_profile']!r}; "
                f"got name={actual_name!r} platform={actual_platform} "
                f"channel_type={actual_channel_type} current_profile={actual_profile!r}"
            )
        if actual_profile == assignment["profile"]:
            continue
        changes.append({**assignment, "target": label})
    return {
        "target": label,
        "region": region,
        "instance_id": instance_id,
        "changes": changes,
    }


def _render_vertex_profile_assignment_sql(plan: dict[str, Any]) -> str:
    changes = plan.get("changes") or []
    if not changes:
        return ""
    lines = ["BEGIN;", "SET LOCAL statement_timeout = '30s';"]
    changed_ids: list[int] = []
    for change in changes:
        payload_b64 = _json_b64({VERTEX_PROFILE_CREDENTIAL_KEY: change["profile"]})
        expected_name_b64 = base64.b64encode(change["name"].encode("utf-8")).decode("ascii")
        current_profile_b64 = base64.b64encode(change["current_profile"].encode("utf-8")).decode("ascii")
        account_id = int(change["id"])
        changed_ids.append(account_id)
        lines.extend([
            "DO $tk_vertex_profile$ DECLARE changed bigint; BEGIN ",
            "UPDATE accounts SET credentials = COALESCE(credentials, '{}'::jsonb) "
            f"|| convert_from(decode('{payload_b64}', 'base64'), 'UTF8')::jsonb, updated_at = NOW() "
            f"WHERE id = {account_id} AND deleted_at IS NULL AND status = 'active' "
            "AND platform = 'newapi' AND channel_type = 41 "
            f"AND name = convert_from(decode('{expected_name_b64}', 'base64'), 'UTF8') "
            f"AND COALESCE(credentials->>'{VERTEX_PROFILE_CREDENTIAL_KEY}', '') = "
            f"convert_from(decode('{current_profile_b64}', 'base64'), 'UTF8'); ",
            "GET DIAGNOSTICS changed = ROW_COUNT; "
            f"IF changed <> 1 THEN RAISE EXCEPTION 'account {account_id} selector changed before profile assignment'; "
            "END IF; END $tk_vertex_profile$;",
        ])
    payload = json.dumps({"account_ids": sorted(changed_ids)}, separators=(",", ":"))
    lines.extend([
        "INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload) "
        f"VALUES ('account_bulk_changed', NULL, NULL, '{payload}'::jsonb);",
        "COMMIT;",
        "SELECT 'PROFILE_ASSIGN_OK' AS status;",
    ])
    return "\n".join(lines) + "\n"


def _apply_vertex_profile_assignment_plan(plan: dict[str, Any]) -> str:
    sql = _render_vertex_profile_assignment_sql(plan)
    if not sql:
        return "PROFILE_ASSIGN_OK"
    shell = (
        "set -euo pipefail\n"
        f"PSQL='{PSQL}'\n"
        f"echo {base64.b64encode(sql.encode('utf-8')).decode('ascii')} | base64 -d | $PSQL\n"
        "echo PROFILE_ASSIGN_OK\n"
    )
    return _ssm_run_shell_b64_region(
        plan["region"], plan["instance_id"],
        base64.b64encode(shell.encode("utf-8")).decode("ascii"),
        "assign Vertex capability profiles",
    )


def cmd_assign_vertex_profiles(args) -> int:
    _set_bundle_path(getattr(args, "bundle", None))
    floor = _load_bundle()["account_model_mapping"]
    assignments = _load_vertex_profile_assignments(args.file, floor)
    if not args.dry_run and args.confirm != VERTEX_PROFILE_ASSIGN_CONFIRM:
        fail(f"assign-vertex-profiles requires --confirm {VERTEX_PROFILE_ASSIGN_CONFIRM}")
    target = _resolve_apply_targets("prod", prod_instance_id=getattr(args, "prod_instance_id", None))[0]
    plan = _collect_vertex_profile_assignment_plan(*target, assignments)
    report = {
        "bundle": str(_BUNDLE_PATH),
        "target": "prod",
        "assignment_count": len(assignments),
        "change_count": len(plan["changes"]),
        "changes": plan["changes"],
    }
    if args.dry_run or not plan["changes"]:
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 0
    out = _apply_vertex_profile_assignment_plan(plan)
    if "PROFILE_ASSIGN_OK" not in out:
        raise RuntimeError("remote SQL did not report PROFILE_ASSIGN_OK")
    verified = _collect_vertex_profile_assignment_plan(*target, [
        {**change, "current_profile": change["profile"]} for change in plan["changes"]
    ])
    if verified["changes"]:
        raise RuntimeError("post-assignment read-back still reports profile changes")
    print(json.dumps({**report, "applied": True, "verified": True}, ensure_ascii=False, indent=2))
    return 0


def _render_apply_sql(plan: dict[str, Any]) -> str:
    lines = [
        "BEGIN;",
        "SET LOCAL statement_timeout = '30s';",
    ]
    if plan.get("activation_floor_sha256"):
        lines.extend([
            "LOCK TABLE settings IN SHARE ROW EXCLUSIVE MODE;",
            "DO $tk_model_activation$ BEGIN "
            f"IF EXISTS (SELECT 1 FROM settings WHERE key = '{SETTING_KEY}') THEN "
            f"RAISE EXCEPTION '{SETTING_KEY} appeared before activation write'; "
            "END IF; END $tk_model_activation$;",
        ])

    by_mapping: dict[str, dict[str, Any]] = {}
    for change in plan.get("account_changes") or []:
        mapping = change.get("desired_model_mapping")
        if not isinstance(mapping, dict) or not mapping:
            continue
        sig = canonical_json({"model_mapping": mapping})
        slot = by_mapping.setdefault(sig, {"ids": [], "payload_b64": _json_b64({"model_mapping": mapping})})
        slot["ids"].append(int(change["id"]))

    for slot in by_mapping.values():
        ids = _ids_sql(slot["ids"])
        payload_b64 = slot["payload_b64"]
        outbox_b64 = _json_b64({"account_ids": sorted({int(i) for i in slot["ids"]})})
        lines.append(
            "UPDATE accounts "
            "SET credentials = COALESCE(credentials, '{}'::jsonb) "
            f"|| convert_from(decode('{payload_b64}', 'base64'), 'UTF8')::jsonb, "
            "updated_at = NOW() "
            f"WHERE id = ANY(ARRAY[{ids}]::bigint[]) AND deleted_at IS NULL;"
        )
        lines.append(
            "INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload) "
            "VALUES ('account_bulk_changed', NULL, NULL, "
            f"convert_from(decode('{outbox_b64}', 'base64'), 'UTF8')::jsonb);"
        )

    group_changes = plan.get("group_changes") or []
    if group_changes:
        group_ids = _ids_sql([int(g["id"]) for g in group_changes])
        scopes = group_changes[0].get("desired_supported_model_scopes")
        if not scopes:
            raise ValueError("group change missing desired_supported_model_scopes from bundle")
        scopes_b64 = _json_b64(scopes)
        lines.append(
            "UPDATE groups "
            f"SET supported_model_scopes = convert_from(decode('{scopes_b64}', 'base64'), 'UTF8')::jsonb, "
            "updated_at = NOW() "
            f"WHERE id = ANY(ARRAY[{group_ids}]::bigint[]) AND deleted_at IS NULL;"
        )
        lines.append(
            "INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload) "
            f"SELECT 'group_changed', NULL, id, NULL FROM unnest(ARRAY[{group_ids}]::bigint[]) AS id;"
        )

    lines.extend([
        "COMMIT;",
        "SELECT 'APPLY_OK' AS status;",
    ])
    return "\n".join(lines) + "\n"


def _apply_plan_remote(plan: dict[str, Any]) -> str:
    sql = _render_apply_sql(plan)
    sql_b64 = base64.b64encode(sql.encode("utf-8")).decode("ascii")
    shell = (
        "set -euo pipefail\n"
        f"PSQL='{PSQL}'\n"
        f"echo {sql_b64} | base64 -d | $PSQL\n"
        "echo APPLY_OK\n"
    )
    return _ssm_run_shell_b64_region(
        plan["region"],
        plan["instance_id"],
        base64.b64encode(shell.encode("utf-8")).decode("ascii"),
        f"account model_mapping explicit apply {plan['target']}",
    )


def cmd_validate(args) -> int:
    _set_bundle_path(getattr(args, "bundle", None))
    doc = load_doc(args.file)
    runtime_reason = _runtime_setting_violation(canonical_json(doc))
    if runtime_reason:
        fail(runtime_reason)
    print(canonical_json(doc))
    return 0


def cmd_check(args) -> int:
    want = load_doc(args.file)
    targets = _resolve_apply_targets(args.target)
    rows: list[dict[str, Any]] = []
    errors: list[dict[str, Any]] = []
    want_json = canonical_json(want)
    with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
        futs = {ex.submit(read_runtime_blob, region, instance_id): label for label, region, instance_id in targets}
        for fut in as_completed(futs):
            label = futs[fut]
            try:
                got = fut.result()
                rows.append({
                    "target": label,
                    "matches": canonical_json(got) == want_json,
                    "file_scopes": _runtime_scope_summary(want),
                    "target_scopes": _runtime_scope_summary(got),
                })
            except Exception as e:  # noqa: BLE001 - report every target.
                errors.append({"target": label, "error": str(e)})
    rows.sort(key=lambda r: str(r.get("target") or ""))
    errors.sort(key=lambda e: str(e.get("target") or ""))
    report = {
        "targets": [t[0] for t in targets],
        "target_count": len(targets),
        "match_count": sum(1 for r in rows if r["matches"]),
        "drift_count": sum(1 for r in rows if not r["matches"]),
        "error_count": len(errors),
        "results": rows,
        "errors": errors,
    }
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    elif errors:
        print(f"ERROR: account model_mapping runtime check could not read {len(errors)} target(s).")
        for e in errors:
            print(f"  [{e['target']}] {e['error']}")
    elif report["drift_count"]:
        print(f"DRIFT: {report['drift_count']} target(s) differ from file.")
        for row in rows:
            if row["matches"]:
                continue
            print(f"  [{row['target']}] file={row['file_scopes']} target={row['target_scopes']}")
    else:
        print(f"OK: account model_mapping runtime matches file across {len(targets)} target(s).")
    if errors:
        return 2
    return 1 if report["drift_count"] else 0


def _account_check_report(
    *,
    skip_prod: bool,
    include_edges: bool,
    parallel: int,
    prod_instance_id: str | None = None,
) -> dict[str, Any]:
    targets = _resolve_check_targets(
        skip_prod,
        include_edges=include_edges,
        prod_instance_id=_normalize_instance_id(prod_instance_id, "--prod-instance-id"),
    )
    violations: list[dict[str, Any]] = []
    errors: list[dict[str, Any]] = []
    runtime_setting_targets: list[str] = []
    with ThreadPoolExecutor(max_workers=max(1, parallel)) as ex:
        futs = {ex.submit(_check_target, *t): t for t in targets}
        for fut in as_completed(futs):
            label = futs[fut][0]
            try:
                got_violations, got_errors, runtime_present = fut.result()
                violations.extend(got_violations)
                errors.extend(got_errors)
                if runtime_present:
                    runtime_setting_targets.append(label)
            except Exception as e:  # noqa: BLE001 - SSM failures should report all reachable targets.
                errors.append({"target": label, "error": str(e)})

    def sort_key(v: dict[str, Any]) -> tuple[str, str, str]:
        return (str(v.get("target") or ""), str(v.get("kind") or ""), str(v.get("id") or ""))

    report = {
        "targets": [t[0] for t in targets],
        "resolved_targets": [
            {"target": label, "region": region, "instance_id": instance_id}
            for label, region, instance_id in targets
        ],
        "target_count": len(targets),
        "bundle": str(_BUNDLE_PATH),
        "floor_sha256": _load_bundle()["floor_sha256"],
        "managed_platforms": list(MANAGED_PLATFORMS),
        "runtime_setting_targets": sorted(runtime_setting_targets),
        "violation_count": len(violations),
        "error_count": len(errors),
        "violations": sorted(violations, key=sort_key),
        "errors": sorted(errors, key=lambda e: str(e.get("target") or "")),
    }
    report["runtime_setting_remediation"] = _runtime_setting_remediation(report)
    return report


def _runtime_setting_violations(report: dict[str, Any]) -> list[dict[str, Any]]:
    return [
        violation
        for violation in (report.get("violations") or [])
        if violation.get("kind") == "runtime_setting"
    ]


def _runtime_setting_remediation(report: dict[str, Any]) -> list[str]:
    violations = _runtime_setting_violations(report)
    targets = sorted({str(v.get("target") or "").strip() for v in violations if v.get("target")})
    steps: list[str] = []
    for target in targets:
        sync_cmd = _command_with_bundle([
            "python3",
            "ops/pricing/manage-account-model-mapping-runtime.py",
            "sync-runtime",
            "--file",
            "CORRECTED_RUNTIME.json",
            "--target",
            target,
        ])
        clear_cmd = " ".join(shlex.quote(part) for part in [
            "python3",
            "ops/pricing/manage-account-model-mapping-runtime.py",
            "clear-runtime",
            "--target",
            target,
        ])
        steps.extend([
            f"Correct the runtime JSON so it complies with the selected bundle policy, then sync {target}: {sync_cmd}",
            f"Or remove the runtime replacement and return {target} to the compiled floor: {clear_cmd}",
        ])
    return steps


def _print_runtime_setting_remediation(report: dict[str, Any]) -> None:
    steps = report.get("runtime_setting_remediation") or _runtime_setting_remediation(report)
    if not steps:
        return
    print("Runtime policy conflicts block account planning. Do not run apply-accounts until they are resolved:")
    for step in steps:
        print(f"  {step}")


def cmd_check_accounts(args) -> int:
    _set_bundle_path(getattr(args, "bundle", None))
    report = _account_check_report(
        skip_prod=args.skip_prod,
        include_edges=args.include_edges,
        parallel=args.parallel,
        prod_instance_id=getattr(args, "prod_instance_id", None),
    )
    target_count = report["target_count"]
    errors = report["errors"]
    violations = report["violations"]
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    elif errors:
        print(f"ERROR: account model_mapping check could not read {len(errors)} target(s).")
        for e in report["errors"]:
            print(f"  [{e['target']}] {e['error']}")
        if violations:
            print(f"Also found {len(violations)} violation(s) on reachable targets:")
            for v in report["violations"]:
                print(f"  [{v['target']}] {v['kind']} {v.get('id')} ({v.get('name')}): {v['reason']}")
    elif violations:
        print(f"FAIL: {len(violations)} account model_mapping violation(s) across {target_count} target(s):")
        for v in report["violations"]:
            print(f"  [{v['target']}] {v['kind']} {v.get('id')} ({v.get('name')}): {v['reason']}")
    else:
        print(f"OK: all managed account model_mapping scopes explicit across {target_count} target(s).")
    if not args.json and violations:
        _print_runtime_setting_remediation(report)
    if errors:
        return 2
    return 1 if violations else 0


def _command_with_bundle(parts: list[str]) -> str:
    if _BUNDLE_PATH.resolve() != DEFAULT_BUNDLE_PATH.resolve():
        parts.extend(["--bundle", str(_BUNDLE_PATH)])
    return " ".join(shlex.quote(part) for part in parts)


def _release_gate_remediation(report: dict[str, Any]) -> list[str]:
    runtime_steps = report.get("runtime_setting_remediation") or _runtime_setting_remediation(report)
    if runtime_steps:
        return runtime_steps
    return [
        _command_with_bundle([
            "python3",
            "ops/pricing/manage-account-model-mapping-runtime.py",
            "apply-accounts",
            "--target",
            "prod",
            "--dry-run",
        ]),
        _command_with_bundle([
            "python3",
            "ops/pricing/manage-account-model-mapping-runtime.py",
            "apply-accounts",
            "--target",
            "prod",
            "--confirm",
            APPLY_CONFIRM,
        ]),
    ]


def _release_gate_status(report: dict[str, Any]) -> str:
    if report.get("error_count"):
        return "error"
    if report.get("violation_count"):
        return "violation"
    return "ok"


def _print_release_gate_human(report: dict[str, Any]) -> None:
    status = _release_gate_status(report)
    if status == "ok":
        print("OK: prod account model_mapping covers the selected bundle floor. Model activation may proceed.")
        return
    if status == "error":
        print("ERROR: model-activation floor check could not read prod account model_mapping.")
        for e in report["errors"]:
            print(f"  [{e['target']}] {e['error']}")
        print("This check is read-only; fix AWS/OIDC/SSM access and rerun the explicit modelops check.")
        return

    runtime_conflicts = _runtime_setting_violations(report)
    if runtime_conflicts:
        print("FAIL: prod runtime model_mapping replacement conflicts with the selected bundle policy.")
        print("Account planning was skipped. Correct and sync the runtime, or clear it to use the compiled floor:")
    else:
        print("FAIL: prod account model_mapping is behind the selected model-activation bundle floor.")
        print("Review and apply the SSOT diff before activating the model surface:")
    for step in _release_gate_remediation(report):
        print(f"  {step}")
    print("First violations:")
    for v in report["violations"][:8]:
        print(f"  [{v['target']}] {v['kind']} {v.get('id')} ({v.get('name')}): {v['reason']}")
    remaining = report["violation_count"] - min(report["violation_count"], 8)
    if remaining > 0:
        print(f"  ... (+{remaining} more)")


def cmd_release_gate(args) -> int:
    _set_bundle_path(getattr(args, "bundle", None))
    report = _account_check_report(
        skip_prod=False,
        include_edges=False,
        parallel=args.parallel,
        prod_instance_id=args.prod_instance_id,
    )
    status = _release_gate_status(report)
    if args.json:
        print(json.dumps({
            "status": status,
            "remediation": _release_gate_remediation(report) if status == "violation" else [],
            **report,
        }, ensure_ascii=False, indent=2))
    else:
        _print_release_gate_human(report)
    if status == "error":
        return 2
    return 1 if status == "violation" else 0


def cmd_apply_accounts(args) -> int:
    _set_bundle_path(getattr(args, "bundle", None))
    if not args.dry_run and args.confirm != APPLY_CONFIRM:
        fail(f"apply-accounts requires --confirm {APPLY_CONFIRM}")
    activation_floor_sha256 = getattr(args, "activation_floor_sha256", None)
    if activation_floor_sha256:
        bundle_sha256 = _load_bundle()["floor_sha256"]
        if activation_floor_sha256 != bundle_sha256:
            fail(
                "--activation-floor-sha256 does not match --bundle: "
                f"got {activation_floor_sha256!r}, expected {bundle_sha256!r}"
            )
        if args.target.strip().lower() != "prod":
            fail("--activation-floor-sha256 is only valid with --target prod")

    # Targeted mode: --account-ids
    account_ids_raw = getattr(args, "account_ids", None)
    account_ids: list[int] | None = None
    if account_ids_raw is not None:
        try:
            account_ids = _parse_account_ids(account_ids_raw)
        except ValueError as e:
            fail(str(e))
        # Reject all-deployable-and-prod in targeted mode
        if args.target.strip().lower() in {"all", "all-deployable-and-prod"}:
            fail("--account-ids requires a single explicit target (prod or edge:<id>), not all-deployable-and-prod")

    targets = _resolve_apply_targets(
        args.target,
        prod_instance_id=getattr(args, "prod_instance_id", None),
    )

    # Targeted mode must have exactly one target
    if account_ids is not None and len(targets) != 1:
        fail("--account-ids requires exactly one target")

    plans: list[dict[str, Any]] = []
    errors: list[dict[str, Any]] = []
    with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
        futs = {
            ex.submit(_collect_apply_plan, *t, activation_floor_sha256, account_ids): t
            for t in targets
        }
        for fut in as_completed(futs):
            label = futs[fut][0]
            try:
                plans.append(fut.result())
            except Exception as e:  # noqa: BLE001 - collect all target planning failures before any write.
                errors.append({"target": label, "error": str(e)})

    plans.sort(key=lambda p: str(p.get("target") or ""))

    report = {
        "bundle": str(_BUNDLE_PATH),
        "floor_sha256": _load_bundle()["floor_sha256"],
        "targets": [p["target"] for p in plans],
        "target_count": len(targets),
        "account_change_count": sum(len(p.get("account_changes") or []) for p in plans),
        "group_change_count": sum(len(p.get("group_changes") or []) for p in plans),
        "plans": plans,
        "errors": sorted(errors, key=lambda e: str(e.get("target") or "")),
    }
    if activation_floor_sha256:
        report["activation_floor_sha256"] = activation_floor_sha256

    # Compute plan_sha256 for targeted mode (always, even if no-op)
    if account_ids is not None and plans and not errors:
        plan_digest = _compute_plan_sha256(plans[0])
        report["plan_sha256"] = plan_digest

    # In targeted mode, missing active IDs -> exit 2 with JSON report
    if account_ids is not None and plans:
        plan = plans[0]
        missing = plan.get("missing_ids") or []
        if missing:
            report["errors"].append({
                "target": plan.get("target"),
                "error": f"active accounts not found: {','.join(str(i) for i in missing)}",
                "missing_ids": missing,
            })
            print(json.dumps(report, ensure_ascii=False, indent=2))
            return 2

    if errors:
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 2

    # Non-dry-run targeted mode requires --expected-plan-sha256 (checked before no-op return)
    expected_plan_sha256 = getattr(args, "expected_plan_sha256", None)
    if account_ids is not None and not args.dry_run:
        if not expected_plan_sha256:
            fail("targeted apply (--account-ids) requires --expected-plan-sha256 in non-dry-run mode")
        # Recompute plan digest and compare
        recomputed = _compute_plan_sha256(plans[0])
        if recomputed != expected_plan_sha256:
            fail(
                f"--expected-plan-sha256 mismatch: got {recomputed!r}, "
                f"expected {expected_plan_sha256!r}; plan changed between dry-run and apply"
            )

    if args.dry_run:
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 0
    if report["account_change_count"] == 0 and report["group_change_count"] == 0:
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 0

    # Targeted mode uses CAS SQL
    if account_ids is not None:
        plan = plans[0]
        changed_ids = plan.get("changed_ids") or []
        try:
            out = _apply_targeted_plan_remote(plan)
            if "TARGETED_APPLY_OK" not in out:
                raise RuntimeError("remote SQL did not report TARGETED_APPLY_OK")
            result = {
                "bundle": str(_BUNDLE_PATH),
                "floor_sha256": _load_bundle()["floor_sha256"],
                "plan_sha256": expected_plan_sha256,
                "changed_ids": changed_ids,
                "applied": [{"target": plan["target"], "account_changes": len(plan.get("account_changes") or []), "group_changes": 0}],
                "errors": [],
            }
        except Exception as e:  # noqa: BLE001
            result = {
                "bundle": str(_BUNDLE_PATH),
                "floor_sha256": _load_bundle()["floor_sha256"],
                "plan_sha256": expected_plan_sha256,
                "changed_ids": [],
                "applied": [],
                "errors": [{"target": plan["target"], "error": str(e)}],
            }
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 2 if result["errors"] else 0

    # Non-targeted mode: batch apply
    apply_errors: list[dict[str, Any]] = []
    applied: list[dict[str, Any]] = []
    with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
        futs = {ex.submit(_apply_plan_remote, p): p for p in plans if p.get("account_changes") or p.get("group_changes")}
        for fut in as_completed(futs):
            plan = futs[fut]
            label = plan["target"]
            try:
                out = fut.result()
                if "APPLY_OK" not in out:
                    raise RuntimeError("remote SQL did not report APPLY_OK")
                applied.append({
                    "target": label,
                    "account_changes": len(plan.get("account_changes") or []),
                    "group_changes": len(plan.get("group_changes") or []),
                })
            except Exception as e:  # noqa: BLE001 - report all failed target writes.
                apply_errors.append({"target": label, "error": str(e)})

    result = {
        "bundle": str(_BUNDLE_PATH),
        "floor_sha256": _load_bundle()["floor_sha256"],
        "applied": sorted(applied, key=lambda p: str(p.get("target") or "")),
        "errors": sorted(apply_errors, key=lambda e: str(e.get("target") or "")),
    }
    if activation_floor_sha256:
        result["activation_floor_sha256"] = activation_floor_sha256
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 2 if apply_errors else 0


def _publish_settings_updated(region: str, instance_id: str, sql: str, comment: str) -> str:
    shell = (
        "set -uo pipefail\n"
        f"PSQL='{PSQL}'\n"
        f"RC='{REDISCLI}'\n"
        "echo '=== update tk_account_model_mapping_runtime ==='\n"
        f"$PSQL -c \"{sql}\" </dev/null && echo UPDATE_OK\n"
        "echo '=== publish settings_updated (fan-out reload) ==='\n"
        "$RC PUBLISH settings_updated refresh </dev/null || "
        "echo 'WARN: redis PUBLISH failed; replicas reload on normal settings cache TTL'\n"
        "echo '=== settings_after ==='\n"
        f"$PSQL -c \"SELECT key, length(value) AS bytes FROM settings WHERE key='{SETTING_KEY}';\" </dev/null\n"
    )
    b64 = base64.b64encode(shell.encode()).decode()
    if len(b64) > 90_000:
        fail(f"encoded SSM payload is {len(b64)}B (>90KB); runtime blob is too large for inline SSM")
    return _ssm_run_shell_b64_region(region, instance_id, b64, comment)


def _sync_runtime_target(label: str, region: str, instance_id: str, payload: bytes, doc: dict) -> dict[str, Any]:
    gz_b64 = base64.b64encode(gzip.compress(payload)).decode()
    sql = (
        f"INSERT INTO settings (key, value, updated_at) VALUES "
        f"('{SETTING_KEY}', convert_from(decode('$JSON_B64','base64'),'UTF8'), NOW()) "
        "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();"
    )
    shell = (
        "set -uo pipefail\n"
        f"PSQL='{PSQL}'\n"
        f"RC='{REDISCLI}'\n"
        f"JSON_B64=\"$(echo {gz_b64} | base64 -d | gunzip | base64 | tr -d '\\n')\"\n"
        "echo '=== update tk_account_model_mapping_runtime ==='\n"
        f"$PSQL -c \"{sql}\" </dev/null && echo UPDATE_OK\n"
        "echo '=== publish settings_updated (fan-out reload) ==='\n"
        "$RC PUBLISH settings_updated refresh </dev/null || "
        "echo 'WARN: redis PUBLISH failed; replicas reload on normal settings cache TTL'\n"
        "echo '=== settings_after ==='\n"
        f"$PSQL -c \"SELECT key, length(value) AS bytes FROM settings WHERE key='{SETTING_KEY}';\" </dev/null\n"
    )
    b64 = base64.b64encode(shell.encode()).decode()
    if len(b64) > 90_000:
        fail(f"encoded SSM payload is {len(b64)}B (>90KB); runtime blob is too large for inline SSM")
    out = _ssm_run_shell_b64_region(region, instance_id, b64, f"account model_mapping runtime: upsert + publish {label}")
    if "UPDATE_OK" not in out:
        raise RuntimeError("settings update did not report success")
    if canonical_json(read_runtime_blob(region, instance_id)) != canonical_json(doc):
        raise RuntimeError("post-sync verify shows runtime does not match file")
    return {"target": label, "bytes": len(payload)}


def _clear_runtime_target(label: str, region: str, instance_id: str) -> dict[str, Any]:
    sql = f"DELETE FROM settings WHERE key='{SETTING_KEY}';"
    out = _publish_settings_updated(region, instance_id, sql, f"account model_mapping runtime: clear + publish {label}")
    if "UPDATE_OK" not in out:
        raise RuntimeError("settings delete did not report success")
    if read_runtime_blob(region, instance_id):
        raise RuntimeError("post-clear verify still sees a runtime setting")
    return {"target": label}


def cmd_sync_runtime(args) -> int:
    _set_bundle_path(getattr(args, "bundle", None))
    doc = load_doc(args.file)
    runtime_reason = _runtime_setting_violation(canonical_json(doc))
    if runtime_reason:
        fail(runtime_reason)
    payload = canonical_json(doc).encode("utf-8")
    targets = _resolve_apply_targets(args.target)
    if args.dry_run:
        print(f"DRY-RUN: would UPSERT settings[{SETTING_KEY}] on {len(targets)} target(s) "
              f"({len(payload)} bytes, scopes={_runtime_scope_summary(doc)}) + PUBLISH settings_updated reload.")
        for label, _, _ in targets:
            print(f"  - {label}")
        return 0
    synced: list[dict[str, Any]] = []
    errors: list[dict[str, Any]] = []
    with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
        futs = {ex.submit(_sync_runtime_target, label, region, instance_id, payload, doc): label for label, region, instance_id in targets}
        for fut in as_completed(futs):
            label = futs[fut]
            try:
                synced.append(fut.result())
            except Exception as e:  # noqa: BLE001 - report every target write.
                errors.append({"target": label, "error": str(e)})
    result = {
        "synced": sorted(synced, key=lambda r: str(r.get("target") or "")),
        "errors": sorted(errors, key=lambda e: str(e.get("target") or "")),
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 2 if errors else 0


def cmd_clear_runtime(args) -> int:
    targets = _resolve_apply_targets(args.target)
    if args.dry_run:
        print(f"DRY-RUN: would DELETE settings[{SETTING_KEY}] on {len(targets)} target(s) + PUBLISH settings_updated reload.")
        for label, _, _ in targets:
            print(f"  - {label}")
        return 0
    cleared: list[dict[str, Any]] = []
    errors: list[dict[str, Any]] = []
    with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
        futs = {ex.submit(_clear_runtime_target, label, region, instance_id): label for label, region, instance_id in targets}
        for fut in as_completed(futs):
            label = futs[fut]
            try:
                cleared.append(fut.result())
            except Exception as e:  # noqa: BLE001 - report every target write.
                errors.append({"target": label, "error": str(e)})
    result = {
        "cleared": sorted(cleared, key=lambda r: str(r.get("target") or "")),
        "errors": sorted(errors, key=lambda e: str(e.get("target") or "")),
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 2 if errors else 0


def cmd_example(_args) -> int:
    doc = {
        "platforms": {
            "grok": {
                "grok": "grok-4.3",
                "grok-latest": "grok-4.3",
                "grok-build": "grok-build-0.1",
            }
        },
        "newapi_channel_types": {
            "41": {
                "imagen-4.0-generate-001": "imagen-4.0-generate-001",
            }
        },
    }
    print(json.dumps(doc, ensure_ascii=False, indent=2, sort_keys=True))
    return 0


def cmd_selftest(_args) -> int:
    clean = normalize_runtime_doc({
        "platforms": {"xai": {" grok ": " grok-4.3 "}, "Claude": {"sonnet": "sonnet"}},
        "newapi_channel_types": {41: {"imagen": "imagen"}},
    })
    assert clean["platforms"]["grok"]["grok"] == "grok-4.3"
    assert clean["platforms"]["anthropic"]["sonnet"] == "sonnet"
    assert clean["newapi_channel_types"]["41"]["imagen"] == "imagen"
    with contextlib.redirect_stderr(io.StringIO()):
        try:
            normalize_runtime_doc({"platforms": {"grok": {}}})
        except SystemExit as e:
            assert e.code == 2
        else:
            raise AssertionError("empty mapping accepted")
    floor = {
        "platforms": {
            "anthropic": {
                "claude-opus-4-8": "claude-opus-4-8",
            },
            "openai": {
                "gpt-5.6": "gpt-5.6",
                "gpt-5.6-luna": "gpt-5.6-luna",
                "gpt-5.6-sol": "gpt-5.6-sol",
                "gpt-5.6-terra": "gpt-5.6-terra",
            },
            "grok": {
                "grok": "grok-4.3",
                "grok-latest": "grok-4.3",
                "grok-build": "grok-build-0.1",
                "grok-4.3": "grok-4.3",
            },
            "antigravity": {
                "claude-sonnet-4-6": "claude-sonnet-4-6",
                "claude-opus-4-6": "claude-opus-4-6-thinking",
                "claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
            },
            "kiro": {
                "claude-sonnet-4-5": "claude-sonnet-4-5",
                "claude-sonnet-5": "claude-sonnet-5",
                "claude-opus-5": "claude-opus-5",
            },
        },
        "newapi_channel_types": {
            "41": {"vertex-shared": "vertex-shared"},
            "45": {"generic-model": "generic-model"},
        },
        "vertex_capability_profiles": {
            "core-pro": {"vertex-shared": "vertex-shared", "vertex-pro": "vertex-pro"},
            "core-ultra": {"vertex-shared": "vertex-shared", "vertex-ultra": "vertex-ultra"},
        },
        "account_overrides": [
            {
                "platform": "newapi",
                "channel_type": 45,
                "base_url": "https://ark.cn-beijing.volces.com/api/plan/v3",
                "model_mapping": {"agent-plan-model": "agent-plan-model"},
            },
        ],
        "antigravity_group_scopes": ["claude", "gemini_text", "gemini_image"],
        "forbidden_model_mapping_keys": {
            "anthropic": ["claude-opus-5"],
            "antigravity": ["test-forbidden-exact"],
        },
        "forbidden_model_mapping_prefixes": {"antigravity": ["test-forbidden-prefix-"]},
    }
    openai_plan = _account_plan(
        {
            "id": 2,
            "platform": "openai",
            "type": "oauth",
            "model_mapping": {
                "gpt-5.6": "gpt-5.6",
                "gpt-5.6-luna": "gpt-5.6-luna",
                "gpt-5.6-terra": "gpt-5.6-terra",
            },
        },
        floor,
    )
    assert openai_plan and openai_plan["diff"]["missing_keys"] == ["gpt-5.6-sol"]
    account_override_row = {
        "id": 12345,
        "platform": "newapi",
        "type": "apikey",
        "channel_type": 45,
        "base_url": "https://ark.cn-beijing.volces.com/api/plan/v3/",
        "model_mapping": {"agent-plan-model": "agent-plan-model"},
    }
    assert _account_plan(account_override_row, floor) is None
    generic_channel_row = {
        "id": 89,
        "platform": "newapi",
        "type": "apikey",
        "channel_type": 45,
        "base_url": "https://ark.cn-beijing.volces.com/api/v3",
        "model_mapping": {"agent-plan-model": "agent-plan-model"},
    }
    generic_channel_plan = _account_plan(generic_channel_row, floor)
    assert generic_channel_plan and generic_channel_plan["scope"] == "newapi_channel_type:45"
    mismatched_override = dict(
        account_override_row,
        base_url="https://ark.cn-beijing.volces.com/api/v3",
    )
    mismatch_plan = _account_plan(mismatched_override, floor)
    assert mismatch_plan and mismatch_plan["scope"] == "newapi_channel_type:45"
    assert mismatch_plan["desired_model_mapping"] == {
        "agent-plan-model": "agent-plan-model",
        "generic-model": "generic-model",
    }
    original_run_check_sql_json = globals()["_run_check_sql_json"]
    original_load_effective_floor = globals()["_load_effective_floor"]
    globals()["_run_check_sql_json"] = lambda _region, _instance_id, _label: {
        "runtime_setting": None,
        "accounts": [mismatched_override],
        "antigravity_groups": [],
    }
    globals()["_load_effective_floor"] = lambda _raw: floor
    try:
        apply_plan = _collect_apply_plan("prod", "test-region", "i-test")
        assert apply_plan["account_changes"][0]["scope"] == "newapi_channel_type:45"
    finally:
        globals()["_run_check_sql_json"] = original_run_check_sql_json
        globals()["_load_effective_floor"] = original_load_effective_floor
    native_anthropic_plan = _account_plan({
        "platform": "anthropic",
        "type": "oauth",
        "model_mapping": {
            "claude-opus-4-8": "claude-opus-4-8",
            "claude-opus-5": "claude-opus-5",
        },
    }, floor)
    assert native_anthropic_plan
    assert native_anthropic_plan["diff"]["forbidden_keys"] == ["claude-opus-5"]
    assert _account_plan({
        "name": "kiro-us3",
        "platform": "anthropic",
        "type": "apikey",
        "mirror_platform": "kiro",
        "model_mapping": dict(floor["platforms"]["kiro"]),
    }, floor) is None
    assert _account_plan({
        "platform": "openai",
        "type": "oauth",
        "model_mapping": {},
    }, floor)
    vertex_known = {
        "id": 47,
        "name": "vertex-47",
        "platform": "newapi",
        "type": "apikey",
        "channel_type": 41,
        "vertex_capability_profile": "core-pro",
        "model_mapping": {
            "vertex-shared": "vertex-shared",
            "vertex-pro": "vertex-pro",
        },
    }
    assert _desired_mapping_for_account(vertex_known, floor)[1] == "newapi_vertex_profile:core-pro"
    assert _account_plan(vertex_known, floor) is None
    vertex_missing = dict(vertex_known, vertex_capability_profile="", model_mapping={"vertex-shared": "vertex-shared"})
    assert _desired_mapping_for_account(vertex_missing, floor)[1] == "newapi_channel_type:41"
    assert _vertex_profile_issue(vertex_missing, floor)
    vertex_unknown = dict(vertex_missing, vertex_capability_profile="unknown")
    assert _vertex_profile_issue(vertex_unknown, floor)
    vertex57 = dict(
        vertex_known,
        id=57,
        vertex_capability_profile="core-ultra",
        model_mapping={
            "vertex-shared": "vertex-shared",
            "vertex-ultra": "vertex-ultra",
            "vertex-pro": "vertex-pro",
        },
    )
    assert _account_plan(vertex57, floor) is None
    compatible_extra_row = {
        "id": 3,
        "platform": "openai",
        "type": "oauth",
        "model_mapping": {
            "gpt-5.6": "gpt-5.6",
            "gpt-5.6-luna": "gpt-5.6-luna",
            "gpt-5.6-sol": "gpt-5.6-sol",
            "gpt-5.6-terra": "gpt-5.6-terra",
            "future-model": "future-model",
        },
    }
    assert _account_plan(compatible_extra_row, floor) is None
    compatible_extra_row["model_mapping"].pop("gpt-5.6-sol")
    preserved_extra_plan = _account_plan(compatible_extra_row, floor)
    assert preserved_extra_plan
    assert preserved_extra_plan["diff"]["compatible_extra_keys"] == ["future-model"]
    assert preserved_extra_plan["desired_model_mapping"]["future-model"] == "future-model"
    plan = _account_plan(
        {"id": 1, "platform": "grok", "type": "oauth", "model_mapping": {"grok": "grok-4.3"}},
        floor,
    )
    assert plan and plan["desired_model_mapping"]["grok-latest"] == "grok-4.3"
    assert _account_plan({
        "platform": "antigravity",
        "type": "oauth",
        "model_mapping": {
            "claude-sonnet-4-6": "claude-sonnet-4-6",
            "claude-opus-4-6": "claude-opus-4-6-thinking",
            "claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
            "claude-sonnet-5": "claude-sonnet-5",
        },
    }, floor) is None
    forbidden_plan = _account_plan({
        "platform": "antigravity",
        "type": "oauth",
        "model_mapping": {
            **floor["platforms"]["antigravity"],
            "future-model": "future-model",
            "test-forbidden-exact": "test-forbidden-exact",
            "test-forbidden-prefix-boundary": "test-forbidden-prefix-boundary",
        },
    }, floor)
    assert forbidden_plan
    assert forbidden_plan["diff"]["forbidden_keys"] == [
        "test-forbidden-exact",
        "test-forbidden-prefix-boundary",
    ]
    assert forbidden_plan["desired_model_mapping"]["future-model"] == "future-model"
    assert "test-forbidden-exact" not in forbidden_plan["desired_model_mapping"]
    policy_scope, policy_forbidden_key = next(
        (scope, sorted(keys)[0])
        for scope, keys in sorted(floor["forbidden_model_mapping_keys"].items())
        if keys
    )
    policy_prefix_scope, policy_forbidden_prefix = next(
        (scope, sorted(prefixes)[0])
        for scope, prefixes in sorted(floor["forbidden_model_mapping_prefixes"].items())
        if prefixes
    )
    policy_prefixed_key = policy_forbidden_prefix + "boundary"
    assert _mapping_policy_violations({
        "platform": policy_scope,
        "type": "oauth",
        "model_mapping": {policy_forbidden_key: "allowed-target"},
    }, floor)
    assert _mapping_policy_violations({
        "platform": policy_prefix_scope,
        "type": "oauth",
        "model_mapping": {policy_prefixed_key: policy_prefixed_key},
    }, floor)
    sql = _render_apply_sql({
        "account_changes": [plan],
        "group_changes": [{"id": 7, "desired_supported_model_scopes": ["claude", "gemini_text", "gemini_image"]}],
    })
    assert "account_bulk_changed" in sql and "group_changed" in sql
    guarded_sql = _render_apply_sql({
        "activation_floor_sha256": "a" * 64,
        "account_changes": [plan],
        "group_changes": [],
    })
    assert "LOCK TABLE settings IN SHARE ROW EXCLUSIVE MODE" in guarded_sql
    assert f"{SETTING_KEY} appeared before activation write" in guarded_sql
    assert guarded_sql.index("LOCK TABLE settings") < guarded_sql.index("UPDATE accounts")
    with tempfile.TemporaryDirectory() as profile_temp_dir:
        profile_path = Path(profile_temp_dir) / "vertex-profiles.json"
        profile_path.write_text(json.dumps({"assignments": [{
            "id": 47,
            "name": "vertex-47",
            "platform": "newapi",
            "channel_type": 41,
            "current_profile": "",
            "profile": "core-pro",
        }]}), encoding="utf-8")
        profile_assignments = _load_vertex_profile_assignments(profile_path, floor)
        original_run_check_sql_json = globals()["_run_check_sql_json"]
        globals()["_run_check_sql_json"] = lambda _region, _instance_id, _label: {
            "runtime_setting": None,
            "accounts": [{
                "id": 47,
                "name": "vertex-47",
                "platform": "newapi",
                "channel_type": 41,
                "vertex_capability_profile": None,
                "model_mapping": {"vertex-shared": "vertex-shared"},
            }],
            "antigravity_groups": [],
        }
        try:
            profile_plan = _collect_vertex_profile_assignment_plan(
                "prod", "test-region", "i-test", profile_assignments)
            assert len(profile_plan["changes"]) == 1
            profile_sql = _render_vertex_profile_assignment_sql(profile_plan)
            assert VERTEX_PROFILE_CREDENTIAL_KEY in profile_sql
            assert "model_mapping" not in profile_sql
            assert "name = convert_from" in profile_sql
            assert "channel_type = 41" in profile_sql
            assert profile_sql.count("'account_bulk_changed'") == 1
            assert '"account_ids":[47]' in profile_sql
            assert "PROFILE_ASSIGN_OK" in profile_sql
        finally:
            globals()["_run_check_sql_json"] = original_run_check_sql_json
    assert _group_violation({"scopes": ["gemini_text"]}, floor)
    assert _group_violation({"scopes": ["claude", "gemini_text", "gemini_image"]}, floor) is None
    assert _runtime_setting_violation('{"platforms":{"grok":{}}}')
    assert _runtime_setting_violation('{"platforms":{"grok":{"grok":"grok-4.3"}}}') is None
    assert _runtime_setting_violation(
        '{"newapi_channel_types":{"41":{"vertex-shared":"vertex-shared"}}}'
    )
    assert _runtime_setting_violation(None) is None
    effective_floor = _load_effective_floor(None)
    real_policy_samples: list[tuple[str, str]] = []
    for scope, keys in sorted((effective_floor.get("forbidden_model_mapping_keys") or {}).items()):
        if keys:
            real_policy_samples.append((scope, sorted(keys)[0]))
    for scope, prefixes in sorted((effective_floor.get("forbidden_model_mapping_prefixes") or {}).items()):
        if prefixes:
            real_policy_samples.append((scope, sorted(prefixes)[0] + "selftest-boundary"))

    forbidden_runtime: str | None = None
    runtime_conflict = "policy conflict boundary"
    for scope, forbidden_sample in real_policy_samples:
        if scope == "newapi":
            runtime_doc = {"newapi_channel_types": {"1": {forbidden_sample: "selftest-target"}}}
            location = "newapi_channel_types.1"
        else:
            runtime_doc = {"platforms": {scope: {forbidden_sample: "selftest-target"}}}
            location = f"platforms.{scope}"
        runtime_raw = canonical_json(runtime_doc)
        conflict = _runtime_setting_violation(runtime_raw)
        assert conflict and "policy conflict" in conflict
        assert location in conflict
        assert forbidden_sample in conflict
        if forbidden_runtime is None:
            forbidden_runtime = runtime_raw
            runtime_conflict = conflict

    if forbidden_runtime is not None:
        original_run_check_sql_json = globals()["_run_check_sql_json"]
        globals()["_run_check_sql_json"] = lambda _region, _instance_id, _label: {
            "runtime_setting": forbidden_runtime,
            "accounts": [],
            "antigravity_groups": [],
        }
        try:
            checked_violations, checked_errors, runtime_present = _check_target(
                "prod", "test-region", "i-test")
            assert checked_errors == []
            assert runtime_present
            assert len(checked_violations) == 1
            assert checked_violations[0]["kind"] == "runtime_setting"
            assert "policy conflict" in checked_violations[0]["reason"]
            try:
                _collect_apply_plan("prod", "test-region", "i-test")
            except RuntimeError as e:
                assert "policy conflict" in str(e)
            else:
                raise AssertionError("apply planning accepted a runtime mapping forbidden by the bundle")
        finally:
            globals()["_run_check_sql_json"] = original_run_check_sql_json
    assert _is_openai_ainzy_relay({
        "platform": "openai",
        "type": "apikey",
        "base_url": "https://api.ainzy.net/v1",
    })
    assert not _is_openai_ainzy_relay({
        "platform": "openai",
        "type": "apikey",
        "base_url": "https://relay.example.com/v1",
    })
    assert not _is_openai_ainzy_relay({
        "platform": "openai",
        "type": "apikey",
        "base_url": "https://api.openai.com/v1",
    })
    original_load_matrix = _ROUTING.load_matrix
    original_load_lightsail_targets = _ROUTING.load_lightsail_targets
    _ROUTING.load_matrix = lambda *_args, **_kwargs: (_ for _ in ()).throw(
        AssertionError("prod-only target resolution touched the EC2 edge registry"))
    _ROUTING.load_lightsail_targets = lambda *_args, **_kwargs: (_ for _ in ()).throw(
        AssertionError("prod-only target resolution touched the Lightsail edge registry"))
    try:
        prod_only = _resolve_check_targets(
            skip_prod=False,
            include_edges=False,
            prod_instance_id="i-00000000000000000",
        )
        assert len(prod_only) == 1 and prod_only[0][0] == "prod"
    finally:
        _ROUTING.load_matrix = original_load_matrix
        _ROUTING.load_lightsail_targets = original_load_lightsail_targets
    violation_report = {"violation_count": 1, "error_count": 0, "violations": [{"kind": "account"}]}
    runtime_violation_report = {
        "violation_count": 1,
        "error_count": 0,
        "violations": [{"target": "prod", "kind": "runtime_setting", "reason": runtime_conflict}],
    }
    ok_report = {"violation_count": 0, "error_count": 0}
    assert _release_gate_status(violation_report) == "violation"
    assert _release_gate_status(ok_report) == "ok"
    runtime_remediation = _release_gate_remediation(runtime_violation_report)
    assert any("sync-runtime" in step for step in runtime_remediation)
    assert any("clear-runtime" in step for step in runtime_remediation)
    assert all("apply-accounts" not in step for step in runtime_remediation)
    original_bundle_path = _BUNDLE_PATH
    with tempfile.TemporaryDirectory() as temp_dir:
        selected_bundle_path = Path(temp_dir) / "selected-bundle.json"
        valid_bundle = {
            "schema_version": BUNDLE_SCHEMA_VERSION,
            "floor_sha256": _BUNDLE.floor_sha256(floor),
            "account_model_mapping": floor,
        }
        selected_bundle_path.write_text(json.dumps(valid_bundle), encoding="utf-8")
        _set_bundle_path(str(selected_bundle_path))
        assert _load_effective_floor(None) == floor
        selected_bundle_remediation = _runtime_setting_remediation(runtime_violation_report)
        sync_steps = [step for step in selected_bundle_remediation if "sync-runtime" in step]
        assert sync_steps
        assert all("--bundle" in step for step in sync_steps)
        assert all(str(selected_bundle_path) in step for step in sync_steps)

        valid_bundle["floor_sha256"] = "0" * 64
        selected_bundle_path.write_text(json.dumps(valid_bundle), encoding="utf-8")
        try:
            _load_bundle()
        except RuntimeError as e:
            assert "floor_sha256 mismatch" in str(e)
        else:
            raise AssertionError("bundle digest tampering was accepted")

        valid_bundle["schema_version"] = BUNDLE_SCHEMA_VERSION + 1
        selected_bundle_path.write_text(json.dumps(valid_bundle), encoding="utf-8")
        try:
            _load_bundle()
        except RuntimeError as e:
            assert "unsupported model surface bundle schema" in str(e)
        else:
            raise AssertionError("bundle schema tampering was accepted")

        invalid_floor = copy.deepcopy(floor)
        invalid_floor["platforms"]["openai"][""] = "invalid-target"
        invalid_bundle = {
            "schema_version": BUNDLE_SCHEMA_VERSION,
            "floor_sha256": _BUNDLE.floor_sha256(invalid_floor),
            "account_model_mapping": invalid_floor,
        }
        selected_bundle_path.write_text(json.dumps(invalid_bundle), encoding="utf-8")
        try:
            _load_bundle()
        except RuntimeError as e:
            assert "empty or non-string key" in str(e)
        else:
            raise AssertionError("self-digested bundle with an invalid mapping was accepted")

        conflicting_floor = copy.deepcopy(floor)
        conflicting_floor["platforms"]["antigravity"]["test-forbidden-exact"] = "conflict"
        conflicting_bundle = {
            "schema_version": BUNDLE_SCHEMA_VERSION,
            "floor_sha256": _BUNDLE.floor_sha256(conflicting_floor),
            "account_model_mapping": conflicting_floor,
        }
        selected_bundle_path.write_text(json.dumps(conflicting_bundle), encoding="utf-8")
        try:
            _load_bundle()
        except RuntimeError as e:
            assert "requires forbidden keys" in str(e)
        else:
            raise AssertionError("bundle with a required/forbidden conflict was accepted")
    globals()["_BUNDLE_PATH"] = original_bundle_path
    parser = _build_parser()
    with contextlib.redirect_stderr(io.StringIO()):
        try:
            parser.parse_args(["release-gate", "--include-edges"])
        except SystemExit as e:
            assert e.code == 2
        else:
            raise AssertionError("release-gate accepted edge scope")
    assert parser.parse_args(["check-accounts", "--include-edges"]).include_edges
    profile_args = parser.parse_args([
        "assign-vertex-profiles", "--file", "profiles.json", "--dry-run",
    ])
    assert profile_args.dry_run
    assert not hasattr(profile_args, "target"), "profile assignment must be prod-only with no target escape hatch"
    selected_bundle_args = ["--bundle", str(DEFAULT_BUNDLE_PATH)]
    assert parser.parse_args(["validate", "--file", "runtime.json", *selected_bundle_args]).bundle
    assert parser.parse_args(["sync-runtime", "--file", "runtime.json", *selected_bundle_args]).bundle
    activation_apply_args = parser.parse_args([
        "apply-accounts",
        "--target", "prod",
        "--dry-run",
        "--prod-instance-id", "i-0123456789abcdef0",
        "--activation-floor-sha256", "a" * 64,
    ])
    assert activation_apply_args.prod_instance_id == "i-0123456789abcdef0"
    assert activation_apply_args.activation_floor_sha256 == "a" * 64

    command_bundle_calls: list[str | None] = []
    account_report_calls: list[dict[str, Any]] = []
    original_command_helpers = {
        "_set_bundle_path": globals()["_set_bundle_path"],
        "load_doc": globals()["load_doc"],
        "_runtime_setting_violation": globals()["_runtime_setting_violation"],
        "_resolve_apply_targets": globals()["_resolve_apply_targets"],
        "_account_check_report": globals()["_account_check_report"],
    }

    def fake_account_check_report(**kwargs) -> dict[str, Any]:
        account_report_calls.append(kwargs)
        return {
            "targets": ["prod"],
            "target_count": 1,
            "violation_count": 0,
            "error_count": 0,
            "violations": [],
            "errors": [],
        }

    globals()["_set_bundle_path"] = lambda raw: command_bundle_calls.append(raw) or DEFAULT_BUNDLE_PATH
    globals()["load_doc"] = lambda _path: {"platforms": {"test": {"test": "test"}}}
    globals()["_runtime_setting_violation"] = lambda _raw: None
    globals()["_resolve_apply_targets"] = lambda _target: [("prod", "test-region", "i-test")]
    globals()["_account_check_report"] = fake_account_check_report
    try:
        with contextlib.redirect_stdout(io.StringIO()):
            assert cmd_validate(argparse.Namespace(
                file=Path("runtime.json"), bundle="validate-bundle")) == 0
            assert cmd_sync_runtime(argparse.Namespace(
                file=Path("runtime.json"), target="prod", dry_run=True,
                parallel=1, bundle="sync-bundle")) == 0
            assert cmd_check_accounts(argparse.Namespace(
                skip_prod=False, include_edges=True, parallel=1,
                prod_instance_id="i-00000000000000000",
                json=False, bundle="check-bundle")) == 0
            assert cmd_release_gate(argparse.Namespace(
                parallel=1, prod_instance_id="i-00000000000000000",
                json=False, bundle="release-bundle")) == 0
    finally:
        globals().update(original_command_helpers)

    assert command_bundle_calls == ["validate-bundle", "sync-bundle", "check-bundle", "release-bundle"]
    assert len(account_report_calls) == 2
    check_call, release_call = account_report_calls
    assert check_call["include_edges"] is True
    assert release_call["include_edges"] is False

    # --- Targeted apply: _parse_account_ids ---
    assert _parse_account_ids("1,2,3") == [1, 2, 3]
    assert _parse_account_ids(" 5 , 3, 5 ,1") == [1, 3, 5]  # dedup + sort
    assert _parse_account_ids("42") == [42]
    try:
        _parse_account_ids("")
    except ValueError:
        pass
    else:
        raise AssertionError("empty --account-ids accepted")
    try:
        _parse_account_ids("1,-2")
    except ValueError as e:
        assert "-2" in str(e)
    else:
        raise AssertionError("negative --account-ids accepted")
    try:
        _parse_account_ids("1,abc")
    except ValueError as e:
        assert "abc" in str(e)
    else:
        raise AssertionError("non-integer --account-ids accepted")

    # --- Targeted apply: selection, no-op, unrelated/group exclusion ---
    targeted_accounts = [
        {"id": 10, "name": "acc-10", "platform": "openai", "type": "oauth",
         "model_mapping": {"gpt-5.6": "gpt-5.6", "gpt-5.6-luna": "gpt-5.6-luna",
                           "gpt-5.6-sol": "gpt-5.6-sol", "gpt-5.6-terra": "gpt-5.6-terra"}},
        {"id": 20, "name": "acc-20", "platform": "openai", "type": "oauth",
         "model_mapping": {"gpt-5.6": "gpt-5.6", "gpt-5.6-luna": "gpt-5.6-luna",
                           "gpt-5.6-terra": "gpt-5.6-terra"}},  # missing sol
        {"id": 30, "name": "acc-30", "platform": "grok", "type": "oauth",
         "model_mapping": {"grok": "grok-4.3"}},  # needs more keys
        {"id": 40, "name": "acc-40", "platform": "unknown", "type": "oauth",
         "model_mapping": {"custom": "custom"}},  # no bundle scope: skipped
    ]
    original_run_check_sql_json = globals()["_run_check_sql_json"]
    original_load_effective_floor = globals()["_load_effective_floor"]
    globals()["_run_check_sql_json"] = lambda _region, _instance_id, _label: {
        "runtime_setting": None,
        "accounts": targeted_accounts,
        "antigravity_groups": [{"id": 7, "name": "g7", "scopes": ["claude"]}],
    }
    globals()["_load_effective_floor"] = lambda _raw: floor
    try:
        # Select only IDs 10,20 -- ID 10 is already compliant, 20 needs changes
        targeted_plan = _collect_apply_plan("prod", "test-region", "i-test", account_ids=[10, 20, 99])
        assert targeted_plan["targeted"] is True
        assert targeted_plan["requested_ids"] == [10, 20, 99]
        assert targeted_plan["found_ids"] == [10, 20]
        assert targeted_plan["missing_ids"] == [99]
        assert targeted_plan["already_compliant_ids"] == [10]
        assert targeted_plan["changed_ids"] == [20]
        assert targeted_plan["counts"]["requested"] == 3
        assert targeted_plan["counts"]["found"] == 2
        assert targeted_plan["counts"]["missing"] == 1
        assert targeted_plan["counts"]["already_compliant"] == 1
        assert targeted_plan["counts"]["changed"] == 1
        assert targeted_plan["counts"]["skipped"] == 0
        # group_changes always empty in targeted mode
        assert targeted_plan["group_changes"] == []
        # Unrelated account 30 should not be in changes
        change_ids = [c["id"] for c in targeted_plan["account_changes"]]
        assert 30 not in change_ids
        # observed_model_mapping present
        assert targeted_plan["account_changes"][0]["observed_model_mapping"] is not None
        # Feed the actual selected plan into SQL rendering: unselected account 30
        # must not appear in updates or the merged outbox payload.
        selected_sql = _render_targeted_apply_sql(targeted_plan)
        assert "id = 20" in selected_sql
        assert "id = 30" not in selected_sql
        selected_outbox_b64 = _json_b64({"account_ids": [20]})
        assert selected_outbox_b64 in selected_sql
        assert _json_b64({"account_ids": [20, 30]}) not in selected_sql

        # Found rows without a bundle scope are reported as skipped, not compliant.
        skipped_plan = _collect_apply_plan("prod", "test-region", "i-test", account_ids=[40])
        assert skipped_plan["found_ids"] == [40]
        assert skipped_plan["skipped_ids"] == [40]
        assert skipped_plan["already_compliant_ids"] == []
        assert skipped_plan["changed_ids"] == []
        assert skipped_plan["counts"]["skipped"] == 1

        # Targeted plan where all selected are compliant (no-op)
        noop_plan = _collect_apply_plan("prod", "test-region", "i-test", account_ids=[10])
        assert noop_plan["targeted"] is True
        assert noop_plan["changed_ids"] == []
        assert noop_plan["already_compliant_ids"] == [10]
        assert noop_plan["account_changes"] == []
    finally:
        globals()["_run_check_sql_json"] = original_run_check_sql_json
        globals()["_load_effective_floor"] = original_load_effective_floor

    # --- Targeted apply: _compute_plan_sha256 determinism ---
    plan_a = {
        "target": "prod",
        "region": "us-east-1",
        "instance_id": "i-00000000000000001",
        "floor_sha256": "f" * 64,
        "targeted": True,
        "requested_ids": [10, 20],
        "found_ids": [10, 20],
        "missing_ids": [],
        "already_compliant_ids": [10],
        "changed_ids": [20],
        "counts": {"requested": 2, "found": 2, "missing": 0, "already_compliant": 1, "changed": 1, "skipped": 0},
        "account_changes": [{
            "id": 20, "scope": "openai",
            "observed_model_mapping": {"gpt-5.6": "gpt-5.6", "gpt-5.6-luna": "gpt-5.6-luna", "gpt-5.6-terra": "gpt-5.6-terra"},
            "desired_model_mapping": {"gpt-5.6": "gpt-5.6", "gpt-5.6-luna": "gpt-5.6-luna", "gpt-5.6-sol": "gpt-5.6-sol", "gpt-5.6-terra": "gpt-5.6-terra"},
            "diff": {"missing_keys": ["gpt-5.6-sol"], "forbidden_keys": [], "compatible_extra_keys": [], "bad_targets": [], "current_count": 3, "desired_count": 4},
        }],
        "group_changes": [],
    }
    sha_a = _compute_plan_sha256(plan_a)
    assert len(sha_a) == 64
    # Same plan repeated -> deterministic
    assert _compute_plan_sha256(plan_a) == sha_a
    # Different instance_id -> different sha (region/instance included in digest)
    plan_b = {**plan_a, "instance_id": "i-00000000000000099"}
    assert _compute_plan_sha256(plan_b) != sha_a
    # Different region -> different sha
    plan_c = {**plan_a, "region": "eu-west-1"}
    assert _compute_plan_sha256(plan_c) != sha_a
    # Different floor_sha256 -> different sha
    plan_e = {**plan_a, "floor_sha256": "e" * 64}
    assert _compute_plan_sha256(plan_e) != sha_a
    # Different found_ids -> different sha
    plan_f = copy.deepcopy(plan_a)
    plan_f["found_ids"] = [10, 20, 30]
    assert _compute_plan_sha256(plan_f) != sha_a
    # Change content -> different sha
    plan_d = copy.deepcopy(plan_a)
    plan_d["account_changes"][0]["desired_model_mapping"]["extra"] = "extra"
    assert _compute_plan_sha256(plan_d) != sha_a

    # --- Targeted apply: _render_targeted_apply_sql CAS structure ---
    targeted_sql_plan = {
        "target": "prod",
        "region": "us-east-1",
        "instance_id": "i-test",
        "targeted": True,
        "account_changes": [
            {
                "id": 20,
                "observed_model_mapping": {"old-key": "old-val"},
                "desired_model_mapping": {"new-key": "new-val"},
            },
            {
                "id": 30,
                "observed_model_mapping": None,  # absent/null
                "desired_model_mapping": {"grok": "grok-4.3", "grok-latest": "grok-4.3"},
            },
        ],
        "group_changes": [],
    }
    targeted_sql = _render_targeted_apply_sql(targeted_sql_plan)
    # Structure checks:
    assert "BEGIN;" in targeted_sql
    assert "COMMIT;" in targeted_sql
    assert "ROW_COUNT" in targeted_sql
    # Per-account CAS with COALESCE for observed
    assert "COALESCE(credentials->'model_mapping','null'::jsonb)" in targeted_sql
    # IDs in the SQL ordered by ID
    id20_pos = targeted_sql.find("id = 20")
    id30_pos = targeted_sql.find("id = 30")
    assert id20_pos < id30_pos, "SQL updates should be ordered by account ID"
    # Single merged outbox entry
    assert targeted_sql.count("'account_bulk_changed'") == 1
    # Outbox contains both IDs in a quoting-safe encoded payload.
    assert _json_b64({"account_ids": [20, 30]}) in targeted_sql
    # ROLLBACK on CAS miss
    assert "RAISE EXCEPTION" in targeted_sql
    # Null observed uses 'null'::jsonb comparison
    assert "'null'::jsonb" in targeted_sql
    # TARGETED_APPLY_OK sentinel
    assert "TARGETED_APPLY_OK" in targeted_sql

    # --- {} CAS preservation: empty dict observed is preserved as {} in SQL, not null ---
    empty_obs_plan = {
        "target": "prod",
        "region": "us-east-1",
        "instance_id": "i-test",
        "targeted": True,
        "account_changes": [
            {
                "id": 50,
                "observed_model_mapping": {},
                "desired_model_mapping": {"new-model": "new-model"},
            },
        ],
        "group_changes": [],
    }
    empty_obs_sql = _render_targeted_apply_sql(empty_obs_plan)
    # {} should encode as base64 of "{}" not "null"
    import base64 as _b64_test
    empty_obj_b64 = _b64_test.b64encode(b"{}").decode("ascii")
    assert empty_obj_b64 in empty_obs_sql, "empty {} observed must be preserved in CAS SQL"
    null_b64 = _b64_test.b64encode(b"null").decode("ascii")
    # null_b64 must NOT appear for this account (it has {}, not null)
    # Find the DO block for account 50
    block_50_start = empty_obs_sql.find("tk_targeted_50")
    block_50_end = empty_obs_sql.find("END $tk_targeted_50$")
    block_50 = empty_obs_sql[block_50_start:block_50_end]
    assert empty_obj_b64 in block_50
    assert null_b64 not in block_50, "{} observed must not encode as null"

    # --- _canonicalize_observed_mapping: {} preserved, non-dict coerced ---
    assert _canonicalize_observed_mapping({"model_mapping": {}}) == {}
    assert _canonicalize_observed_mapping({"model_mapping": None}) is None
    assert _canonicalize_observed_mapping({}) is None  # absent key
    assert _canonicalize_observed_mapping({"model_mapping": "not-a-dict"}) == "not-a-dict"
    assert _canonicalize_observed_mapping({"model_mapping": {"b": "2", "a": "1"}}) == {"a": "1", "b": "2"}

    # --- Empty string --account-ids rejection ---
    try:
        _parse_account_ids("")
    except ValueError:
        pass
    else:
        raise AssertionError("empty string --account-ids accepted")

    # --- Targeted apply: digest mismatch rejection ---
    # Parser accepts --account-ids and --expected-plan-sha256
    targeted_parser_args = parser.parse_args([
        "apply-accounts",
        "--target", "prod",
        "--account-ids", "10,20",
        "--expected-plan-sha256", "b" * 64,
        "--dry-run",
    ])
    assert targeted_parser_args.account_ids == "10,20"
    assert targeted_parser_args.expected_plan_sha256 == "b" * 64

    # Reject --account-ids with all-deployable-and-prod target
    original_resolve = globals()["_resolve_apply_targets"]
    globals()["_resolve_apply_targets"] = lambda target, **kw: [("prod", "r", "i-t")]
    original_load_effective_floor = globals()["_load_effective_floor"]
    globals()["_load_effective_floor"] = lambda _raw: floor
    original_run_check_sql_json = globals()["_run_check_sql_json"]
    globals()["_run_check_sql_json"] = lambda _r, _i, _l: {
        "runtime_setting": None,
        "accounts": targeted_accounts,
        "antigravity_groups": [],
    }
    try:
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            try:
                cmd_apply_accounts(argparse.Namespace(
                    target="all-deployable-and-prod",
                    account_ids="10",
                    expected_plan_sha256=None,
                    confirm=APPLY_CONFIRM,
                    dry_run=True,
                    prod_instance_id=None,
                    activation_floor_sha256=None,
                    bundle=None,
                    parallel=1,
                ))
            except SystemExit as e:
                assert e.code == 2, "should reject --account-ids with all-deployable-and-prod"
            else:
                raise AssertionError("--account-ids with all-deployable-and-prod was accepted")
    finally:
        globals()["_resolve_apply_targets"] = original_resolve
        globals()["_load_effective_floor"] = original_load_effective_floor
        globals()["_run_check_sql_json"] = original_run_check_sql_json

    # --- Targeted apply: cmd-level digest mismatch with SSM mocked (no write call) ---
    ssm_calls: list[str] = []
    original_ssm = globals()["_ssm_run_shell_b64_region"]
    globals()["_ssm_run_shell_b64_region"] = lambda r, i, b, desc: ssm_calls.append(desc) or "TARGETED_APPLY_OK"
    original_resolve2 = globals()["_resolve_apply_targets"]
    globals()["_resolve_apply_targets"] = lambda target, **kw: [("prod", "us-east-1", "i-prod")]
    original_load_effective_floor2 = globals()["_load_effective_floor"]
    globals()["_load_effective_floor"] = lambda _raw: floor
    original_run_check_sql_json2 = globals()["_run_check_sql_json"]
    globals()["_run_check_sql_json"] = lambda _r, _i, _l: {
        "runtime_setting": None,
        "accounts": targeted_accounts,
        "antigravity_groups": [],
    }
    try:
        ssm_calls.clear()
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            try:
                cmd_apply_accounts(argparse.Namespace(
                    target="prod",
                    account_ids="20",
                    expected_plan_sha256="wrong" * 13,  # 65 chars, definitely wrong
                    confirm=APPLY_CONFIRM,
                    dry_run=False,
                    prod_instance_id=None,
                    activation_floor_sha256=None,
                    bundle=None,
                    parallel=1,
                ))
            except SystemExit as e:
                assert e.code == 2, f"digest mismatch should exit 2, got {e.code}"
            else:
                raise AssertionError("digest mismatch was not rejected")
        assert len(ssm_calls) == 0, "SSM should NOT be called when digest mismatches"
    finally:
        globals()["_ssm_run_shell_b64_region"] = original_ssm
        globals()["_resolve_apply_targets"] = original_resolve2
        globals()["_load_effective_floor"] = original_load_effective_floor2
        globals()["_run_check_sql_json"] = original_run_check_sql_json2

    # --- Targeted apply: digest changes on region/instance/floor/found state ---
    plan_base = {
        "target": "prod",
        "region": "us-east-1",
        "instance_id": "i-00000000000000001",
        "floor_sha256": "a" * 64,
        "targeted": True,
        "requested_ids": [10, 20],
        "found_ids": [10, 20],
        "missing_ids": [],
        "already_compliant_ids": [10],
        "changed_ids": [20],
        "account_changes": [{
            "id": 20,
            "observed_model_mapping": {"k": "v"},
            "desired_model_mapping": {"k": "v", "k2": "v2"},
            "diff": {"missing_keys": ["k2"], "forbidden_keys": [], "compatible_extra_keys": [], "bad_targets": [], "current_count": 1, "desired_count": 2},
        }],
        "group_changes": [],
    }
    sha_base = _compute_plan_sha256(plan_base)
    # Different region -> different sha
    plan_diff_region = {**plan_base, "region": "eu-west-1"}
    assert _compute_plan_sha256(plan_diff_region) != sha_base, "region must affect digest"
    # Different instance_id -> different sha
    plan_diff_instance = {**plan_base, "instance_id": "i-99999"}
    assert _compute_plan_sha256(plan_diff_instance) != sha_base, "instance_id must affect digest"
    # Different floor_sha256 -> different sha
    plan_diff_floor = {**plan_base, "floor_sha256": "b" * 64}
    assert _compute_plan_sha256(plan_diff_floor) != sha_base, "floor_sha256 must affect digest"
    # Different found_ids -> different sha
    plan_diff_found = {**plan_base, "found_ids": [20]}
    assert _compute_plan_sha256(plan_diff_found) != sha_base, "found_ids must affect digest"
    # Different already_compliant_ids -> different sha
    plan_diff_compliant = {**plan_base, "already_compliant_ids": []}
    assert _compute_plan_sha256(plan_diff_compliant) != sha_base, "already_compliant_ids must affect digest"
    # Different missing_ids -> different sha
    plan_diff_missing = {**plan_base, "missing_ids": [99]}
    assert _compute_plan_sha256(plan_diff_missing) != sha_base, "missing_ids must affect digest"

    # --- Targeted apply: compatible-extra preservation in desired ---
    # _account_plan reconciles: keeps extras not in forbidden_keys, adds wanted keys
    # This is implicitly tested by _account_plan returning reconciled mapping.
    # Explicitly test that _collect_apply_plan preserves compatible extras in the plan.
    compatible_extra_accounts = [
        {"id": 40, "name": "acc-40", "platform": "openai", "type": "oauth",
         "model_mapping": {"gpt-5.6": "gpt-5.6", "gpt-5.6-luna": "gpt-5.6-luna",
                           "gpt-5.6-terra": "gpt-5.6-terra",
                           "custom-extra": "custom-target"}},  # missing sol, has compatible extra
    ]
    globals()["_run_check_sql_json"] = lambda _r, _i, _l: {
        "runtime_setting": None,
        "accounts": compatible_extra_accounts,
        "antigravity_groups": [],
    }
    globals()["_load_effective_floor"] = lambda _raw: floor
    try:
        compat_plan = _collect_apply_plan("prod", "us-east-1", "i-test", account_ids=[40])
        assert compat_plan["changed_ids"] == [40]
        change = compat_plan["account_changes"][0]
        # desired_model_mapping should contain both the floor keys AND the compatible extra
        assert "custom-extra" in change["desired_model_mapping"], \
            "compatible extras must be preserved in desired_model_mapping"
        assert change["desired_model_mapping"]["custom-extra"] == "custom-target"
        assert "gpt-5.6-sol" in change["desired_model_mapping"], \
            "missing floor key must be added"
    finally:
        globals()["_run_check_sql_json"] = original_run_check_sql_json2
        globals()["_load_effective_floor"] = original_load_effective_floor2

    print("selftest ok")
    return 0


def _build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--selftest", action="store_true")
    sub = ap.add_subparsers(dest="cmd")
    sp = sub.add_parser("validate", help="validate and print canonical JSON")
    sp.add_argument("--file", type=Path, required=True)
    sp.add_argument("--bundle", help="generated model-surface bundle to validate against")
    sp = sub.add_parser("check", help="compare runtime settings to a JSON file")
    sp.add_argument("--file", type=Path, required=True)
    sp.add_argument("--target", default=DEFAULT_RUNTIME_TARGET, help="prod, edge:<id>, or all-deployable-and-prod")
    sp.add_argument("--json", action="store_true", help="machine-readable output")
    sp.add_argument("--parallel", type=int, default=6, help="parallel SSM workers")
    sp = sub.add_parser("check-accounts", help="post-release read-only account model_mapping SSOT diff (prod only by default)")
    sp.add_argument("--json", action="store_true", help="machine-readable output")
    sp.add_argument("--skip-prod", action="store_true", help="check deployable edges only")
    sp.add_argument("--include-edges", action="store_true", help="also check deployable edges (default: prod only)")
    sp.add_argument("--prod-instance-id", help="use this prod EC2 instance id instead of resolving the prod stack")
    sp.add_argument("--bundle", help="generated model-surface bundle to check against")
    sp.add_argument("--parallel", type=int, default=6, help="parallel SSM workers")
    sp = sub.add_parser(
        "release-gate",
        help="explicit model-activation check: prod account model_mapping must cover the selected bundle floor",
    )
    sp.add_argument("--json", action="store_true", help="machine-readable output")
    sp.add_argument("--prod-instance-id", help="use this prod EC2 instance id instead of resolving the prod stack")
    sp.add_argument("--bundle", help="generated model-surface bundle to check against")
    sp.add_argument("--parallel", type=int, default=6, help="parallel SSM workers")
    sp = sub.add_parser("apply-accounts", help="explicitly apply reviewed SSOT diffs to live accounts")
    sp.add_argument("--target", required=True, help="prod, edge:<id>, or all-deployable-and-prod")
    sp.add_argument("--account-ids", help="comma-separated positive account IDs for targeted apply (single explicit target only)")
    sp.add_argument("--expected-plan-sha256", help="required in targeted mode for non-dry-run; must match recomputed plan digest")
    sp.add_argument("--confirm", help=f"required for writes: {APPLY_CONFIRM}")
    sp.add_argument("--dry-run", action="store_true", help="print the planned account/group changes without writing")
    sp.add_argument("--prod-instance-id", help="pin prod planning and apply to this EC2 instance id")
    sp.add_argument("--activation-floor-sha256", help=argparse.SUPPRESS)
    sp.add_argument("--bundle", help="generated model-surface bundle to apply")
    sp.add_argument("--parallel", type=int, default=3, help="parallel SSM workers")
    sp = sub.add_parser(
        "assign-vertex-profiles",
        help="guardedly assign ch41 capability profiles on prod without changing model_mapping",
    )
    sp.add_argument("--file", type=Path, required=True)
    sp.add_argument("--confirm", help=f"required for writes: {VERTEX_PROFILE_ASSIGN_CONFIRM}")
    sp.add_argument("--dry-run", action="store_true", help="verify selectors and print profile-only changes")
    sp.add_argument("--prod-instance-id", help="pin prod planning and apply to this EC2 instance id")
    sp.add_argument("--bundle", help="generated model-surface bundle whose profile names are allowed")
    sp = sub.add_parser("sync-runtime", help="hot-push a JSON file to runtime settings")
    sp.add_argument("--file", type=Path, required=True)
    sp.add_argument("--target", default=DEFAULT_RUNTIME_TARGET, help="prod, edge:<id>, or all-deployable-and-prod")
    sp.add_argument("--dry-run", action="store_true")
    sp.add_argument("--bundle", help="generated model-surface bundle to validate against")
    sp.add_argument("--parallel", type=int, default=3, help="parallel SSM workers")
    sp = sub.add_parser("clear-runtime", help="delete runtime override and use compiled floor")
    sp.add_argument("--target", default=DEFAULT_RUNTIME_TARGET, help="prod, edge:<id>, or all-deployable-and-prod")
    sp.add_argument("--dry-run", action="store_true")
    sp.add_argument("--parallel", type=int, default=3, help="parallel SSM workers")
    sub.add_parser("example", help="print an example runtime JSON")
    return ap


def main() -> int:
    ap = _build_parser()
    args = ap.parse_args()
    if args.selftest:
        return cmd_selftest(args)
    if args.cmd == "validate":
        return cmd_validate(args)
    if args.cmd == "check":
        return cmd_check(args)
    if args.cmd == "check-accounts":
        return cmd_check_accounts(args)
    if args.cmd == "release-gate":
        return cmd_release_gate(args)
    if args.cmd == "apply-accounts":
        return cmd_apply_accounts(args)
    if args.cmd == "assign-vertex-profiles":
        return cmd_assign_vertex_profiles(args)
    if args.cmd == "sync-runtime":
        return cmd_sync_runtime(args)
    if args.cmd == "clear-runtime":
        return cmd_clear_runtime(args)
    if args.cmd == "example":
        return cmd_example(args)
    ap.print_help()
    return 2


if __name__ == "__main__":
    sys.exit(main())
