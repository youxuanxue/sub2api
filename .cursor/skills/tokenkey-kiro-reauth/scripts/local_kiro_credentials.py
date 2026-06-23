#!/usr/bin/env python3
"""Extract or locally refresh Kiro OAuth credentials from ~/.aws/sso/cache.

This script never writes back to the local cache. It only emits one of:
  - full credentials JSON
  - admin apply payload JSON
  - hash-only summary JSON
"""

from __future__ import annotations

import argparse
import glob
import hashlib
import json
import os
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path
from typing import Any


def unix_seconds(value: Any) -> str | None:
    if value in (None, ""):
        return None
    if isinstance(value, str):
        raw = value.strip()
        normalized = raw.replace("UTC", "+00:00")
        if normalized.endswith("Z"):
            normalized = normalized[:-1] + "+00:00"
        try:
            return str(int(datetime.fromisoformat(normalized).timestamp()))
        except ValueError:
            value = raw
    try:
        number = int(float(value))
    except (TypeError, ValueError):
        return None
    if number > 10_000_000_000:
        number //= 1000
    return str(number)


def md5_16(value: str) -> str:
    if not value:
        return ""
    return hashlib.md5(value.encode("utf-8")).hexdigest()[:16]


def read_json(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def latest_idc_registration(cache_dir: Path) -> tuple[Path, dict[str, Any]] | None:
    matches: list[tuple[float, str, Path, dict[str, Any]]] = []
    for raw in glob.glob(str(cache_dir / "*.json")):
        path = Path(raw)
        if path.name == "kiro-auth-token.json":
            continue
        try:
            payload = read_json(path)
        except Exception:
            continue
        if "clientId" not in payload or "clientSecret" not in payload:
            continue
        matches.append((path.stat().st_mtime, str(path), path, payload))
    if not matches:
        return None
    matches.sort(reverse=True)
    _, _, path, payload = matches[0]
    return path, payload


def load_local_credentials() -> tuple[dict[str, Any], dict[str, Any]]:
    cache_dir = Path.home() / ".aws" / "sso" / "cache"
    token_path = cache_dir / "kiro-auth-token.json"
    if not token_path.exists():
        raise SystemExit(f"missing local Kiro token cache: {token_path}")

    token = read_json(token_path)
    auth_method = "social" if str(token.get("authMethod", "")).lower() == "social" else "idc"
    credentials: dict[str, Any] = {
        "access_token": token["accessToken"],
        "refresh_token": token["refreshToken"],
        "region": token.get("region", "us-east-1"),
        "auth_method": auth_method,
    }

    expires_at = unix_seconds(token.get("expiresAt") or token.get("expires_at"))
    if expires_at is None and token.get("expiresIn") not in (None, ""):
        expires_at = str(int(time.time()) + int(float(token["expiresIn"])))
    if expires_at:
        credentials["expires_at"] = expires_at

    meta: dict[str, Any] = {
        "token_path": str(token_path),
        "token_mtime_local": int(token_path.stat().st_mtime),
        "token_source": "cache",
        "raw_expires_at": token.get("expiresAt") or token.get("expires_at") or "",
    }

    if auth_method == "idc":
        latest = latest_idc_registration(cache_dir)
        if latest is None:
            raise SystemExit("missing Kiro IdC client registration in ~/.aws/sso/cache")
        reg_path, reg = latest
        credentials["client_id"] = reg["clientId"]
        credentials["client_secret"] = reg["clientSecret"]
        meta["registration_path"] = str(reg_path)
        meta["registration_mtime_local"] = int(reg_path.stat().st_mtime)

    return credentials, meta


def refresh_locally(credentials: dict[str, Any], timeout: int) -> tuple[dict[str, Any], dict[str, Any]]:
    auth_method = str(credentials.get("auth_method", "")).lower()
    region = str(credentials.get("region") or "us-east-1")

    if auth_method == "social":
        url = "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"
        payload = {"refreshToken": credentials["refresh_token"]}
    else:
        client_id = str(credentials.get("client_id") or "")
        client_secret = str(credentials.get("client_secret") or "")
        if not client_id or not client_secret:
            raise SystemExit("OIDC refresh requires client_id and client_secret")
        url = f"https://oidc.{region}.amazonaws.com/token"
        payload = {
            "clientId": client_id,
            "clientSecret": client_secret,
            "refreshToken": credentials["refresh_token"],
            "grantType": "refresh_token",
        }

    request = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            body = response.read()
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        raise SystemExit(f"refresh failed: status {exc.code}, body: {body[:400]}")
    except urllib.error.URLError as exc:
        raise SystemExit(f"refresh failed: {exc}")

    try:
        result = json.loads(body.decode("utf-8"))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"refresh returned invalid JSON: {exc}")

    refreshed = dict(credentials)
    refreshed["access_token"] = result["accessToken"]
    refreshed["refresh_token"] = result.get("refreshToken") or credentials["refresh_token"]

    expires_in = result.get("expiresIn")
    if expires_in not in (None, ""):
        refreshed["expires_at"] = str(int(time.time()) + int(expires_in))
    if result.get("profileArn"):
        refreshed["profile_arn"] = result["profileArn"]

    meta = {
        "token_source": "refreshed",
        "refreshed_at_utc": datetime.utcnow().replace(microsecond=0).isoformat() + "Z",
        "refresh_endpoint": url,
    }
    return refreshed, meta


