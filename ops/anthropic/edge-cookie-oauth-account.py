#!/usr/bin/env python3
"""
Create a TokenKey Anthropic OAuth account on an edge by using local Claude
Desktop web cookies while keeping all Anthropic network egress on the edge.

Local side:
  - reads the Chromium/Electron cookie DB in read-only mode
  - decrypts Claude cookies with macOS Keychain + openssl
  - uploads a short-lived JSON bundle to S3
  - invokes ops/observability/run-probe.sh against the target edge

Remote edge side:
  - fetches the short-lived bundle
  - calls claude.ai/platform.claude.com from the edge IP
  - creates the TokenKey account through the edge admin API

No cookie, token, API key, or password is printed by this script.
"""
from __future__ import annotations

import argparse
import binascii
import hashlib
import json
import os
import pathlib
import sqlite3
import subprocess
import sys
import tempfile
import time
import uuid
from typing import Any

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
RUN_PROBE = REPO_ROOT / "ops/observability/run-probe.sh"

DEFAULT_COOKIE_DB = pathlib.Path.home() / "Library/Application Support/Claude/Cookies"
DEFAULT_KEYCHAIN_SERVICE = "Claude Safe Storage"
DEFAULT_BUNDLE_BUCKET = os.environ.get(
    "TOKENKEY_COOKIE_BUNDLE_BUCKET",
    "layer-zip-repro-682751977094-us-east-1",
)
DEFAULT_BUNDLE_REGION = os.environ.get("TOKENKEY_COOKIE_BUNDLE_REGION", "us-east-1")
DEFAULT_CONFIRM = "yes-create-anthropic-oauth-edge-account"
DEFAULT_CLAUDE_UA = (
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
    "AppleWebKit/537.36 (KHTML, like Gecko) "
    "Chrome/126.0.0.0 Safari/537.36"
)

CLAUDE_SCOPE = (
    "user:profile user:inference user:sessions:claude_code "
    "user:mcp_servers user:file_upload"
)


