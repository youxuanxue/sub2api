#!/usr/bin/env python3
"""Probe the Kiro *.kiro.dev runtime/management gateways with a local IDE token.

Replaces the brittle mitm path for HTTP protocol discovery: reads
~/.aws/sso/cache/kiro-auth-token.json (same source as onboarding docs) and
issues the same shape of requests the real Kiro IDE sends to
runtime.us-east-1.kiro.dev / management.us-east-1.kiro.dev.

Subcommands:
  all                 Run runtime-chat + management-usage (default).
  runtime-chat        POST /generateAssistantResponse on runtime host.
  management-usage    GET management Get-Usage-Limits (control plane).
  legacy-q-usage      POST q.us-east-1.amazonaws.com/ + X-Amz-Target GetUsageLimits.
  legacy-usage        GET legacy codewhisperer getUsageLimits (comparison).

Exit codes: 0 = all requested probes returned 2xx, 1 = probe HTTP/auth failure,
2 = usage/env error (missing token, bad args).

stdlib-only. Pure helpers are unit-tested by test_probe_runtime_gateway.py.
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import ssl
import sys
import time
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import ProxyHandler, Request, build_opener, HTTPSHandler

from capture_kiro_fingerprint import expected_amz_user_agent, expected_user_agent, load_kiro_constants

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_TOKEN_CACHE = Path.home() / ".aws" / "sso" / "cache" / "kiro-auth-token.json"

RUNTIME_ENDPOINT = "https://runtime.us-east-1.kiro.dev"
MANAGEMENT_ENDPOINT = "https://management.us-east-1.kiro.dev"
LEGACY_Q_ENDPOINT = "https://q.us-east-1.amazonaws.com"
LEGACY_CW_ENDPOINT = "https://codewhisperer.us-east-1.amazonaws.com"

X_AMZ_TARGET_RUNTIME_USAGE = (
    "com.amazon.aws.codewhisperer.runtime.AmazonCodeWhispererService.GetUsageLimits"
)
X_AMZ_TARGET_STREAMING_CHAT = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"

DEFAULT_PROBE_MODEL = "auto"
AUTO_MODEL = "auto"
DEFAULT_TOKEN_CACHE_DIR = Path.home() / ".aws" / "sso" / "cache"
SOCIAL_REFRESH_URL = "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"
DEFAULT_TIMEOUT_S = 45
MAX_BODY_SNIPPET = 800


@dataclass(frozen=True)
class ProbeSpec:
    name: str
    method: str
    url: str
    headers: dict[str, str]
    body: bytes | None


@dataclass(frozen=True)
class ProbeResult:
    name: str
    ok: bool
    status: int | None
    url: str
    method: str
    request_headers: dict[str, str]
    body_snippet: str
    error: str | None = None

    def to_dict(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "ok": self.ok,
            "status": self.status,
            "url": self.url,
            "method": self.method,
            "request_headers": self.request_headers,
            "body_snippet": self.body_snippet,
            "error": self.error,
        }


class ProbeEnvError(RuntimeError):
    """Missing token or invalid local setup (exit 2)."""


def redact_token(value: str) -> str:
    value = value.strip()
    if len(value) <= 12:
        return "<redacted>"
    return f"{value[:6]}…{value[-4:]}"


def redact_headers(headers: dict[str, str]) -> dict[str, str]:
    out: dict[str, str] = {}
    for key, val in headers.items():
        if key.lower() == "authorization":
            if val.lower().startswith("bearer "):
                out[key] = f"Bearer {redact_token(val[7:])}"
            else:
                out[key] = redact_token(val)
        else:
            out[key] = val
    return out


def load_local_token(path: Path = DEFAULT_TOKEN_CACHE) -> dict[str, Any]:
    if not path.is_file():
        raise ProbeEnvError(
            f"Kiro token cache not found: {path}\n"
            "Open Kiro IDE, sign in, then rerun this probe."
        )
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise ProbeEnvError(f"Invalid JSON in {path}: {exc}") from exc
    token = str(payload.get("accessToken") or "").strip()
    if not token:
        raise ProbeEnvError(f"{path} has no accessToken — sign in to Kiro IDE first.")
    return {
        "access_token": token,
        "refresh_token": str(payload.get("refreshToken") or "").strip(),
        "region": str(payload.get("region") or "us-east-1").strip() or "us-east-1",
        "profile_arn": str(payload.get("profileArn") or "").strip(),
        "auth_method": str(payload.get("authMethod") or "").strip(),
        "token_path": str(path),
    }


def build_ide_user_agent(kiro_ide_version: str, machine_id: str = "probe") -> str:
    machine_id = machine_id.strip() or "probe"
    return f"KiroIDE {kiro_ide_version} {machine_id}"


def build_headers(
    *,
    style: str,
    host: str,
    bearer_token: str,
    content_type: str,
    extra: dict[str, str] | None = None,
    machine_id: str = "probe",
) -> dict[str, str]:
    headers = {
        "Authorization": f"Bearer {bearer_token}",
        "Content-Type": content_type,
        "Accept": "*/*",
        "Host": host,
    }
    if style == "ide":
        consts = load_kiro_constants()
        headers["User-Agent"] = build_ide_user_agent(consts["kiro_ide_version"], machine_id)
    elif style == "tokenkey":
        consts = load_kiro_constants()
        headers["User-Agent"] = expected_user_agent(consts)
        headers["x-amz-user-agent"] = expected_amz_user_agent(consts)
        headers["x-amzn-codewhisperer-optout"] = "true"
    else:
        raise ValueError(f"unknown header style: {style}")
    if extra:
        headers.update(extra)
    return headers


def build_legacy_q_usage_spec(
    *,
    token: dict[str, Any],
    style: str,
    machine_id: str,
) -> ProbeSpec:
    """IDE Ur3 transport: POST q endpoint root with JSON-RPC style X-Amz-Target."""
    body_obj: dict[str, Any] = {"origin": "AI_EDITOR"}
    if token.get("profile_arn"):
        body_obj["profileArn"] = token["profile_arn"]
    body = json.dumps(body_obj, separators=(",", ":")).encode("utf-8")
    host = "q.us-east-1.amazonaws.com"
    headers = build_headers(
        style=style,
        host=host,
        bearer_token=token["access_token"],
        content_type="application/x-amz-json-1.0",
        extra={"X-Amz-Target": X_AMZ_TARGET_RUNTIME_USAGE},
        machine_id=machine_id,
    )
    return ProbeSpec(
        name="legacy-q-usage",
        method="POST",
        url=f"{LEGACY_Q_ENDPOINT}/",
        headers=headers,
        body=body,
    )


def build_management_usage_spec(
    *,
    token: dict[str, Any],
    style: str,
    machine_id: str,
) -> ProbeSpec:
    host = "management.us-east-1.kiro.dev"
    query = "origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true"
    if token.get("profile_arn"):
        query += f"&profileArn={quote(token['profile_arn'], safe='')}"
    headers = build_headers(
        style=style,
        host=host,
        bearer_token=token["access_token"],
        content_type="application/json",
        machine_id=machine_id,
    )
    return ProbeSpec(
        name="management-usage",
        method="GET",
        url=f"{MANAGEMENT_ENDPOINT}/Get-Usage-Limits?{query}",
        headers=headers,
        body=None,
    )


def build_runtime_chat_spec(
    *,
    token: dict[str, Any],
    style: str,
    machine_id: str,
    message: str,
    model_id: str,
) -> ProbeSpec:
    payload: dict[str, Any] = {
        "conversationState": {
            "chatTriggerType": "MANUAL",
            "conversationId": f"probe-{uuid.uuid4()}",
            "currentMessage": {
                "userInputMessage": {
                    "content": message,
                    "modelId": model_id,
                    "origin": "AI_EDITOR",
                }
            },
        }
    }
    if token.get("profile_arn"):
        payload["profileArn"] = token["profile_arn"]
    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    host = "runtime.us-east-1.kiro.dev"
    extra = {
        "X-Amz-Target": X_AMZ_TARGET_STREAMING_CHAT,
        "x-amzn-kiro-agent-mode": "vibe",
        "x-amzn-codewhisperer-optout": "true",
    }
    content_type = "application/json"
    headers = build_headers(
        style=style,
        host=host,
        bearer_token=token["access_token"],
        content_type=content_type,
        extra=extra,
        machine_id=machine_id,
    )
    return ProbeSpec(
        name="runtime-chat",
        method="POST",
        url=f"{RUNTIME_ENDPOINT}/generateAssistantResponse",
        headers=headers,
        body=body,
    )


def build_legacy_usage_spec(
    *,
    token: dict[str, Any],
    style: str,
    machine_id: str,
) -> ProbeSpec:
    host = "codewhisperer.us-east-1.amazonaws.com"
    query = "origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true"
    if token.get("profile_arn"):
        query += f"&profileArn={quote(token['profile_arn'], safe='')}"
    headers = build_headers(
        style=style,
        host=host,
        bearer_token=token["access_token"],
        content_type="application/json",
        machine_id=machine_id,
    )
    return ProbeSpec(
        name="legacy-usage",
        method="GET",
        url=f"{LEGACY_CW_ENDPOINT}/getUsageLimits?{query}",
        headers=headers,
        body=None,
    )


def make_http_opener(proxy_url: str | None) -> Any:
    handlers: list[Any] = [HTTPSHandler(context=ssl.create_default_context())]
    if proxy_url:
        handlers.insert(0, ProxyHandler({"http": proxy_url, "https": proxy_url}))
    return build_opener(*handlers)


def execute_probe(spec: ProbeSpec, *, timeout_s: int, proxy_url: str | None) -> ProbeResult:
    req = Request(spec.url, data=spec.body, method=spec.method)
    for key, val in spec.headers.items():
        req.add_header(key, val)
    redacted = redact_headers(spec.headers)
    try:
        with make_http_opener(proxy_url).open(req, timeout=timeout_s) as resp:
            raw = resp.read(4096)
            snippet = raw.decode("utf-8", errors="replace")[:MAX_BODY_SNIPPET]
            status = getattr(resp, "status", None) or resp.getcode()
            ok = 200 <= int(status) < 300
            return ProbeResult(
                name=spec.name,
                ok=ok,
                status=int(status),
                url=spec.url,
                method=spec.method,
                request_headers=redacted,
                body_snippet=snippet,
            )
    except HTTPError as exc:
        body = exc.read(4096).decode("utf-8", errors="replace")[:MAX_BODY_SNIPPET]
        return ProbeResult(
            name=spec.name,
            ok=False,
            status=exc.code,
            url=spec.url,
            method=spec.method,
            request_headers=redacted,
            body_snippet=body,
            error=str(exc),
        )
    except URLError as exc:
        return ProbeResult(
            name=spec.name,
            ok=False,
            status=None,
            url=spec.url,
            method=spec.method,
            request_headers=redacted,
            body_snippet="",
            error=str(exc.reason or exc),
        )


def resolve_proxy(explicit: str | None) -> str | None:
    if explicit:
        return explicit
    for key in ("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"):
        val = os.environ.get(key, "").strip()
        if val:
            return val
    return None


def print_human_report(results: list[ProbeResult]) -> None:
    for result in results:
        mark = "OK" if result.ok else "FAIL"
        status = result.status if result.status is not None else "—"
        print(f"[{mark}] {result.name}: HTTP {status} {result.method} {result.url}")
        if result.error:
            print(f"      error: {result.error}")
        if result.body_snippet:
            one_line = result.body_snippet.replace("\n", " ").strip()
            if len(one_line) > 240:
                one_line = one_line[:240] + "…"
            print(f"      body: {one_line}")


def apply_profile_arn(token: dict[str, Any], profile_arn: str | None) -> dict[str, Any]:
    merged = dict(token)
    if profile_arn:
        merged["profile_arn"] = profile_arn.strip()
    return merged


def auth_method_is_social(token: dict[str, Any]) -> bool:
    return str(token.get("auth_method") or "").lower() == "social"


def latest_idc_registration(cache_dir: Path) -> dict[str, str] | None:
    matches: list[tuple[float, dict[str, str]]] = []
    for raw in glob.glob(str(cache_dir / "*.json")):
        path = Path(raw)
        if path.name == "kiro-auth-token.json":
            continue
        try:
            payload = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        client_id = str(payload.get("clientId") or "").strip()
        client_secret = str(payload.get("clientSecret") or "").strip()
        if client_id and client_secret:
            matches.append((path.stat().st_mtime, {"client_id": client_id, "client_secret": client_secret}))
    if not matches:
        return None
    matches.sort(key=lambda item: item[0], reverse=True)
    return matches[0][1]


def refresh_access_token(
    token: dict[str, Any],
    *,
    cache_dir: Path = DEFAULT_TOKEN_CACHE_DIR,
    timeout_s: int = DEFAULT_TIMEOUT_S,
    proxy_url: str | None = None,
) -> dict[str, Any]:
    """Mint a fresh access token from the local refresh token (does not write cache)."""
    refresh_token = str(token.get("refresh_token") or "").strip()
    if not refresh_token:
        raise ProbeEnvError("cannot --refresh-token: refresh_token missing in token cache")

    region = str(token.get("region") or "us-east-1").strip() or "us-east-1"
    if auth_method_is_social(token):
        url = SOCIAL_REFRESH_URL
        body = {"refreshToken": refresh_token}
    else:
        registration = latest_idc_registration(cache_dir)
        if registration is None:
            raise ProbeEnvError(
                f"cannot --refresh-token: no IdC client registration in {cache_dir}"
            )
        url = f"https://oidc.{region}.amazonaws.com/token"
        body = {
            "clientId": registration["client_id"],
            "clientSecret": registration["client_secret"],
            "refreshToken": refresh_token,
            "grantType": "refresh_token",
        }

    req = Request(
        url,
        data=json.dumps(body).encode("utf-8"),
        method="POST",
        headers={"Content-Type": "application/json", "Accept": "application/json"},
    )
    try:
        with make_http_opener(proxy_url).open(req, timeout=timeout_s) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
    except HTTPError as exc:
        detail = exc.read(512).decode("utf-8", errors="replace")
        raise ProbeEnvError(f"token refresh failed: HTTP {exc.code}: {detail[:400]}") from exc
    except URLError as exc:
        raise ProbeEnvError(f"token refresh failed: {exc.reason or exc}") from exc

    access_token = str(payload.get("accessToken") or "").strip()
    if not access_token:
        raise ProbeEnvError("token refresh returned no accessToken")

    refreshed = dict(token)
    refreshed["access_token"] = access_token
    refreshed["refresh_token"] = str(payload.get("refreshToken") or refresh_token).strip()
    profile_arn = str(payload.get("profileArn") or "").strip()
    if profile_arn:
        refreshed["profile_arn"] = profile_arn
    refreshed["token_source"] = "refreshed"
    return refreshed


def http_json(
    *,
    method: str,
    url: str,
    token: dict[str, Any],
    style: str,
    machine_id: str,
    proxy_url: str | None,
    timeout_s: int,
    body: dict[str, Any] | None = None,
    content_type: str = "application/json",
) -> Any:
    host = url.split("//", 1)[-1].split("/", 1)[0]
    headers = build_headers(
        style=style,
        host=host,
        bearer_token=token["access_token"],
        content_type=content_type,
        machine_id=machine_id,
    )
    data = None if body is None else json.dumps(body, separators=(",", ":")).encode("utf-8")
    req = Request(url, data=data, method=method, headers=headers)
    try:
        with make_http_opener(proxy_url).open(req, timeout=timeout_s) as resp:
            raw = resp.read()
            if not raw:
                return {}
            return json.loads(raw.decode("utf-8"))
    except HTTPError as exc:
        detail = exc.read(512).decode("utf-8", errors="replace")
        raise ProbeEnvError(f"{method} {url} failed: HTTP {exc.code}: {detail[:400]}") from exc
    except URLError as exc:
        raise ProbeEnvError(f"{method} {url} failed: {exc.reason or exc}") from exc
    except json.JSONDecodeError as exc:
        raise ProbeEnvError(f"{method} {url} returned invalid JSON") from exc


def parse_profile_arns(payload: Any) -> list[str]:
    if not isinstance(payload, dict):
        return []
    profiles = payload.get("profiles")
    if not isinstance(profiles, list):
        return []
    out: list[str] = []
    for item in profiles:
        if isinstance(item, dict):
            arn = str(item.get("arn") or "").strip()
            if arn:
                out.append(arn)
    return out


def parse_model_ids(payload: Any) -> list[str]:
    if not isinstance(payload, dict):
        return []
    models = payload.get("models")
    if not isinstance(models, list):
        return []
    out: list[str] = []
    for item in models:
        if isinstance(item, dict):
            model_id = str(item.get("modelId") or "").strip()
            if model_id:
                out.append(model_id)
    return out


def resolve_profile_arn_via_api(
    token: dict[str, Any],
    *,
    style: str,
    machine_id: str,
    proxy_url: str | None,
    timeout_s: int,
) -> str:
    url = f"{MANAGEMENT_ENDPOINT}/List-Available-Profiles"
    payload = http_json(
        method="POST",
        url=url,
        token=token,
        style=style,
        machine_id=machine_id,
        proxy_url=proxy_url,
        timeout_s=timeout_s,
        body={"maxResults": 10},
    )
    arns = parse_profile_arns(payload)
    if not arns:
        raise ProbeEnvError("ListAvailableProfiles returned no profile arn")
    return arns[0]


def resolve_model_id_via_api(
    token: dict[str, Any],
    profile_arn: str,
    *,
    style: str,
    machine_id: str,
    proxy_url: str | None,
    timeout_s: int,
) -> str:
    query = f"origin=AI_EDITOR&maxResults=20&profileArn={quote(profile_arn, safe='')}"
    url = f"{MANAGEMENT_ENDPOINT}/List-Available-Models?{query}"
    payload = http_json(
        method="GET",
        url=url,
        token=token,
        style=style,
        machine_id=machine_id,
        proxy_url=proxy_url,
        timeout_s=timeout_s,
        body=None,
    )
    model_ids = parse_model_ids(payload)
    if not model_ids:
        raise ProbeEnvError("ListAvailableModels returned no modelId")
    return model_ids[0]


def prepare_token_for_probes(
    args: argparse.Namespace,
    *,
    needs_profile: bool,
    needs_model: bool,
) -> tuple[dict[str, Any], str]:
    token = apply_profile_arn(
        load_local_token(Path(args.token_cache)),
        args.profile_arn or None,
    )
    proxy_url = resolve_proxy(args.proxy or None)

    if args.refresh_token:
        cache_dir = Path(args.token_cache).resolve().parent
        token = refresh_access_token(
            token,
            cache_dir=cache_dir,
            timeout_s=args.timeout,
            proxy_url=proxy_url,
        )
        print("info: access token refreshed locally (cache file not modified)", file=sys.stderr)

    if needs_profile and not token.get("profile_arn") and not args.no_auto_profile:
        arn = resolve_profile_arn_via_api(
            token,
            style=args.header_style,
            machine_id=args.machine_id,
            proxy_url=proxy_url,
            timeout_s=args.timeout,
        )
        token = apply_profile_arn(token, arn)
        print(f"info: auto-resolved profileArn={arn}", file=sys.stderr)

    model_id = args.model_id
    if needs_model and model_id == AUTO_MODEL:
        if not token.get("profile_arn"):
            raise ProbeEnvError(
                "cannot auto-resolve modelId without profileArn "
                "(pass --profile-arn or drop --no-auto-profile)"
            )
        model_id = resolve_model_id_via_api(
            token,
            token["profile_arn"],
            style=args.header_style,
            machine_id=args.machine_id,
            proxy_url=proxy_url,
            timeout_s=args.timeout,
        )
        print(f"info: auto-resolved modelId={model_id}", file=sys.stderr)

    return token, model_id


def cmd_probe(args: argparse.Namespace) -> int:
    commands = args.commands or ["all"]
    needs_profile = any(c in commands for c in ("all", "management-usage", "runtime-chat", "legacy-usage"))
    needs_model = any(c in commands for c in ("all", "runtime-chat"))

    try:
        token, model_id = prepare_token_for_probes(
            args,
            needs_profile=needs_profile,
            needs_model=needs_model,
        )
    except ProbeEnvError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    proxy_url = resolve_proxy(args.proxy or None)
    probes: list[ProbeSpec] = []

    def add(spec: ProbeSpec) -> None:
        probes.append(spec)

    for command in commands:
        if command == "all":
            add(
                build_management_usage_spec(
                    token=token, style=args.header_style, machine_id=args.machine_id
                )
            )
            add(
                build_runtime_chat_spec(
                    token=token,
                    style=args.header_style,
                    machine_id=args.machine_id,
                    message=args.message,
                    model_id=model_id,
                )
            )
            if args.include_legacy:
                add(
                    build_legacy_q_usage_spec(
                        token=token, style=args.header_style, machine_id=args.machine_id
                    )
                )
                add(
                    build_legacy_usage_spec(
                        token=token, style=args.header_style, machine_id=args.machine_id
                    )
                )
        elif command == "legacy-q-usage":
            add(
                build_legacy_q_usage_spec(
                    token=token, style=args.header_style, machine_id=args.machine_id
                )
            )
        elif command == "management-usage":
            add(
                build_management_usage_spec(
                    token=token, style=args.header_style, machine_id=args.machine_id
                )
            )
        elif command == "runtime-chat":
            add(
                build_runtime_chat_spec(
                    token=token,
                    style=args.header_style,
                    machine_id=args.machine_id,
                    message=args.message,
                    model_id=model_id,
                )
            )
        elif command == "legacy-usage":
            add(
                build_legacy_usage_spec(
                    token=token, style=args.header_style, machine_id=args.machine_id
                )
            )
        else:
            print(f"error: unknown command: {command}", file=sys.stderr)
            return 2

    if args.dry_run:
        for spec in probes:
            print(json.dumps({
                "name": spec.name,
                "method": spec.method,
                "url": spec.url,
                "headers": redact_headers(spec.headers),
                "body": spec.body.decode("utf-8") if spec.body else None,
            }, ensure_ascii=False, indent=2))
        return 0

    results = [
        execute_probe(spec, timeout_s=args.timeout, proxy_url=proxy_url) for spec in probes
    ]
    print_human_report(results)
    if args.json:
        print(json.dumps([r.to_dict() for r in results], ensure_ascii=False, indent=2))

    if all(r.ok for r in results):
        return 0
    return 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Probe Kiro runtime/management *.kiro.dev gateways with a local IDE token."
    )
    parser.add_argument(
        "commands",
        nargs="*",
        choices=["all", "runtime-chat", "management-usage", "legacy-q-usage", "legacy-usage"],
        help="probes to run (default: all)",
    )
    parser.add_argument(
        "--token-cache",
        default=str(DEFAULT_TOKEN_CACHE),
        help=f"path to kiro-auth-token.json (default: {DEFAULT_TOKEN_CACHE})",
    )
    parser.add_argument(
        "--header-style",
        choices=["ide", "tokenkey"],
        default="ide",
        help="ide = KiroIDE UA (real IDE shape); tokenkey = aws-sdk-js UA (TokenKey forward shape)",
    )
    parser.add_argument("--machine-id", default="probe", help="machine id suffix for ide UA")
    parser.add_argument(
        "--refresh-token",
        action="store_true",
        help="refresh access token from local refresh token before probing (does not write cache)",
    )
    parser.add_argument(
        "--no-auto-profile",
        action="store_true",
        help="do not call ListAvailableProfiles when profileArn is missing",
    )
    parser.add_argument("--profile-arn", default="", help="override profileArn when token cache lacks it")
    parser.add_argument("--message", default="ping", help="runtime-chat user message")
    parser.add_argument(
        "--model-id",
        default=DEFAULT_PROBE_MODEL,
        help='runtime-chat modelId, or "auto" to pick the first ListAvailableModels entry (default: auto)',
    )
    parser.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT_S, help="HTTP timeout seconds")
    parser.add_argument(
        "--proxy",
        default="",
        help="explicit proxy URL (else HTTPS_PROXY / HTTP_PROXY); Clash example: http://127.0.0.1:7890",
    )
    parser.add_argument("--include-legacy", action="store_true", help="with all, also probe codewhisperer host")
    parser.add_argument("--dry-run", action="store_true", help="print request shapes without sending")
    parser.add_argument("--json", action="store_true", help="also print JSON report on stdout")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if not args.commands:
        args.commands = ["all"]
    return cmd_probe(args)


if __name__ == "__main__":
    raise SystemExit(main())
