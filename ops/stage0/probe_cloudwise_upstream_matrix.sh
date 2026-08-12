#!/usr/bin/env bash
# Direct CloudWise upstream capability matrix for one TokenKey OpenAI apikey account.
# Reads credentials from prod postgres; output never includes api_key.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-45}"

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)

python3 - "$ACCOUNT_ID" "$REQUEST_TIMEOUT_SECONDS" <<'PY'
import json
import re
import subprocess
import sys
import urllib.error
import urllib.request
from urllib.parse import urljoin

account_id, timeout_raw = sys.argv[1:3]
timeout = int(timeout_raw)

sql = f"""
SELECT json_build_object(
  'id', a.id,
  'name', a.name,
  'platform', a.platform,
  'type', a.type,
  'channel_type', a.channel_type,
  'api_key', COALESCE(a.credentials->>'api_key', ''),
  'base_url', COALESCE(a.credentials->>'base_url', ''),
  'model_mapping', COALESCE(a.credentials->'model_mapping', '{{}}'::jsonb)
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

if not isinstance(account, dict):
    print(json.dumps({"verdict": "setup_error", "error": "account not found"}, ensure_ascii=False))
    raise SystemExit(0)

api_key = str(account.get("api_key") or "").strip()
base_url = str(account.get("base_url") or "").strip().rstrip("/")

if not api_key:
    print(json.dumps({"verdict": "setup_error", "error": "missing api_key on account"}, ensure_ascii=False))
    raise SystemExit(0)
if not base_url:
    print(json.dumps({"verdict": "setup_error", "error": "missing base_url on account"}, ensure_ascii=False))
    raise SystemExit(0)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


opener = urllib.request.build_opener(NoRedirect())


def request(method, url, body_obj=None, extra_headers=None):
    headers = {"Authorization": f"Bearer {api_key}", "Accept": "application/json"}
    if extra_headers:
        headers.update(extra_headers)
    data = None
    if body_obj is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body_obj).encode("utf-8")
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with opener.open(req, timeout=timeout) as resp:
            raw = resp.read(2 << 20)
            return resp.status, dict(resp.headers), raw
    except urllib.error.HTTPError as exc:
        raw = exc.read(4096)
        return exc.code, dict(exc.headers), raw
    except Exception as exc:  # noqa: BLE001
        return None, {}, str(exc).encode("utf-8")


def join_path(base, suffix):
    if suffix.startswith("/"):
        suffix = suffix[1:]
    if base.endswith("/"):
        return base + suffix
    return base + "/" + suffix


def parse_models(raw):
    try:
        payload = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return []
    data = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(data, list):
        return []
    out = []
    for item in data:
        if not isinstance(item, dict):
            continue
        model_id = str(item.get("id") or "").strip()
        if not model_id:
            continue
        arch = item.get("architecture") if isinstance(item.get("architecture"), dict) else {}
        modality = str(arch.get("modality") or "")
        out.append({"id": model_id, "modality": modality})
    return out


def pick_probe_models(models):
    text = next((m["id"] for m in models if "text->text" in m.get("modality", "") or m.get("modality", "").endswith("->text")), None)
    image = next((m["id"] for m in models if "->text+image" in m.get("modality", "") or "image" in m.get("modality", "")), None)
    if not text and models:
        text = models[0]["id"]
    return text, image


def excerpt(raw, limit=240):
    text = raw.decode("utf-8", errors="replace") if isinstance(raw, (bytes, bytearray)) else str(raw)
    text = re.sub(r"\s+", " ", text).strip()
    return text[:limit]


models_url = join_path(base_url, "v1/models")
status, headers, raw = request("GET", models_url)
models = parse_models(raw) if status and 200 <= status < 300 else []
text_model, image_model = pick_probe_models(models)

mapping = account.get("model_mapping") if isinstance(account.get("model_mapping"), dict) else {}
mapped_models = sorted(str(k) for k in mapping.keys())

protocols = []

# models list
protocols.append({
    "protocol": "models_list",
    "method": "GET",
    "path": "/v1/models",
    "url": models_url,
    "http_status": status,
    "supported": bool(status and 200 <= status < 300 and models),
    "model_count": len(models),
    "location": headers.get("Location") or headers.get("location"),
    "error_excerpt": None if status and 200 <= status < 300 else excerpt(raw),
})

chat_model = mapped_models[0] if mapped_models else text_model
if chat_model:
    chat_url = join_path(base_url, "v1/chat/completions")
    body = {
        "model": chat_model,
        "messages": [{"role": "user", "content": "Reply OK only."}],
        "max_tokens": 8,
        "stream": False,
    }
    status, _, raw = request("POST", chat_url, body)
    protocols.append({
        "protocol": "chat_completions",
        "method": "POST",
        "path": "/v1/chat/completions",
        "url": chat_url,
        "probe_model": chat_model,
        "http_status": status,
        "supported": bool(status and 200 <= status < 300),
        "error_excerpt": None if status and 200 <= status < 300 else excerpt(raw),
    })

if chat_model:
    resp_url = join_path(base_url, "v1/responses")
    body = {
        "model": chat_model,
        "input": [{"role": "user", "content": [{"type": "input_text", "text": "Reply OK only."}]}],
        "max_output_tokens": 8,
        "stream": False,
    }
    status, _, raw = request("POST", resp_url, body)
    protocols.append({
        "protocol": "responses",
        "method": "POST",
        "path": "/v1/responses",
        "url": resp_url,
        "probe_model": chat_model,
        "http_status": status,
        "supported": bool(status and 200 <= status < 300),
        "error_excerpt": None if status and 200 <= status < 300 else excerpt(raw),
    })

if chat_model:
    emb_url = join_path(base_url, "v1/embeddings")
    body = {"model": chat_model, "input": "hello"}
    status, _, raw = request("POST", emb_url, body)
    protocols.append({
        "protocol": "embeddings",
        "method": "POST",
        "path": "/v1/embeddings",
        "url": emb_url,
        "probe_model": chat_model,
        "http_status": status,
        "supported": bool(status and 200 <= status < 300),
        "error_excerpt": None if status and 200 <= status < 300 else excerpt(raw),
    })

if image_model or chat_model:
    img_model = image_model or chat_model
    img_url = join_path(base_url, "v1/images/generations")
    body = {"model": img_model, "prompt": "a red circle", "n": 1, "size": "512x512"}
    status, _, raw = request("POST", img_url, body)
    protocols.append({
        "protocol": "images_generations",
        "method": "POST",
        "path": "/v1/images/generations",
        "url": img_url,
        "probe_model": img_model,
        "http_status": status,
        "supported": bool(status and 200 <= status < 300),
        "error_excerpt": None if status and 200 <= status < 300 else excerpt(raw),
    })

vid_url = join_path(base_url, "v1/videos/generations")
vid_model = image_model or chat_model or "sora-2"
body = {"model": vid_model, "prompt": "a cat walking", "duration": 2}
status, _, raw = request("POST", vid_url, body)
protocols.append({
    "protocol": "videos_generations",
    "method": "POST",
    "path": "/v1/videos/generations",
    "url": vid_url,
    "probe_model": vid_model,
    "http_status": status,
    "supported": bool(status and 200 <= status < 300),
    "error_excerpt": None if status and 200 <= status < 300 else excerpt(raw),
})

if chat_model:
    msg_url = join_path(base_url, "v1/messages")
    body = {
        "model": chat_model,
        "max_tokens": 8,
        "messages": [{"role": "user", "content": "Reply OK only."}],
    }
    status, _, raw = request("POST", msg_url, body, {"anthropic-version": "2023-06-01"})
    protocols.append({
        "protocol": "anthropic_messages",
        "method": "POST",
        "path": "/v1/messages",
        "url": msg_url,
        "probe_model": chat_model,
        "http_status": status,
        "supported": bool(status and 200 <= status < 300),
        "error_excerpt": None if status and 200 <= status < 300 else excerpt(raw),
    })

# modality breakdown
modality_counts = {}
for m in models:
    mod = m.get("modality") or "unknown"
    modality_counts[mod] = modality_counts.get(mod, 0) + 1

print(json.dumps({
    "verdict": "ok" if models else "partial",
    "account": {
        "id": int(account_id),
        "name": account.get("name"),
        "platform": account.get("platform"),
        "type": account.get("type"),
        "channel_type": account.get("channel_type"),
        "base_url": base_url,
    },
    "upstream_models": {
        "count": len(models),
        "modality_counts": modality_counts,
        "ids": [m["id"] for m in models],
        "details": models,
    },
    "model_mapping_keys": mapped_models,
    "protocols": protocols,
}, ensure_ascii=False, indent=2))
PY
