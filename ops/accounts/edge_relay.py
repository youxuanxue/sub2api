"""Prod ↔ edge OAuth relay orchestration for ops/accounts/import-accounts.py."""
from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any, Callable

REPO_ROOT = Path(__file__).resolve().parents[2]
EDGE_TARGETS_PATH = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"

EDGE_STUB_BASE_URL_RE = re.compile(r"^https://api-([a-z0-9]+)\.tokenkey\.dev/?$", re.IGNORECASE)

# Mirrors backend/internal/service/edge_accounts_aggregator_tk.go edgeStubPlatforms
# (gemini has no edge relay path).
RELAY_POOL_PLATFORMS = (
    "anthropic",
    "openai",
    "antigravity",
    "grok",
    "kiro",
)

DEFAULT_PROD_RELAY_NAMES: dict[str, str] = {
    "anthropic": "cc-{edge_id}",
    "openai": "openai-{edge_id}",
    "antigravity": "ag-{edge_id}",
    "grok": "grok-{edge_id}",
    "kiro": "kiro-{edge_id}",
}


def normalize_edge_id(edge_id: str) -> str:
    return str(edge_id or "").strip().lower()


def edge_base_url(edge_id: str) -> str:
    eid = normalize_edge_id(edge_id)
    if not eid:
        raise ValueError("edge_id 不能为空")
    return f"https://api-{eid}.tokenkey.dev"


def edge_id_from_base_url(base_url: str) -> str | None:
    match = EDGE_STUB_BASE_URL_RE.match(str(base_url or "").strip())
    if not match:
        return None
    return match.group(1).lower()


def load_edge_targets() -> dict[str, Any]:
    if not EDGE_TARGETS_PATH.is_file():
        return {"targets": {}}
    return json.loads(EDGE_TARGETS_PATH.read_text(encoding="utf-8"))


def list_deployable_edges() -> list[dict[str, str]]:
    data = load_edge_targets()
    out: list[dict[str, str]] = []
    for edge_id, meta in sorted((data.get("targets") or {}).items()):
        if not isinstance(meta, dict):
            continue
        if not meta.get("deployable"):
            continue
        domain = str(meta.get("domain") or edge_base_url(edge_id))
        out.append(
            {
                "edge_id": edge_id,
                "domain": domain.rstrip("/"),
                "purpose": str(meta.get("purpose") or ""),
            }
        )
    return out


def prod_stub_pool_platform(account: dict[str, Any]) -> str:
    """Which edge pool a prod mirror stub represents (mirror_platform wins)."""
    creds = account.get("credentials") if isinstance(account.get("credentials"), dict) else {}
    mirror = str(creds.get("mirror_platform") or "").strip().lower()
    if mirror:
        return mirror
    platform = str(account.get("platform") or "").strip().lower()
    if platform == "newapi":
        # Legacy grok bridge before platform=grok convergence.
        return "grok"
    return platform


def is_prod_mirror_stub(account: dict[str, Any]) -> bool:
    if str(account.get("type") or "").strip().lower() != "apikey":
        return False
    creds = account.get("credentials") if isinstance(account.get("credentials"), dict) else {}
    base_url = str(creds.get("base_url") or "").strip()
    return bool(base_url and EDGE_STUB_BASE_URL_RE.match(base_url))


def find_prod_relay_stub(
    accounts: list[dict[str, Any]],
    *,
    edge_id: str,
    pool_platform: str,
) -> dict[str, Any] | None:
    want_edge = normalize_edge_id(edge_id)
    want_pool = str(pool_platform or "").strip().lower()
    for account in accounts:
        if not is_prod_mirror_stub(account):
            continue
        creds = account.get("credentials") if isinstance(account.get("credentials"), dict) else {}
        stub_edge = edge_id_from_base_url(str(creds.get("base_url") or ""))
        if stub_edge != want_edge:
            continue
        if prod_stub_pool_platform(account) != want_pool:
            continue
        return account
    return None


def relay_transport_platform(pool_platform: str, mirror_platform: str | None = None) -> str:
    """Prod account.platform for a mirror stub (transport shape)."""
    pool = str(pool_platform or "").strip().lower()
    mirror = str(mirror_platform or "").strip().lower()
    if mirror == "kiro" or pool == "kiro":
        return "anthropic"
    return pool


def default_prod_relay_name(pool_platform: str, edge_id: str) -> str:
    pool = str(pool_platform or "").strip().lower()
    template = DEFAULT_PROD_RELAY_NAMES.get(pool, "{pool}-{edge_id}")
    return template.format(pool=pool, edge_id=normalize_edge_id(edge_id))


