#!/usr/bin/env python3
"""Edge OAuth pool + prod mirror stub onboard (create + probe).

Evidence-backed path today: ``antigravity``. Other platforms are profile placeholders.

Typical flow:
  ./edge-prod-relay-onboard.sh plan  --platform antigravity --edge uk3
  ./edge-prod-relay-onboard.sh apply --platform antigravity --edge uk3 --yes
  ./edge-prod-relay-onboard.sh probe --platform antigravity --edge uk3
  ./edge-prod-relay-onboard.sh all   --platform antigravity --edge uk1 --edge uk2 --yes
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parents[1]
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

from edge_relay import (  # noqa: E402
    edge_base_url,
    find_prod_relay_stub,
    find_user_api_key_by_name,
    list_admin_accounts,
    list_admin_user_api_keys,
    list_deployable_edges,
    normalize_edge_id,
)
from relay_onboard_profiles import (  # noqa: E402
    format_name,
    list_platforms,
    require_implemented,
)

PROD_BASE_DEFAULT = "https://api.tokenkey.dev"
STAGE0_DIR = REPO_ROOT / "ops" / "stage0"


class HttpAPIError(Exception):
    def __init__(self, code: int, method: str, path: str, detail: str) -> None:
        self.code = code
        self.method = method
        self.path = path
        self.detail = detail
        super().__init__(f"HTTP {code} {method} {path}: {detail}")


class PostRedirect(urllib.request.HTTPRedirectHandler):
    """Preserve POST across api.tokenkey.dev → tokenkey.dev 301 (urllib would GET)."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[no-untyped-def]
        data = req.data
        hdrs = {k: v for k, v in req.headers.items() if k.lower() != "content-length"}
        return urllib.request.Request(newurl, data=data, headers=hdrs, method=req.get_method())


_OPENER = urllib.request.build_opener(PostRedirect)


def log(msg: str) -> None:
    print(f"[relay-onboard] {msg}", file=sys.stderr)


def die(msg: str, code: int = 1) -> None:
    log(f"error: {msg}")
    raise SystemExit(code)


def emit(obj: Any) -> None:
    print(json.dumps(obj, ensure_ascii=False, indent=2))


def admin_key(env_name: str, *, required: bool = True) -> str:
    key = os.environ.get(env_name, "").strip()
    if not key and env_name != "TOKENKEY_ADMIN_API_KEY":
        key = os.environ.get("TOKENKEY_ADMIN_API_KEY", "").strip()
    if not key and required:
        die(
            f"{env_name} unset. Export prod/edge admin API key "
            "(or rely on SSM fetch for edges via --fetch-edge-admin-key)."
        )
    return key


