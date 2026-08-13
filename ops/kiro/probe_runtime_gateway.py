#!/usr/bin/env python3
"""Synthetic Kiro upstream-compatibility probe.

This script uses credentials from the local Kiro CLI cache and request identity
from the committed CLI canonical profile. It does not launch the real client and
is therefore never valid fingerprint evidence. Reports contain status and a
redacted request shape only: no credential, profile ARN, user body, URL query,
or upstream response body is emitted.
"""
from __future__ import annotations

import argparse
import json
import os
import ssl
import sys
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlsplit, urlunsplit
from urllib.request import HTTPSHandler, ProxyHandler, Request, build_opener

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_TOKEN_CACHE = Path.home() / ".aws/sso/cache/kiro-auth-token.json"
DEFAULT_CLI_PROFILE = REPO_ROOT / "deploy/aws/stage0/tk_canonical_kiro_cli.json"
RUNTIME_ENDPOINT = "https://runtime.us-east-1.kiro.dev"
MANAGEMENT_ENDPOINT = "https://management.us-east-1.kiro.dev"
LEGACY_Q_ENDPOINT = "https://q.us-east-1.amazonaws.com"
LEGACY_CW_ENDPOINT = "https://codewhisperer.us-east-1.amazonaws.com"
X_AMZ_TARGET_RUNTIME_USAGE = "com.amazon.aws.codewhisperer.runtime.AmazonCodeWhispererService.GetUsageLimits"
X_AMZ_TARGET_STREAMING_CHAT = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
DEFAULT_TIMEOUT_S = 45
AUTO_MODEL = "auto"


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
    method: str
    url: str
    request_headers: dict[str, str]
    error: str | None = None

    def to_dict(self) -> dict[str, Any]:
        return {
            "evidence_eligible": False,
            "name": self.name,
            "ok": self.ok,
            "status": self.status,
            "method": self.method,
            "url": redact_url(self.url),
            "request_headers": redact_headers(self.request_headers),
            "error": self.error,
        }


class ProbeEnvError(RuntimeError):
    pass


def load_local_token(path: Path = DEFAULT_TOKEN_CACHE) -> dict[str, Any]:
    if not path.is_file():
        raise ProbeEnvError(f"Kiro CLI token cache not found: {path}")
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise ProbeEnvError(f"invalid token cache JSON: {path}") from exc
    if not isinstance(payload, dict):
        raise ProbeEnvError("token cache must be an object")
    access_token = str(payload.get("accessToken") or payload.get("access_token") or "").strip()
    if not access_token:
        raise ProbeEnvError("Kiro CLI token cache has no access token")
    return {
        "access_token": access_token,
        "profile_arn": str(payload.get("profileArn") or payload.get("profile_arn") or "").strip(),
    }


def load_cli_identity(path: Path = DEFAULT_CLI_PROFILE) -> dict[str, str]:
    if not path.is_file():
        raise ProbeEnvError(f"Kiro CLI canonical profile not found: {path}")
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
        observed = payload["observed"]
        user_agent = str(observed["user_agent"]).strip()
        amz_user_agent = str(observed["x_amz_user_agent"]).strip()
    except (json.JSONDecodeError, KeyError, TypeError) as exc:
        raise ProbeEnvError("Kiro CLI canonical profile has no observed HTTP identity") from exc
    if payload.get("name") != "tk_canonical_kiro_cli" or not user_agent or not amz_user_agent:
        raise ProbeEnvError("Kiro CLI canonical profile identity is invalid")
    return {"user_agent": user_agent, "x_amz_user_agent": amz_user_agent}


def redact_headers(headers: dict[str, str]) -> dict[str, str]:
    return {key: "<redacted>" if key.lower() == "authorization" else value for key, value in headers.items()}


def redact_url(url: str) -> str:
    split = urlsplit(url)
    return urlunsplit((split.scheme, split.netloc, split.path, "", ""))


def build_headers(*, host: str, bearer_token: str, content_type: str, identity: dict[str, str], extra: dict[str, str] | None = None) -> dict[str, str]:
    headers = {
        "Authorization": f"Bearer {bearer_token}",
        "Content-Type": content_type,
        "Accept": "*/*",
        "Host": host,
        "User-Agent": identity["user_agent"],
        "x-amz-user-agent": identity["x_amz_user_agent"],
        "x-amzn-codewhisperer-optout": "false",
    }
    if extra:
        headers.update(extra)
    return headers


def build_legacy_q_usage_spec(*, token: dict[str, Any], identity: dict[str, str]) -> ProbeSpec:
    body: dict[str, Any] = {"origin": "AI_EDITOR"}
    if token.get("profile_arn"):
        body["profileArn"] = token["profile_arn"]
    return ProbeSpec(
        "legacy-q-usage", "POST", f"{LEGACY_Q_ENDPOINT}/",
        build_headers(host="q.us-east-1.amazonaws.com", bearer_token=token["access_token"], content_type="application/x-amz-json-1.0", identity=identity, extra={"X-Amz-Target": X_AMZ_TARGET_RUNTIME_USAGE}),
        json.dumps(body, separators=(",", ":")).encode(),
    )


