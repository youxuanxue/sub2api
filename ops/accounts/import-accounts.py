#!/usr/bin/env python3
"""Programmatic TokenKey account import for ops / SRE.

Uses the Admin API with an Admin API Key (``x-api-key`` header). Supports all
scheduling platforms (anthropic, openai, gemini, antigravity, newapi, kiro,
grok) plus platform-specific import shortcuts (Codex session, Antigravity OAuth
export, Grok SSO batch).

Environment:
  TOKENKEY_BASE_URL / TOKENKEY_PROD_BASE_URL   prod API (default https://api.tokenkey.dev)
  TOKENKEY_EDGE_BASE_URL                      edge API (edge_oauth_relay)
  TOKENKEY_ADMIN_API_KEY                      fallback admin key
  TOKENKEY_PROD_ADMIN_API_KEY                 prod admin key (edge_oauth_relay)
  TOKENKEY_EDGE_ADMIN_API_KEY                 edge admin key (edge_oauth_relay)

Examples:
  ./import-accounts.sh validate examples/edge-oauth-relay-antigravity-us6.json
  ./import-accounts.sh import examples/edge-oauth-relay-antigravity-us6.json --dry-run
  ./import-accounts.sh import examples/newapi-deepseek-apikey.json --dry-run
  ./import-accounts.sh import path/to/dir/ --yes
  ./import-accounts.sh list-channel-types
  ./import-accounts.sh selftest
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from edge_relay import (
    ensure_edge_relay_api_key,
    list_deployable_edges,
    plan_edge_oauth_relay,
    validate_edge_oauth_relay,
)

SCHEDULING_PLATFORMS = (
    "anthropic",
    "openai",
    "gemini",
    "antigravity",
    "newapi",
    "kiro",
    "grok",
)
ACCOUNT_TYPES = (
    "oauth",
    "setup-token",
    "apikey",
    "upstream",
    "bedrock",
    "service_account",
)
BASE_URL_DEFAULT = os.environ.get("TOKENKEY_BASE_URL", "https://api.tokenkey.dev").rstrip("/")
PROD_BASE_URL_DEFAULT = os.environ.get(
    "TOKENKEY_PROD_BASE_URL",
    os.environ.get("TOKENKEY_BASE_URL", "https://api.tokenkey.dev"),
).rstrip("/")
SCRIPT_DIR = Path(__file__).resolve().parent
EXAMPLES_DIR = SCRIPT_DIR / "examples"
ANTIGRAVITY_IMPORT_PATH = "/admin/accounts/import/antigravity-oauth"
CODEX_IMPORT_PATH = "/admin/accounts/import/codex-session"


class HttpAPIError(Exception):
    def __init__(self, code: int, method: str, path: str, detail: str) -> None:
        self.code = code
        self.method = method
        self.path = path
        self.detail = detail
        super().__init__(f"HTTP {code} {method} {path}: {detail}")


def log(msg: str) -> None:
    print(f"[import-accounts] {msg}", file=sys.stderr)


def die(msg: str, code: int = 1) -> None:
    log(f"error: {msg}")
    raise SystemExit(code)


def admin_api_key(env_name: str = "TOKENKEY_ADMIN_API_KEY") -> str:
    key = os.environ.get(env_name, "").strip()
    if not key and env_name != "TOKENKEY_ADMIN_API_KEY":
        key = os.environ.get("TOKENKEY_ADMIN_API_KEY", "").strip()
    if not key:
        die(
            f"{env_name} 未设置。"
            "请在 Admin 后台 系统设置 → 安全与认证 → 管理员 API Key 创建/复制密钥，"
            f"导出为环境变量 {env_name}=<admin-...>"
        )
    if not key.startswith("admin-"):
        log("warning: Admin API Key 通常以 admin- 开头，请确认密钥来源正确")
    return key


def prod_admin_api_key() -> str:
    return admin_api_key("TOKENKEY_PROD_ADMIN_API_KEY")


def edge_admin_api_key() -> str:
    return admin_api_key("TOKENKEY_EDGE_ADMIN_API_KEY")


def http_json(
    base_url: str,
    path: str,
    *,
    method: str = "GET",
    payload: dict | list | None = None,
    api_key: str | None = None,
    idempotency_key: str | None = None,
    timeout: int = 120,
) -> Any:
    url = f"{base_url}/api/v1{path}"
    headers = {"Accept": "application/json"}
    body = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    if api_key:
        headers["x-api-key"] = api_key
    if idempotency_key:
        headers["Idempotency-Key"] = idempotency_key
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8")
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")[:2000]
        raise HttpAPIError(exc.code, method, path, detail) from exc


def http_json_or_die(*args: Any, **kwargs: Any) -> Any:
    try:
        return http_json(*args, **kwargs)
    except HttpAPIError as exc:
        die(str(exc))


def unwrap_data(resp: Any) -> Any:
    if isinstance(resp, dict) and "data" in resp:
        return resp["data"]
    return resp


def load_json_file(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        die(f"{path}: JSON 解析失败: {exc}")


def looks_like_codex_session(doc: dict[str, Any]) -> bool:
    if doc.get("type") == "antigravity":
        return False
    if doc.get("platform") not in (None, "", "openai"):
        return False
    markers = (
        "accessToken",
        "sessionToken",
        "chatgpt_account_id",
        "chatgptAccountId",
        "agent_identity",
        "agentIdentity",
    )
    if any(doc.get(k) for k in markers):
        return True
    tokens = doc.get("tokens")
    return isinstance(tokens, dict) and bool(tokens.get("access_token") or tokens.get("accessToken"))


def looks_like_antigravity_export(doc: dict[str, Any]) -> bool:
    if doc.get("type") == "antigravity":
        return True
    if doc.get("platform") == "antigravity":
        return False
    has_access = bool(str(doc.get("access_token") or doc.get("accessToken") or "").strip())
    has_project = bool(str(doc.get("project_id") or doc.get("projectId") or "").strip())
    return has_access and has_project and not doc.get("platform")


def detect_route(doc: Any) -> str:
    if isinstance(doc, list):
        return "list"
    if not isinstance(doc, dict):
        die(f"不支持的 JSON 根类型: {type(doc).__name__}")
    profile = str(doc.get("import_profile") or "").strip()
    if profile in {"antigravity_oauth", "codex_session", "grok_sso", "edge_oauth_relay"}:
        return profile
    if doc.get("sso_tokens") or doc.get("sso_token"):
        return "grok_sso"
    if doc.get("accounts") and isinstance(doc["accounts"], list):
        return "batch_bundle"
    if looks_like_antigravity_export(doc):
        return "antigravity_oauth"
    if looks_like_codex_session(doc):
        return "codex_session"
    if doc.get("platform") and doc.get("type") and isinstance(doc.get("credentials"), dict):
        return "create_account"
    die(
        "无法识别导入格式。请使用 examples/ 下的规范 JSON，"
        "或显式设置 import_profile（antigravity_oauth / codex_session / grok_sso / edge_oauth_relay）。"
    )


def validate_create_account(doc: dict[str, Any], *, path_hint: str = "") -> None:
    prefix = f"{path_hint}: " if path_hint else ""
    platform = str(doc.get("platform") or "").strip()
    acc_type = str(doc.get("type") or "").strip()
    if platform not in SCHEDULING_PLATFORMS:
        die(f"{prefix}platform 必须是 {', '.join(SCHEDULING_PLATFORMS)} 之一，收到 {platform!r}")
    if acc_type not in ACCOUNT_TYPES:
        die(f"{prefix}type 必须是 {', '.join(ACCOUNT_TYPES)} 之一，收到 {acc_type!r}")
    creds = doc.get("credentials")
    if not isinstance(creds, dict) or not creds:
        die(f"{prefix}credentials 必须是非空对象")

    if platform == "newapi":
        channel_type = int(doc.get("channel_type") or 0)
        if channel_type <= 0:
            die(f"{prefix}newapi 账号必须设置 channel_type > 0")
        base_url = str(creds.get("base_url") or creds.get("baseUrl") or "").strip()
        if not base_url:
            die(f"{prefix}newapi credentials.base_url 必填")

    if platform == "kiro":
        if not _truthy(creds.get("tos_acknowledged")):
            die(f"{prefix}kiro 必须 credentials.tos_acknowledged=true")
        for key in ("access_token", "refresh_token", "region", "auth_method"):
            if not str(creds.get(key) or "").strip():
                die(f"{prefix}kiro credentials.{key} 必填")
        auth_method = str(creds.get("auth_method") or "").strip()
        if auth_method not in {"social", "idc"}:
            die(f"{prefix}kiro credentials.auth_method 必须是 social 或 idc")
        if auth_method == "idc":
            for key in ("client_id", "client_secret"):
                if not str(creds.get(key) or "").strip():
                    die(f"{prefix}kiro idc 模式需要 credentials.{key}")


def _truthy(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return bool(value)


def build_create_payload(doc: dict[str, Any]) -> dict[str, Any]:
    allowed = {
        "name",
        "notes",
        "platform",
        "type",
        "credentials",
        "extra",
        "proxy_id",
        "concurrency",
        "priority",
        "channel_type",
        "rate_multiplier",
        "load_factor",
        "group_ids",
        "expires_at",
        "auto_pause_on_expired",
        "confirm_mixed_channel_risk",
        "account_email",
    }
    payload = {k: doc[k] for k in allowed if k in doc}
    if not str(payload.get("name") or "").strip():
        die("create_account 规格缺少 name")
    return payload


def _pick_str(doc: dict[str, Any], *keys: str) -> str:
    for key in keys:
        value = doc.get(key)
        if value is None:
            continue
        text = str(value).strip()
        if text:
            return text
    return ""


def _parse_rfc3339(value: str) -> datetime | None:
    text = value.strip()
    if not text:
        return None
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        return datetime.fromisoformat(text).astimezone(timezone.utc)
    except ValueError:
        return None


def parse_antigravity_expires_at_unix(doc: dict[str, Any]) -> int | None:
    expires_at = doc.get("expires_at", doc.get("expiresAt"))
    if isinstance(expires_at, (int, float)) and expires_at > 0:
        return int(expires_at)

    expired = _pick_str(doc, "expired", "expires")
    if expired:
        parsed = _parse_rfc3339(expired)
        if parsed:
            return int(parsed.timestamp()) - 300

    timestamp = doc.get("timestamp")
    expires_in = doc.get("expires_in", doc.get("expiresIn"))
    if timestamp is not None and expires_in is not None:
        try:
            ts = int(timestamp)
            if ts > 1_000_000_000_000:
                ts //= 1000
            return ts + int(expires_in) - 300
        except (TypeError, ValueError):
            pass
    return None


def antigravity_export_to_create_spec(doc: dict[str, Any]) -> dict[str, Any]:
    access_token = _pick_str(doc, "access_token", "accessToken", "token")
    refresh_token = _pick_str(doc, "refresh_token", "refreshToken")
    email = _pick_str(doc, "email")
    project_id = _pick_str(doc, "project_id", "projectId")
    if not access_token:
        die("antigravity 导出缺少 access_token，无法回退到 POST /admin/accounts")
    if not project_id:
        die(
            "antigravity 导出缺少 project_id。"
            "当前服务端尚无 import/antigravity-oauth，回退创建时必须自带 project_id。"
        )

    credentials: dict[str, Any] = {
        "access_token": access_token,
        "token_type": _pick_str(doc, "token_type", "tokenType") or "Bearer",
        "project_id": project_id,
    }
    if refresh_token:
        credentials["refresh_token"] = refresh_token
    if email:
        credentials["email"] = email
    expires_unix = parse_antigravity_expires_at_unix(doc)
    if expires_unix is not None:
        credentials["expires_at"] = str(expires_unix)

    name = _pick_str(doc, "name") or email or project_id or "antigravity-import"
    spec: dict[str, Any] = {
        "name": name,
        "platform": "antigravity",
        "type": "oauth",
        "credentials": credentials,
        "extra": {
            **(doc.get("extra") or {}),
            "import_source": "antigravity_oauth_export",
        },
    }
    if email:
        spec["account_email"] = email
    for key, value in build_import_common_options(doc).items():
        if key not in spec and value is not None:
            spec[key] = value
    return spec


def post_antigravity_oauth_import(
    base_url: str,
    api_key: str,
    doc: dict[str, Any],
    *,
    dry_run: bool,
    source: str,
) -> Any:
    payload = build_antigravity_import_payload(doc)
    log(f"{source}: POST {ANTIGRAVITY_IMPORT_PATH}")
    if dry_run:
        fallback = antigravity_export_to_create_spec(doc)
        return {
            "dry_run": True,
            "route": "antigravity_oauth",
            "payload": payload,
            "fallback_if_404": {
                "route": "create_account",
                "payload": build_create_payload(fallback),
            },
        }
    try:
        return unwrap_data(
            http_json(
                base_url,
                ANTIGRAVITY_IMPORT_PATH,
                method="POST",
                payload=payload,
                api_key=api_key,
                idempotency_key=idempotency_key_for("antigravity", payload),
            )
        )
    except HttpAPIError as exc:
        if exc.code != 404:
            die(str(exc))
        log(
            f"{source}: 服务端尚无 {ANTIGRAVITY_IMPORT_PATH}（404），"
            "回退 POST /admin/accounts"
        )
        fallback_doc = antigravity_export_to_create_spec(doc)
        return dispatch_import(
            base_url,
            api_key,
            "create_account",
            fallback_doc,
            dry_run=False,
            source=source,
        )


def build_import_common_options(doc: dict[str, Any]) -> dict[str, Any]:
    keys = (
        "name",
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
        "update_existing",
        "skip_default_group_bind",
        "confirm_mixed_channel_risk",
    )
    return {k: doc[k] for k in keys if k in doc}


def build_antigravity_import_payload(doc: dict[str, Any]) -> dict[str, Any]:
    payload = build_import_common_options(doc)
    payload["content"] = json.dumps(doc, ensure_ascii=False)
    if "fill_project_id" in doc:
        payload["fill_project_id"] = doc["fill_project_id"]
    else:
        payload.setdefault("fill_project_id", True)
    return payload


def build_codex_import_payload(doc: dict[str, Any]) -> dict[str, Any]:
    payload = build_import_common_options(doc)
    payload["content"] = json.dumps(doc, ensure_ascii=False)
    if "credential_extras" in doc:
        payload["credential_extras"] = doc["credential_extras"]
    return payload


def build_grok_sso_payload(doc: dict[str, Any]) -> dict[str, Any]:
    payload = build_import_common_options(doc)
    for key in (
        "sso_tokens",
        "sso_token",
        "credentials",
        "extra",
        "concurrency",
        "load_factor",
        "priority",
        "rate_multiplier",
        "expires_at",
        "auto_pause_on_expired",
        "confirm_mixed_channel_risk",
    ):
        if key in doc:
            payload[key] = doc[key]
    tokens = payload.get("sso_tokens")
    if not tokens:
        token = str(payload.get("sso_token") or "").strip()
        if token:
            payload["sso_tokens"] = [token]
    if not payload.get("sso_tokens"):
        die("grok_sso 导入需要 sso_tokens 或 sso_token")
    return payload


def idempotency_key_for(label: str, payload: Any) -> str:
    digest = hashlib.sha256(json.dumps(payload, sort_keys=True, default=str).encode()).hexdigest()[:24]
    return f"import-accounts-{label}-{digest}"


def dispatch_import(
    base_url: str,
    api_key: str,
    route: str,
    doc: dict[str, Any],
    *,
    dry_run: bool,
    source: str,
) -> Any:
    if route == "create_account":
        validate_create_account(doc, path_hint=source)
        payload = build_create_payload(doc)
        log(f"{source}: POST /admin/accounts name={payload.get('name')!r} platform={payload['platform']}")
        if dry_run:
            return {"dry_run": True, "route": route, "payload": payload}
        return unwrap_data(
            http_json_or_die(
                base_url,
                "/admin/accounts",
                method="POST",
                payload=payload,
                api_key=api_key,
                idempotency_key=idempotency_key_for("create", payload),
            )
        )

    if route == "antigravity_oauth":
        return post_antigravity_oauth_import(
            base_url,
            api_key,
            doc,
            dry_run=dry_run,
            source=source,
        )

    if route == "codex_session":
        payload = build_codex_import_payload(doc)
        log(f"{source}: POST {CODEX_IMPORT_PATH}")
        if dry_run:
            return {"dry_run": True, "route": route, "payload": payload}
        return unwrap_data(
            http_json_or_die(
                base_url,
                CODEX_IMPORT_PATH,
                method="POST",
                payload=payload,
                api_key=api_key,
                idempotency_key=idempotency_key_for("codex", payload),
            )
        )

    if route == "grok_sso":
        payload = build_grok_sso_payload(doc)
        log(f"{source}: POST /admin/grok/sso-to-oauth tokens={len(payload.get('sso_tokens') or [])}")
        if dry_run:
            return {"dry_run": True, "route": route, "payload": payload}
        return unwrap_data(
            http_json_or_die(
                base_url,
                "/admin/grok/sso-to-oauth",
                method="POST",
                payload=payload,
                api_key=api_key,
                idempotency_key=idempotency_key_for("grok-sso", payload),
            )
        )

    die(f"未实现的路由: {route}")


def resolve_edge_base_url(doc: dict[str, Any], fallback: str) -> str:
    explicit = str(doc.get("edge_base_url") or os.environ.get("TOKENKEY_EDGE_BASE_URL") or "").strip()
    if explicit:
        return explicit.rstrip("/")
    edge_id = str(doc.get("edge_id") or "").strip()
    if edge_id:
        from edge_relay import edge_base_url as _edge_base_url

        return _edge_base_url(edge_id)
    if fallback and fallback != PROD_BASE_URL_DEFAULT:
        return fallback.rstrip("/")
    die("edge_oauth_relay 需要 edge_id、edge_base_url 或 TOKENKEY_EDGE_BASE_URL")


def dispatch_edge_oauth_relay(
    doc: dict[str, Any],
    *,
    prod_base_url: str,
    edge_base_url: str,
    prod_api_key: str,
    edge_api_key: str,
    dry_run: bool,
    source: str,
) -> dict[str, Any]:
    validate_edge_oauth_relay(doc, path_hint=source)

    def list_prod_accounts() -> list[dict[str, Any]]:
        from edge_relay import list_admin_accounts as _list_admin_accounts

        return _list_admin_accounts(
            http_json,
            unwrap_data,
            base_url=prod_base_url,
            api_key=prod_api_key,
        )

    plan = plan_edge_oauth_relay(doc, list_prod_accounts=list_prod_accounts)
    edge_spec = plan["edge_oauth"]
    edge_route = detect_route(edge_spec)
    log(
        f"{source}: edge_oauth_relay edge={plan['edge_id']} pool={plan['pool_platform']} "
        f"edge_route={edge_route} prod={plan['prod_action']}"
    )

    out: dict[str, Any] = {
        "import_profile": "edge_oauth_relay",
        "edge_id": plan["edge_id"],
        "pool_platform": plan["pool_platform"],
        "prod_action": plan["prod_action"],
        "prod_reason": plan["prod_reason"],
    }
    if plan.get("prod_existing"):
        out["prod_existing"] = {
            "id": plan["prod_existing"].get("id"),
            "name": plan["prod_existing"].get("name"),
        }

    if dry_run:
        out["edge"] = {
            "base_url": edge_base_url,
            "route": edge_route,
            "payload_preview": dispatch_import(
                edge_base_url,
                edge_api_key,
                edge_route,
                edge_spec,
                dry_run=True,
                source=f"{source}:edge",
            ),
        }
        if plan["prod_create"]:
            edge_key, issue_meta = ensure_edge_relay_api_key(
                doc,
                http_json=http_json,
                unwrap_data=unwrap_data,
                http_json_or_die=http_json_or_die,
                edge_base_url=edge_base_url,
                edge_admin_api_key=edge_api_key,
                dry_run=True,
            )
            prod_payload = dict(plan["prod_create"])
            prod_payload.setdefault("credentials", {})
            prod_payload["credentials"]["api_key"] = edge_key
            out["prod"] = {
                "base_url": prod_base_url,
                "payload": prod_payload,
                "edge_api_key_issue": issue_meta,
            }
        return out

    edge_result = dispatch_import(
        edge_base_url,
        edge_api_key,
        edge_route,
        edge_spec,
        dry_run=False,
        source=f"{source}:edge",
    )
    out["edge"] = edge_result

    if plan["prod_action"] == "skip":
        log(f"{source}: 跳过 prod 中继 — {plan['prod_reason']}")
        return out

    edge_key, issue_meta = ensure_edge_relay_api_key(
        doc,
        http_json=http_json,
        unwrap_data=unwrap_data,
        http_json_or_die=http_json_or_die,
        edge_base_url=edge_base_url,
        edge_admin_api_key=edge_api_key,
        dry_run=False,
    )
    out["edge_api_key_issue"] = issue_meta

    prod_payload = dict(plan["prod_create"] or {})
    prod_payload.setdefault("credentials", {})
    prod_payload["credentials"]["api_key"] = edge_key
    validate_create_account(prod_payload, path_hint=f"{source}:prod_relay")
    prod_result = unwrap_data(
        http_json_or_die(
            prod_base_url,
            "/admin/accounts",
            method="POST",
            payload=prod_payload,
            api_key=prod_api_key,
            idempotency_key=idempotency_key_for(
                f"prod-relay-{plan['edge_id']}-{plan['pool_platform']}",
                prod_payload,
            ),
        )
    )
    out["prod"] = prod_result
    return out


def process_document(
    base_url: str,
    api_key: str,
    doc: Any,
    *,
    dry_run: bool,
    source: str,
    prod_base_url: str | None = None,
    edge_base_url_override: str | None = None,
    prod_api_key: str | None = None,
    edge_api_key: str | None = None,
) -> list[Any]:
    if isinstance(doc, list):
        results: list[Any] = []
        for index, item in enumerate(doc, start=1):
            if not isinstance(item, dict):
                die(f"{source}[{index}] 必须是对象")
            results.extend(
                process_document(
                    base_url,
                    api_key,
                    item,
                    dry_run=dry_run,
                    source=f"{source}[{index}]",
                    prod_base_url=prod_base_url,
                    edge_base_url_override=edge_base_url_override,
                    prod_api_key=prod_api_key,
                    edge_api_key=edge_api_key,
                )
            )
        return results

    if not isinstance(doc, dict):
        die(f"{source}: 必须是 JSON 对象")

    route = detect_route(doc)
    results: list[Any] = []

    if route == "edge_oauth_relay":
        prod_url = (prod_base_url or base_url).rstrip("/")
        edge_url = resolve_edge_base_url(doc, edge_base_url_override or base_url)
        return [
            dispatch_edge_oauth_relay(
                doc,
                prod_base_url=prod_url,
                edge_base_url=edge_url,
                prod_api_key=prod_api_key or api_key,
                edge_api_key=edge_api_key or api_key,
                dry_run=dry_run,
                source=source,
            )
        ]

    if route == "list":
        for index, item in enumerate(doc, start=1):
            if not isinstance(item, dict):
                die(f"{source}[{index}] 必须是对象")
            results.append(
                dispatch_import(
                    base_url,
                    api_key,
                    detect_route(item),
                    item,
                    dry_run=dry_run,
                    source=f"{source}[{index}]",
                )
            )
        return results

    if route == "batch_bundle":
        create_rows: list[dict[str, Any]] = []
        for index, item in enumerate(doc.get("accounts") or [], start=1):
            if not isinstance(item, dict):
                die(f"{source}.accounts[{index}] 必须是对象")
            item_route = detect_route(item)
            if item_route != "create_account":
                results.append(
                    dispatch_import(
                        base_url,
                        api_key,
                        item_route,
                        _merge_bundle_defaults(doc, item),
                        dry_run=dry_run,
                        source=f"{source}.accounts[{index}]",
                    )
                )
                continue
            merged = _merge_bundle_defaults(doc, item)
            validate_create_account(merged, path_hint=f"{source}.accounts[{index}]")
            create_rows.append(build_create_payload(merged))

        if create_rows:
            batch_payload = {"accounts": create_rows}
            log(f"{source}: POST /admin/accounts/batch count={len(create_rows)}")
            if dry_run:
                results.append({"dry_run": True, "route": "batch_create", "payload": batch_payload})
            else:
                results.append(
                    unwrap_data(
                        http_json_or_die(
                            base_url,
                            "/admin/accounts/batch",
                            method="POST",
                            payload=batch_payload,
                            api_key=api_key,
                            idempotency_key=idempotency_key_for("batch", batch_payload),
                        )
                    )
                )
        return results

    merged = doc
    results.append(
        dispatch_import(base_url, api_key, route, merged, dry_run=dry_run, source=source)
    )
    return results


def _merge_bundle_defaults(bundle: dict[str, Any], item: dict[str, Any]) -> dict[str, Any]:
    out = dict(item)
    for key in (
        "group_ids",
        "proxy_id",
        "concurrency",
        "priority",
        "rate_multiplier",
        "load_factor",
        "skip_default_group_bind",
        "confirm_mixed_channel_risk",
    ):
        if key not in out and key in bundle:
            out[key] = bundle[key]
    if not str(out.get("name") or "").strip() and str(bundle.get("name") or "").strip():
        out["name"] = bundle["name"]
    return out


def collect_input_paths(paths: list[str]) -> list[Path]:
    out: list[Path] = []
    for raw in paths:
        path = Path(raw).expanduser()
        if path.is_dir():
            out.extend(sorted(path.glob("*.json")))
            continue
        if not path.is_file():
            die(f"路径不存在: {path}")
        out.append(path)
    if not out:
        die("未找到任何 .json 输入文件")
    return out


def cmd_validate(args: argparse.Namespace) -> int:
    paths = collect_input_paths(args.paths)
    for path in paths:
        doc = load_json_file(path)
        route = detect_route(doc)
        log(f"OK {path} -> {route}")
        if route == "list":
            for index, item in enumerate(doc, start=1):
                detect_route(item)
                if detect_route(item) == "create_account":
                    validate_create_account(item, path_hint=f"{path}[{index}]")
        elif route == "batch_bundle":
            for index, item in enumerate(doc.get("accounts") or [], start=1):
                item_route = detect_route(item)
                log(f"  accounts[{index}] -> {item_route}")
                if item_route == "create_account":
                    validate_create_account(
                        _merge_bundle_defaults(doc, item),
                        path_hint=f"{path}.accounts[{index}]",
                    )
        elif route == "create_account":
            validate_create_account(doc, path_hint=str(path))
        elif route == "edge_oauth_relay":
            validate_edge_oauth_relay(doc, path_hint=str(path))
            edge_spec = doc.get("edge_oauth")
            if isinstance(edge_spec, dict):
                inner_route = detect_route(edge_spec)
                log(f"  edge_oauth -> {inner_route}")
                if inner_route == "create_account":
                    validate_create_account(edge_spec, path_hint=f"{path}.edge_oauth")
    print(json.dumps({"validated": len(paths)}, ensure_ascii=False))
    return 0


def cmd_import(args: argparse.Namespace) -> int:
    prod_base = args.prod_base_url.rstrip("/")
    edge_base = args.edge_base_url.rstrip("/") if args.edge_base_url else ""
    prod_key = "" if args.dry_run else prod_admin_api_key()
    edge_key = "" if args.dry_run else edge_admin_api_key()
    # Single-host imports still accept TOKENKEY_BASE_URL / TOKENKEY_ADMIN_API_KEY.
    fallback_key = "" if args.dry_run else admin_api_key()
    paths = collect_input_paths(args.paths)
    all_results: list[dict[str, Any]] = []
    for path in paths:
        doc = load_json_file(path)
        results = process_document(
            prod_base,
            fallback_key,
            doc,
            dry_run=args.dry_run,
            source=str(path),
            prod_base_url=prod_base,
            edge_base_url_override=edge_base or None,
            prod_api_key=prod_key or fallback_key,
            edge_api_key=edge_key or fallback_key,
        )
        all_results.append({"file": str(path), "results": results})
    print(json.dumps(all_results, ensure_ascii=False, indent=2))
    if args.dry_run:
        log("dry-run 完成，未写入 TokenKey")
    return 0


def cmd_list_platforms(_: argparse.Namespace) -> int:
    rows = []
    for platform in SCHEDULING_PLATFORMS:
        rows.append(
            {
                "platform": platform,
                "common_types": _platform_type_hints(platform),
                "notes": _platform_notes(platform),
            }
        )
    print(json.dumps(rows, ensure_ascii=False, indent=2))
    return 0


def _platform_type_hints(platform: str) -> list[str]:
    mapping = {
        "anthropic": ["oauth", "setup-token", "apikey", "bedrock", "service_account"],
        "openai": ["oauth", "apikey"],
        "gemini": ["oauth", "apikey", "service_account"],
        "antigravity": ["oauth", "apikey", "upstream"],
        "newapi": ["apikey", "service_account"],
        "kiro": ["oauth"],
        "grok": ["oauth", "apikey"],
    }
    return mapping.get(platform, list(ACCOUNT_TYPES))


def _platform_notes(platform: str) -> str:
    notes = {
        "openai": "OAuth/Codex session 可用 import_profile=codex_session 或原始 session JSON",
        "antigravity": "OAuth 导出 JSON 可用 import_profile=antigravity_oauth 或原始 export JSON",
        "newapi": "必须 channel_type + credentials.base_url；channel 列表见 list-channel-types",
        "grok": "SSO 批量可用 import_profile=grok_sso + sso_tokens",
        "kiro": "必须 credentials.tos_acknowledged=true",
    }
    return notes.get(platform, "")


def cmd_list_channel_types(args: argparse.Namespace) -> int:
    api_key = admin_api_key()
    data = unwrap_data(http_json_or_die(args.base_url.rstrip("/"), "/admin/channel-types", api_key=api_key))
    print(json.dumps(data, ensure_ascii=False, indent=2))
    return 0


def cmd_list_edges(_: argparse.Namespace) -> int:
    print(json.dumps(list_deployable_edges(), ensure_ascii=False, indent=2))
    return 0


def cmd_selftest(_: argparse.Namespace) -> int:
    import unittest

    loader = unittest.TestLoader()
    suite = unittest.TestSuite()
    for pattern in ("test_import_accounts.py", "test_edge_relay.py"):
        suite.addTests(loader.discover(str(SCRIPT_DIR), pattern=pattern))
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    return 0 if result.wasSuccessful() else 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="TokenKey ops account import tool")
    parser.add_argument(
        "--base-url",
        default=BASE_URL_DEFAULT,
        help=f"TokenKey prod API base URL (default: {PROD_BASE_URL_DEFAULT})",
    )
    parser.add_argument(
        "--prod-base-url",
        default=os.environ.get("TOKENKEY_PROD_BASE_URL", BASE_URL_DEFAULT),
        help="Prod API base URL for edge_oauth_relay (default: TOKENKEY_PROD_BASE_URL or --base-url)",
    )
    parser.add_argument(
        "--edge-base-url",
        default=os.environ.get("TOKENKEY_EDGE_BASE_URL", ""),
        help="Edge API base URL override for edge_oauth_relay",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_validate = sub.add_parser("validate", help="Validate JSON specs locally")
    p_validate.add_argument("paths", nargs="+", help="JSON files or directories")
    p_validate.set_defaults(func=cmd_validate)

    p_import = sub.add_parser("import", help="Import accounts via Admin API")
    p_import.add_argument("paths", nargs="+", help="JSON files or directories")
    p_import.add_argument(
        "--dry-run",
        action="store_true",
        help="Print resolved Admin API payloads without calling the server",
    )
    p_import.add_argument(
        "--yes",
        action="store_true",
        help="Required to perform real writes (ignored when --dry-run is set)",
    )
    p_import.set_defaults(func=cmd_import)

    p_platforms = sub.add_parser("list-platforms", help="Show supported platforms/types")
    p_platforms.set_defaults(func=cmd_list_platforms)

    p_channels = sub.add_parser("list-channel-types", help="Fetch live newapi channel types")
    p_channels.set_defaults(func=cmd_list_channel_types)

    p_edges = sub.add_parser("list-edges", help="List deployable edge nodes from fleet JSON")
    p_edges.set_defaults(func=cmd_list_edges)

    p_self = sub.add_parser("selftest", help="Run offline unit tests")
    p_self.set_defaults(func=cmd_selftest)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command == "import" and not args.dry_run and not args.yes:
        die("真实导入必须显式传 --yes；预览请加 --dry-run")
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
