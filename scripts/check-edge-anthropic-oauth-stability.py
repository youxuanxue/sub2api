#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import subprocess
import sys
from typing import Any

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
DEFAULT_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
DEFAULT_BASELINE = REPO_ROOT / "deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json"
CONFIRM_UPDATE = "yes-update-anthropic-stable-list"


class CheckError(Exception):
    pass


def fail(message: str) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise CheckError(message)


def log(message: str, *, quiet: bool = False) -> None:
    if not quiet:
        print(message)


def load_json(path: pathlib.Path) -> dict[str, Any]:
    if not path.is_file():
        fail(f"file not found: {path}")
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        fail(f"invalid JSON in {path}: {exc}")


def write_json(path: pathlib.Path, data: dict[str, Any]) -> None:
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2, sort_keys=False) + "\n", encoding="utf-8")


def run_cmd(args: list[str], *, input_text: str | None = None) -> str:
    try:
        proc = subprocess.run(
            args,
            input=input_text,
            text=True,
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    except FileNotFoundError:
        fail(f"command not found: {args[0]}")
    if proc.returncode != 0:
        stderr = proc.stderr.strip()
        stdout = proc.stdout.strip()
        detail = stderr or stdout or f"exit {proc.returncode}"
        fail(f"command failed: {' '.join(args)}\n{detail}")
    return proc.stdout


def resolve_edge(matrix: dict[str, Any], edge_id: str, *, allow_planned: bool = False) -> dict[str, Any]:
    targets = matrix.get("targets") or {}
    target = targets.get(edge_id)
    if target is None:
        fail(f"unknown edge_id {edge_id}; known edges: {', '.join(sorted(targets))}")
    if not target.get("deployable") and not allow_planned:
        fail(f"edge_id {edge_id} is planned but not deployable")
    for key in ("region", "stack", "domain", "ssm_prefix"):
        if not target.get(key):
            fail(f"edge_id {edge_id} missing required field {key}")
    return {
        "edge_id": edge_id,
        "region": target["region"],
        "stack": target["stack"],
        "domain": target["domain"],
        "ssm_prefix": target["ssm_prefix"],
        "deployable": bool(target.get("deployable")),
    }


def resolve_edges(matrix: dict[str, Any], edge_id: str, *, allow_planned: bool = False) -> tuple[list[dict[str, Any]], list[dict[str, str]]]:
    if edge_id != "all":
        return [resolve_edge(matrix, edge_id, allow_planned=allow_planned)], []

    targets = matrix.get("targets") or {}
    edges: list[dict[str, Any]] = []
    excluded: list[dict[str, str]] = []
    for candidate in sorted(targets):
        target = targets[candidate] or {}
        deployable = bool(target.get("deployable"))
        if not deployable and not allow_planned:
            excluded.append({"edge_id": candidate, "reason": "deployable=false"})
            continue
        edges.append(resolve_edge(matrix, candidate, allow_planned=True))
    return edges, excluded


def resolve_instance_id(edge: dict[str, Any], instance_id: str) -> str:
    if instance_id:
        return instance_id
    out = run_cmd([
        "aws",
        "cloudformation",
        "describe-stacks",
        "--region",
        edge["region"],
        "--stack-name",
        edge["stack"],
        "--query",
        "Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue",
        "--output",
        "text",
    ]).strip()
    if not out or out == "None":
        fail(f"could not resolve InstanceId from stack {edge['stack']}")
    return out


def sql_literal(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def build_account_names_query() -> str:
    return """
SELECT COALESCE(jsonb_agg(name ORDER BY name), '[]'::jsonb)
FROM accounts
WHERE platform = 'anthropic'
  AND type = 'oauth'
  AND deleted_at IS NULL;
""".strip()


def build_live_query(account_name: str) -> str:
    return f"""
WITH target AS (
  SELECT * FROM accounts
  WHERE name = {sql_literal(account_name)}
    AND platform = 'anthropic'
    AND type = 'oauth'
    AND deleted_at IS NULL
  ORDER BY id
  LIMIT 1
), account_stable AS (
  SELECT CASE WHEN COUNT(*) = 0 THEN NULL ELSE jsonb_build_object(
    'id', max(id),
    'name', max(name),
    'platform', max(platform),
    'type', max(type),
    'proxy_id', max(proxy_id),
    'concurrency', max(concurrency),
    'load_factor', max(load_factor),
    'priority', max(priority),
    'rate_multiplier', max(rate_multiplier),
    'auto_pause_on_expired', bool_or(auto_pause_on_expired),
    'channel_type', max(channel_type),
    'stability_tier', max(NULLIF(extra->>'stability_tier', '')),
    'status', max(status),
    'error_message', max(error_message),
    'last_used_at', max(last_used_at)
  ) END AS value
  FROM target
), credentials_stable AS (
  SELECT COALESCE(jsonb_object_agg(key, value ORDER BY key), '{{}}'::jsonb) AS value
  FROM target, jsonb_each(credentials)
  WHERE key IN (
    'intercept_warmup_requests',
    'temp_unschedulable_enabled',
    'temp_unschedulable_rules'
  )
), extra_stable AS (
  SELECT COALESCE(jsonb_object_agg(key, value ORDER BY key), '{{}}'::jsonb) AS value
  FROM target, jsonb_each(extra)
  WHERE key IN (
    'enable_tls_fingerprint',
    'tls_fingerprint_profile_id',
    'base_rpm',
    'rpm_strategy',
    'rpm_sticky_buffer',
    'user_msg_queue_mode',
    'max_sessions',
    'session_idle_timeout_minutes',
    'session_id_masking_enabled',
    'cache_ttl_override_enabled',
    'cache_ttl_override_target',
    'window_cost_limit',
    'window_cost_sticky_reserve',
    'custom_base_url_enabled',
    'custom_base_url'
  )
), group_stable AS (
  SELECT COALESCE(jsonb_agg(g.name ORDER BY g.name), '[]'::jsonb) AS names,
         COALESCE(jsonb_agg(g.id ORDER BY g.id), '[]'::jsonb) AS ids
  FROM target t
  LEFT JOIN account_groups ag ON ag.account_id = t.id
  LEFT JOIN groups g ON g.id = ag.group_id
  WHERE g.id IS NOT NULL
), tls_profile AS (
  SELECT CASE WHEN p.id IS NULL THEN NULL ELSE jsonb_build_object(
    'name', p.name,
    'description', p.description,
    'enable_grease', p.enable_grease,
    'cipher_suites', COALESCE(p.cipher_suites, '[]'::jsonb),
    'curves', COALESCE(p.curves, '[]'::jsonb),
    'point_formats', COALESCE(p.point_formats, '[]'::jsonb),
    'signature_algorithms', COALESCE(p.signature_algorithms, '[]'::jsonb),
    'alpn_protocols', COALESCE(p.alpn_protocols, '[]'::jsonb),
    'supported_versions', COALESCE(p.supported_versions, '[]'::jsonb),
    'key_share_groups', COALESCE(p.key_share_groups, '[]'::jsonb),
    'psk_modes', COALESCE(p.psk_modes, '[]'::jsonb),
    'extensions', COALESCE(p.extensions, '[]'::jsonb)
  ) END AS value
  FROM target t
  LEFT JOIN tls_fingerprint_profiles p ON p.id = NULLIF(t.extra->>'tls_fingerprint_profile_id', '')::bigint
)
SELECT jsonb_pretty(jsonb_build_object(
  'found', EXISTS(SELECT 1 FROM target),
  'account', (SELECT value FROM account_stable),
  'credentials', (SELECT value FROM credentials_stable),
  'extra', (SELECT value FROM extra_stable),
  'groups', jsonb_build_object('ids', (SELECT ids FROM group_stable), 'names', (SELECT names FROM group_stable)),
  'tls_profile', (SELECT value FROM tls_profile)
));
""".strip()


def run_remote_query(edge: dict[str, Any], instance_id: str, sql: str, *, comment: str) -> tuple[str, str]:
    remote = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -t -A -v ON_ERROR_STOP=1"
    command = f"set -euo pipefail\n{remote} <<'SQL'\n{sql}\nSQL"
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    cmd_id = run_cmd([
        "aws",
        "ssm",
        "send-command",
        "--region",
        edge["region"],
        "--instance-ids",
        instance_id,
        "--document-name",
        "AWS-RunShellScript",
        "--comment",
        comment,
        "--parameters",
        params,
        "--query",
        "Command.CommandId",
        "--output",
        "text",
    ]).strip()
    run_cmd([
        "aws",
        "ssm",
        "wait",
        "command-executed",
        "--region",
        edge["region"],
        "--command-id",
        cmd_id,
        "--instance-id",
        instance_id,
    ])
    stdout = run_cmd([
        "aws",
        "ssm",
        "get-command-invocation",
        "--region",
        edge["region"],
        "--command-id",
        cmd_id,
        "--instance-id",
        instance_id,
        "--query",
        "StandardOutputContent",
        "--output",
        "text",
    ]).strip()
    return cmd_id, stdout


def read_live_account_names(edge: dict[str, Any], instance_id: str) -> tuple[list[str], str]:
    cmd_id, stdout = run_remote_query(
        edge,
        instance_id,
        build_account_names_query(),
        comment=f"list Anthropic OAuth accounts edge={edge['edge_id']}",
    )
    try:
        data = json.loads(stdout)
    except json.JSONDecodeError as exc:
        fail(f"failed to parse account list JSON from SSM command {cmd_id}: {exc}\n{stdout[:1000]}")
    if not isinstance(data, list):
        fail(f"unexpected account list payload from SSM command {cmd_id}: {type(data).__name__}")
    names = [str(item) for item in data]
    return names, cmd_id


def read_live_account(edge: dict[str, Any], instance_id: str, account_name: str) -> dict[str, Any]:
    cmd_id, stdout = run_remote_query(
        edge,
        instance_id,
        build_live_query(account_name),
        comment=f"check Anthropic OAuth stability edge={edge['edge_id']} account={account_name}",
    )
    try:
        data = json.loads(stdout)
    except json.JSONDecodeError as exc:
        fail(f"failed to parse live account JSON from SSM command {cmd_id}: {exc}\n{stdout[:1000]}")
    data["ssm_command_id"] = cmd_id
    return data


def normalize_scalar(value: Any) -> Any:
    if isinstance(value, float) and value.is_integer():
        return int(value)
    return value


def normalize_value(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: normalize_value(value[key]) for key in sorted(value)}
    if isinstance(value, list):
        return [normalize_value(item) for item in value]
    return normalize_scalar(value)


def diff_dict(section: str, expected: dict[str, Any], actual: dict[str, Any] | None) -> list[dict[str, Any]]:
    actual = actual or {}
    diffs: list[dict[str, Any]] = []
    for key, expected_value in expected.items():
        actual_value = actual.get(key)
        if actual_value is None and expected_value is False:
            actual_value = False
        if normalize_value(actual_value) != normalize_value(expected_value):
            diffs.append({
                "path": f"/{section}/{key}",
                "expected": expected_value,
                "actual": actual.get(key),
            })
    return diffs


def diff_absent(section: str, absent_keys: list[str], actual: dict[str, Any] | None) -> list[dict[str, Any]]:
    actual = actual or {}
    diffs: list[dict[str, Any]] = []
    for key in absent_keys:
        if key in actual and actual[key] not in (None, ""):
            diffs.append({
                "path": f"/{section}/{key}",
                "expected": "absent",
                "actual": actual[key],
            })
    return diffs


def resolve_effective_baseline(
    baseline: dict[str, Any],
    live: dict[str, Any],
    *,
    edge_id: str,
    account_name: str,
    default_tier: str,
) -> dict[str, Any]:
    if not baseline.get("tiers"):
        return {"baseline": baseline.get("baseline") or {}}

    account = live.get("account") or {}
    tier_key = str(account.get("stability_tier") or "").strip().lower()
    if not tier_key and default_tier:
        tier_key = default_tier
    if not tier_key:
        fail(
            f"account {account_name} on edge {edge_id} missing account.extra.stability_tier; "
            "expected one of l1/l2/l3/l4"
        )

    tiers = baseline.get("tiers") or {}
    tier_cfg = tiers.get(tier_key)
    if not isinstance(tier_cfg, dict):
        fail(
            f"account {account_name} on edge {edge_id} has unsupported stability_tier={tier_key}; "
            f"known tiers: {', '.join(sorted(tiers))}"
        )

    shared = baseline.get("shared_baseline") or {}
    tier_base = tier_cfg.get("baseline") or {}
    effective = {
        "account": {**(shared.get("account") or {}), **(tier_base.get("account") or {})},
        "credentials": {**(shared.get("credentials") or {}), **(tier_base.get("credentials") or {})},
        "extra": {**(shared.get("extra") or {}), **(tier_base.get("extra") or {})},
        "extra_absent": list(shared.get("extra_absent") or []),
        "tls_profile": {**(shared.get("tls_profile") or {}), **(tier_base.get("tls_profile") or {})},
    }
    return {
        "baseline": effective,
        "selected_tier": tier_key,
        "selected_factor": tier_cfg.get("factor"),
        "tier_source": "account.extra.stability_tier" if (live.get("account") or {}).get("stability_tier") else "default_tier",
    }


def compare_live_to_baseline(live: dict[str, Any], baseline: dict[str, Any]) -> list[dict[str, Any]]:
    if not live.get("found"):
        return [{"path": "/account", "expected": "existing anthropic oauth account", "actual": None}]
    base = baseline.get("baseline") or {}
    diffs: list[dict[str, Any]] = []
    diffs.extend(diff_dict("account", base.get("account") or {}, live.get("account") or {}))
    diffs.extend(diff_dict("credentials", base.get("credentials") or {}, live.get("credentials") or {}))
    diffs.extend(diff_dict("extra", base.get("extra") or {}, live.get("extra") or {}))
    diffs.extend(diff_absent("extra", base.get("extra_absent") or [], live.get("extra") or {}))
    diffs.extend(diff_dict("tls_profile", base.get("tls_profile") or {}, live.get("tls_profile") or {}))
    return diffs


def pg_json(value: Any) -> str:
    return sql_literal(json.dumps(value, ensure_ascii=False, separators=(",", ":"))) + "::jsonb"


def pg_array_json(value: Any) -> str:
    return pg_json(value)


def generate_sql(edge: dict[str, Any], account_name: str, baseline: dict[str, Any]) -> str:
    base = baseline.get("baseline") or {}
    account = base.get("account") or {}
    credentials = base.get("credentials") or {}
    extra = dict(base.get("extra") or {})
    selected_tier = str(baseline.get("selected_tier") or "").strip().lower()
    if selected_tier:
        extra["stability_tier"] = selected_tier
    extra_absent = base.get("extra_absent") or []
    profile = base.get("tls_profile") or {}
    generated_at = dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat()

    profile_name = profile.get("name")
    if not profile_name:
        fail("baseline.tls_profile.name is required to generate SQL")

    profile_columns = [
        "name",
        "description",
        "enable_grease",
        "cipher_suites",
        "curves",
        "point_formats",
        "signature_algorithms",
        "alpn_protocols",
        "supported_versions",
        "key_share_groups",
        "psk_modes",
        "extensions",
    ]
    insert_values = []
    for column in profile_columns:
        value = profile.get(column)
        if column in ("name", "description"):
            insert_values.append("NULL" if value is None else sql_literal(str(value)))
        elif column == "enable_grease":
            insert_values.append("true" if bool(value) else "false")
        else:
            insert_values.append(pg_array_json(value or []))

    conflict_sets = [f"{column} = EXCLUDED.{column}" for column in profile_columns if column != "name"]
    conflict_sets.append("updated_at = NOW()")

    account_sets = []
    for key in ("proxy_id", "concurrency", "load_factor", "priority", "rate_multiplier", "auto_pause_on_expired", "channel_type"):
        if key not in account:
            continue
        value = account[key]
        if value is None:
            if key in ("proxy_id", "load_factor"):
                account_sets.append(f"{key} = NULL")
        elif isinstance(value, bool):
            account_sets.append(f"{key} = {'true' if value else 'false'}")
        elif isinstance(value, str):
            account_sets.append(f"{key} = {sql_literal(value)}")
        else:
            account_sets.append(f"{key} = {value}")

    extra["tls_fingerprint_profile_id"] = "__PROFILE_ID__"
    extra_items = []
    for key, value in extra.items():
        if value == "__PROFILE_ID__":
            extra_items.append(f"{sql_literal(key)}, (SELECT id FROM profile)")
        else:
            extra_items.append(f"{sql_literal(key)}, {pg_json(value)}")
    extra_object = "jsonb_build_object(" + ", ".join(extra_items) + ")"

    statements = [
        "-- Generated by scripts/check-edge-anthropic-oauth-stability.py",
        f"-- edge_id: {edge['edge_id']}",
        f"-- account_name: {account_name}",
        f"-- generated_at: {generated_at}",
        "-- Review before running. This SQL does not contain OAuth access/refresh tokens.",
        "BEGIN;",
        "WITH profile AS (",
        "  INSERT INTO tls_fingerprint_profiles (" + ", ".join(profile_columns) + ", created_at, updated_at)",
        "  VALUES (" + ", ".join(insert_values) + ", NOW(), NOW())",
        "  ON CONFLICT (name) DO UPDATE SET " + ", ".join(conflict_sets),
        "  RETURNING id",
        "), target AS (",
        "  SELECT id FROM accounts",
        f"  WHERE name = {sql_literal(account_name)} AND platform = 'anthropic' AND type = 'oauth' AND deleted_at IS NULL",
        "  ORDER BY id LIMIT 1",
        "), updated AS (",
        "  UPDATE accounts a",
        "  SET",
    ]
    set_lines = [f"      {item}" for item in account_sets]
    set_lines.append(f"      credentials = COALESCE(a.credentials, '{{}}'::jsonb) || {pg_json(credentials)}")
    extra_base = "COALESCE(a.extra, '{}')::jsonb"
    for key in extra_absent:
        extra_base += f" - {sql_literal(str(key))}"
    set_lines.append(f"      extra = {extra_base} || {extra_object}")
    set_lines.append("      updated_at = NOW()")
    statements.append(",\n".join(set_lines))
    statements.extend([
        "  FROM target",
        "  WHERE a.id = target.id",
        "  RETURNING a.id, a.name",
        ")",
        "SELECT * FROM updated;",
        "COMMIT;",
        "",
    ])
    return "\n".join(statements)


def update_stable_list(path: pathlib.Path, data: dict[str, Any], edge_id: str, account_name: str) -> None:
    entries = data.setdefault("stable_accounts", [])
    now = dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat()
    found = False
    for entry in entries:
        if entry.get("edge_id") == edge_id and entry.get("account_name") == account_name:
            entry["updated_at"] = now
            entry.setdefault("source", "manual-confirmed")
            found = True
            break
    if not found:
        entries.append({
            "edge_id": edge_id,
            "account_name": account_name,
            "added_at": now,
            "source": "manual-confirmed",
            "notes": "Added by check-edge-anthropic-oauth-stability.py after operator confirmation."
        })
    entries.sort(key=lambda item: (item.get("edge_id", ""), item.get("account_name", "")))
    write_json(path, data)


def format_diff(diffs: list[dict[str, Any]]) -> str:
    lines = []
    for item in diffs:
        lines.append(f"- {item['path']}: expected={json.dumps(item['expected'], ensure_ascii=False)} actual={json.dumps(item['actual'], ensure_ascii=False)}")
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser(description="Check Edge Anthropic OAuth account stability baseline drift.")
    parser.add_argument("--edge-id", required=True)
    parser.add_argument("--account-name", required=True)
    parser.add_argument("--matrix", default=str(DEFAULT_MATRIX))
    parser.add_argument("--baseline", default=str(DEFAULT_BASELINE))
    parser.add_argument("--instance-id", default="")
    parser.add_argument("--allow-planned", action="store_true")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--emit-sql", default="")
    parser.add_argument("--update-stable-list", action="store_true")
    parser.add_argument("--confirm", default="")
    parser.add_argument("--default-tier", default="")
    args = parser.parse_args()

    if args.json and args.quiet:
        fail("--json and --quiet are mutually exclusive")

    is_edge_all = args.edge_id == "all"
    is_account_all = args.account_name == "all"
    is_batch = is_edge_all or is_account_all

    if args.emit_sql and is_batch:
        fail("--emit-sql only supports single edge + single account (no all)")
    if args.update_stable_list and is_batch:
        fail("--update-stable-list only supports single edge + single account (no all)")
    if args.instance_id and is_edge_all:
        fail("--instance-id cannot be used with --edge-id all")

    default_tier = str(args.default_tier or "").strip().lower()
    matrix_path = pathlib.Path(args.matrix)
    baseline_path = pathlib.Path(args.baseline)
    matrix = load_json(matrix_path)
    baseline = load_json(baseline_path)
    if default_tier and default_tier not in (baseline.get("tiers") or {}):
        fail(f"--default-tier={default_tier} not found in baseline tiers")
    edges, excluded_edges = resolve_edges(matrix, args.edge_id, allow_planned=args.allow_planned)

    if args.update_stable_list:
        if args.confirm != CONFIRM_UPDATE:
            fail(f"--update-stable-list requires --confirm {CONFIRM_UPDATE}")
        update_stable_list(baseline_path, baseline, args.edge_id, args.account_name)
        log(f"updated stable_accounts in {baseline_path}: edge={args.edge_id} account={args.account_name}", quiet=args.quiet)
        return 0

    items: list[dict[str, Any]] = []
    error_count = 0

    for edge in edges:
        try:
            instance_id = resolve_instance_id(edge, args.instance_id)
        except CheckError as exc:
            if not is_batch:
                raise
            items.append({
                "edge": {**edge, "instance_id": None},
                "account_name": args.account_name,
                "status": "error",
                "diff_count": 0,
                "diffs": [],
                "error_message": exc.args[0] if exc.args else "unknown error",
                "sql_path": None,
            })
            error_count += 1
            continue

        if is_account_all:
            try:
                names, list_cmd_id = read_live_account_names(edge, instance_id)
            except CheckError as exc:
                if not is_batch:
                    raise
                items.append({
                    "edge": {**edge, "instance_id": instance_id},
                    "account_name": "all",
                    "status": "error",
                    "diff_count": 0,
                    "diffs": [],
                    "error_message": exc.args[0] if exc.args else "unknown error",
                    "sql_path": None,
                })
                error_count += 1
                continue
            if not names:
                items.append({
                    "edge": {**edge, "instance_id": instance_id},
                    "account_name": "all",
                    "status": "ok",
                    "diff_count": 0,
                    "diffs": [],
                    "ssm_command_id": list_cmd_id,
                    "accounts_found": 0,
                    "sql_path": None,
                })
                continue
            account_names = names
        else:
            account_names = [args.account_name]

        for account_name in account_names:
            try:
                live = read_live_account(edge, instance_id, account_name)
                effective_baseline = resolve_effective_baseline(
                    baseline,
                    live,
                    edge_id=edge["edge_id"],
                    account_name=account_name,
                    default_tier=default_tier,
                )
                diffs = compare_live_to_baseline(live, effective_baseline)
                item = {
                    "edge": {**edge, "instance_id": instance_id},
                    "account_name": account_name,
                    "account_stability_tier": (live.get("account") or {}).get("stability_tier"),
                    "baseline_tier": effective_baseline.get("selected_tier"),
                    "baseline_factor": effective_baseline.get("selected_factor"),
                    "tier_source": effective_baseline.get("tier_source"),
                    "ssm_command_id": live.get("ssm_command_id"),
                    "status": "ok" if not diffs else "drift",
                    "diff_count": len(diffs),
                    "diffs": diffs,
                    "sql_path": args.emit_sql or None,
                }
                if args.emit_sql:
                    sql_path = pathlib.Path(args.emit_sql)
                    sql_path.write_text(generate_sql(edge, account_name, effective_baseline), encoding="utf-8")
                items.append(item)
            except CheckError as exc:
                if not is_batch:
                    raise
                items.append({
                    "edge": {**edge, "instance_id": instance_id},
                    "account_name": account_name,
                    "status": "error",
                    "diff_count": 0,
                    "diffs": [],
                    "error_message": exc.args[0] if exc.args else "unknown error",
                    "sql_path": None,
                })
                error_count += 1

    drift_count = sum(1 for item in items if item.get("status") == "drift")
    ok_count = sum(1 for item in items if item.get("status") == "ok")
    has_failures = drift_count > 0 or error_count > 0

    if not is_batch and len(items) == 1:
        result = items[0]
        if args.json:
            print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
        elif not args.quiet:
            edge = result["edge"]
            print(f"edge_id={edge['edge_id']}")
            print(f"account_name={result['account_name']}")
            print(f"region={edge['region']}")
            print(f"instance_id={edge['instance_id']}")
            print(f"ssm_command_id={result.get('ssm_command_id')}")
            print(f"status={result['status']}")
            if result.get("baseline_tier"):
                print(f"baseline_tier={result['baseline_tier']}")
            print(f"diff_count={result['diff_count']}")
            if result["diffs"]:
                print("\nDiff:")
                print(format_diff(result["diffs"]))
            if result.get("sql_path"):
                print(f"\nsql_path={result['sql_path']}")
        return 1 if has_failures else 0

    summary = {
        "edge_total": len(edges),
        "excluded_edge_total": len(excluded_edges),
        "account_result_total": len(items),
        "ok_count": ok_count,
        "drift_count": drift_count,
        "error_count": error_count,
    }
    batch_result = {
        "mode": "batch",
        "selector": {
            "edge_id": args.edge_id,
            "account_name": args.account_name,
            "allow_planned": args.allow_planned,
        },
        "summary": summary,
        "excluded_edges": excluded_edges,
        "items": items,
    }

    if args.json:
        print(json.dumps(batch_result, ensure_ascii=False, indent=2, sort_keys=True))
    elif not args.quiet:
        print(f"mode=batch")
        print(f"edge_selector={args.edge_id}")
        print(f"account_selector={args.account_name}")
        print(f"edge_total={summary['edge_total']}")
        print(f"excluded_edge_total={summary['excluded_edge_total']}")
        print(f"account_result_total={summary['account_result_total']}")
        print(f"ok_count={summary['ok_count']}")
        print(f"drift_count={summary['drift_count']}")
        print(f"error_count={summary['error_count']}")
        if drift_count:
            print("\nDrift items:")
            for item in items:
                if item.get("status") != "drift":
                    continue
                edge = item["edge"]
                print(f"- {edge['edge_id']}/{item['account_name']} diff_count={item['diff_count']}")
        if error_count:
            print("\nError items:")
            for item in items:
                if item.get("status") != "error":
                    continue
                edge = item["edge"]
                print(f"- {edge['edge_id']}/{item['account_name']}: {item.get('error_message', 'unknown error')}")

    return 1 if has_failures else 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except CheckError:
        raise SystemExit(2)