REMOTE_SCRIPT = r'''#!/usr/bin/env bash
set -euo pipefail

COOKIE_URL="${COOKIE_URL:-}"
ACCOUNT_NAME="${ACCOUNT_NAME:-}"
TIER="${TIER:-l3}"
BASE_URL="${BASE_URL:-auto}"
IF_EXISTS="${IF_EXISTS:-fail}"
ORG_UUID="${ORG_UUID:-}"
CLAUDE_UA="${CLAUDE_UA:-Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36}"

if [ -z "$COOKIE_URL" ] || [ -z "$ACCOUNT_NAME" ]; then
  echo '{"ok":false,"error":"COOKIE_URL and ACCOUNT_NAME are required"}'
  exit 2
fi

python3 - "$COOKIE_URL" "$ACCOUNT_NAME" "$TIER" "$BASE_URL" "$IF_EXISTS" "$ORG_UUID" "$CLAUDE_UA" <<'PY'
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

cookie_url, account_name, tier, base_url, if_exists, org_uuid_hint, ua = sys.argv[1:8]

CLIENT_ID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
REDIRECT_URI = "https://platform.claude.com/oauth/code/callback"
SCOPE = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
CLAUDE_BASE = "https://claude.ai"
TOKEN_URL = "https://platform.claude.com/v1/oauth/token"
DEFAULT_GROUP_ID = 1

def die(message, **extra):
    out = {"ok": False, "error": message}
    out.update(extra)
    print(json.dumps(out, ensure_ascii=False, indent=2))
    sys.exit(1)

def run(cmd):
    return subprocess.run(cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)

def request_json(url, *, method="GET", headers=None, body=None, timeout=45, allow_text=False):
    h = dict(headers or {})
    data = None
    if body is not None:
        data = json.dumps(body, separators=(",", ":")).encode("utf-8")
        h.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=data, headers=h, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            status = resp.getcode()
            ctype = resp.headers.get("content-type", "")
    except UnicodeEncodeError as exc:
        return {
            "_header_error": True,
            "status": 0,
            "content_type": "",
            "body_kind": "client_header_error",
            "detail": type(exc).__name__,
        }
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        status = exc.code
        ctype = exc.headers.get("content-type", "") if exc.headers else ""
        return {
            "_http_error": True,
            "status": status,
            "content_type": ctype,
            "body_kind": "html" if raw.lstrip().startswith((b"<!DOCTYPE html", b"<html")) else "text_or_json",
        }
    except Exception as exc:
        return {
            "_request_error": True,
            "status": 0,
            "content_type": "",
            "body_kind": "request_error",
            "detail": type(exc).__name__,
        }
    text = raw.decode("utf-8", "replace")
    if allow_text:
        return {"status": status, "content_type": ctype, "text": text}
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return {
            "_parse_error": True,
            "status": status,
            "content_type": ctype,
            "body_kind": "html" if raw.lstrip().startswith((b"<!DOCTYPE html", b"<html")) else "text",
        }

def read_env_file(path="/var/lib/tokenkey/.env"):
    values = {}
    try:
        for raw in Path(path).read_text(encoding="utf-8", errors="replace").splitlines():
            line = raw.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, v = line.split("=", 1)
            values[k.strip()] = v.strip().strip("'\"")
    except Exception:
        pass
    return values

def resolve_base_url():
    if base_url and base_url != "auto":
        return base_url.rstrip("/")
    env = read_env_file()
    domain = env.get("API_DOMAIN") or ""
    if not domain:
        die("api_domain_not_found")
    return "https://" + domain.rstrip("/") + "/api/v1"

base_url = resolve_base_url()

def b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode("ascii").rstrip("=")

def code_verifier() -> str:
    return b64url(secrets.token_bytes(32))

def code_challenge(verifier: str) -> str:
    return b64url(hashlib.sha256(verifier.encode("ascii")).digest())

def psql_scalar(sql: str) -> str:
    cmd = [
        "docker", "exec", "tokenkey-postgres", "psql", "-U", "tokenkey", "-d", "tokenkey",
        "-X", "-A", "-t", "-c", sql,
    ]
    cp = run(cmd)
    if cp.returncode != 0:
        return ""
    return cp.stdout.strip().splitlines()[0].strip() if cp.stdout.strip() else ""

def get_admin_key():
    return psql_scalar("SELECT value FROM settings WHERE key='admin_api_key' AND value <> '' LIMIT 1")

def get_admin_auth_header(cookie_doc):
    key = get_admin_key()
    if key:
        return {"x-api-key": key}, "api_key"

    env = read_env_file()
    email = env.get("ADMIN_EMAIL", "") or str(cookie_doc.get("admin_email") or "")
    password = env.get("ADMIN_PASSWORD", "") or str(cookie_doc.get("admin_password") or "")
    if not email or not password:
        die(
            "admin_credentials_not_found",
            env_file_exists=Path("/var/lib/tokenkey/.env").exists(),
            has_admin_email=bool(email),
            has_admin_password=bool(password),
        )
    login = request_json(base_url + "/auth/login", method="POST", headers={
        "Accept": "application/json",
        "Content-Type": "application/json",
    }, body={"email": email, "password": password})
    if isinstance(login, dict) and login.get("_http_error"):
        die("admin_login_http_error", status=login.get("status"), body_kind=login.get("body_kind"))
    if isinstance(login, dict) and login.get("_request_error"):
        die("admin_login_request_error", detail=login.get("detail"))
    data = login.get("data") if isinstance(login, dict) else None
    access_token = data.get("access_token") if isinstance(data, dict) else ""
    if not access_token:
        die("admin_login_missing_access_token")
    return {"Authorization": "Bearer " + access_token}, "jwt"

def api(path, *, method="GET", body=None, auth_headers):
    url = base_url + path
    headers = {"Accept": "application/json"}
    headers.update(auth_headers)
    res = request_json(url, method=method, headers=headers, body=body)
    if isinstance(res, dict) and res.get("_http_error"):
        die("tokenkey_api_http_error", method=method, path=path, status=res.get("status"), body_kind=res.get("body_kind"))
    if isinstance(res, dict) and res.get("_request_error"):
        die("tokenkey_api_request_error", method=method, path=path, detail=res.get("detail"))
    if isinstance(res, dict) and res.get("code") not in (None, 0, "0"):
        die("tokenkey_api_error", method=method, path=path, code=res.get("code"), message=res.get("message"))
    return res.get("data") if isinstance(res, dict) and "data" in res else res

def token_summary(token_info):
    email = str(token_info.get("email_address") or token_info.get("email") or "")
    return {
        "has_access_token": bool(token_info.get("access_token")),
        "has_refresh_token": bool(token_info.get("refresh_token")),
        "token_type": token_info.get("token_type") or "",
        "scope": token_info.get("scope") or "",
        "expires_at_unix": token_info.get("expires_at"),
        "email_domain": email.split("@", 1)[1] if "@" in email else "",
        "has_org_uuid": bool(token_info.get("org_uuid")),
        "has_account_uuid": bool(token_info.get("account_uuid")),
    }

def account_db_summary(account_id):
    sql = f"""
    SELECT jsonb_build_object(
      'id', a.id,
      'name', a.name,
      'platform', a.platform,
      'type', a.type,
      'status', a.status,
      'schedulable', a.schedulable,
      'tier_id', a.tier_id,
      'stability_tier', COALESCE(a.extra->>'stability_tier',''),
      'concurrency', a.concurrency,
      'priority', a.priority,
      'group_names', COALESCE((
        SELECT jsonb_agg(g.name ORDER BY g.name)
        FROM account_groups ag
        JOIN groups g ON g.id = ag.group_id
        WHERE ag.account_id = a.id
      ), '[]'::jsonb),
      'has_access_token', COALESCE(a.credentials ? 'access_token', false),
      'has_refresh_token', COALESCE(a.credentials ? 'refresh_token', false)
    )
    FROM accounts a
    WHERE a.id = {int(account_id)} AND a.deleted_at IS NULL
    LIMIT 1
    """
    raw = psql_scalar(sql)
    if not raw:
        return {}
    try:
        return json.loads(raw)
    except Exception:
        return {}

payload = request_json(cookie_url, allow_text=True, timeout=20)
if payload.get("status") != 200:
    die("cookie_url_fetch_failed", status=payload.get("status"), body_kind=payload.get("body_kind"))
try:
    cookie_doc = json.loads(payload["text"])
except Exception as exc:
    die("cookie_doc_parse_failed", detail=type(exc).__name__)

cookie_header = str(cookie_doc.get("cookie_header") or "")
if "sessionKey=" not in cookie_header:
    die("cookie_doc_missing_sessionKey")

admin_auth, admin_auth_kind = get_admin_auth_header(cookie_doc)

existing = api(
    "/admin/accounts?page=1&page_size=50&lite=1&platform=anthropic&type=oauth&search=" + urllib.parse.quote(account_name),
    auth_headers=admin_auth,
)
items = existing.get("items") if isinstance(existing, dict) else []
for item in items or []:
    if item.get("name") == account_name:
        if if_exists == "verify":
            summary = account_db_summary(item.get("id"))
            print(json.dumps({
                "ok": True,
                "edge_account_exists": True,
                "admin_auth_kind": admin_auth_kind,
                "account": summary or {"id": item.get("id"), "name": item.get("name")},
            }, ensure_ascii=False, indent=2))
            sys.exit(0)
        die("target_account_already_exists", account_name=account_name)

common_headers = {
    "Cookie": cookie_header,
    "User-Agent": ua,
    "Accept": "application/json",
    "Referer": "https://claude.ai/new",
}

orgs = request_json(f"{CLAUDE_BASE}/api/organizations", headers=common_headers)
if isinstance(orgs, dict) and orgs.get("_http_error"):
    die("claude_orgs_http_error", status=orgs.get("status"), content_type=orgs.get("content_type"), body_kind=orgs.get("body_kind"))
if isinstance(orgs, dict) and orgs.get("_header_error"):
    die("claude_orgs_header_error", body_kind=orgs.get("body_kind"))
if isinstance(orgs, dict) and orgs.get("_request_error"):
    die("claude_orgs_request_error", detail=orgs.get("detail"))
if isinstance(orgs, dict) and orgs.get("_parse_error"):
    die("claude_orgs_parse_error", status=orgs.get("status"), content_type=orgs.get("content_type"), body_kind=orgs.get("body_kind"))
if not isinstance(orgs, list) or not orgs:
    die("claude_orgs_empty_or_unexpected")

chosen = None
if org_uuid_hint:
    for org in orgs:
        if org.get("uuid") == org_uuid_hint:
            chosen = org
            break
    if chosen is None:
        die("requested_org_uuid_not_found")
if chosen is None:
    for org in orgs:
        if org.get("raven_type") == "team":
            chosen = org
            break
if chosen is None:
    chosen = orgs[0]
org_uuid = chosen.get("uuid")
if not org_uuid:
    die("claude_org_missing_uuid")

verifier = code_verifier()
state = b64url(secrets.token_bytes(32))
auth_body = {
    "response_type": "code",
    "client_id": CLIENT_ID,
    "organization_uuid": org_uuid,
    "redirect_uri": REDIRECT_URI,
    "scope": SCOPE,
    "state": state,
    "code_challenge": code_challenge(verifier),
    "code_challenge_method": "S256",
}
auth_headers = dict(common_headers)
auth_headers.update({
    "Origin": "https://claude.ai",
    "Content-Type": "application/json",
    "Accept-Language": "en-US,en;q=0.9",
    "Cache-Control": "no-cache",
})
auth = request_json(f"{CLAUDE_BASE}/v1/oauth/{org_uuid}/authorize", method="POST", headers=auth_headers, body=auth_body)
if isinstance(auth, dict) and auth.get("_http_error"):
    die("claude_authorize_http_error", status=auth.get("status"), content_type=auth.get("content_type"), body_kind=auth.get("body_kind"))
if isinstance(auth, dict) and auth.get("_request_error"):
    die("claude_authorize_request_error", detail=auth.get("detail"))
redirect_uri = auth.get("redirect_uri") if isinstance(auth, dict) else ""
if not redirect_uri:
    die("claude_authorize_missing_redirect")
qs = urllib.parse.parse_qs(urllib.parse.urlparse(redirect_uri).query)
code = (qs.get("code") or [""])[0]
resp_state = (qs.get("state") or [""])[0]
if not code:
    die("claude_authorize_missing_code")

token_body = {
    "code": code,
    "grant_type": "authorization_code",
    "client_id": CLIENT_ID,
    "redirect_uri": REDIRECT_URI,
    "code_verifier": verifier,
}
if resp_state:
    token_body["state"] = resp_state
token = request_json(TOKEN_URL, method="POST", headers={
    "Accept": "application/json, text/plain, */*",
    "Content-Type": "application/json",
    "User-Agent": "axios/1.13.6",
}, body=token_body)
if isinstance(token, dict) and token.get("_http_error"):
    die("claude_token_http_error", status=token.get("status"), content_type=token.get("content_type"), body_kind=token.get("body_kind"))
if isinstance(token, dict) and token.get("_request_error"):
    die("claude_token_request_error", detail=token.get("detail"))
if not token.get("access_token") or not token.get("refresh_token"):
    die("claude_token_missing_access_or_refresh")

now = int(time.time())
expires_in = int(token.get("expires_in") or 0)
token_info = {
    "access_token": token.get("access_token"),
    "refresh_token": token.get("refresh_token"),
    "token_type": token.get("token_type") or "Bearer",
    "expires_in": str(expires_in),
    "expires_at": str(now + expires_in) if expires_in > 0 else "",
    "scope": token.get("scope") or SCOPE,
}
org_info = token.get("organization") or {}
acct_info = token.get("account") or {}
token_info["org_uuid"] = org_info.get("uuid") or org_uuid
if acct_info.get("uuid"):
    token_info["account_uuid"] = acct_info.get("uuid")
if acct_info.get("email_address"):
    token_info["email_address"] = acct_info.get("email_address")
    token_info["email"] = acct_info.get("email_address")

extra = {}
for key in ("org_uuid", "account_uuid", "email_address", "email"):
    if token_info.get(key):
        extra[key] = token_info[key]

create_body = {
    "name": account_name,
    "platform": "anthropic",
    "type": "oauth",
    "credentials": token_info,
    "extra": extra,
    "concurrency": 30,
    "priority": 1,
    "channel_type": 0,
    "group_ids": [DEFAULT_GROUP_ID],
    "auto_pause_on_expired": True,
    "account_email": token_info.get("email_address") or "",
}
created = api("/admin/accounts", method="POST", body=create_body, auth_headers=admin_auth)
account_id = created.get("id") if isinstance(created, dict) else None
if not account_id:
    die("create_account_missing_id")

api(f"/admin/accounts/{account_id}/apply-tier", method="POST", body={"tier": tier}, auth_headers=admin_auth)
summary = account_db_summary(account_id)
print(json.dumps({
    "ok": True,
    "admin_auth_kind": admin_auth_kind,
    "account": summary or {"id": account_id, "name": account_name},
    "oauth": token_summary(token_info),
}, ensure_ascii=False, indent=2))
PY
'''


