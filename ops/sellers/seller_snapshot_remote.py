#!/usr/bin/env python3
"""Read seller state; optionally ensure platform keys; never emit credentials."""
import base64
import gzip
import hashlib
import json
import subprocess
import sys
import urllib.request
from datetime import datetime, timezone


def query(sql):
    result = subprocess.run(
        ["sudo", "docker", "exec", "-i", "tokenkey-postgres", "psql",
         "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t",
         "-v", "ON_ERROR_STOP=1", "-c", sql],
        text=True, capture_output=True, check=False,
    )
    if result.returncode:
        raise RuntimeError("seller snapshot database query failed")
    return result.stdout.strip()


def get_json(path, key=None):
    headers = {"Authorization": "Bearer " + key} if key else {}
    request = urllib.request.Request("https://api.tokenkey.dev" + path, headers=headers)
    with urllib.request.urlopen(request, timeout=45) as response:
        return json.load(response)


def ensure_platform_keys(uid, keys):
    names = ("nanogpt", "poe", "huggingface", "eurouter")
    pending = []
    for name in names:
        matches = [k for k in keys if k["name"] == name]
        if matches:
            if len(matches) != 1 or matches[0]["status"] != "active" or matches[0]["routing_mode"] != "universal" or matches[0]["group_id"] is not None:
                raise RuntimeError("existing platform key needs manual reconciliation")
        else:
            pending.append(name)
    if not pending:
        return
    admin_key = query("SELECT value FROM settings WHERE key='admin_api_key'")
    if not admin_key:
        raise RuntimeError("admin authentication unavailable")
    for name in pending:
        request = urllib.request.Request(
            f"https://tokenkey.dev/api/v1/admin/users/{uid}/api-keys",
            data=json.dumps({"name": name, "routing_mode": "universal"}).encode(),
            headers={"x-api-key": admin_key, "Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            payload = json.load(response)
        data = payload.get("data", payload)
        if not isinstance(data, dict) or not data.get("id"):
            raise RuntimeError("platform key create failed")


def snapshot(ensure_keys=False):
    config = json.loads(query("SELECT value FROM settings WHERE key='tk_openrouter_provider_config'"))
    uid = int(config["billing_user_id"])
    if uid <= 0 or not config.get("enabled"):
        raise RuntimeError("seller catalog is not enabled")
    user = json.loads(query(
        "SELECT json_build_object('id',id,'email',email,'username',username,'status',status) "
        f"FROM users WHERE id={uid} AND deleted_at IS NULL"
    ))
    groups = json.loads(query(
        "SELECT coalesce(json_agg(row_to_json(s) ORDER BY s.id),'[]'::json) FROM ("
        "SELECT g.id,g.name,g.platform,g.status,g.rate_multiplier,"
        "g.peak_rate_enabled,g.peak_start,g.peak_end,g.peak_rate_multiplier,"
        "r.rate_multiplier AS user_rate_multiplier,r.rpm_override "
        "FROM user_allowed_groups a JOIN groups g ON g.id=a.group_id "
        "LEFT JOIN user_group_rate_multipliers r ON r.user_id=a.user_id AND r.group_id=g.id "
        f"WHERE a.user_id={uid} AND g.deleted_at IS NULL) s"
    ))
    keys_sql = (
        "SELECT coalesce(json_agg(row_to_json(s) ORDER BY s.id),'[]'::json) FROM ("
        "SELECT id,name,status,group_id,routing_mode FROM api_keys "
        f"WHERE user_id={uid} AND deleted_at IS NULL) s"
    )
    keys = json.loads(query(keys_sql))
    usable = [k for k in keys if k["name"] == "openrouter" and k["status"] == "active"]
    if len(usable) != 1 or usable[0]["routing_mode"] != "universal":
        raise RuntimeError("expected one active universal OpenRouter seller key")
    if ensure_keys:
        ensure_platform_keys(uid, keys)
        keys = json.loads(query(keys_sql))
    key = query(
        f"SELECT key FROM api_keys WHERE id={int(usable[0]['id'])} AND user_id={uid} "
        "AND deleted_at IS NULL AND status='active'"
    )
    catalog = get_json("/openrouter/v1/models", key)
    public = get_json("/api/v1/public/pricing")
    # Public API responses may be wrapped by the common application envelope.
    if isinstance(public.get("data"), dict):
        public = public["data"]
    prefix = config.get("model_id_prefix", "tokenkey/")
    source_ids = {row["id"].removeprefix(prefix) for row in catalog["data"]}
    public["data"] = [row for row in public["data"] if row["model_id"] in source_ids]
    return {
        "captured_at": datetime.now(timezone.utc).isoformat(),
        "user": user, "groups": groups, "api_keys": keys,
        "model_id_prefix": prefix,
        "catalog": catalog, "public_pricing": public,
        "catalog_excluded_model_ids": config.get("catalog_excluded_model_ids", []),
        "stream_only_model_ids": config.get("stream_only_model_ids", []),
    }


if __name__ == "__main__":
    try:
        raw = json.dumps(snapshot(ensure_keys="--ensure-platform-keys" in sys.argv), separators=(",", ":")).encode()
        packed = base64.b64encode(gzip.compress(raw)).decode()
        if len(packed) > 22000:
            raise RuntimeError("snapshot exceeds SSM inline output budget")
        print(json.dumps({"sha256": hashlib.sha256(raw).hexdigest(), "gzip_base64": packed}))
    except Exception as exc:
        # Network/process exceptions can embed credentials; only emit the class.
        raise SystemExit("seller snapshot failed: " + type(exc).__name__) from None