def build_prod_relay_create_spec(
    *,
    edge_id: str,
    pool_platform: str,
    prod_relay: dict[str, Any],
    edge_api_key: str,
) -> dict[str, Any]:
    pool = str(pool_platform or "").strip().lower()
    mirror_platform = str(prod_relay.get("mirror_platform") or "").strip().lower()
    if pool == "kiro" and not mirror_platform:
        mirror_platform = "kiro"

    name = str(prod_relay.get("name") or "").strip()
    if not name:
        name = default_prod_relay_name(pool, edge_id)

    credentials: dict[str, Any] = {
        "api_key": edge_api_key,
        "base_url": edge_base_url(edge_id),
    }
    if prod_relay.get("pool_mode") is not False:
        credentials["pool_mode"] = True
        credentials["pool_mode_retry_count"] = int(
            prod_relay.get("pool_mode_retry_count", 3)
        )
    if mirror_platform:
        credentials["mirror_platform"] = mirror_platform

    spec: dict[str, Any] = {
        "name": name,
        "platform": relay_transport_platform(pool, mirror_platform or None),
        "type": "apikey",
        "credentials": credentials,
    }
    for key in (
        "notes",
        "group_ids",
        "proxy_id",
        "concurrency",
        "priority",
        "rate_multiplier",
        "load_factor",
        "expires_at",
        "auto_pause_on_expired",
        "extra",
        "skip_default_group_bind",
        "confirm_mixed_channel_risk",
    ):
        if key in prod_relay:
            spec[key] = prod_relay[key]
    return spec


def default_edge_relay_key_name(pool_platform: str, edge_id: str) -> str:
    pool = str(pool_platform or "").strip().lower()
    return f"relay-{pool}-{normalize_edge_id(edge_id)}"


def extract_edge_api_key_issue(doc: dict[str, Any]) -> dict[str, Any]:
    prod_relay = doc.get("prod_relay") if isinstance(doc.get("prod_relay"), dict) else {}
    issue = doc.get("edge_api_key_issue")
    if issue is not None and not isinstance(issue, dict):
        raise ValueError("edge_api_key_issue 必须是对象")
    issue = dict(issue or {})
    for key in ("user_id", "name", "group_id", "routing_mode"):
        if key in issue:
            continue
        alt = f"edge_api_key_{key}"
        if alt in prod_relay:
            issue[key] = prod_relay[alt]
        elif key in prod_relay:
            issue[key] = prod_relay[key]
    if "user_id" not in issue and doc.get("edge_api_key_user_id") is not None:
        issue["user_id"] = doc.get("edge_api_key_user_id")
    return issue


def prod_relay_needs_edge_api_key(doc: dict[str, Any], *, list_prod_accounts: Callable[[], list[dict[str, Any]]]) -> bool:
    edge_id = normalize_edge_id(str(doc.get("edge_id") or ""))
    pool_platform = str(doc.get("pool_platform") or doc.get("relay_pool") or "").strip().lower()
    existing = find_prod_relay_stub(list_prod_accounts(), edge_id=edge_id, pool_platform=pool_platform)
    return existing is None


def has_edge_api_key_source(doc: dict[str, Any]) -> bool:
    prod_relay = doc.get("prod_relay") if isinstance(doc.get("prod_relay"), dict) else {}
    manual = str(prod_relay.get("edge_api_key") or doc.get("edge_api_key") or "").strip()
    if manual:
        return True
    issue = extract_edge_api_key_issue(doc)
    return bool(issue.get("user_id"))


def list_admin_user_api_keys(
    http_json: Callable[..., Any],
    unwrap_data: Callable[[Any], Any],
    *,
    base_url: str,
    api_key: str,
    user_id: int,
) -> list[dict[str, Any]]:
    items: list[dict[str, Any]] = []
    page = 1
    while True:
        resp = unwrap_data(
            http_json(
                base_url,
                f"/admin/users/{user_id}/api-keys?page={page}&page_size=100",
                api_key=api_key,
            )
        )
        batch = resp.get("items") if isinstance(resp, dict) else resp
        if not isinstance(batch, list) or not batch:
            break
        for row in batch:
            if isinstance(row, dict):
                items.append(row)
        total = int(resp.get("total") or 0) if isinstance(resp, dict) else len(batch)
        if len(items) >= total:
            break
        page += 1
    return items


def find_user_api_key_by_name(keys: list[dict[str, Any]], name: str) -> dict[str, Any] | None:
    want = str(name or "").strip()
    if not want:
        return None
    for row in keys:
        if str(row.get("name") or "").strip() == want:
            return row
    return None