def fail(message: str, code: int = 2) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(code)


def run_capture(cmd: list[str], *, input_bytes: bytes | None = None, check: bool = True) -> subprocess.CompletedProcess[bytes]:
    proc = subprocess.run(cmd, input=input_bytes, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    if check and proc.returncode != 0:
        stderr = proc.stderr.decode("utf-8", "replace").strip()
        fail(f"command failed: {cmd[0]} ({stderr[:240]})")
    return proc


def normalize_edge_id(edge_id: str) -> str:
    value = edge_id.strip()
    if value.startswith("edge:"):
        value = value.split(":", 1)[1]
    if value.startswith("edge-"):
        value = value.split("-", 1)[1]
    if not value or any(ch.isspace() for ch in value):
        fail(f"invalid edge id: {edge_id!r}")
    return value


def default_admin_credentials_file(edge_id: str) -> pathlib.Path:
    return pathlib.Path.home() / "Codes/keys" / f"tokenkey-{edge_id}-admin-password.txt"


def parse_admin_credentials_text(text: str) -> dict[str, str]:
    out: dict[str, str] = {}
    stripped = text.strip()
    for raw in text.splitlines():
        if "=" not in raw:
            continue
        key, value = raw.split("=", 1)
        key = key.strip()
        if key in {"email", "password"}:
            out[key] = value.strip()
    if "password" not in out and stripped and "\n" not in stripped:
        out["password"] = stripped
    return out


def read_admin_credentials(path: pathlib.Path | None) -> dict[str, str]:
    if path is None or not path.exists():
        return {}
    return parse_admin_credentials_text(path.read_text(encoding="utf-8", errors="replace"))


def read_keychain_password(service: str) -> bytes:
    proc = run_capture(["security", "find-generic-password", "-w", "-s", service])
    password = proc.stdout.rstrip(b"\n")
    if not password:
        fail(f"empty keychain password for service {service!r}")
    return password


def openssl_aes_128_cbc_decrypt(ciphertext: bytes, key: bytes, iv: bytes) -> bytes:
    proc = run_capture(
        [
            "openssl",
            "enc",
            "-d",
            "-aes-128-cbc",
            "-K",
            binascii.hexlify(key).decode("ascii"),
            "-iv",
            binascii.hexlify(iv).decode("ascii"),
        ],
        input_bytes=ciphertext,
    )
    return proc.stdout


def decrypt_chromium_cookie(host_key: str, encrypted_value: bytes, key: bytes) -> str:
    if not encrypted_value:
        return ""
    data = bytes(encrypted_value)
    if data.startswith((b"v10", b"v11")):
        plain = openssl_aes_128_cbc_decrypt(data[3:], key, b" " * 16)
    else:
        plain = data
    host_digest = hashlib.sha256(host_key.encode("utf-8")).digest()
    if plain.startswith(host_digest):
        plain = plain[len(host_digest):]
    return plain.decode("utf-8", "replace")


def cookie_header_safe(name: str, value: str) -> bool:
    if not name or not value:
        return False
    if any(ch in name for ch in ";\r\n="):
        return False
    if any(ch in value for ch in ";\r\n"):
        return False
    return all(32 <= ord(ch) <= 126 for ch in name + value)


def extract_claude_cookie_header(cookie_db: pathlib.Path, keychain_service: str) -> tuple[str, dict[str, Any]]:
    if not cookie_db.exists():
        fail(f"cookie DB not found: {cookie_db}")
    password = read_keychain_password(keychain_service)
    key = hashlib.pbkdf2_hmac("sha1", password, b"saltysalt", 1003, 16)
    uri = "file:" + str(cookie_db.resolve()) + "?mode=ro&immutable=1"
    conn = sqlite3.connect(uri, uri=True)
    try:
        rows = conn.execute(
            """
            SELECT host_key, name, value, encrypted_value
            FROM cookies
            WHERE host_key IN ('claude.ai', '.claude.ai')
               OR host_key LIKE '%.claude.ai'
            ORDER BY
              CASE name
                WHEN 'sessionKey' THEN 0
                WHEN 'sessionKeyLC' THEN 1
                WHEN 'cf_clearance' THEN 2
                WHEN '__cf_bm' THEN 3
                ELSE 10
              END,
              name
            """
        ).fetchall()
    finally:
        conn.close()

    parts: list[str] = []
    names: list[str] = []
    skipped_unsafe = 0
    for host_key, name, value, encrypted_value in rows:
        cookie_value = value or decrypt_chromium_cookie(host_key, encrypted_value, key)
        if not cookie_header_safe(name, cookie_value):
            skipped_unsafe += 1
            continue
        names.append(name)
        parts.append(f"{name}={cookie_value}")

    if "sessionKey" not in names:
        fail("sessionKey cookie not found in Claude cookie DB")

    summary = {
        "cookie_count": len(parts),
        "has_sessionKey": "sessionKey" in names,
        "has_sessionKeyLC": "sessionKeyLC" in names,
        "has_cf_clearance": "cf_clearance" in names,
        "skipped_unsafe_cookie_count": skipped_unsafe,
    }
    return "; ".join(parts), summary


def write_secret_bundle(path: pathlib.Path, cookie_header: str, admin_creds: dict[str, str], admin_email_override: str) -> dict[str, Any]:
    doc = {
        "cookie_header": cookie_header,
        "admin_email": admin_creds.get("email") or admin_email_override,
        "admin_password": admin_creds.get("password") or "",
    }
    path.write_text(json.dumps(doc, separators=(",", ":")), encoding="utf-8")
    path.chmod(0o600)
    return {
        "has_admin_email": bool(doc["admin_email"]),
        "has_admin_password": bool(doc["admin_password"]),
    }


def aws_s3_cp(local_path: pathlib.Path, bucket: str, key: str, region: str) -> None:
    run_capture(["aws", "s3", "cp", str(local_path), f"s3://{bucket}/{key}", "--region", region, "--only-show-errors"])


def aws_s3_presign(bucket: str, key: str, region: str, expires_in: int) -> str:
    proc = run_capture(
        ["aws", "s3", "presign", f"s3://{bucket}/{key}", "--region", region, "--expires-in", str(expires_in)]
    )
    url = proc.stdout.decode("utf-8", "replace").strip()
    if not url:
        fail("aws s3 presign returned an empty URL")
    return url


def aws_s3_rm(bucket: str, key: str, region: str) -> None:
    subprocess.run(
        ["aws", "s3", "rm", f"s3://{bucket}/{key}", "--region", region, "--only-show-errors"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )


def run_remote_create(args: argparse.Namespace, cookie_url: str, remote_script_path: pathlib.Path) -> int:
    envs = [
        f"COOKIE_URL={cookie_url}",
        f"ACCOUNT_NAME={args.account_name}",
        f"TIER={args.tier}",
        f"BASE_URL={args.base_url}",
        f"IF_EXISTS={args.if_exists}",
        f"CLAUDE_UA={args.claude_ua}",
    ]
    if args.org_uuid:
        envs.append(f"ORG_UUID={args.org_uuid}")

    cmd = [
        "bash",
        str(RUN_PROBE),
        "--target",
        f"edge:{args.edge_id}",
        "--script",
        str(remote_script_path),
        "--timeout-seconds",
        str(args.timeout_seconds),
    ]
    for env in envs:
        cmd.extend(["--env", env])
    return subprocess.run(cmd, cwd=str(REPO_ROOT), check=False).returncode


def cmd_local_summary(args: argparse.Namespace) -> int:
    _, summary = extract_claude_cookie_header(args.cookie_db, args.keychain_service)
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0


def cmd_create(args: argparse.Namespace) -> int:
    args.edge_id = normalize_edge_id(args.edge_id)
    if args.confirm != DEFAULT_CONFIRM:
        fail(f"missing confirm code: --confirm {DEFAULT_CONFIRM}")
    if not RUN_PROBE.exists():
        fail(f"run-probe wrapper not found: {RUN_PROBE}")

    if args.admin_credentials_file is None:
        args.admin_credentials_file = default_admin_credentials_file(args.edge_id)
    admin_creds = read_admin_credentials(args.admin_credentials_file)
    cookie_header, cookie_summary = extract_claude_cookie_header(args.cookie_db, args.keychain_service)

    with tempfile.TemporaryDirectory(prefix="tk-anthropic-cookie-oauth-") as tmp:
        tmpdir = pathlib.Path(tmp)
        bundle_path = tmpdir / "bundle.json"
        remote_script_path = tmpdir / "edge_cookie_oauth_remote.sh"
        remote_script_path.write_text(REMOTE_SCRIPT, encoding="utf-8")
        remote_script_path.chmod(0o700)
        bundle_summary = write_secret_bundle(bundle_path, cookie_header, admin_creds, args.admin_email)

        object_key = (
            f"{args.s3_prefix.rstrip('/')}/edge-{args.edge_id}-{args.account_name}-"
            f"{int(time.time())}-{uuid.uuid4().hex}.json"
        )
        uploaded = False
        try:
            aws_s3_cp(bundle_path, args.bundle_bucket, object_key, args.bundle_region)
            uploaded = True
            cookie_url = aws_s3_presign(args.bundle_bucket, object_key, args.bundle_region, args.bundle_ttl_seconds)
            print(
                json.dumps(
                    {
                        "local_bundle": {
                            **cookie_summary,
                            **bundle_summary,
                            "s3_ttl_seconds": args.bundle_ttl_seconds,
                        },
                        "edge": args.edge_id,
                        "account_name": args.account_name,
                        "tier": args.tier,
                    },
                    ensure_ascii=False,
                    indent=2,
                )
            )
            return run_remote_create(args, cookie_url, remote_script_path)
        finally:
            if uploaded:
                aws_s3_rm(args.bundle_bucket, object_key, args.bundle_region)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Create a TokenKey Anthropic OAuth edge account from local Claude cookies with edge-only Anthropic egress."
    )
    sub = parser.add_subparsers(dest="command", required=True)

    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--cookie-db", type=pathlib.Path, default=DEFAULT_COOKIE_DB)
    common.add_argument("--keychain-service", default=DEFAULT_KEYCHAIN_SERVICE)

    summary = sub.add_parser("local-summary", parents=[common], help="Read local Claude cookies and print a safe summary only.")
    summary.set_defaults(func=cmd_local_summary)

    create = sub.add_parser("create", parents=[common], help="Create the OAuth account on the target edge.")
    create.add_argument("--edge-id", required=True, help="Edge id, e.g. us6, edge-us6, or edge:us6.")
    create.add_argument("--account-name", required=True, help="New TokenKey account name, e.g. edge-or-2-c.")
    create.add_argument("--tier", default="l3", help="Apply-tier name after account creation.")
    create.add_argument("--org-uuid", default="", help="Optional Claude org UUID to force when cookies see multiple orgs.")
    create.add_argument("--claude-ua", default=DEFAULT_CLAUDE_UA, help="User-Agent for claude.ai web-cookie calls on the edge.")
    create.add_argument("--base-url", default="auto", help="Edge admin API base URL, or auto to read API_DOMAIN from the edge.")
    create.add_argument("--if-exists", choices=["fail", "verify"], default="fail")
    create.add_argument("--admin-email", default="", help="Fallback admin email when the admin credential file only contains a password.")
    create.add_argument("--admin-credentials-file", type=pathlib.Path, default=None)
    create.add_argument("--bundle-bucket", default=DEFAULT_BUNDLE_BUCKET)
    create.add_argument("--bundle-region", default=DEFAULT_BUNDLE_REGION)
    create.add_argument("--bundle-ttl-seconds", type=int, default=300)
    create.add_argument("--s3-prefix", default="tmp/tokenkey-cookie-oauth")
    create.add_argument("--timeout-seconds", type=int, default=180)
    create.add_argument("--confirm", required=True, help=f"Must equal {DEFAULT_CONFIRM}.")
    create.set_defaults(func=cmd_create)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
