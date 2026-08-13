#!/usr/bin/env bash
# Direct CloudWise upstream chat probe for one model spelling (case sensitivity).
# Reads api_key/base_url from prod postgres; output never includes api_key.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
MODEL="${MODEL:?MODEL required}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-45}"

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)

python3 - "$ACCOUNT_ID" "$MODEL" "$REQUEST_TIMEOUT_SECONDS" <<'PY'
import json
import re
import subprocess
import sys
import urllib.error
import urllib.request

account_id, model, timeout_raw = sys.argv[1:4]
timeout = int(timeout_raw)

sql = f"""
SELECT json_build_object(
  'id', a.id,
  'name', a.name,
  'base_url', COALESCE(a.credentials->>'base_url', ''),
  'api_key', COALESCE(a.credentials->>'api_key', '')
)::text
FROM accounts a
WHERE a.id={account_id} AND a.deleted_at IS NULL;
"""
psql = [
    "sudo", "docker", "exec", "-i", "tokenkey-postgres", "psql",
    "-U", "tokenkey", "-d", "tokenkey", "-X", "-q", "-A", "-t",
    "-v", "ON_ERROR_STOP=1", "-c", sql,
]
try:
    raw = subprocess.run(psql, check=True, text=True, capture_output=True).stdout.strip()
    account = json.loads(raw)
except (subprocess.CalledProcessError, json.JSONDecodeError) as exc:
    print(json.dumps({"verdict": "setup_error", "error": f"account lookup failed: {exc}"}, ensure_ascii=False))
    raise SystemExit(0)

api_key = str(account.get("api_key") or "").strip()
base_url = str(account.get("base_url") or "").strip().rstrip("/")
if not api_key or not base_url:
    print(json.dumps({"verdict": "setup_error", "error": "missing api_key or base_url"}, ensure_ascii=False))
    raise SystemExit(0)


def join_path(base, suffix):
    if suffix.startswith("/"):
        suffix = suffix[1:]
    if base.endswith("/"):
        return base + suffix
    return base + "/" + suffix


def excerpt(raw, limit=320):
    text = raw.decode("utf-8", errors="replace") if isinstance(raw, (bytes, bytearray)) else str(raw)
    text = re.sub(r"\s+", " ", text).strip()
    return text[:limit]


url = join_path(base_url, "v1/chat/completions")
body = {
    "model": model,
    "messages": [{"role": "user", "content": "Reply OK only."}],
    "max_tokens": 8,
    "stream": False,
}
headers = {
    "Authorization": f"Bearer {api_key}",
    "Accept": "application/json",
    "Content-Type": "application/json",
}
req = urllib.request.Request(url, data=json.dumps(body).encode("utf-8"), method="POST", headers=headers)
try:
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read(1 << 20)
        status = resp.status
except urllib.error.HTTPError as exc:
    status = exc.code
    raw = exc.read(4096)
except Exception as exc:  # noqa: BLE001
    print(json.dumps({
        "verdict": "setup_error",
        "error": str(exc),
        "probe": {"account_id": int(account_id), "model": model, "endpoint": "chat_completions"},
    }, ensure_ascii=False))
    raise SystemExit(0)

response_model = None
try:
    payload = json.loads(raw.decode("utf-8"))
    if isinstance(payload, dict):
        response_model = payload.get("model")
except (UnicodeDecodeError, json.JSONDecodeError):
    payload = None

if 200 <= status < 300:
    verdict = "servable"
elif status in {401, 403}:
    verdict = "upstream_auth_rejected"
else:
    verdict = "upstream_rejected"

print(json.dumps({
    "verdict": verdict,
    "http_status": status,
    "probe": {
        "kind": "cloudwise_upstream_chat_case",
        "account_id": int(account_id),
        "account_name": account.get("name"),
        "base_url": base_url,
        "requested_model": model,
        "endpoint": "chat_completions",
    },
    "response": {
        "model_field": response_model,
        "body_excerpt": excerpt(raw),
    },
}, ensure_ascii=False, indent=2))
PY
