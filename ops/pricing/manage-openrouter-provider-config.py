#!/usr/bin/env python3
"""Prod ops for tk_openrouter_provider_config and OpenRouter seller API keys.

Subcommands
-----------
  snapshot      Read-only: user 32 allowed groups, existing OR-named keys, live config.
  bootstrap     Create inference + monitor keys for user 32 (if missing), upsert
                tk_openrouter_provider_config, publish settings_updated.
  update-config Upsert tk_openrouter_provider_config (billing user +
                exclude/stream lists). Supply groups and key ids are NOT
                stored — runtime reads user_allowed_groups and keys named
                openrouter-inference / openrouter-monitor.

Secrets policy: bootstrap prints only api_key ids and config field names — never
the raw key material returned by the admin create endpoint.
"""
from __future__ import annotations

import argparse
import base64
import gzip
import importlib.util
import json
import sys
from pathlib import Path
from typing import Any, NoReturn

REPO_ROOT = Path(__file__).resolve().parents[2]
SETTING_KEY = "tk_openrouter_provider_config"
BILLING_USER_ID = 32
INFERENCE_KEY_NAME = "openrouter-inference"
MONITOR_KEY_NAME = "openrouter-monitor"
APP_URL = "http://127.0.0.1:8080"

PSQL = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1"
REDISCLI = "env -u REDISCLI_AUTH sudo docker exec tokenkey-redis redis-cli"

_ssm_spec = importlib.util.spec_from_file_location(
    "tk_ssm_execution", REPO_ROOT / "ops" / "stage0" / "ssm_execution.py")
_SSM = importlib.util.module_from_spec(_ssm_spec)
_ssm_spec.loader.exec_module(_SSM)


def fail(msg: str) -> NoReturn:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(2)