def http_json(
    base_url: str,
    path: str,
    *,
    method: str = "GET",
    payload: dict | list | None = None,
    api_key: str | None = None,
    timeout: int = 120,
) -> Any:
    url = f"{base_url.rstrip('/')}/api/v1{path}"
    headers = {"Accept": "application/json"}
    body = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    if api_key:
        headers["x-api-key"] = api_key
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with _OPENER.open(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8")
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")[:2000]
        raise HttpAPIError(exc.code, method, path, detail) from exc


def unwrap_data(resp: Any) -> Any:
    if isinstance(resp, dict) and "data" in resp:
        return resp["data"]
    return resp


def http_data(
    base_url: str,
    path: str,
    *,
    method: str = "GET",
    payload: dict | list | None = None,
    api_key: str,
) -> Any:
    return unwrap_data(
        http_json(base_url, path, method=method, payload=payload, api_key=api_key)
    )


def fetch_edge_admin_key_via_ssm(edge_id: str) -> str:
    sys.path.insert(0, str(STAGE0_DIR))
    from edge_ssm_execution import resolve_edge_execution_identity

    ident = resolve_edge_execution_identity(REPO_ROOT, edge_id)
    sql = (
        "SELECT value FROM settings WHERE key='admin_api_key' "
        "AND value <> '' LIMIT 1;"
    )
    remote = (
        "echo %s | base64 -d | sudo docker exec -i tokenkey-postgres "
        "psql -U tokenkey -d tokenkey -X -A -t"
        % base64.b64encode(sql.encode()).decode()
    )
    cid = subprocess.check_output(
        [
            "aws",
            "ssm",
            "send-command",
            "--region",
            ident.region,
            "--instance-ids",
            ident.instance_id,
            "--document-name",
            "AWS-RunShellScript",
            "--comment",
            f"tk-relay-onboard-admin-{edge_id}",
            "--parameters",
            json.dumps({"commands": [remote]}),
            "--query",
            "Command.CommandId",
            "--output",
            "text",
        ],
        text=True,
    ).strip()
    for _ in range(30):
        st = subprocess.check_output(
            [
                "aws",
                "ssm",
                "get-command-invocation",
                "--region",
                ident.region,
                "--command-id",
                cid,
                "--instance-id",
                ident.instance_id,
                "--query",
                "Status",
                "--output",
                "text",
            ],
            text=True,
        ).strip()
        if st in {"Success", "Failed", "Cancelled", "TimedOut"}:
            break
        time.sleep(1)
    out = subprocess.check_output(
        [
            "aws",
            "ssm",
            "get-command-invocation",
            "--region",
            ident.region,
            "--command-id",
            cid,
            "--instance-id",
            ident.instance_id,
            "--query",
            "StandardOutputContent",
            "--output",
            "text",
        ],
        text=True,
    )
    key = (out or "").strip().splitlines()[0].strip() if out.strip() else ""
    if not key.startswith("admin-"):
        die(f"edge {edge_id}: SSM admin_api_key missing or unexpected (len={len(key)})")
    return key


def ssm_psql(edge_id: str, sql: str) -> str:
    sys.path.insert(0, str(STAGE0_DIR))
    from edge_ssm_execution import resolve_edge_execution_identity

    ident = resolve_edge_execution_identity(REPO_ROOT, edge_id)
    remote = (
        "echo %s | base64 -d | sudo docker exec -i tokenkey-postgres "
        "psql -U tokenkey -d tokenkey -X -A -F'|'"
        % base64.b64encode(sql.encode()).decode()
    )
    cid = subprocess.check_output(
        [
            "aws",
            "ssm",
            "send-command",
            "--region",
            ident.region,
            "--instance-ids",
            ident.instance_id,
            "--document-name",
            "AWS-RunShellScript",
            "--comment",
            f"tk-relay-onboard-sql-{edge_id}",
            "--parameters",
            json.dumps({"commands": [remote]}),
            "--query",
            "Command.CommandId",
            "--output",
            "text",
        ],
        text=True,
    ).strip()
    for _ in range(30):
        st = subprocess.check_output(
            [
                "aws",
                "ssm",
                "get-command-invocation",
                "--region",
                ident.region,
                "--command-id",
                cid,
                "--instance-id",
                ident.instance_id,
                "--query",
                "Status",
                "--output",
                "text",
            ],
            text=True,
        ).strip()
        if st in {"Success", "Failed", "Cancelled", "TimedOut"}:
            break
        time.sleep(1)
    if st != "Success":
        err = subprocess.check_output(
            [
                "aws",
                "ssm",
                "get-command-invocation",
                "--region",
                ident.region,
                "--command-id",
                cid,
                "--instance-id",
                ident.instance_id,
                "--query",
                "StandardErrorContent",
                "--output",
                "text",
            ],
            text=True,
        )
        die(f"edge {edge_id}: SSM psql {st}: {err[:400]}")
    return subprocess.check_output(
        [
            "aws",
            "ssm",
            "get-command-invocation",
            "--region",
            ident.region,
            "--command-id",
            cid,
            "--instance-id",
            ident.instance_id,
            "--query",
            "StandardOutputContent",
            "--output",
            "text",
        ],
        text=True,
    )


def list_groups(base_url: str, api_key: str) -> list[dict[str, Any]]:
    data = http_data(base_url, "/admin/groups/all", api_key=api_key)
    return data if isinstance(data, list) else []


def find_group_by_name(groups: list[dict[str, Any]], name: str) -> dict[str, Any] | None:
    want = name.strip()
    for g in groups:
        if str(g.get("name") or "").strip() == want:
            return g
    return None


def find_account_by_name(
    base_url: str, api_key: str, name: str, *, platform: str | None = None
) -> dict[str, Any] | None:
    page = 1
    while page <= 40:
        q = f"/admin/accounts?page={page}&page_size=100&lite=1"
        if platform:
            q += f"&platform={platform}"
        data = http_data(base_url, q, api_key=api_key)
        items = data.get("items") if isinstance(data, dict) else data
        if not items:
            break
        for row in items:
            if isinstance(row, dict) and str(row.get("name") or "").strip() == name:
                return row
        total = int(data.get("total") or 0) if isinstance(data, dict) else 0
        if page * 100 >= total:
            break
        page += 1
    return None


def resolve_prod_group_ids(
    prod_base: str, prod_key: str, profile: dict[str, Any]
) -> list[int]:
    names = profile.get("prod_group_names") or []
    groups = list_groups(prod_base, prod_key)
    ids: list[int] = []
    for name in names:
        hit = find_group_by_name(groups, str(name))
        if not hit:
            die(f"prod group not found: {name}")
        ids.append(int(hit["id"]))
    if ids:
        return ids
    fallback = profile.get("prod_group_ids_fallback") or []
    if not fallback:
        die("profile missing prod_group_names and prod_group_ids_fallback")
    return [int(x) for x in fallback]


def load_parity_from_stub(
    prod_base: str, prod_key: str, stub_name: str
) -> dict[str, Any]:
    stub = find_account_by_name(prod_base, prod_key, stub_name, platform="antigravity")
    if not stub:
        # broader search
        stub = find_account_by_name(prod_base, prod_key, stub_name)
    if not stub:
        die(f"parity stub not found on prod: {stub_name}")
    full = http_data(prod_base, f"/admin/accounts/{stub['id']}", api_key=prod_key)
    creds = full.get("credentials") if isinstance(full.get("credentials"), dict) else {}
    mapping = creds.get("model_mapping")
    if not isinstance(mapping, dict) or not mapping:
        die(f"parity stub {stub_name} missing model_mapping")
    extra = full.get("extra") if isinstance(full.get("extra"), dict) else {}
    return {
        "model_mapping": dict(mapping),
        "extra": dict(extra),
        "source_id": full.get("id"),
        "source_name": full.get("name"),
    }


def build_plan(
    *,
    platform: str,
    edge_ids: list[str],
    prod_base: str,
    prod_key: str,
) -> dict[str, Any]:
    profile = require_implemented(platform)
    deployable = {e["edge_id"] for e in list_deployable_edges()}
    edges_out: list[dict[str, Any]] = []
    for raw in edge_ids:
        eid = normalize_edge_id(raw)
        if eid not in deployable:
            die(f"edge_id {eid!r} not in deployable lightsail targets")
        edges_out.append(
            {
                "edge_id": eid,
                "edge_base_url": edge_base_url(eid),
                "edge_group_name": profile["edge_group_name"],
                "placeholder_name": format_name(
                    profile["placeholder_name"], edge_id=eid
                ),
                "relay_key_name": format_name(profile["relay_key_name"], edge_id=eid),
                "prod_stub_name": format_name(profile["prod_stub_name"], edge_id=eid),
            }
        )
    group_ids = resolve_prod_group_ids(prod_base, prod_key, profile)
    parity = load_parity_from_stub(
        prod_base, prod_key, str(profile["prod_parity_stub_name"])
    )
    existing = list_admin_accounts(
        http_json,
        unwrap_data,
        base_url=prod_base,
        api_key=prod_key,
        platform=str(profile["pool_platform"]),
    )
    for edge in edges_out:
        stub = find_prod_relay_stub(
            existing,
            edge_id=edge["edge_id"],
            pool_platform=str(profile["pool_platform"]),
        )
        edge["prod_action"] = "skip" if stub else "create"
        if stub:
            edge["prod_existing"] = {"id": stub.get("id"), "name": stub.get("name")}
    return {
        "platform": platform,
        "pool_platform": profile["pool_platform"],
        "prod_base_url": prod_base,
        "prod_group_ids": group_ids,
        "prod_parity": {
            "source_id": parity["source_id"],
            "source_name": parity["source_name"],
            "mapping_n": len(parity["model_mapping"]),
            "extra_keys": sorted(parity["extra"].keys()),
        },
        "probe": profile["probe"],
        "ops_reauth": profile["ops_reauth"],
        "edges": edges_out,
    }


def ensure_edge_group(
    edge_base: str, edge_key: str, profile: dict[str, Any], *, dry_run: bool
) -> dict[str, Any]:
    name = str(profile["edge_group_name"])
    groups = list_groups(edge_base, edge_key)
    hit = find_group_by_name(groups, name)
    if hit:
        return {"action": "exists", "id": hit.get("id"), "name": name}
    if dry_run:
        return {
            "action": "would_create",
            "name": name,
            "platform": profile["edge_group_platform"],
        }
    created = http_data(
        edge_base,
        "/admin/groups",
        method="POST",
        api_key=edge_key,
        payload={
            "name": name,
            "platform": profile["edge_group_platform"],
            "rate_multiplier": 1,
            "is_exclusive": False,
            "status": "active",
        },
    )
    return {"action": "created", "id": created.get("id"), "name": name}


def ensure_edge_placeholder(
    edge_base: str,
    edge_key: str,
    profile: dict[str, Any],
    *,
    edge_id: str,
    group_id: int,
    dry_run: bool,
) -> dict[str, Any]:
    name = format_name(profile["placeholder_name"], edge_id=edge_id)
    email = format_name(profile["placeholder_email"], edge_id=edge_id)
    existing = find_account_by_name(
        edge_base, edge_key, name, platform=str(profile["pool_platform"])
    )
    if existing:
        aid = int(existing["id"])
        if not dry_run and existing.get("schedulable") is not False:
            http_data(
                edge_base,
                f"/admin/accounts/{aid}/schedulable",
                method="POST",
                api_key=edge_key,
                payload={"schedulable": False},
            )
        return {
            "action": "exists",
            "id": aid,
            "name": name,
            "schedulable": False,
        }
    payload = {
        "name": name,
        "platform": profile["pool_platform"],
        "type": "oauth",
        "account_email": email,
        "group_ids": [group_id],
        "concurrency": 10,
        "priority": 50,
        "notes": profile["placeholder_notes"],
        "credentials": {
            "access_token": "PLACEHOLDER_ACCESS_TOKEN_UPDATE_ME",
            "refresh_token": "PLACEHOLDER_REFRESH_TOKEN_UPDATE_ME",
            "token_type": "Bearer",
            "project_id": "PLACEHOLDER_PROJECT_ID",
            "email": email,
            "expires_at": "4102358100",
        },
        "extra": {"import_source": "edge_prod_relay_onboard_placeholder"},
    }
    if dry_run:
        return {"action": "would_create", "name": name, "group_id": group_id}
    created = http_data(
        edge_base, "/admin/accounts", method="POST", api_key=edge_key, payload=payload
    )
    aid = int(created["id"])
    http_data(
        edge_base,
        f"/admin/accounts/{aid}/schedulable",
        method="POST",
        api_key=edge_key,
        payload={"schedulable": False},
    )
    return {"action": "created", "id": aid, "name": name, "schedulable": False}


def ensure_edge_relay_key(
    edge_base: str,
    edge_key: str,
    profile: dict[str, Any],
    *,
    edge_id: str,
    group_id: int,
    dry_run: bool,
) -> tuple[str, dict[str, Any]]:
    name = format_name(profile["relay_key_name"], edge_id=edge_id)
    user_id = int(profile.get("relay_key_user_id") or 1)
    existing = find_user_api_key_by_name(
        list_admin_user_api_keys(
            http_json,
            unwrap_data,
            base_url=edge_base,
            api_key=edge_key,
            user_id=user_id,
        ),
        name,
    )
    if existing and str(existing.get("key") or "").strip():
        return str(existing["key"]).strip(), {
            "action": "reused",
            "id": existing.get("id"),
            "name": name,
            "group_id": existing.get("group_id"),
        }
    payload = {"name": name, "routing_mode": "direct", "group_id": group_id}
    if dry_run:
        return "sk_DRY_RUN", {"action": "would_create", "name": name, "payload": payload}
    created = http_data(
        edge_base,
        f"/admin/users/{user_id}/api-keys",
        method="POST",
        api_key=edge_key,
        payload=payload,
    )
    key = str((created or {}).get("key") or "").strip()
    if not key:
        die(f"edge {edge_id}: relay key create missing key field")
    return key, {"action": "created", "id": created.get("id"), "name": name, "group_id": group_id}


def ensure_edge_capability(
    edge_id: str, profile: dict[str, Any], *, dry_run: bool
) -> dict[str, Any]:
    cap = profile.get("edge_capability")
    if not isinstance(cap, dict):
        return {"action": "skipped", "reason": "no edge_capability in profile"}
    protocols = cap.get("required_protocols") or []
    evidence = cap.get("probe_evidence") or {}
    proto_json = json.dumps(protocols)
    evidence_json = json.dumps(evidence)
    # Update every capability row currently linked from active schedulable antigravity oauth,
    # and also the common shared row if present with empty protocols.
    sql = f"""
WITH targets AS (
  SELECT DISTINCT a.protocol_endpoint_capability_id AS id
  FROM accounts a
  WHERE a.platform = 'antigravity'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
    AND a.protocol_endpoint_capability_id IS NOT NULL
)
UPDATE protocol_endpoint_capabilities c
SET supported_protocols = '{proto_json}'::jsonb,
    revision = GREATEST(c.revision, 1) + 1,
    probe_evidence = '{evidence_json}'::jsonb,
    updated_at = NOW()
FROM targets t
WHERE c.id = t.id
  AND (
    c.supported_protocols = '[]'::jsonb
    OR c.supported_protocols IS NULL
    OR NOT (c.supported_protocols ? 'gemini_generate_content')
    OR coalesce(c.probe_evidence->'verdicts'->>'gemini_generate_content','') <> 'positive'
  )
RETURNING c.id, c.revision, c.supported_protocols,
  c.probe_evidence->'verdicts'->>'gemini_generate_content' AS verdict;
"""
    if dry_run:
        return {"action": "would_repair_if_needed", "sql_targets": "linked antigravity oauth caps"}
    out = ssm_psql(edge_id, sql)
    return {"action": "repaired", "psql": out.strip()}


def ensure_prod_stub(
    prod_base: str,
    prod_key: str,
    profile: dict[str, Any],
    *,
    edge_id: str,
    edge_api_key: str,
    group_ids: list[int],
    parity: dict[str, Any],
    dry_run: bool,
) -> dict[str, Any]:
    name = format_name(profile["prod_stub_name"], edge_id=edge_id)
    accounts = list_admin_accounts(
        http_json,
        unwrap_data,
        base_url=prod_base,
        api_key=prod_key,
        platform=str(profile["pool_platform"]),
    )
    existing = find_prod_relay_stub(
        accounts, edge_id=edge_id, pool_platform=str(profile["pool_platform"])
    )
    if existing:
        return {
            "action": "exists",
            "id": existing.get("id"),
            "name": existing.get("name"),
        }
    payload = {
        "name": name,
        "platform": profile["pool_platform"],
        "type": "apikey",
        "credentials": {
            "api_key": edge_api_key,
            "base_url": edge_base_url(edge_id),
            "pool_mode": True,
            "pool_mode_retry_count": int(profile.get("prod_pool_mode_retry_count") or 3),
            "model_mapping": parity["model_mapping"],
        },
        "group_ids": group_ids,
        "concurrency": int(profile.get("prod_concurrency") or 40),
        "priority": int(profile.get("prod_priority") or 1),
        "extra": parity["extra"],
        "confirm_mixed_channel_risk": True,
    }
    if dry_run:
        redacted = json.loads(json.dumps(payload))
        redacted["credentials"]["api_key"] = "<redacted>"
        return {"action": "would_create", "payload": redacted}
    created = http_data(
        prod_base, "/admin/accounts", method="POST", api_key=prod_key, payload=payload
    )
    if not isinstance(created, dict) or not created.get("id"):
        die(
            f"prod stub create for {name} returned unexpected shape "
            f"(keys={list(created)[:12] if isinstance(created, dict) else type(created)})"
        )
    return {"action": "created", "id": created.get("id"), "name": created.get("name")}


def touch_account(base_url: str, api_key: str, account_id: int) -> None:
    full = http_data(base_url, f"/admin/accounts/{account_id}", api_key=api_key)
    http_data(
        base_url,
        f"/admin/accounts/{account_id}",
        method="PUT",
        api_key=api_key,
        payload={
            "concurrency": full.get("concurrency") or 10,
            "priority": full.get("priority") or 1,
            "confirm_mixed_channel_risk": True,
        },
    )


def apply_edge(
    *,
    platform: str,
    edge_id: str,
    prod_base: str,
    prod_key: str,
    edge_key: str,
    dry_run: bool,
) -> dict[str, Any]:
    profile = require_implemented(platform)
    edge_base = edge_base_url(edge_id)
    group_meta = ensure_edge_group(edge_base, edge_key, profile, dry_run=dry_run)
    if group_meta.get("id") is not None:
        group_id = int(group_meta["id"])
    else:
        # dry-run would_create: best-effort resolve existing id for previews
        hit = find_group_by_name(
            list_groups(edge_base, edge_key), str(profile["edge_group_name"])
        )
        group_id = int(hit["id"]) if hit else 0
        if not dry_run:
            die(f"edge {edge_id}: group create did not return id")

    ph = ensure_edge_placeholder(
        edge_base,
        edge_key,
        profile,
        edge_id=edge_id,
        group_id=group_id,
        dry_run=dry_run,
    )
    key_value, key_meta = ensure_edge_relay_key(
        edge_base,
        edge_key,
        profile,
        edge_id=edge_id,
        group_id=group_id,
        dry_run=dry_run,
    )
    cap_meta = ensure_edge_capability(edge_id, profile, dry_run=dry_run)
    if not dry_run:
        # Reload schedulable oauth after capability repair.
        items = http_data(
            edge_base,
            f"/admin/accounts?page=1&page_size=100&platform={profile['pool_platform']}",
            api_key=edge_key,
        )
        for row in (items.get("items") if isinstance(items, dict) else items) or []:
            if (
                isinstance(row, dict)
                and row.get("schedulable")
                and row.get("status") == "active"
                and row.get("type") == "oauth"
            ):
                try:
                    touch_account(edge_base, edge_key, int(row["id"]))
                except Exception as exc:  # noqa: BLE001 — best-effort cache nudge
                    log(f"touch account {row.get('id')} failed: {exc}")

    group_ids = resolve_prod_group_ids(prod_base, prod_key, profile)
    parity = load_parity_from_stub(
        prod_base, prod_key, str(profile["prod_parity_stub_name"])
    )
    prod_meta = ensure_prod_stub(
        prod_base,
        prod_key,
        profile,
        edge_id=edge_id,
        edge_api_key=key_value,
        group_ids=group_ids,
        parity=parity,
        dry_run=dry_run,
    )
    if not dry_run and prod_meta.get("action") == "created" and prod_meta.get("id"):
        try:
            touch_account(prod_base, prod_key, int(prod_meta["id"]))
        except Exception as exc:  # noqa: BLE001
            log(f"touch prod stub failed: {exc}")

    return {
        "edge_id": edge_id,
        "edge_base_url": edge_base,
        "group": group_meta,
        "placeholder": ph,
        "relay_key": {k: v for k, v in key_meta.items() if k != "payload"},
        "capability": cap_meta,
        "prod": prod_meta,
        "ops_reauth": profile["ops_reauth"],
    }


def run_probe(
    *,
    platform: str,
    edge_id: str,
    prod_base: str,
    prod_key: str,
) -> dict[str, Any]:
    profile = require_implemented(platform)
    accounts = list_admin_accounts(
        http_json,
        unwrap_data,
        base_url=prod_base,
        api_key=prod_key,
        platform=str(profile["pool_platform"]),
    )
    stub = find_prod_relay_stub(
        accounts, edge_id=edge_id, pool_platform=str(profile["pool_platform"])
    )
    if not stub or not stub.get("id"):
        die(f"prod stub missing for {platform}/{edge_id}; run apply first")
    probe = profile["probe"]
    cmd = [
        "bash",
        str(REPO_ROOT / "ops/observability/run-probe.sh"),
        "--target",
        "prod",
        "--script",
        "ops/stage0/probe_account_model.sh",
        "--env",
        f"ACCOUNT_ID={stub['id']}",
        "--env",
        f"MODEL={probe['model']}",
        "--env",
        f"ENDPOINT={probe['endpoint']}",
        "--env",
        f"MAX_TOKENS={probe.get('max_tokens', 32)}",
        "--timeout-seconds",
        "180",
        "--comment",
        f"relay-onboard {platform} {edge_id}",
    ]
    log(" ".join(cmd))
    cp = subprocess.run(cmd, cwd=str(REPO_ROOT), capture_output=True, text=True)
    stdout = cp.stdout.strip()
    stderr = cp.stderr.strip()
    verdict: dict[str, Any]
    try:
        verdict = json.loads(stdout)
    except json.JSONDecodeError:
        start = stdout.rfind("{")
        if start < 0:
            die(f"probe non-json (exit={cp.returncode}): {stdout[:500]}")
        verdict = json.loads(stdout[start:])
    usage = verdict.get("usage_match") if isinstance(verdict.get("usage_match"), dict) else {}
    return {
        "edge_id": edge_id,
        "prod_stub_id": stub.get("id"),
        "prod_stub_name": stub.get("name"),
        "probe_exit": cp.returncode,
        "verdict": verdict.get("verdict"),
        "usage_account_id": usage.get("account_id"),
        "upstream_model": usage.get("upstream_model"),
        "gemini_valid": ((verdict.get("response") or {}).get("gemini") or {}).get("valid"),
        "body_excerpt": ((verdict.get("response") or {}).get("body_excerpt") or "")[:200],
        "stderr_tail": stderr.splitlines()[-3:],
    }


def parse_edges(values: list[str] | None) -> list[str]:
    if not values:
        die("provide at least one --edge")
    out: list[str] = []
    for v in values:
        for part in str(v).split(","):
            part = part.strip()
            if part:
                out.append(normalize_edge_id(part))
    if not out:
        die("no edge ids parsed from --edge")
    return out


def cmd_list_platforms(_: argparse.Namespace) -> int:
    emit({"platforms": list_platforms(), "deployable_edges": list_deployable_edges()})
    return 0


def cmd_plan(args: argparse.Namespace) -> int:
    prod_base = (args.prod_base_url or PROD_BASE_DEFAULT).rstrip("/")
    prod_key = admin_key("TOKENKEY_PROD_ADMIN_API_KEY")
    plan = build_plan(
        platform=args.platform,
        edge_ids=parse_edges(args.edge),
        prod_base=prod_base,
        prod_key=prod_key,
    )
    emit(plan)
    return 0


def resolve_edge_key(edge_id: str, args: argparse.Namespace) -> str:
    if args.edge_admin_key:
        return args.edge_admin_key.strip()
    env_key = os.environ.get("TOKENKEY_EDGE_ADMIN_API_KEY", "").strip()
    if env_key:
        return env_key
    if args.fetch_edge_admin_key:
        return fetch_edge_admin_key_via_ssm(edge_id)
    die(
        f"edge {edge_id}: set TOKENKEY_EDGE_ADMIN_API_KEY, --edge-admin-key, "
        "or --fetch-edge-admin-key"
    )


def cmd_apply(args: argparse.Namespace) -> int:
    dry_run = not args.yes
    if dry_run:
        log("dry-run (pass --yes to write)")
    prod_base = (args.prod_base_url or PROD_BASE_DEFAULT).rstrip("/")
    prod_key = admin_key("TOKENKEY_PROD_ADMIN_API_KEY")
    results = []
    for edge_id in parse_edges(args.edge):
        edge_key = resolve_edge_key(edge_id, args)
        results.append(
            apply_edge(
                platform=args.platform,
                edge_id=edge_id,
                prod_base=prod_base,
                prod_key=prod_key,
                edge_key=edge_key,
                dry_run=dry_run,
            )
        )
    emit({"dry_run": dry_run, "results": results})
    return 0


def cmd_probe(args: argparse.Namespace) -> int:
    prod_base = (args.prod_base_url or PROD_BASE_DEFAULT).rstrip("/")
    prod_key = admin_key("TOKENKEY_PROD_ADMIN_API_KEY")
    results = []
    failed = False
    for edge_id in parse_edges(args.edge):
        row = run_probe(
            platform=args.platform,
            edge_id=edge_id,
            prod_base=prod_base,
            prod_key=prod_key,
        )
        results.append(row)
        if row.get("verdict") != "servable" or row.get("usage_account_id") is None:
            failed = True
    emit({"results": results})
    return 1 if failed else 0


def cmd_all(args: argparse.Namespace) -> int:
    rc = cmd_apply(args)
    if rc != 0:
        return rc
    if args.skip_probe:
        return 0
    if not args.yes:
        log("skipping probe in dry-run; re-run with --yes or invoke probe separately")
        return 0
    return cmd_probe(args)


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="cmd", required=True)

    sub.add_parser("list-platforms", help="list platform profiles + deployable edges")

    def add_common(sp: argparse.ArgumentParser) -> None:
        sp.add_argument("--platform", required=True, help="profile key, e.g. antigravity")
        sp.add_argument(
            "--edge",
            action="append",
            required=True,
            help="edge id (repeatable or comma-separated)",
        )
        sp.add_argument("--prod-base-url", default=PROD_BASE_DEFAULT)
        sp.add_argument(
            "--fetch-edge-admin-key",
            action="store_true",
            help="load edge admin_api_key from settings via SSM+psql",
        )
        sp.add_argument("--edge-admin-key", default="", help="override edge admin key")

    sp = sub.add_parser("plan", help="read-only plan")
    add_common(sp)
    sp.set_defaults(func=cmd_plan)

    sp = sub.add_parser("apply", help="create/repair edge+prod resources")
    add_common(sp)
    sp.add_argument("--yes", action="store_true", help="write (default is dry-run)")
    sp.set_defaults(func=cmd_apply)

    sp = sub.add_parser("probe", help="gateway probe prod stub via run-probe.sh")
    add_common(sp)
    sp.set_defaults(func=cmd_probe)

    sp = sub.add_parser("all", help="apply then probe")
    add_common(sp)
    sp.add_argument("--yes", action="store_true")
    sp.add_argument("--skip-probe", action="store_true")
    sp.set_defaults(func=cmd_all)
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.cmd == "list-platforms":
        return cmd_list_platforms(args)
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