def resolve_edge_group_id(doc: dict[str, Any], issue: dict[str, Any]) -> int | None:
    if issue.get("group_id") is not None:
        return int(issue["group_id"])
    prod_relay = doc.get("prod_relay") if isinstance(doc.get("prod_relay"), dict) else {}
    for source in (prod_relay.get("group_ids"), doc.get("group_ids")):
        if isinstance(source, list) and source:
            return int(source[0])
    edge_oauth = doc.get("edge_oauth") if isinstance(doc.get("edge_oauth"), dict) else {}
    oauth_groups = edge_oauth.get("group_ids")
    if isinstance(oauth_groups, list) and oauth_groups:
        return int(oauth_groups[0])
    return None


def ensure_edge_relay_api_key(
    doc: dict[str, Any],
    *,
    http_json: Callable[..., Any],
    unwrap_data: Callable[[Any], Any],
    http_json_or_die: Callable[..., Any],
    edge_base_url: str,
    edge_admin_api_key: str,
    dry_run: bool,
) -> tuple[str, dict[str, Any]]:
    prod_relay = doc.get("prod_relay") if isinstance(doc.get("prod_relay"), dict) else {}
    manual = str(prod_relay.get("edge_api_key") or doc.get("edge_api_key") or "").strip()
    if manual:
        return manual, {"action": "provided"}

    issue = extract_edge_api_key_issue(doc)
    user_id = issue.get("user_id")
    if user_id is None:
        raise ValueError(
            "prod 尚无中继 stub：请提供 prod_relay.edge_api_key，"
            "或 edge_api_key_issue.user_id / prod_relay.edge_api_key_user_id"
        )
    user_id = int(user_id)
    edge_id = normalize_edge_id(str(doc.get("edge_id") or ""))
    pool_platform = str(doc.get("pool_platform") or doc.get("relay_pool") or "").strip().lower()
    name = str(issue.get("name") or prod_relay.get("edge_api_key_name") or "").strip()
    if not name:
        name = default_edge_relay_key_name(pool_platform, edge_id)
    group_id = resolve_edge_group_id(doc, issue)
    routing_mode = str(issue.get("routing_mode") or "direct").strip().lower()
    reuse = prod_relay.get("reuse_existing_edge_api_key", doc.get("reuse_existing_edge_api_key", True))
    reuse_existing = reuse is not False

    meta: dict[str, Any] = {
        "action": "create",
        "user_id": user_id,
        "name": name,
        "group_id": group_id,
        "routing_mode": routing_mode,
    }

    if reuse_existing:
        existing = find_user_api_key_by_name(
            list_admin_user_api_keys(
                http_json,
                unwrap_data,
                base_url=edge_base_url,
                api_key=edge_admin_api_key,
                user_id=user_id,
            ),
            name,
        )
        if existing and str(existing.get("key") or "").strip():
            meta["action"] = "reused"
            meta["api_key_id"] = existing.get("id")
            return str(existing["key"]).strip(), meta

    payload: dict[str, Any] = {
        "name": name,
        "routing_mode": routing_mode,
    }
    if group_id is not None:
        payload["group_id"] = group_id

    if dry_run:
        meta["payload"] = payload
        meta["action"] = "would_create"
        return "tk_DRY_RUN_EDGE_RELAY_KEY", meta

    created = unwrap_data(
        http_json_or_die(
            edge_base_url,
            f"/admin/users/{user_id}/api-keys",
            method="POST",
            payload=payload,
            api_key=edge_admin_api_key,
        )
    )
    key = str((created or {}).get("key") or "").strip()
    if not key:
        raise RuntimeError(f"edge 签发 API Key 失败：响应缺少 key 字段 {created!r}")
    meta["api_key_id"] = (created or {}).get("id")
    return key, meta


def validate_edge_oauth_relay(doc: dict[str, Any], *, path_hint: str = "") -> None:
    prefix = f"{path_hint}: " if path_hint else ""
    edge_id = normalize_edge_id(str(doc.get("edge_id") or ""))
    if not edge_id:
        raise ValueError(f"{prefix}edge_oauth_relay 需要 edge_id（如 us6）")

    pool_platform = str(doc.get("pool_platform") or doc.get("relay_pool") or "").strip().lower()
    if pool_platform not in RELAY_POOL_PLATFORMS:
        raise ValueError(
            f"{prefix}pool_platform 必须是 {', '.join(RELAY_POOL_PLATFORMS)} 之一，"
            f"收到 {pool_platform!r}"
        )

    edge_oauth = doc.get("edge_oauth")
    if not isinstance(edge_oauth, dict) or not edge_oauth:
        raise ValueError(f"{prefix}edge_oauth_relay 需要 edge_oauth 对象（edge 真 OAuth 规格）")

    skip_prod = doc.get("skip_prod_relay_if_exists", True)
    if skip_prod is not False and skip_prod is not True:
        raise ValueError(f"{prefix}skip_prod_relay_if_exists 必须是布尔值")

    prod_relay = doc.get("prod_relay")
    if prod_relay is not None and not isinstance(prod_relay, dict):
        raise ValueError(f"{prefix}prod_relay 必须是对象")

    issue = extract_edge_api_key_issue(doc)
    if issue.get("user_id") is not None:
        try:
            int(issue["user_id"])
        except (TypeError, ValueError) as exc:
            raise ValueError(f"{prefix}edge_api_key_issue.user_id 必须是整数") from exc