def build_management_usage_spec(*, token: dict[str, Any], identity: dict[str, str]) -> ProbeSpec:
    query = "origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true"
    if token.get("profile_arn"):
        query += f"&profileArn={quote(token['profile_arn'], safe='')}"
    return ProbeSpec(
        "management-usage", "GET", f"{MANAGEMENT_ENDPOINT}/Get-Usage-Limits?{query}",
        build_headers(host="management.us-east-1.kiro.dev", bearer_token=token["access_token"], content_type="application/json", identity=identity),
        None,
    )


def build_runtime_chat_spec(*, token: dict[str, Any], identity: dict[str, str], message: str, model_id: str) -> ProbeSpec:
    payload: dict[str, Any] = {"conversationState": {
        "chatTriggerType": "MANUAL",
        "conversationId": f"synthetic-probe-{uuid.uuid4()}",
        "currentMessage": {"userInputMessage": {"content": message, "modelId": model_id, "origin": "AI_EDITOR"}},
    }}
    if token.get("profile_arn"):
        payload["profileArn"] = token["profile_arn"]
    return ProbeSpec(
        "runtime-chat", "POST", f"{RUNTIME_ENDPOINT}/generateAssistantResponse",
        build_headers(host="runtime.us-east-1.kiro.dev", bearer_token=token["access_token"], content_type="application/json", identity=identity, extra={"X-Amz-Target": X_AMZ_TARGET_STREAMING_CHAT, "x-amzn-kiro-agent-mode": "vibe"}),
        json.dumps(payload, separators=(",", ":")).encode(),
    )


def build_legacy_usage_spec(*, token: dict[str, Any], identity: dict[str, str]) -> ProbeSpec:
    query = "origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true"
    if token.get("profile_arn"):
        query += f"&profileArn={quote(token['profile_arn'], safe='')}"
    return ProbeSpec(
        "legacy-usage", "GET", f"{LEGACY_CW_ENDPOINT}/getUsageLimits?{query}",
        build_headers(host="codewhisperer.us-east-1.amazonaws.com", bearer_token=token["access_token"], content_type="application/json", identity=identity),
        None,
    )


def resolve_proxy(explicit: str) -> str | None:
    if explicit:
        return explicit
    for key in ("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"):
        if os.environ.get(key, "").strip():
            return os.environ[key].strip()
    return None


def make_http_opener(proxy: str | None):
    handlers: list[Any] = [HTTPSHandler(context=ssl.create_default_context())]
    if proxy:
        handlers.insert(0, ProxyHandler({"http": proxy, "https": proxy}))
    return build_opener(*handlers)


def execute_probe(spec: ProbeSpec, *, timeout: int, proxy: str | None) -> ProbeResult:
    request = Request(spec.url, data=spec.body, method=spec.method, headers=spec.headers)
    try:
        with make_http_opener(proxy).open(request, timeout=timeout) as response:
            status = int(getattr(response, "status", None) or response.getcode())
            response.read()
            return ProbeResult(spec.name, 200 <= status < 300, status, spec.method, spec.url, spec.headers)
    except HTTPError as exc:
        exc.read()
        return ProbeResult(spec.name, False, exc.code, spec.method, spec.url, spec.headers, f"HTTP {exc.code}")
    except URLError:
        return ProbeResult(spec.name, False, None, spec.method, spec.url, spec.headers, "network error")


def parse_profile_arns(payload: Any) -> list[str]:
    if not isinstance(payload, dict) or not isinstance(payload.get("profiles"), list):
        return []
    return [str(item.get("arn")).strip() for item in payload["profiles"] if isinstance(item, dict) and str(item.get("arn") or "").strip()]


def parse_model_ids(payload: Any) -> list[str]:
    if not isinstance(payload, dict) or not isinstance(payload.get("models"), list):
        return []
    return [str(item.get("modelId")).strip() for item in payload["models"] if isinstance(item, dict) and str(item.get("modelId") or "").strip()]


def http_json(*, method: str, url: str, token: dict[str, Any], identity: dict[str, str], proxy: str | None, timeout: int, body: dict[str, Any] | None = None) -> Any:
    host = urlsplit(url).netloc
    request = Request(
        url,
        data=None if body is None else json.dumps(body, separators=(",", ":")).encode(),
        method=method,
        headers=build_headers(host=host, bearer_token=token["access_token"], content_type="application/json", identity=identity),
    )
    try:
        with make_http_opener(proxy).open(request, timeout=timeout) as response:
            raw = response.read()
        return json.loads(raw) if raw else {}
    except HTTPError as exc:
        exc.read()
        raise ProbeEnvError(f"synthetic compatibility lookup failed: HTTP {exc.code}") from exc
    except (URLError, json.JSONDecodeError) as exc:
        raise ProbeEnvError("synthetic compatibility lookup failed") from exc


