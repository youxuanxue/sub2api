#!/usr/bin/env python3
"""Apply Kiro OAuth credentials to a TokenKey edge account safely.

The script supports either:
  - admin API key auth via x-api-key
  - admin email/password login via /api/v1/auth/login

Payload input should usually come from:
  python3 local_kiro_credentials.py --mode admin-payload
"""

from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


def read_text(path: str) -> str:
    if path == "-":
        return sys.stdin.read()
    return Path(path).read_text(encoding="utf-8")


def read_json(path: str) -> dict[str, Any]:
    try:
        data = json.loads(read_text(path))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"invalid JSON in {path}: {exc}")
    if not isinstance(data, dict):
        raise SystemExit(f"expected JSON object in {path}")
    return data


def strip_trailing_slash(value: str) -> str:
    return value[:-1] if value.endswith("/") else value


def request_json(
    method: str,
    url: str,
    *,
    body: dict[str, Any] | None = None,
    headers: dict[str, str] | None = None,
    timeout: int = 30,
) -> dict[str, Any]:
    payload = None
    req_headers = dict(headers or {})
    if body is not None:
        payload = json.dumps(body).encode("utf-8")
        req_headers["Content-Type"] = "application/json"
    req_headers.setdefault("Accept", "application/json")
    request = urllib.request.Request(url, data=payload, headers=req_headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read()
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise SystemExit(f"{method} {url} failed: status {exc.code}, body: {detail[:500]}")
    except urllib.error.URLError as exc:
        raise SystemExit(f"{method} {url} failed: {exc}")

    try:
        parsed = json.loads(raw.decode("utf-8"))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"{method} {url} returned invalid JSON: {exc}")
    if not isinstance(parsed, dict):
        raise SystemExit(f"{method} {url} returned non-object JSON")
    return parsed


def unwrap_api_data(response_obj: dict[str, Any]) -> dict[str, Any]:
    if "data" in response_obj and isinstance(response_obj["data"], dict):
        return response_obj["data"]
    return response_obj


def extract_login_token(response_obj: dict[str, Any]) -> str:
    data = unwrap_api_data(response_obj)
    candidates = []
    if isinstance(data, dict):
        candidates.extend([
            data.get("access_token"),
            data.get("token"),
            data.get("jwt"),
        ])
    candidates.extend([
        response_obj.get("access_token"),
        response_obj.get("token"),
        response_obj.get("jwt"),
    ])
    for candidate in candidates:
        if isinstance(candidate, str) and candidate.strip():
            return candidate
    raise SystemExit("login succeeded but response contained no bearer token")


def parse_password_file(path: str) -> tuple[str | None, str]:
    raw = Path(path).read_text(encoding="utf-8").strip()
    email = None
    password = None
    lines = [line.strip() for line in raw.splitlines() if line.strip()]
    for line in lines:
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip().lower()
        value = value.strip()
        if key == "email":
            email = value
        elif key == "password":
            password = value
    if password:
        return email, password
    if not raw:
        raise SystemExit(f"admin password file is empty: {path}")
    return None, raw


def resolve_auth_headers(args: argparse.Namespace, base_url: str) -> tuple[dict[str, str], str, str]:
    api_key = args.admin_api_key
    if api_key:
        return {"x-api-key": api_key}, "api_key", ""

    email = args.admin_email
    password = args.admin_password
    if args.admin_password_file:
        file_email, file_password = parse_password_file(args.admin_password_file)
        password = password or file_password
        email = email or file_email

    if not email or not password:
        raise SystemExit("need either --admin-api-key or (--admin-email and password source)")

    response_obj = request_json(
        "POST",
        f"{base_url}/api/v1/auth/login",
        body={"email": email, "password": password},
        timeout=args.timeout,
    )
    token = extract_login_token(response_obj)
    return {"Authorization": f"Bearer {token}"}, "login_token", email


