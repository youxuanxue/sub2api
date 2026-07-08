#!/usr/bin/env python3
"""Manage the TK account model_mapping runtime layer and explicit account apply.

The compiled floor lives in Go:
  - native platforms: supported*CatalogModels + pricing/display gates
  - newapi: tk_served_models.json display projection
  - aliases: DefaultAntigravityModelMapping / xai.DefaultModelMapping

This tool writes an optional runtime replacement layer to settings key
``tk_account_model_mapping_runtime``. A present scope REPLACES the compiled floor
for that platform or newapi channel_type; absent scopes keep the compiled floor.
Writing the setting does not mutate accounts. Use ``check-accounts`` to diff
live accounts against the Go SSOT, then ``apply-accounts --confirm ...`` when
an operator has reviewed the diff and wants to overwrite persisted mappings.

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
import contextlib
import gzip
import importlib.util
import io
import json
import subprocess
import sys
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Any, NoReturn

REPO_ROOT = Path(__file__).resolve().parents[2]
SETTING_KEY = "tk_account_model_mapping_runtime"
MANAGED_PLATFORMS = ("anthropic", "openai", "gemini", "antigravity", "newapi", "kiro", "grok")

ANTIGRAVITY_CANONICAL_SCOPES = {"claude", "gemini_text", "gemini_image"}
ANTIGRAVITY_LIVE_CLAUDE_MAPPING = {
    "claude-sonnet-4-6": "claude-sonnet-4-6",
    "claude-opus-4-6": "claude-opus-4-6-thinking",
    "claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
}
ANTIGRAVITY_STRUCTURAL_DEAD_MODEL_MAPPING_KEYS = {
    "gemini-2.5-flash-image-preview",
    "gemini-3-flash-preview",
    "gemini-3-pro-high",
    "gemini-3-pro-image-preview",
    "gemini-3-pro-low",
    "gemini-3-pro-preview",
    "gemini-3.1-pro-high",
    "gemini-3.1-pro-preview",
}
ANTIGRAVITY_UNPRICED_MODEL_MAPPING_KEYS = {"tab_flash_lite_preview"}
GROK_REQUIRED_ALIASES = {
    "grok": "grok-4.3",
    "grok-latest": "grok-4.3",
    "grok-build": "grok-build-0.1",
    "grok-4.3-latest": "grok-4.3",
    "grok-4-fast-reasoning": "grok-4.3",
    "grok-4.20-reasoning": "grok-4.20-0309-reasoning",
    "grok-4.20-non-reasoning": "grok-4.20-0309-non-reasoning",
    "grok-code-fast": "grok-build-0.1",
    "grok-code-fast-1-0825": "grok-build-0.1",
}
KIRO_REQUIRED_MODELS = {"claude-sonnet-4-5", "claude-sonnet-5"}

PSQL = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1"
REDISCLI = "env -u REDISCLI_AUTH sudo docker exec tokenkey-redis redis-cli"
APPLY_CONFIRM = "yes-apply-account-model-mapping"
GO_HELPER = ["go", "run", "./cmd/account-model-mapping", "floors", "--runtime-json", "-"]

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
      'auth_mode', a.credentials->>'auth_mode'
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
    return json.dumps(doc, sort_keys=True, ensure_ascii=False, separators=(",", ":"))


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


def read_runtime_blob(instance_id: str) -> dict:
    shell = (
        f"{PSQL} -c \"SELECT value FROM settings WHERE key='{SETTING_KEY}';\""
        " | gzip -c | base64 | tr -d '\\n'"
    )
    b64 = base64.b64encode(shell.encode()).decode()
    out = _SSM.run_shell_b64(instance_id, b64, "account model_mapping runtime: read settings")
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


def _resolve_check_targets(skip_prod: bool) -> list[tuple[str, str, str]]:
    ec2_matrix = _ROUTING.load_matrix(REPO_ROOT / "deploy/aws/stage0/edge-targets.json")
    ls_targets = _ROUTING.load_lightsail_targets(REPO_ROOT)
    targets: list[tuple[str, str, str]] = []
    for eid in _ROUTING.iter_effective_deployable_edge_ids(ec2_matrix, ls_targets):
        ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, eid)
        targets.append((f"edge:{eid}", ident.region, ident.instance_id))
    if not skip_prod:
        targets.append(("prod", _SSM.PROD_REGION, _SSM.resolve_prod_instance()))
    return targets


def _resolve_single_edge_target(edge_id: str) -> tuple[str, str, str]:
    ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, edge_id)
    return (f"edge:{edge_id}", ident.region, ident.instance_id)


def _resolve_apply_targets(target: str) -> list[tuple[str, str, str]]:
    target = target.strip().lower()
    if target == "prod":
        return [("prod", _SSM.PROD_REGION, _SSM.resolve_prod_instance())]
    if target.startswith("edge:"):
        edge_id = target.split(":", 1)[1].strip()
        if not edge_id:
            fail("--target edge:<id> requires an edge id")
        return [_resolve_single_edge_target(edge_id)]
    if target in {"all", "all-deployable-and-prod"}:
        return _resolve_check_targets(skip_prod=False)
    fail("--target must be prod, edge:<id>, or all-deployable-and-prod")


_FLOOR_CACHE: dict[str, dict[str, Any]] = {}
_FLOOR_LOCK = threading.Lock()


def _runtime_cache_key(raw: Any) -> str:
    if raw is None or str(raw).strip() == "":
        return ""
    try:
        doc = normalize_runtime_doc(json.loads(str(raw)))
    except SystemExit as e:
        raise ValueError(f"invalid {SETTING_KEY} document (exit {e.code})") from e
    return canonical_json(doc)


def _load_effective_floor(runtime_raw: Any) -> dict[str, Any]:
    key = _runtime_cache_key(runtime_raw)
    with _FLOOR_LOCK:
        cached = _FLOOR_CACHE.get(key)
        if cached is not None:
            return cached

    proc = subprocess.run(
        GO_HELPER,
        cwd=REPO_ROOT / "backend",
        input=key,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            "Go account model_mapping SSOT helper failed: "
            + (proc.stderr or proc.stdout).strip()[:1600]
        )
    try:
        floor = json.loads(proc.stdout)
    except json.JSONDecodeError as e:
        raise RuntimeError(f"Go account model_mapping SSOT helper emitted invalid JSON: {e}") from e
    if not isinstance(floor.get("platforms"), dict):
        raise RuntimeError("Go account model_mapping SSOT helper omitted platforms")
    if not isinstance(floor.get("newapi_channel_types"), dict):
        raise RuntimeError("Go account model_mapping SSOT helper omitted newapi_channel_types")
    with _FLOOR_LOCK:
        _FLOOR_CACHE[key] = floor
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


def _account_scope(row: dict[str, Any]) -> str:
    if _is_kiro_scope(row):
        return "kiro"
    platform = str(row.get("platform") or "").strip().lower()
    if platform == "anthropic" and str(row.get("type") or "") == "bedrock":
        return "bedrock"
    return platform


def _account_invariant_violations(row: dict[str, Any]) -> list[str]:
    scope = _account_scope(row)
    mm, err = _model_mapping(row)
    if err:
        return [f"{err} (scope={scope})"]
    reasons: list[str] = []

    if scope == "antigravity":
        missing = sorted(k for k in ANTIGRAVITY_LIVE_CLAUDE_MAPPING if k not in mm)
        if missing:
            reasons.append("missing live Antigravity Claude keys: " + ", ".join(missing))
        bad_targets = sorted(
            k for k, want in ANTIGRAVITY_LIVE_CLAUDE_MAPPING.items()
            if k in mm and mm[k] != want
        )
        if bad_targets:
            reasons.append("bad Antigravity Claude remaps: " + ", ".join(
                f"{k}->{mm[k]!r} want {ANTIGRAVITY_LIVE_CLAUDE_MAPPING[k]!r}" for k in bad_targets
            ))
        leaked = sorted(
            k for k in mm
            if (k.startswith("claude-") and k not in ANTIGRAVITY_LIVE_CLAUDE_MAPPING)
            or k.startswith("gpt-oss-")
        )
        if leaked:
            reasons.append("serves unsupported Antigravity models: " + ", ".join(leaked))
        stale = sorted(k for k in mm if k in ANTIGRAVITY_STRUCTURAL_DEAD_MODEL_MAPPING_KEYS)
        if stale:
            reasons.append("contains structural-dead Antigravity aliases: " + ", ".join(stale))
        unpriced = sorted(k for k in mm if k in ANTIGRAVITY_UNPRICED_MODEL_MAPPING_KEYS)
        if unpriced:
            reasons.append("contains unpriced Antigravity $0-risk models: " + ", ".join(unpriced))

    if scope == "grok":
        missing_aliases = sorted(k for k in GROK_REQUIRED_ALIASES if k not in mm)
        if missing_aliases:
            reasons.append("missing Grok compatibility aliases: " + ", ".join(missing_aliases))
        bad_aliases = sorted(k for k, want in GROK_REQUIRED_ALIASES.items() if k in mm and mm[k] != want)
        if bad_aliases:
            reasons.append("bad Grok alias remaps: " + ", ".join(
                f"{k}->{mm[k]!r} want {GROK_REQUIRED_ALIASES[k]!r}" for k in bad_aliases
            ))

    if scope == "kiro":
        missing_kiro = sorted(k for k in KIRO_REQUIRED_MODELS if k not in mm)
        if missing_kiro:
            reasons.append("missing Kiro required model keys: " + ", ".join(missing_kiro))

    return reasons


def _desired_mapping_for_account(row: dict[str, Any], floor: dict[str, Any]) -> tuple[dict[str, str] | None, str]:
    scope = _account_scope(row)
    if scope == "newapi":
        ct = str(row.get("channel_type") or "").strip()
        mapping = (floor.get("newapi_channel_types") or {}).get(ct)
        return mapping if isinstance(mapping, dict) else None, f"newapi_channel_type:{ct or '0'}"
    mapping = (floor.get("platforms") or {}).get(scope)
    return mapping if isinstance(mapping, dict) else None, scope


def _mapping_diff(got: dict[str, str], want: dict[str, str]) -> dict[str, Any]:
    missing = sorted(k for k in want if k not in got)
    extra = sorted(k for k in got if k not in want)
    bad = sorted(k for k in want if k in got and got[k] != want[k])
    return {
        "missing_keys": missing,
        "extra_keys": extra,
        "bad_targets": [
            {"key": k, "got": got[k], "want": want[k]}
            for k in bad
        ],
        "current_count": len(got),
        "desired_count": len(want),
    }


def _has_mapping_diff(diff: dict[str, Any]) -> bool:
    return bool(diff["missing_keys"] or diff["extra_keys"] or diff["bad_targets"])


def _short_list(values: list[Any], limit: int = 8) -> str:
    if len(values) <= limit:
        return ", ".join(str(v) for v in values)
    return ", ".join(str(v) for v in values[:limit]) + f", ... (+{len(values) - limit})"


def _format_mapping_diff_reason(scope: str, diff: dict[str, Any]) -> str:
    parts = [f"model_mapping differs from SSOT (scope={scope})"]
    if diff["missing_keys"]:
        parts.append("missing: " + _short_list(diff["missing_keys"]))
    if diff["extra_keys"]:
        parts.append("extra: " + _short_list(diff["extra_keys"]))
    if diff["bad_targets"]:
        bad = [f"{b['key']}->{b['got']!r} want {b['want']!r}" for b in diff["bad_targets"]]
        parts.append("bad_targets: " + _short_list(bad))
    parts.append(f"count current={diff['current_count']} desired={diff['desired_count']}")
    return "; ".join(parts)


def _account_plan(row: dict[str, Any], floor: dict[str, Any]) -> dict[str, Any] | None:
    want, scope = _desired_mapping_for_account(row, floor)
    if not want:
        return None
    got, err = _model_mapping(row)
    if err:
        diff = {
            "missing_keys": sorted(want),
            "extra_keys": [],
            "bad_targets": [],
            "current_count": 0,
            "desired_count": len(want),
        }
        reason = f"{err}; will replace with SSOT (scope={scope}, desired_count={len(want)})"
    else:
        diff = _mapping_diff(got, want)
        if not _has_mapping_diff(diff):
            invariants = _account_invariant_violations(row)
            if not invariants:
                return None
            reason = "; ".join(invariants)
        else:
            reason = _format_mapping_diff_reason(scope, diff)
    return {
        "kind": "account",
        "id": row.get("id"),
        "name": row.get("name"),
        "platform": row.get("platform"),
        "type": row.get("type"),
        "scope": scope,
        "reason": reason,
        "diff": diff,
        "desired_model_mapping": dict(sorted(want.items())),
    }


def _group_violation(row: dict[str, Any]) -> str | None:
    scopes = row.get("scopes")
    if not scopes or not isinstance(scopes, list):
        return "empty/missing supported_model_scopes"
    got = {str(s).strip() for s in scopes}
    if got == ANTIGRAVITY_CANONICAL_SCOPES:
        return None
    parts: list[str] = []
    extra = sorted(got - ANTIGRAVITY_CANONICAL_SCOPES)
    missing = sorted(ANTIGRAVITY_CANONICAL_SCOPES - got)
    if extra:
        parts.append("unexpected: " + ", ".join(extra))
    if missing:
        parts.append("missing: " + ", ".join(missing))
    return "scopes not canonical (" + "; ".join(parts) + ")"


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
            normalize_runtime_doc(doc)
        except SystemExit:
            return (stderr.getvalue() or f"invalid {SETTING_KEY} document").strip()
    return None


def _check_target(label: str, region: str, instance_id: str) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    bundle = _run_check_sql_json(region, instance_id, label)
    violations: list[dict[str, Any]] = []
    runtime_reason = _runtime_setting_violation(bundle.get("runtime_setting"))
    if runtime_reason:
        violations.append({
            "target": label,
            "kind": "runtime_setting",
            "id": SETTING_KEY,
            "name": SETTING_KEY,
            "reason": runtime_reason,
        })
        return violations, []
    floor = _load_effective_floor(bundle.get("runtime_setting"))
    for row in bundle.get("accounts") or []:
        plan = _account_plan(row, floor)
        if not plan:
            for reason in _account_invariant_violations(row):
                violations.append({
                    "target": label,
                    "kind": "account",
                    "id": row.get("id"),
                    "name": row.get("name"),
                    "platform": row.get("platform"),
                    "type": row.get("type"),
                    "scope": _account_scope(row),
                    "reason": reason,
                })
            continue
        plan["target"] = label
        violations.append(plan)
    for row in bundle.get("antigravity_groups") or []:
        reason = _group_violation(row)
        if reason:
            violations.append({
                "target": label,
                "kind": "group",
                "id": row.get("id"),
                "name": row.get("name"),
                "platform": "antigravity",
                "reason": reason,
            })
    return violations, []


def _collect_apply_plan(label: str, region: str, instance_id: str) -> dict[str, Any]:
    bundle = _run_check_sql_json(region, instance_id, label)
    runtime_reason = _runtime_setting_violation(bundle.get("runtime_setting"))
    if runtime_reason:
        raise RuntimeError(runtime_reason)
    floor = _load_effective_floor(bundle.get("runtime_setting"))
    account_changes: list[dict[str, Any]] = []
    for row in bundle.get("accounts") or []:
        plan = _account_plan(row, floor)
        if plan and plan.get("desired_model_mapping"):
            plan["target"] = label
            account_changes.append(plan)

    group_changes: list[dict[str, Any]] = []
    desired_scopes = list(floor.get("antigravity_group_scopes") or sorted(ANTIGRAVITY_CANONICAL_SCOPES))
    for row in bundle.get("antigravity_groups") or []:
        reason = _group_violation(row)
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
    return {
        "target": label,
        "region": region,
        "instance_id": instance_id,
        "account_changes": sorted(account_changes, key=lambda p: int(p.get("id") or 0)),
        "group_changes": sorted(group_changes, key=lambda p: int(p.get("id") or 0)),
    }


def _ids_sql(ids: list[int]) -> str:
    clean = sorted({int(i) for i in ids if int(i) > 0})
    if not clean:
        raise ValueError("empty id list")
    return ",".join(str(i) for i in clean)


def _json_b64(doc: Any) -> str:
    raw = json.dumps(doc, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return base64.b64encode(raw).decode("ascii")


def _render_apply_sql(plan: dict[str, Any]) -> str:
    lines = [
        "BEGIN;",
        "SET LOCAL statement_timeout = '30s';",
    ]

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
        scopes = group_changes[0].get("desired_supported_model_scopes") or sorted(ANTIGRAVITY_CANONICAL_SCOPES)
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
        "tmp=/tmp/tk_account_model_mapping_apply_$$.sql\n"
        f"echo {sql_b64} | base64 -d > \"$tmp\"\n"
        "$PSQL -f \"$tmp\"\n"
        "rm -f \"$tmp\"\n"
    )
    return _ssm_run_shell_b64_region(
        plan["region"],
        plan["instance_id"],
        base64.b64encode(shell.encode("utf-8")).decode("ascii"),
        f"account model_mapping explicit apply {plan['target']}",
    )


def cmd_validate(args) -> int:
    doc = load_doc(args.file)
    print(canonical_json(doc))
    return 0


def cmd_check(args) -> int:
    want = load_doc(args.file)
    inst = _SSM.resolve_prod_instance()
    got = read_runtime_blob(inst)
    if canonical_json(got) == canonical_json(want):
        print("OK: prod account model_mapping runtime matches file.")
        return 0
    print("DRIFT: prod account model_mapping runtime differs from file.")
    print(f"file scopes: {list(want.keys())}")
    print(f"prod scopes: {list(got.keys())}")
    return 1


def cmd_check_accounts(args) -> int:
    targets = _resolve_check_targets(args.skip_prod)
    violations: list[dict[str, Any]] = []
    errors: list[dict[str, Any]] = []
    with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
        futs = {ex.submit(_check_target, *t): t for t in targets}
        for fut in as_completed(futs):
            label = futs[fut][0]
            try:
                got_violations, got_errors = fut.result()
                violations.extend(got_violations)
                errors.extend(got_errors)
            except Exception as e:  # noqa: BLE001 - SSM failures should report all reachable targets.
                errors.append({"target": label, "error": str(e)})

    def sort_key(v: dict[str, Any]) -> tuple[str, str, str]:
        return (str(v.get("target") or ""), str(v.get("kind") or ""), str(v.get("id") or ""))

    report = {
        "targets": [t[0] for t in targets],
        "target_count": len(targets),
        "managed_platforms": list(MANAGED_PLATFORMS),
        "violation_count": len(violations),
        "error_count": len(errors),
        "violations": sorted(violations, key=sort_key),
        "errors": sorted(errors, key=lambda e: str(e.get("target") or "")),
    }
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
        print(f"FAIL: {len(violations)} account model_mapping violation(s) across {len(targets)} target(s):")
        for v in report["violations"]:
            print(f"  [{v['target']}] {v['kind']} {v.get('id')} ({v.get('name')}): {v['reason']}")
    else:
        print(f"OK: all managed account model_mapping scopes explicit across {len(targets)} target(s).")
    if errors:
        return 2
    return 1 if violations else 0


def cmd_apply_accounts(args) -> int:
    if not args.dry_run and args.confirm != APPLY_CONFIRM:
        fail(f"apply-accounts requires --confirm {APPLY_CONFIRM}")
    targets = _resolve_apply_targets(args.target)
    plans: list[dict[str, Any]] = []
    errors: list[dict[str, Any]] = []
    with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
        futs = {ex.submit(_collect_apply_plan, *t): t for t in targets}
        for fut in as_completed(futs):
            label = futs[fut][0]
            try:
                plans.append(fut.result())
            except Exception as e:  # noqa: BLE001 - collect all target planning failures before any write.
                errors.append({"target": label, "error": str(e)})

    plans.sort(key=lambda p: str(p.get("target") or ""))
    report = {
        "targets": [p["target"] for p in plans],
        "target_count": len(targets),
        "account_change_count": sum(len(p.get("account_changes") or []) for p in plans),
        "group_change_count": sum(len(p.get("group_changes") or []) for p in plans),
        "plans": plans,
        "errors": sorted(errors, key=lambda e: str(e.get("target") or "")),
    }
    if errors:
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 2
    if args.dry_run:
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 0
    if report["account_change_count"] == 0 and report["group_change_count"] == 0:
        print(json.dumps(report, ensure_ascii=False, indent=2))
        print("apply-accounts: no changes.")
        return 0

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
        "applied": sorted(applied, key=lambda p: str(p.get("target") or "")),
        "errors": sorted(apply_errors, key=lambda e: str(e.get("target") or "")),
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 2 if apply_errors else 0


def _publish_settings_updated(instance_id: str, sql: str, comment: str) -> str:
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
    return _SSM.run_shell_b64(instance_id, b64, comment)


def cmd_sync_runtime(args) -> int:
    doc = load_doc(args.file)
    payload = canonical_json(doc).encode("utf-8")
    if args.dry_run:
        print(f"DRY-RUN: would UPSERT settings[{SETTING_KEY}] on prod "
              f"({len(payload)} bytes, scopes={list(doc.keys())}) + PUBLISH settings_updated reload.")
        return 0
    inst = _SSM.resolve_prod_instance()
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
    out = _SSM.run_shell_b64(inst, b64, "account model_mapping runtime: upsert + publish")
    print(out)
    if "UPDATE_OK" not in out:
        fail("settings update did not report success")
    if canonical_json(read_runtime_blob(inst)) != canonical_json(doc):
        fail("post-sync verify shows prod runtime does not match file")
    print("synced + verified: prod account model_mapping runtime == file.")
    return 0


def cmd_clear_runtime(args) -> int:
    if args.dry_run:
        print(f"DRY-RUN: would DELETE settings[{SETTING_KEY}] on prod + PUBLISH settings_updated reload.")
        return 0
    inst = _SSM.resolve_prod_instance()
    sql = f"DELETE FROM settings WHERE key='{SETTING_KEY}';"
    out = _publish_settings_updated(inst, sql, "account model_mapping runtime: clear + publish")
    print(out)
    if "UPDATE_OK" not in out:
        fail("settings delete did not report success")
    print("cleared: future check/apply uses the compiled account model_mapping floor.")
    return 0


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
    assert _account_invariant_violations({
        "platform": "openai",
        "type": "oauth",
        "model_mapping": {},
    })
    assert not _account_invariant_violations({
        "platform": "grok",
        "type": "oauth",
        "model_mapping": {**GROK_REQUIRED_ALIASES, "grok-4.3": "grok-4.3"},
    })
    assert _account_invariant_violations({
        "platform": "grok",
        "type": "oauth",
        "model_mapping": {"grok": "grok-4.3"},
    })
    assert _account_invariant_violations({
        "platform": "antigravity",
        "type": "oauth",
        "model_mapping": {**ANTIGRAVITY_LIVE_CLAUDE_MAPPING, "claude-sonnet-5": "claude-sonnet-5"},
    })
    assert not _account_invariant_violations({
        "platform": "kiro",
        "type": "oauth",
        "model_mapping": {"claude-sonnet-4-5": "claude-sonnet-4-5", "claude-sonnet-5": "claude-sonnet-5"},
    })
    assert _account_invariant_violations({
        "platform": "anthropic",
        "type": "apikey",
        "name": "kiro-us6",
        "base_url": "https://api-us6.tokenkey.dev",
        "model_mapping": {"claude-sonnet-4-5": "claude-sonnet-4-5"},
    })
    plan = _account_plan(
        {"id": 1, "platform": "grok", "type": "oauth", "model_mapping": {"grok": "grok-4.3"}},
        {"platforms": {"grok": {**GROK_REQUIRED_ALIASES, "grok-4.3": "grok-4.3"}}, "newapi_channel_types": {}},
    )
    assert plan and plan["desired_model_mapping"]["grok-latest"] == "grok-4.3"
    sql = _render_apply_sql({
        "account_changes": [plan],
        "group_changes": [{"id": 7, "desired_supported_model_scopes": ["claude", "gemini_text", "gemini_image"]}],
    })
    assert "account_bulk_changed" in sql and "group_changed" in sql
    assert _group_violation({"scopes": ["gemini_text"]})
    assert _group_violation({"scopes": ["claude", "gemini_text", "gemini_image"]}) is None
    assert _runtime_setting_violation('{"platforms":{"grok":{}}}')
    assert _runtime_setting_violation('{"platforms":{"grok":{"grok":"grok-4.3"}}}') is None
    print("selftest ok")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--selftest", action="store_true")
    sub = ap.add_subparsers(dest="cmd")
    sp = sub.add_parser("validate", help="validate and print canonical JSON")
    sp.add_argument("--file", type=Path, required=True)
    sp = sub.add_parser("check", help="compare prod runtime settings to a JSON file")
    sp.add_argument("--file", type=Path, required=True)
    sp = sub.add_parser("check-accounts", help="post-release read-only account model_mapping SSOT diff")
    sp.add_argument("--json", action="store_true", help="machine-readable output")
    sp.add_argument("--skip-prod", action="store_true", help="check deployable edges only")
    sp.add_argument("--parallel", type=int, default=6, help="parallel SSM workers")
    sp = sub.add_parser("apply-accounts", help="explicitly apply reviewed SSOT diffs to live accounts")
    sp.add_argument("--target", required=True, help="prod, edge:<id>, or all-deployable-and-prod")
    sp.add_argument("--confirm", help=f"required for writes: {APPLY_CONFIRM}")
    sp.add_argument("--dry-run", action="store_true", help="print the planned account/group changes without writing")
    sp.add_argument("--parallel", type=int, default=3, help="parallel SSM workers")
    sp = sub.add_parser("sync-runtime", help="hot-push a JSON file to prod settings only")
    sp.add_argument("--file", type=Path, required=True)
    sp.add_argument("--dry-run", action="store_true")
    sp = sub.add_parser("clear-runtime", help="delete prod runtime override and use compiled floor")
    sp.add_argument("--dry-run", action="store_true")
    sub.add_parser("example", help="print an example runtime JSON")
    args = ap.parse_args()
    if args.selftest:
        return cmd_selftest(args)
    if args.cmd == "validate":
        return cmd_validate(args)
    if args.cmd == "check":
        return cmd_check(args)
    if args.cmd == "check-accounts":
        return cmd_check_accounts(args)
    if args.cmd == "apply-accounts":
        return cmd_apply_accounts(args)
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