def prepare_token(args: argparse.Namespace, identity: dict[str, str], *, needs_profile: bool, needs_model: bool) -> tuple[dict[str, Any], str]:
    token = load_local_token(Path(args.token_cache))
    if args.profile_arn:
        token["profile_arn"] = args.profile_arn
    proxy = resolve_proxy(args.proxy)
    if needs_profile and not token.get("profile_arn"):
        payload = http_json(method="POST", url=f"{MANAGEMENT_ENDPOINT}/List-Available-Profiles", token=token, identity=identity, proxy=proxy, timeout=args.timeout, body={"maxResults": 10})
        arns = parse_profile_arns(payload)
        if not arns:
            raise ProbeEnvError("ListAvailableProfiles returned no profile")
        token["profile_arn"] = arns[0]
    model_id = args.model_id
    if needs_model and model_id == AUTO_MODEL:
        if not token.get("profile_arn"):
            raise ProbeEnvError("cannot auto-resolve model without profile")
        query = f"origin=AI_EDITOR&maxResults=20&profileArn={quote(token['profile_arn'], safe='')}"
        payload = http_json(method="GET", url=f"{MANAGEMENT_ENDPOINT}/List-Available-Models?{query}", token=token, identity=identity, proxy=proxy, timeout=args.timeout)
        models = parse_model_ids(payload)
        if not models:
            raise ProbeEnvError("ListAvailableModels returned no model")
        model_id = models[0]
    return token, model_id


def safe_shape(spec: ProbeSpec) -> dict[str, Any]:
    body_keys: list[str] = []
    if spec.body:
        try:
            body = json.loads(spec.body)
            if isinstance(body, dict):
                body_keys = sorted(key for key in body if key != "profileArn")
        except json.JSONDecodeError:
            pass
    return {
        "evidence_eligible": False,
        "name": spec.name,
        "method": spec.method,
        "url": redact_url(spec.url),
        "headers": redact_headers(spec.headers),
        "body_keys": body_keys,
    }


def cmd_probe(args: argparse.Namespace) -> int:
    commands = args.commands or ["all"]
    try:
        identity = load_cli_identity(Path(args.cli_profile))
        needs_profile = any(command in {"all", "management-usage", "runtime-chat", "legacy-usage"} for command in commands)
        needs_model = any(command in {"all", "runtime-chat"} for command in commands)
        token, model_id = prepare_token(args, identity, needs_profile=needs_profile, needs_model=needs_model)
    except ProbeEnvError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    builders = {
        "management-usage": lambda: build_management_usage_spec(token=token, identity=identity),
        "runtime-chat": lambda: build_runtime_chat_spec(token=token, identity=identity, message=args.message, model_id=model_id),
        "legacy-q-usage": lambda: build_legacy_q_usage_spec(token=token, identity=identity),
        "legacy-usage": lambda: build_legacy_usage_spec(token=token, identity=identity),
    }
    specs: list[ProbeSpec] = []
    for command in commands:
        if command == "all":
            specs.extend((builders["management-usage"](), builders["runtime-chat"]()))
            if args.include_legacy:
                specs.extend((builders["legacy-q-usage"](), builders["legacy-usage"]()))
        else:
            specs.append(builders[command]())

    if args.dry_run:
        print(json.dumps([safe_shape(spec) for spec in specs], ensure_ascii=False, indent=2))
        return 0
    results = [execute_probe(spec, timeout=args.timeout, proxy=resolve_proxy(args.proxy)) for spec in specs]
    report = [result.to_dict() for result in results]
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if all(result.ok for result in results) else 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("commands", nargs="*", choices=["all", "runtime-chat", "management-usage", "legacy-q-usage", "legacy-usage"])
    parser.add_argument("--token-cache", default=str(DEFAULT_TOKEN_CACHE))
    parser.add_argument("--cli-profile", default=os.environ.get("TOKENKEY_KIRO_CLI_PROFILE", str(DEFAULT_CLI_PROFILE)))
    parser.add_argument("--profile-arn", default="", help=argparse.SUPPRESS)
    parser.add_argument("--message", default="ping")
    parser.add_argument("--model-id", default=AUTO_MODEL)
    parser.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT_S)
    parser.add_argument("--proxy", default="")
    parser.add_argument("--include-legacy", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    return cmd_probe(build_parser().parse_args(argv))


if __name__ == "__main__":
    raise SystemExit(main())