def _sql_literal(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def _decode_settings_value(out: str) -> str:
    out = out.strip()
    if not out or out == "\\N":
        return ""
    return out


def _run_psql_query(instance_id: str, sql: str, comment: str) -> str:
    shell = f"{PSQL} -c {json.dumps(sql)}"
    b64 = base64.b64encode(shell.encode()).decode()
    return _SSM.run_shell_b64(instance_id, b64, comment).strip()


def _remote_bootstrap_script(group_id: int, config: dict, create_group: bool) -> str:
    """Shell script executed on prod; uses embedded python for admin API I/O."""
    config_json = json.dumps(config, ensure_ascii=False, separators=(",", ":"))
    py = r'''
import json, sys

USER_ID = 32
REQUESTED_GROUP_ID = __GROUP_ID__
CREATE_GROUP = __CREATE_GROUP__
INFERENCE_NAME = "openrouter-inference"
MONITOR_NAME = "openrouter-monitor"
CONFIG = json.loads("""__CONFIG_JSON__""")
APP_URL = "http://127.0.0.1:8080"
APP_CONTAINER = "tokenkey-green"

def psql_scalar(sql):
    import subprocess
    cmd = ["sudo", "docker", "exec", "-i", "tokenkey-postgres",
           "psql", "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", sql]
    out = subprocess.check_output(cmd, text=True).strip()
    return out if out and out != r"\N" else ""

def admin_request(method, path, payload=None):
    import subprocess
    admin_key = psql_scalar("SELECT value FROM settings WHERE key='admin_api_key' LIMIT 1")
    if not admin_key:
        raise SystemExit("admin_api_key missing in settings")
    cmd = ["sudo", "docker", "exec", APP_CONTAINER, "curl", "-sS", "-X", method,
           "-H", f"x-api-key: {admin_key}", APP_URL + path]
    if payload is not None:
        cmd.extend(["-H", "Content-Type: application/json", "-d", json.dumps(payload)])
    try:
        out = subprocess.check_output(cmd, text=True)
    except subprocess.CalledProcessError as e:
        detail = (e.output or str(e))[:500]
        raise SystemExit(f"admin {method} {path} failed: {detail}") from e
    return json.loads(out)

def unwrap(resp):
    if isinstance(resp, dict) and "data" in resp:
        return resp["data"]
    return resp

def list_groups():
    resp = admin_request("GET", "/api/v1/admin/groups?page=1&page_size=200")
    data = unwrap(resp)
    if isinstance(data, list):
        return data
    if isinstance(data, dict):
        return data.get("items") or []
    return []

def list_user_keys():
    resp = admin_request("GET", f"/api/v1/admin/users/{USER_ID}/api-keys?page=1&page_size=100")
    data = unwrap(resp)
    if isinstance(data, list):
        return data
    if isinstance(data, dict):
        return data.get("items") or []
    return []

def find_group_by_name(name):
    target = name.lower()
    for item in list_groups():
        if (item.get("name") or "").lower() == target:
            return int(item["id"])
    return 0

def ensure_group_id():
    if REQUESTED_GROUP_ID > 0:
        return REQUESTED_GROUP_ID
    gid = find_group_by_name("OpenRouter")
    if gid > 0:
        return gid
    if not CREATE_GROUP:
        raise SystemExit("OpenRouter group not found; pass --group-id or allow --create-group")
    resp = admin_request("POST", "/api/v1/admin/groups", {
        "name": "OpenRouter",
        "platform": "newapi",
        "is_exclusive": True,
        "rate_multiplier": 1.0,
        "description": "OpenRouter provider dedicated supply",
    })
    return int(unwrap(resp)["id"])

def ensure_user_allowed_group(group_id):
    user = unwrap(admin_request("GET", f"/api/v1/admin/users/{USER_ID}"))
    allowed = user.get("allowed_groups") or []
    if allowed == [group_id]:
        return
    admin_request("PUT", f"/api/v1/admin/users/{USER_ID}", {"allowed_groups": [group_id]})

def find_key_id(keys, name):
    for item in keys or []:
        if (item.get("name") or "") == name and (item.get("status") or "") != "disabled":
            return int(item["id"])
    return 0

def create_key(name, group_id):
    resp = admin_request("POST", f"/api/v1/admin/users/{USER_ID}/api-keys", {
        "name": name,
        "group_id": group_id,
        "routing_mode": "direct",
    })
    data = unwrap(resp)
    kid = int(data.get("id") or 0)
    if kid <= 0:
        raise SystemExit(f"create {name}: missing id in response envelope")
    return kid

group_id = ensure_group_id()
ensure_user_allowed_group(group_id)

existing = list_user_keys()
inference_id = find_key_id(existing, INFERENCE_NAME)
monitor_id = find_key_id(existing, MONITOR_NAME)
if inference_id <= 0:
    inference_id = create_key(INFERENCE_NAME, group_id)
if monitor_id <= 0:
    monitor_id = create_key(MONITOR_NAME, group_id)

CONFIG["billing_user_id"] = USER_ID
CONFIG.pop("group_ids", None)  # runtime reads user_allowed_groups
CONFIG.pop("allowed_api_key_ids", None)  # runtime matches openrouter-inference by name
CONFIG.pop("monitor_api_key_ids", None)  # runtime matches openrouter-monitor by name
CONFIG["enabled"] = True

config_text = json.dumps(CONFIG, ensure_ascii=False, separators=(",", ":"))
config_b64 = __import__("base64").b64encode(config_text.encode()).decode()

upsert_sql = f"""
INSERT INTO settings (key, value, updated_at)
VALUES ('tk_openrouter_provider_config', convert_from(decode('{config_b64}','base64'),'UTF8'), NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
SELECT key || '|' || length(value)::text FROM settings WHERE key='tk_openrouter_provider_config';
"""

import subprocess
psql = ["sudo", "docker", "exec", "-i", "tokenkey-postgres",
        "psql", "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1"]
proc = subprocess.Popen(psql, stdin=subprocess.PIPE)
proc.communicate(input=upsert_sql.encode())
if proc.returncode != 0:
    raise SystemExit("settings upsert failed")
subprocess.run(
    ["env", "-u", "REDISCLI_AUTH", "sudo", "docker", "exec", "tokenkey-redis", "redis-cli",
     "PUBLISH", "settings_updated", "refresh"],
    check=False,
)
print(json.dumps({
    "billing_user_id": USER_ID,
    "group_id": group_id,
    "inference_key_id": inference_id,
    "monitor_key_id": monitor_id,
    "inference_key_name": INFERENCE_NAME,
    "monitor_key_name": MONITOR_NAME,
    "config_key": "tk_openrouter_provider_config",
    "enabled": True,
}, ensure_ascii=False))
'''.replace("__GROUP_ID__", str(group_id)).replace("__CREATE_GROUP__", "True" if create_group else "False").replace("__CONFIG_JSON__", config_json.replace("\\", "\\\\").replace('"', '\\"'))
    return (
        "set -euo pipefail\n"
        f"python3 <<'PYEOF'\n{py}\nPYEOF\n"
    )


_EXAMPLE_LIST_FIELDS = (
    "catalog_excluded_model_ids",
    "stream_only_model_ids",
)


def _load_default_config() -> dict:
    example = REPO_ROOT / "ops/pricing/examples/openrouter-provider-config.example.json"
    cfg = json.loads(example.read_text(encoding="utf-8"))
    cfg.pop("group_ids", None)
    cfg.pop("billing_user_id", None)
    cfg.pop("allowed_api_key_ids", None)
    cfg.pop("monitor_api_key_ids", None)
    return cfg


def _merge_missing_list_fields(cfg: dict[str, Any], example: dict[str, Any] | None = None) -> dict[str, Any]:
    """Backfill list SSOT fields from example when live settings omit or null them."""
    if example is None:
        example = _load_default_config()
    merged = dict(cfg)
    for key in _EXAMPLE_LIST_FIELDS:
        if key not in merged or merged[key] is None:
            merged[key] = list(example.get(key) or [])
    return merged


def _resolve_group_id(instance_id: str, group_id: int | None, create_group: bool) -> int:
    if group_id and group_id > 0:
        return group_id
    sql = (
        "SELECT g.id, g.name FROM groups g "
        "WHERE g.deleted_at IS NULL AND lower(g.name) = 'openrouter' "
        "ORDER BY g.id LIMIT 1;"
    )
    out = _run_psql_query(instance_id, sql, "or-provider: find OpenRouter group")
    if out:
        parts = out.splitlines()[0].split("|")
        gid = int(parts[0])
        name = parts[1] if len(parts) > 1 else ""
        print(f"resolved group_id={gid} name={name!r}", file=sys.stderr)
        return gid
    if create_group:
        print("OpenRouter group not found; remote bootstrap will create it", file=sys.stderr)
        return 0
    fail("OpenRouter group not found; pass --group-id or use --create-group")


def cmd_snapshot(_args) -> int:
    inst = _SSM.resolve_prod_instance()
    user_sql = (
        f"SELECT id, email, username FROM users WHERE id = {BILLING_USER_ID} AND deleted_at IS NULL;"
    )
    groups_sql = (
        "SELECT g.id, g.name, g.is_exclusive, g.platform FROM groups g "
        "JOIN user_allowed_groups uag ON uag.group_id = g.id "
        f"WHERE uag.user_id = {BILLING_USER_ID} AND g.deleted_at IS NULL ORDER BY g.id;"
    )
    keys_sql = (
        "SELECT id, name, status, group_id, routing_mode FROM api_keys "
        f"WHERE user_id = {BILLING_USER_ID} AND deleted_at IS NULL ORDER BY id;"
    )
    cfg_sql = f"SELECT value FROM settings WHERE key = {_sql_literal(SETTING_KEY)} LIMIT 1;"
    snapshot = {
        "user": _run_psql_query(inst, user_sql, "or-provider snapshot: user"),
        "groups": _run_psql_query(inst, groups_sql, "or-provider snapshot: groups"),
        "api_keys": _run_psql_query(inst, keys_sql, "or-provider snapshot: keys"),
        "config_present": bool(_decode_settings_value(_run_psql_query(inst, cfg_sql, "or-provider snapshot: config"))),
    }
    print(json.dumps(snapshot, indent=2, ensure_ascii=False))
    return 0


def _remote_update_config_script(
    inference_key_id: int,
    monitor_key_id: int,
    config: dict,
    supply_group_ids: list[int],
) -> str:
    config_json = json.dumps(config, ensure_ascii=False, separators=(",", ":"))
    py = r'''
import json, subprocess, base64

USER_ID = 32
INFERENCE_ID = __INFERENCE_ID__
MONITOR_ID = __MONITOR_ID__
SUPPLY_GROUP_IDS = __SUPPLY_GROUP_IDS__
CONFIG = json.loads("""__CONFIG_JSON__""")

CONFIG["billing_user_id"] = USER_ID
CONFIG.pop("group_ids", None)  # runtime reads user_allowed_groups for billing_user_id
CONFIG.pop("allowed_api_key_ids", None)
CONFIG.pop("monitor_api_key_ids", None)
CONFIG["enabled"] = True

config_text = json.dumps(CONFIG, ensure_ascii=False, separators=(",", ":"))
config_b64 = base64.b64encode(config_text.encode()).decode()
upsert_sql = f"""
INSERT INTO settings (key, value, updated_at)
VALUES ('tk_openrouter_provider_config', convert_from(decode('{config_b64}','base64'),'UTF8'), NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
SELECT key || '|' || length(value)::text FROM settings WHERE key='tk_openrouter_provider_config';
"""
psql = ["sudo", "docker", "exec", "-i", "tokenkey-postgres",
        "psql", "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1"]
proc = subprocess.Popen(psql, stdin=subprocess.PIPE)
proc.communicate(input=upsert_sql.encode())
if proc.returncode != 0:
    raise SystemExit("settings upsert failed")
subprocess.run(
    ["env", "-u", "REDISCLI_AUTH", "sudo", "docker", "exec", "tokenkey-redis", "redis-cli",
     "PUBLISH", "settings_updated", "refresh"],
    check=False,
)
print(json.dumps({
    "billing_user_id": USER_ID,
    "supply_group_ids_from_user": SUPPLY_GROUP_IDS,
    "inference_key_id": INFERENCE_ID,
    "monitor_key_id": MONITOR_ID,
    "inference_key_name": "openrouter-inference",
    "monitor_key_name": "openrouter-monitor",
    "config_key": "tk_openrouter_provider_config",
    "enabled": True,
}, ensure_ascii=False))
'''.replace("__SUPPLY_GROUP_IDS__", json.dumps(supply_group_ids)).replace(
        "__INFERENCE_ID__", str(inference_key_id)
    ).replace("__MONITOR_ID__", str(monitor_key_id)).replace(
        "__CONFIG_JSON__", config_json.replace("\\", "\\\\").replace('"', '\\"')
    )
    return "set -euo pipefail\n" f"python3 <<'PYEOF'\n{py}\nPYEOF\n"


def _parse_snapshot_groups(groups_raw: str) -> list[int]:
    ids: list[int] = []
    for line in groups_raw.splitlines():
        line = line.strip()
        if not line:
            continue
        parts = line.split("|")
        if parts and parts[0].isdigit():
            ids.append(int(parts[0]))
    return ids


def _parse_snapshot_key_id(api_keys_raw: str, name: str) -> int:
    for line in api_keys_raw.splitlines():
        parts = line.split("|")
        if len(parts) >= 2 and parts[1] == name and parts[0].isdigit():
            return int(parts[0])
    return 0


def cmd_update_config(args) -> int:
    inst = _SSM.resolve_prod_instance()
    groups_sql = (
        "SELECT g.id, g.name, g.is_exclusive, g.platform FROM groups g "
        "JOIN user_allowed_groups uag ON uag.group_id = g.id "
        f"WHERE uag.user_id = {BILLING_USER_ID} AND g.deleted_at IS NULL ORDER BY g.id;"
    )
    keys_sql = (
        "SELECT id, name, status, group_id, routing_mode FROM api_keys "
        f"WHERE user_id = {BILLING_USER_ID} AND deleted_at IS NULL ORDER BY id;"
    )
    groups_raw = _run_psql_query(inst, groups_sql, "or-provider update: groups")
    keys_raw = _run_psql_query(inst, keys_sql, "or-provider update: keys")
    supply_group_ids = _parse_snapshot_groups(groups_raw)
    if not supply_group_ids:
        fail("user 32 has no allowed groups; catalog would be empty")
    inference_id = _parse_snapshot_key_id(keys_raw, INFERENCE_KEY_NAME)
    monitor_id = _parse_snapshot_key_id(keys_raw, MONITOR_KEY_NAME)
    if inference_id <= 0 or monitor_id <= 0:
        fail(f"missing OR keys: inference={inference_id} monitor={monitor_id}; run bootstrap first")

    cfg_sql = f"SELECT value FROM settings WHERE key = {_sql_literal(SETTING_KEY)} LIMIT 1;"
    existing_raw = _decode_settings_value(_run_psql_query(inst, cfg_sql, "or-provider update: config"))
    if existing_raw:
        cfg = _merge_missing_list_fields(json.loads(existing_raw))
    else:
        cfg = _load_default_config()
    cfg.pop("group_ids", None)
    cfg.pop("allowed_api_key_ids", None)
    cfg.pop("monitor_api_key_ids", None)

    if args.dry_run:
        print(json.dumps({
            "dry_run": True,
            "billing_user_id": BILLING_USER_ID,
            "supply_group_ids_from_user": supply_group_ids,
            "inference_key_id": inference_id,
            "monitor_key_id": monitor_id,
            "inference_key_name": INFERENCE_KEY_NAME,
            "monitor_key_name": MONITOR_KEY_NAME,
            "would_upsert": SETTING_KEY,
            "note": "group_ids and key id lists are not written; runtime reads user_allowed_groups + key names",
        }, indent=2, ensure_ascii=False))
        return 0

    shell = _remote_update_config_script(inference_id, monitor_id, cfg, supply_group_ids)
    b64 = base64.b64encode(shell.encode()).decode()
    out = _SSM.run_shell_b64(inst, b64, "or-provider update-config")
    for line in reversed(out.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            result = json.loads(line)
            print(json.dumps(result, indent=2, ensure_ascii=False))
            return 0
    fail(f"update-config did not return result JSON; output tail:\n{out[-1500:]}")


def cmd_bootstrap(args) -> int:
    inst = _SSM.resolve_prod_instance()
    group_id = _resolve_group_id(inst, args.group_id, args.create_group)
    cfg = _load_default_config()
    if args.dry_run:
        print(json.dumps({
            "dry_run": True,
            "billing_user_id": BILLING_USER_ID,
            "group_id": group_id,
            "would_upsert": SETTING_KEY,
            "inference_key_name": INFERENCE_KEY_NAME,
            "monitor_key_name": MONITOR_KEY_NAME,
        }, indent=2, ensure_ascii=False))
        return 0
    shell = _remote_bootstrap_script(group_id, cfg, args.create_group)
    b64 = base64.b64encode(shell.encode()).decode()
    out = _SSM.run_shell_b64(inst, b64, "or-provider bootstrap: keys + config")
    # Remote prints one JSON line with ids only.
    for line in reversed(out.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            result = json.loads(line)
            print(json.dumps(result, indent=2, ensure_ascii=False))
            return 0
    fail(f"bootstrap did not return result JSON; output tail:\n{out[-1500:]}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("snapshot", help="read-only prod state for user 32")
    boot = sub.add_parser("bootstrap", help="create keys + upsert tk_openrouter_provider_config")
    boot.add_argument("--group-id", type=int, default=0, help="OR dedicated group id (auto-detect/create if omitted)")
    boot.add_argument("--create-group", action="store_true", default=True,
                      help="create OpenRouter group when missing (default: true)")
    boot.add_argument("--no-create-group", dest="create_group", action="store_false")
    boot.add_argument("--dry-run", action="store_true")
    upd = sub.add_parser("update-config", help="upsert config keys for user 32; supply groups come from user_allowed_groups at runtime")
    upd.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    if args.cmd == "snapshot":
        return cmd_snapshot(args)
    if args.cmd == "update-config":
        return cmd_update_config(args)
    if args.cmd == "bootstrap":
        return cmd_bootstrap(args)
    fail(f"unknown command {args.cmd!r}")


if __name__ == "__main__":
    raise SystemExit(main())
