#!/usr/bin/env bash
# Account upstream-model-list or direct model test through TokenKey's canonical
# admin service. MODEL mode may update the account's normal test/recovery state.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:-}"
BASE_URL="${BASE_URL:-http://127.0.0.1:8080/api/v1}"
TARGET_MODELS="${TARGET_MODELS:-}"
MODEL="${MODEL:-}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-60}"

# Canonical app-container resolver (ops/lib). run-probe uploads lib next to this
# script under /tmp; a repo checkout has it at ../lib.
TK_LIB_DIR="${TK_LIB_DIR:-$(cd "$(dirname "$0")/../lib" && pwd)}"
export TK_LIB_DIR

if [[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]; then
  echo '{"verdict":"setup_error","error":"ACCOUNT_ID must be numeric"}'
  exit 0
fi
if [[ ! "$REQUEST_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$REQUEST_TIMEOUT_SECONDS" -lt 1 ]]; then
  echo '{"verdict":"setup_error","error":"REQUEST_TIMEOUT_SECONDS must be a positive integer"}'
  exit 0
fi

python3 - "$ACCOUNT_ID" "$BASE_URL" "$TARGET_MODELS" "$MODEL" "$REQUEST_TIMEOUT_SECONDS" <<'PY'
import ipaddress
import json
import os
import re
import ssl
import subprocess
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import urljoin, urlsplit
from urllib.request import HTTPRedirectHandler, HTTPSHandler, Request, build_opener

sys.path.insert(0, os.environ.get("TK_LIB_DIR", ""))
from resolve_app_container import resolve as resolve_app_container_owner

account_id, base_url, target_models_raw, model, timeout_raw = sys.argv[1:6]
targets = target_models_raw.split()
timeout = int(timeout_raw)
VOLCENGINE_AGENT_PLAN_BASE_URL = "https://ark.cn-beijing.volces.com/api/plan/v3"

def setup_error(message):
    print(json.dumps({"verdict": "setup_error", "error": message}, ensure_ascii=False))
    raise SystemExit(0)


def validate_base_url(raw):
    try:
        parsed = urlsplit(raw)
        port = parsed.port
    except ValueError:
        setup_error("BASE_URL is invalid")
    hostname = (parsed.hostname or "").lower()
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        setup_error("BASE_URL must not contain userinfo, query, or fragment")
    if parsed.path.rstrip("/") != "/api/v1":
        setup_error("BASE_URL path must be /api/v1")
    try:
        address = ipaddress.ip_address(hostname)
    except ValueError:
        address = None
    is_loopback = hostname == "localhost" or bool(address and address.is_loopback)
    private_networks = (
        ipaddress.ip_network("10.0.0.0/8"),
        ipaddress.ip_network("172.16.0.0/12"),
        ipaddress.ip_network("192.168.0.0/16"),
    )
    is_private_literal = bool(
        address and address.version == 4
        and any(address in network for network in private_networks)
    )
    is_tokenkey_api = re.fullmatch(r"api(?:-[a-z0-9]+(?:-[a-z0-9]+)*)?\.tokenkey\.dev", hostname)
    if parsed.scheme == "http" and (is_loopback or is_private_literal):
        return raw.rstrip("/")
    if parsed.scheme == "https" and is_tokenkey_api and port in {None, 443}:
        return raw.rstrip("/")
    setup_error("BASE_URL must use private/loopback literal HTTP or a TokenKey API HTTPS host")


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _same_netloc(old_url, location):
    old = urlsplit(old_url)
    new = urlsplit(urljoin(old_url, location))
    return bool(
        old.netloc
        and new.netloc
        and old.netloc.lower() == new.netloc.lower()
        and new.scheme in {"http", "https"}
    )


def resolve_app_container():
    name, _notes = resolve_app_container_owner("auto")
    return name


def docker_admin_post(url, body, admin_key, timeout):
    container = resolve_app_container()
    if not container:
        return None, None
    parsed = urlsplit(url)
    inner = f"http://127.0.0.1:8080{parsed.path}"
    if parsed.query:
        inner += "?" + parsed.query
    cmd = [
        "docker", "exec", container,
        "wget", "-qO-", f"--timeout={timeout}",
        f"--header=x-api-key: {admin_key}",
        "--header=Content-Type: application/json",
        f"--post-data={body.decode('utf-8')}",
        inner,
    ]
    proc = subprocess.run(cmd, check=False, text=True, capture_output=True)
    raw = ((proc.stdout or "") + (proc.stderr or "")).encode("utf-8", errors="replace")
    status = 200 if proc.returncode == 0 else 500
    codes = re.findall(r"HTTP/\S+\s+(\d{3})\b", proc.stderr or "")
    if codes:
        status = int(codes[-1])
    return status, raw[: 8 << 20]


base_url = validate_base_url(base_url)
bootstrap_sql = f"""
SELECT json_build_object(
  'admin_key', (SELECT value FROM settings WHERE key='admin_api_key' LIMIT 1),
  'account', (
    SELECT json_build_object(
      'name', a.name,
      'platform', a.platform,
      'type', a.type,
      'channel_type', a.channel_type,
      'mirror_platform', a.credentials->>'mirror_platform',
      'base_url', a.credentials->>'base_url'
    )
    FROM accounts a
    WHERE a.id={account_id} AND a.deleted_at IS NULL
    LIMIT 1
  )
)::text;
"""
psql = [
    "sudo", "docker", "exec", "-i", "tokenkey-postgres", "psql",
    "-U", "tokenkey", "-d", "tokenkey", "-X", "-q", "-A", "-t",
    "-v", "ON_ERROR_STOP=1", "-c", bootstrap_sql,
]
try:
    bootstrap_raw = subprocess.run(psql, check=True, text=True, capture_output=True).stdout.strip()
    bootstrap = json.loads(bootstrap_raw)
except (subprocess.CalledProcessError, json.JSONDecodeError):
    setup_error("failed to read admin api key and account metadata")
if not isinstance(bootstrap, dict):
    setup_error("admin/account metadata query returned an invalid payload")
admin_key = str(bootstrap.get("admin_key") or "").strip()
account = bootstrap.get("account")
if not admin_key:
    setup_error("admin api key not found")
if not isinstance(account, dict):
    setup_error("account not found")


def derive_account_scope(row):
    platform = str(row.get("platform") or "").strip().lower()
    account_type = str(row.get("type") or "").strip().lower()
    mirror_platform = str(row.get("mirror_platform") or "").strip().lower()
    name = str(row.get("name") or "").strip().lower()
    account_base_url = str(row.get("base_url") or "").strip().lower().rstrip("/")
    if platform == "kiro":
        return "kiro"
    if platform == "anthropic" and account_type == "apikey":
        if mirror_platform == "kiro" or (
            name.startswith("kiro-")
            and account_base_url.startswith("https://api-")
            and account_base_url.endswith(".tokenkey.dev")
        ):
            return "kiro"
    if platform == "anthropic" and account_type == "bedrock":
        return "bedrock"
    if platform == "openai" and account_type == "apikey" and account_base_url in {
        "https://api.ainzy.net", "https://api.ainzy.net/v1",
    }:
        return "openai_ainzy_relay"
    if platform == "openai" and account_type == "apikey" and account_base_url in {
        "https://agent.tokensea.ai", "https://agent.tokensea.ai/v1",
    }:
        return "openai_tokensea_relay"
    if platform == "openai" and account_type == "apikey" and account_base_url in {
        "https://api.cloudwise.ai/api",
        "https://api-us.cloudwise.ai/api",
    }:
        return "openai_cloudwise_relay"
    if platform == "anthropic" and account_type == "apikey" and account_base_url in {
        "https://agent.tokensea.ai", "https://agent.tokensea.ai/v1",
    }:
        return "anthropic_tokensea_relay"
    if platform == "newapi":
        channel_type = account.get("channel_type")
        if isinstance(channel_type, int) and channel_type > 0:
            if (
                channel_type == 45
                and account_base_url == VOLCENGINE_AGENT_PLAN_BASE_URL
            ):
                return f"account_override:{platform}:{channel_type}:{account_base_url}"
            return f"newapi_channel_type:{channel_type}"
    return platform


account_platform = str(account.get("platform") or "").strip().lower()
account_base_url = str(account.get("base_url") or "").strip().lower().rstrip("/")
account_scope = derive_account_scope(account)
if not account_platform or not account_scope:
    setup_error("account platform/scope metadata is incomplete")

if model:
    url = base_url + f"/admin/accounts/{account_id}/test"
    body = json.dumps({"model_id": model, "prompt": "Reply OK only."}).encode("utf-8")
else:
    url = base_url + f"/admin/accounts/{account_id}/models/sync-upstream"
    body = b"{}"
status = None
raw = b""
opener = build_opener(NoRedirect(), HTTPSHandler(context=ssl.create_default_context()))

def _post(target):
    request = Request(target, data=body, method="POST", headers={
        "x-api-key": admin_key,
        "content-type": "application/json",
    })
    with opener.open(request, timeout=timeout) as response:
        return response.status, response.read(8 << 20)

try:
    status, raw = _post(url)
except HTTPError as exc:
    status = exc.code
    raw = exc.read(4096)
    loc = (exc.headers.get("Location") or "").strip()
    if status in {301, 302, 307, 308} and loc and _same_netloc(url, loc):
        try:
            status, raw = _post(urljoin(url, loc))
        except HTTPError as retry_exc:
            status = retry_exc.code
            raw = retry_exc.read(4096)
except URLError as exc:
    parsed = urlsplit(url)
    host = (parsed.hostname or "").lower()
    if host in {"127.0.0.1", "localhost"}:
        status, raw = docker_admin_post(url, body, admin_key, timeout)
    if status is None:
        print(json.dumps({
            "verdict": "inconclusive_transport",
            "account_id": int(account_id),
            "http_status": None,
            "error": str(exc.reason or exc),
        }, ensure_ascii=False))
        raise SystemExit(0)

if model:
    events = []
    for line in raw.decode("utf-8", errors="replace").splitlines():
        if not line.startswith("data:"):
            continue
        try:
            event = json.loads(line.split(":", 1)[1].strip())
        except json.JSONDecodeError:
            continue
        if isinstance(event, dict):
            events.append(event)
    error = next((str(event.get("error") or "") for event in events if event.get("type") == "error"), "")
    complete = any(event.get("type") == "test_complete" and event.get("success") is True for event in events)
    content = "".join(str(event.get("text") or "") for event in events if event.get("type") == "content")
    if 200 <= status < 300 and complete and not error:
        verdict = "servable"
    elif 200 <= status < 300:
        verdict = "upstream_rejected"
    else:
        verdict = "upstream_error"
    print(json.dumps({
        "verdict": verdict,
        "probe": "account_test",
        "account_id": int(account_id),
        "account_platform": account_platform,
        "account_base_url": account_base_url,
        "account_scope": account_scope,
        "model": model,
        "http_status": status,
        "content_excerpt": re.sub(r"\s+", " ", content).strip()[:300],
        "error": error[:500] if error else None,
    }, ensure_ascii=False))
    raise SystemExit(0)

try:
    payload = json.loads(raw.decode("utf-8"))
except (UnicodeDecodeError, json.JSONDecodeError):
    payload = {}
data = payload.get("data") if isinstance(payload, dict) else None
models = data.get("models") if isinstance(data, dict) else None
models = sorted({str(model).strip() for model in (models or []) if str(model).strip()})

if 200 <= status < 300 and models:
    verdict = "listed" if all(target in models for target in targets) else "not_listed"
elif 200 <= status < 300:
    verdict = "empty"
else:
    verdict = "upstream_error"

out = {
    "verdict": verdict,
    "account_id": int(account_id),
    "account_platform": account_platform,
    "account_base_url": account_base_url,
    "account_scope": account_scope,
    "http_status": status,
    "model_count": len(models),
    "targets": {target: target in models for target in targets},
    "models": models,
}
if status >= 300:
    message = payload.get("message") if isinstance(payload, dict) else None
    out["error"] = str(message or "upstream model list request failed")[:300]
print(json.dumps(out, ensure_ascii=False))
PY