def render_summary(credentials: dict[str, Any], meta: dict[str, Any]) -> dict[str, Any]:
    return {
        "token_source": meta.get("token_source", "cache"),
        "token_path": meta.get("token_path", ""),
        "token_mtime_local": meta.get("token_mtime_local"),
        "registration_path": meta.get("registration_path", ""),
        "registration_mtime_local": meta.get("registration_mtime_local"),
        "auth_method": credentials.get("auth_method", ""),
        "region": credentials.get("region", ""),
        "expires_at": credentials.get("expires_at", ""),
        "raw_expires_at": meta.get("raw_expires_at", ""),
        "profile_arn_present": bool(credentials.get("profile_arn")),
        "access_md5_16": md5_16(str(credentials.get("access_token", ""))),
        "refresh_md5_16": md5_16(str(credentials.get("refresh_token", ""))),
        "client_id_md5_16": md5_16(str(credentials.get("client_id", ""))),
        "client_secret_md5_16": md5_16(str(credentials.get("client_secret", ""))),
        "refreshed_at_utc": meta.get("refreshed_at_utc", ""),
        "refresh_endpoint": meta.get("refresh_endpoint", ""),
    }


def render_admin_payload(credentials: dict[str, Any], token_version_ms: int | None) -> dict[str, Any]:
    payload_credentials = dict(credentials)
    payload_credentials["_token_version"] = str(token_version_ms or int(time.time() * 1000))
    payload_credentials["tos_acknowledged"] = True
    return {
        "type": "oauth",
        "credentials": payload_credentials,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--mode",
        choices=("full", "summary", "admin-payload"),
        default="full",
        help="output format",
    )
    parser.add_argument(
        "--refresh",
        action="store_true",
        help="mint a fresh access token locally using the current refresh token",
    )
    parser.add_argument(
        "--token-version-ms",
        type=int,
        default=None,
        help="override _token_version when --mode admin-payload",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=30,
        help="HTTP timeout in seconds for --refresh",
    )
    args = parser.parse_args()

    credentials, meta = load_local_credentials()
    if args.refresh:
        credentials, refresh_meta = refresh_locally(credentials, timeout=args.timeout)
        meta.update(refresh_meta)

    if args.mode == "full":
        output: dict[str, Any] = dict(credentials)
    elif args.mode == "summary":
        output = render_summary(credentials, meta)
    else:
        output = render_admin_payload(credentials, token_version_ms=args.token_version_ms)

    json.dump(output, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