def validate_payload(payload: dict[str, Any]) -> dict[str, Any]:
    if payload.get("type") != "oauth":
        raise SystemExit("payload.type must be oauth")
    credentials = payload.get("credentials")
    if not isinstance(credentials, dict) or not credentials:
        raise SystemExit("payload.credentials must be a non-empty object")
    required = ["access_token", "refresh_token", "region", "auth_method"]
    missing = [key for key in required if not str(credentials.get(key) or "").strip()]
    if missing:
        raise SystemExit(f"payload.credentials missing required keys: {', '.join(missing)}")
    if str(credentials.get("auth_method", "")).lower() == "idc":
        for key in ("client_id", "client_secret"):
            if not str(credentials.get(key) or "").strip():
                raise SystemExit(f"payload.credentials missing required key for idc: {key}")
    return credentials


def summarize_account(
    account: dict[str, Any],
    *,
    base_url: str,
    auth_mode: str,
    auth_email: str,
    ensured_schedulable: bool,
) -> dict[str, Any]:
    return {
        "base_url": base_url,
        "auth_mode": auth_mode,
        "auth_email": auth_email,
        "account_id": account.get("id"),
        "name": account.get("name"),
        "platform": account.get("platform"),
        "type": account.get("type"),
        "status": account.get("status"),
        "schedulable": account.get("schedulable"),
        "ensured_schedulable": ensured_schedulable,
        "temp_unschedulable_until": account.get("temp_unschedulable_until"),
        "error_message_prefix": str(account.get("error_message") or "")[:240],
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True, help="edge base URL, e.g. https://api-us6.tokenkey.dev")
    parser.add_argument("--account-id", required=True, type=int)
    parser.add_argument("--expected-account-name", default="", help="fail if response name differs")
    parser.add_argument("--payload-file", default="-", help="JSON payload file path or - for stdin")
    parser.add_argument("--ensure-schedulable", action="store_true", help="force schedulable=true if apply leaves it false")
    parser.add_argument("--dry-run", action="store_true", help="validate inputs and auth plan without live writes")
    parser.add_argument("--timeout", type=int, default=30)

    parser.add_argument("--admin-api-key", default="")
    parser.add_argument("--admin-email", default="")
    parser.add_argument("--admin-password", default="")
    parser.add_argument("--admin-password-file", default="")

    args = parser.parse_args()

    base_url = strip_trailing_slash(args.base_url)
    payload = read_json(args.payload_file)
    credentials = validate_payload(payload)

    # Auth resolution is intentionally early so dry-run can verify auth source shape.
    auth_headers, auth_mode, auth_email = resolve_auth_headers(args, base_url)

    if args.dry_run:
        output = {
            "dry_run": True,
            "base_url": base_url,
            "account_id": args.account_id,
            "expected_account_name": args.expected_account_name,
            "auth_mode": auth_mode,
            "auth_email": auth_email,
            "payload_keys": sorted(payload.keys()),
            "credential_keys": sorted(credentials.keys()),
            "ensure_schedulable": args.ensure_schedulable,
        }
        json.dump(output, sys.stdout, ensure_ascii=False, indent=2)
        sys.stdout.write("\n")
        return 0

    apply_response = request_json(
        "POST",
        f"{base_url}/api/v1/admin/accounts/{args.account_id}/apply-oauth-credentials",
        body=payload,
        headers=auth_headers,
        timeout=args.timeout,
    )
    account = unwrap_api_data(apply_response)
    if not isinstance(account, dict):
        raise SystemExit("apply response contained no account object")

    if args.expected_account_name:
        response_name = str(account.get("name") or "")
        if response_name != args.expected_account_name:
            raise SystemExit(
                f"apply response name mismatch: expected {args.expected_account_name!r}, got {response_name!r}"
            )

    ensured_schedulable = False
    if args.ensure_schedulable and account.get("schedulable") is False:
        sched_response = request_json(
            "POST",
            f"{base_url}/api/v1/admin/accounts/{args.account_id}/schedulable",
            body={"schedulable": True},
            headers=auth_headers,
            timeout=args.timeout,
        )
        account = unwrap_api_data(sched_response)
        if not isinstance(account, dict):
            raise SystemExit("schedulable response contained no account object")
        ensured_schedulable = True

    output = summarize_account(
        account,
        base_url=base_url,
        auth_mode=auth_mode,
        auth_email=auth_email,
        ensured_schedulable=ensured_schedulable,
    )
    json.dump(output, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
