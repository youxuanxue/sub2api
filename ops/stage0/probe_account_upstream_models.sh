#!/usr/bin/env bash
# Read-only account upstream-model-list probe through TokenKey's canonical service.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:-}"
BASE_URL="${BASE_URL:-http://127.0.0.1:8080/api/v1}"
TARGET_MODELS="${TARGET_MODELS:-}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-60}"

if [[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]; then
  echo '{"verdict":"setup_error","error":"ACCOUNT_ID must be numeric"}'
  exit 0
fi
if [[ ! "$REQUEST_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$REQUEST_TIMEOUT_SECONDS" -lt 1 ]]; then
  echo '{"verdict":"setup_error","error":"REQUEST_TIMEOUT_SECONDS must be a positive integer"}'
  exit 0
fi

python3 - "$ACCOUNT_ID" "$BASE_URL" "$TARGET_MODELS" "$REQUEST_TIMEOUT_SECONDS" <<'PY'
import json
import ssl
import subprocess
import sys
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

account_id, base_url, target_models_raw, timeout_raw = sys.argv[1:5]
targets = target_models_raw.split()
timeout = int(timeout_raw)

psql = [
    "sudo", "docker", "exec", "-i", "tokenkey-postgres", "psql",
    "-U", "tokenkey", "-d", "tokenkey", "-X", "-q", "-A", "-t",
    "-v", "ON_ERROR_STOP=1", "-c",
    "SELECT value FROM settings WHERE key='admin_api_key' LIMIT 1;",
]
try:
    admin_key = subprocess.run(psql, check=True, text=True, capture_output=True).stdout.strip()
except subprocess.CalledProcessError:
    print(json.dumps({"verdict": "setup_error", "error": "failed to read admin api key"}))
    raise SystemExit(0)
if not admin_key:
    print(json.dumps({"verdict": "setup_error", "error": "admin api key not found"}))
    raise SystemExit(0)

url = base_url.rstrip("/") + f"/admin/accounts/{account_id}/models/sync-upstream"
req = Request(url, data=b"{}", method="POST", headers={
    "x-api-key": admin_key,
    "content-type": "application/json",
})
status = None
try:
    with urlopen(req, timeout=timeout, context=ssl.create_default_context()) as response:
        status = response.status
        raw = response.read(8 << 20)
except HTTPError as exc:
    status = exc.code
    raw = exc.read(4096)
except URLError as exc:
    print(json.dumps({
        "verdict": "inconclusive_transport",
        "account_id": int(account_id),
        "http_status": None,
        "error": str(exc.reason or exc),
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