def extract_edge_oauth_spec(doc: dict[str, Any]) -> dict[str, Any]:
    edge_oauth = dict(doc.get("edge_oauth") or {})
    for key in (
        "group_ids",
        "proxy_id",
        "concurrency",
        "priority",
        "rate_multiplier",
        "load_factor",
        "update_existing",
        "skip_default_group_bind",
        "confirm_mixed_channel_risk",
        "fill_project_id",
    ):
        if key not in edge_oauth and key in doc:
            edge_oauth[key] = doc[key]
    edge_oauth.setdefault("update_existing", doc.get("update_existing", True))
    pool = str(doc.get("pool_platform") or doc.get("relay_pool") or "").strip().lower()
    edge_oauth.setdefault("platform", pool)
    edge_oauth.setdefault("type", "oauth")
    return edge_oauth


def list_admin_accounts(
    http_json: Callable[..., Any],
    unwrap_data: Callable[[Any], Any],
    *,
    base_url: str,
    api_key: str,
    platform: str | None = None,
    account_type: str | None = None,
) -> list[dict[str, Any]]:
    items: list[dict[str, Any]] = []
    page = 1
    while True:
        query = f"/admin/accounts?page={page}&page_size=100&lite=1"
        if platform:
            query += f"&platform={platform}"
        if account_type:
            query += f"&type={account_type}"
        resp = unwrap_data(http_json(base_url, query, api_key=api_key))
        batch = resp.get("items") if isinstance(resp, dict) else resp
        if not isinstance(batch, list) or not batch:
            break
        for row in batch:
            if isinstance(row, dict):
                items.append(row)
        total = int(resp.get("total") or 0) if isinstance(resp, dict) else len(batch)
        if len(items) >= total:
            break
        page += 1
    return items


def plan_edge_oauth_relay(
    doc: dict[str, Any],
    *,
    list_prod_accounts: Callable[[], list[dict[str, Any]]],
) -> dict[str, Any]:
    validate_edge_oauth_relay(doc)
    edge_id = normalize_edge_id(str(doc["edge_id"]))
    pool_platform = str(doc.get("pool_platform") or doc.get("relay_pool")).strip().lower()
    skip_prod_if_exists = doc.get("skip_prod_relay_if_exists", True) is not False
    prod_relay_cfg = doc.get("prod_relay") if isinstance(doc.get("prod_relay"), dict) else {}

    existing = find_prod_relay_stub(
        list_prod_accounts(),
        edge_id=edge_id,
        pool_platform=pool_platform,
    )

    prod_action = "skip"
    prod_reason = "prod 已有同 edge 同 pool 的中继 stub"
    prod_spec: dict[str, Any] | None = None
    if existing is None:
        if not has_edge_api_key_source(doc):
            raise ValueError(
                "prod 尚无中继 stub：请提供 prod_relay.edge_api_key，"
                "或 edge_api_key_issue.user_id / prod_relay.edge_api_key_user_id（自动签发）"
            )
        edge_api_key = str(prod_relay_cfg.get("edge_api_key") or doc.get("edge_api_key") or "").strip()
        prod_spec = build_prod_relay_create_spec(
            edge_id=edge_id,
            pool_platform=pool_platform,
            prod_relay=prod_relay_cfg,
            edge_api_key=edge_api_key or "tk_PENDING_EDGE_ISSUE",
        )
        prod_action = "create"
        prod_reason = "prod 无匹配 stub，将新建中继账号"
    elif not skip_prod_if_exists:
        raise ValueError(
            f"prod 已有中继 stub id={existing.get('id')} name={existing.get('name')!r}，"
            "且 skip_prod_relay_if_exists=false（当前不支持覆盖更新 prod stub）"
        )

    return {
        "edge_id": edge_id,
        "edge_base_url": edge_base_url(edge_id),
        "pool_platform": pool_platform,
        "edge_oauth": extract_edge_oauth_spec(doc),
        "prod_action": prod_action,
        "prod_reason": prod_reason,
        "prod_existing": existing,
        "prod_create": prod_spec,
    }
