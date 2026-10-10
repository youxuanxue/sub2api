#!/usr/bin/env python3
"""migrate-edge-accounts.py — copy specific accounts (with all credentials) + their
groups from one Stage0 edge to another, preserving the live credential blobs that
cannot be re-entered through the admin UI (e.g. kiro OAuth grants).

Secret hygiene: account credentials are plaintext JSONB in the `accounts` table and
are portable across hosts (no per-instance encryption — see internal/repository/
account_repo.go normalizeJSONMap; SecretEncryptor only covers TOTP/channel/backup).
This tool moves them server->S3->server and through the operator's local .cache
(gitignored). It NEVER prints credential values to stdout (which would land in an
agent transcript). Dry-run summaries show credential KEY names only.

Scheduler visibility: a raw SQL insert bypasses both the synchronous Redis snapshot
write and the scheduler_outbox enqueue done by the repository layer. We re-trigger a
full snapshot rebuild by inserting one `full_rebuild` scheduler_outbox row (handled by
SchedulerSnapshotService.triggerFullRebuild); the gateway also full-rebuilds every
gateway.scheduling.full_rebuild_interval_seconds (default 300s) as a backstop.

Flow (run as discrete, reviewable steps):
  extract  --from edge:us1 --account-ids 5,6,7         # us1 -> S3 -> local .cache (data + live column types)
  extract  --from edge:us4 --all --include-api-keys    # full edge clone (accounts+groups+keys+PEC)
  build    --rename kiro-us1-real=kiro-us6-real \
           --rename-group kiro-us1=kiro-us6            # local: generate migrate.sql, print sanitized summary
  build    --preserve-schedulable --replace-target     # full clone: keep schedulable; wipe target rows first
  load     --to edge:us6 [--execute]                   # local migrate.sql -> S3 -> us6 psql (dry-run unless --execute)
  load     --to hetzner:us4 --execute                  # Hetzner Hybrid mi-* via SSM param

Full clone also copies protocol_endpoint_capabilities by capability_key and relinks
accounts (PEC ids are host-local; copying bare ids would leave support_error).

Helper write-ops (used during smoke + teardown; small UPDATEs, no secrets):
  set-schedulable --to edge:us6 --account-name kiro-us6-real --value true|false
  soft-delete     --from edge:us1 --account-ids 5,6,7 [--execute]

All write subcommands default to dry-run; pass --execute to apply.
"""
from __future__ import annotations

import argparse
import base64
import gzip
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
STATE_DIR = REPO_ROOT / ".cache" / "migrate-edge-accounts"
S3_BUCKET = os.environ.get("SSM_OUTPUT_S3_BUCKET", "layer-zip-repro-682751977094-us-east-1")
S3_REGION = os.environ.get("SSM_OUTPUT_S3_REGION", "us-east-1")
S3_PREFIX = "tokenkey/migrate-edge-accounts"
PRESIGN_TTL = int(os.environ.get("PRESIGN_TTL_SEC", "3600"))
SSM_WAIT_MAX = int(os.environ.get("AWS_SSM_WAIT_MAX", "600"))

PG = "docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t"

# Columns never copied verbatim (identity / lifecycle), regardless of table.
SKIP_ALWAYS = {"id", "created_at", "updated_at", "deleted_at"}
# Account columns reset to a clean/paused state on the target.
ACCOUNT_RESET_NULL = {
    "error_message", "last_used_at", "rate_limited_at", "rate_limit_reset_at",
    "overload_until", "session_window_start", "session_window_end",
    "session_window_status", "temp_unschedulable_until", "temp_unschedulable_reason",
    "proxy_id",  # proxy config is host-specific
    "tier_id",   # FK into the target host's account_tiers; not portable
    # PEC rows are host-local; empty staging DBs have no matching ids.
    "protocol_endpoint_capability_id",
}


def log(msg: str) -> None:
    print(f"[migrate-edge-accounts] {msg}", file=sys.stderr)


def die(msg: str) -> None:
    log(f"error: {msg}")
    sys.exit(1)


def run(cmd: list[str], **kw) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, check=True, text=True, capture_output=True, **kw)


def resolve_edge(target: str) -> tuple[str, str]:
    """target = 'edge:<id>' | 'hetzner:<id>' | 'prod' -> (region, instance_id).

    ``edge:`` resolves Lightsail (or auto-route). ``hetzner:`` reads
    ``/tokenkey/hetzner/<id>/ssm_managed_instance_id`` in eu-west-2 so staging
    Hetzner hosts can be load targets while Lightsail remains formal.
    """
    if target == "prod":
        out = run([
            "aws", "cloudformation", "describe-stacks", "--region", "us-east-1",
            "--stack-name", "tokenkey-prod-stage0",
            "--query", "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue",
            "--output", "text",
        ]).stdout.strip()
        return "us-east-1", out
    if target.startswith("hetzner:"):
        edge_id = target.split(":", 1)[1]
        if not edge_id:
            die("hetzner: requires an edge id")
        region = "eu-west-2"
        name = f"/tokenkey/hetzner/{edge_id}/ssm_managed_instance_id"
        out = run([
            "aws", "ssm", "get-parameter", "--region", region,
            "--name", name, "--query", "Parameter.Value", "--output", "text",
        ]).stdout.strip()
        if not out or out == "None":
            die(f"no Hetzner managed instance for {edge_id} ({name})")
        return region, out
    if not target.startswith("edge:"):
        die("--from/--to must be 'edge:<id>', 'hetzner:<id>', or 'prod'")
    edge_id = target.split(":", 1)[1]
    out = run([
        sys.executable, str(REPO_ROOT / "ops/stage0/edge_ssm_execution.py"),
        "--repo-root", str(REPO_ROOT), "--edge-id", edge_id, "--format", "json",
    ]).stdout
    obj = json.loads(out)
    return obj["region"], obj["instance_id"]


def ssm_run(region: str, instance_id: str, commands: list[str], comment: str,
            want_stdout: bool = True) -> str:
    # Hetzner Hybrid SSM uses /bin/sh (dash). Ship a base64 script and run under
    # bash so newlines / `set -o pipefail` survive the SSM parameter envelope.
    body = "\n".join(commands)
    b64 = base64.b64encode(body.encode()).decode()
    outer = f"echo {b64} | base64 -d > /tmp/tk-migrate-ssm.sh && bash /tmp/tk-migrate-ssm.sh"
    params = json.dumps({"commands": [outer]})
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        f.write(params)
        pfile = f.name
    try:
        cmd_id = run([
            "aws", "ssm", "send-command", "--region", region,
            "--instance-ids", instance_id, "--document-name", "AWS-RunShellScript",
            "--comment", comment, "--timeout-seconds", str(SSM_WAIT_MAX),
            "--parameters", f"file://{pfile}",
            "--query", "Command.CommandId", "--output", "text",
        ]).stdout.strip()
    finally:
        os.unlink(pfile)
    deadline = time.time() + SSM_WAIT_MAX
    status = "InProgress"
    while time.time() < deadline:
        try:
            status = run([
                "aws", "ssm", "get-command-invocation", "--region", region,
                "--command-id", cmd_id, "--instance-id", instance_id,
                "--query", "Status", "--output", "text",
            ]).stdout.strip()
        except subprocess.CalledProcessError:
            status = "InProgress"
        if status in ("Success", "Failed", "TimedOut", "Cancelled"):
            break
        time.sleep(6)
    inv = run([
        "aws", "ssm", "get-command-invocation", "--region", region,
        "--command-id", cmd_id, "--instance-id", instance_id,
        "--query", "{stdout:StandardOutputContent,stderr:StandardErrorContent}",
        "--output", "json",
    ]).stdout
    res = json.loads(inv)
    if status != "Success":
        log(f"remote status={status}")
        log(f"remote stderr: {res.get('stderr', '')[:2000]}")
        die(f"SSM command failed on {instance_id}")
    return res.get("stdout", "") if want_stdout else ""


def presign(method: str, key: str) -> str:
    """Generate a presigned URL using a throwaway boto3 venv (matches
    fetch-gateway-debug-log.sh). method = 'put_object' | 'get_object'."""
    scratch = tempfile.mkdtemp()
    try:
        venv = os.path.join(scratch, "venv")
        run([sys.executable, "-m", "venv", venv])
        pip = os.path.join(venv, "bin", "pip")
        py = os.path.join(venv, "bin", "python")
        run([pip, "install", "-q", "boto3"])
        code = (
            "import boto3,os;"
            "print(boto3.client('s3',region_name=os.environ['R'])"
            ".generate_presigned_url(os.environ['M'],"
            "Params={'Bucket':os.environ['B'],'Key':os.environ['K']},"
            "ExpiresIn=int(os.environ['E'])),end='')"
        )
        env = {**os.environ, "R": S3_REGION, "M": method, "B": S3_BUCKET,
               "K": key, "E": str(PRESIGN_TTL)}
        return run([py, "-c", code], env=env).stdout
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


# ---------------------------------------------------------------------------
# extract
# ---------------------------------------------------------------------------
def cmd_extract(args: argparse.Namespace) -> None:
    region, iid = resolve_edge(args.from_target)
    include_keys = bool(getattr(args, "include_api_keys", False))
    extract_all = bool(getattr(args, "all", False))
    if extract_all:
        if args.account_ids:
            die("--all is mutually exclusive with --account-ids")
        acct_filter = "a.deleted_at IS NULL"
        id_list = "ALL"
        # Full clone: every live group (api_keys.group_id may reference groups
        # with no account_groups binding).
        groups_sql = (
            "(SELECT json_agg(row_to_json(g)) FROM groups g WHERE g.deleted_at IS NULL)"
        )
        bindings_sql = (
            "(SELECT json_agg(row_to_json(b)) FROM account_groups b "
            "WHERE b.account_id IN (SELECT id FROM accounts WHERE deleted_at IS NULL))"
        )
    else:
        acct_ids = [int(x) for x in (args.account_ids or "").split(",") if x.strip()]
        if not acct_ids:
            die("--account-ids required (or pass --all)")
        id_list = ",".join(str(i) for i in acct_ids)
        acct_filter = f"a.id IN ({id_list}) AND a.deleted_at IS NULL"
        groups_sql = (
            f"(SELECT json_agg(row_to_json(g)) FROM groups g WHERE g.id IN "
            f"(SELECT DISTINCT group_id FROM account_groups WHERE account_id IN ({id_list})) "
            f"AND g.deleted_at IS NULL)"
        )
        bindings_sql = (
            f"(SELECT json_agg(row_to_json(b)) FROM account_groups b "
            f"WHERE b.account_id IN ({id_list}))"
        )

    # Server-side: build one JSON doc {schema, groups, accounts, bindings[, api_keys]}
    # into a host file (NOT stdout), gzip, presigned-PUT to S3.
    stamp = run(["date", "-u", "+%Y%m%dT%H%M%SZ"]).stdout.strip()
    label = args.from_target.replace(":", "-")
    key = f"{S3_PREFIX}/{label}/{stamp}.json.gz"
    put_url = presign("put_object", key)
    put_b64 = base64.b64encode(put_url.encode()).decode()

    schema_parts = [
        "'accounts', (SELECT json_agg(json_build_object('column_name',column_name,'data_type',data_type) ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='accounts')",
        "'groups',   (SELECT json_agg(json_build_object('column_name',column_name,'data_type',data_type) ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='groups')",
    ]
    if include_keys:
        schema_parts.append(
            "'api_keys', (SELECT json_agg(json_build_object('column_name',column_name,'data_type',data_type) ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='api_keys')"
        )
    # Full clone needs PEC rows; selective migrates still include referenced PECs so
    # target accounts do not land with NULL capability links (support_error).
    schema_parts.append(
        "'protocol_endpoint_capabilities', (SELECT json_agg(json_build_object('column_name',column_name,'data_type',data_type) ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='protocol_endpoint_capabilities')"
    )
    if extract_all:
        pecs_sql = (
            "(SELECT json_agg(row_to_json(c) ORDER BY c.id) FROM protocol_endpoint_capabilities c)"
        )
    else:
        pecs_sql = (
            "(SELECT json_agg(row_to_json(c) ORDER BY c.id) FROM protocol_endpoint_capabilities c "
            f"WHERE c.id IN (SELECT DISTINCT protocol_endpoint_capability_id FROM accounts a "
            f"WHERE {acct_filter} AND a.protocol_endpoint_capability_id IS NOT NULL))"
        )
    payload_parts = [
        f"'schema', json_build_object({', '.join(schema_parts)})",
        f"'accounts', (SELECT json_agg(row_to_json(a)) FROM accounts a WHERE {acct_filter})",
        f"'bindings', {bindings_sql}",
        f"'groups',   {groups_sql}",
        f"'protocol_endpoint_capabilities', {pecs_sql}",
        # name→capability_key so build can relink after upsert (ids are host-local).
        "'pec_links', (SELECT json_agg(json_build_object("
        "'account_name', a.name, 'capability_key', c.capability_key) ORDER BY a.name) "
        "FROM accounts a JOIN protocol_endpoint_capabilities c "
        "ON c.id = a.protocol_endpoint_capability_id "
        f"WHERE {acct_filter} AND a.protocol_endpoint_capability_id IS NOT NULL)",
    ]
    if include_keys:
        payload_parts.append(
            "'api_keys', (SELECT json_agg(row_to_json(k)) FROM api_keys k WHERE k.deleted_at IS NULL)"
        )
    sql = "SELECT json_build_object(" + ", ".join(payload_parts) + ")"
    remote = f"/tmp/migrate-extract-{stamp}.json"
    commands = [
        "set -euo pipefail",
        f"{PG} -c {shq(sql)} > {remote}",
        f"test -s {remote}",
        f"gzip -f {remote}",
        f"echo {put_b64} | base64 -d > /tmp/migrate-put.url",
        f"curl -fS --max-time 600 -X PUT --upload-file {remote}.gz \"$(cat /tmp/migrate-put.url)\"",
        "rm -f /tmp/migrate-put.url",
        f"rm -f {remote}.gz",
        "echo EXTRACT_OK",
    ]
    log(f"extract from={args.from_target} region={region} accounts={id_list} "
        f"include_api_keys={include_keys}")
    out = ssm_run(region, iid, commands, f"migrate extract {label}")
    if "EXTRACT_OK" not in out:
        die(f"extract did not confirm OK; stdout tail: {out[-500:]}")

    STATE_DIR.mkdir(parents=True, exist_ok=True)
    local_gz = STATE_DIR / "payload.json.gz"
    local_json = STATE_DIR / "payload.json"
    run(["aws", "s3", "cp", "--region", S3_REGION, f"s3://{S3_BUCKET}/{key}", str(local_gz)])
    with gzip.open(local_gz, "rb") as fi, open(local_json, "wb") as fo:
        fo.write(fi.read())
    local_gz.unlink(missing_ok=True)
    run(["aws", "s3", "rm", "--region", S3_REGION, f"s3://{S3_BUCKET}/{key}"])

    payload = json.loads(local_json.read_text())
    _print_payload_summary(payload)
    log(f"saved {local_json} (gitignored). next: build")


def _print_payload_summary(payload: dict) -> None:
    print("=== extracted (sanitized; credential/key values NOT shown) ===")
    for g in payload.get("groups") or []:
        print(f"  group  id={g['id']} name={g['name']!r} platform={g['platform']} "
              f"allow_image_generation={g.get('allow_image_generation')} "
              f"claude_code_only={g.get('claude_code_only')} "
              f"fallback_group_id={g.get('fallback_group_id')}")
    for a in payload.get("accounts") or []:
        cred_keys = sorted((a.get("credentials") or {}).keys())
        print(f"  account id={a['id']} name={a['name']!r} platform={a['platform']} "
              f"type={a['type']} channel_type={a.get('channel_type')} "
              f"schedulable={a.get('schedulable')} credential_keys={cred_keys}")
    for b in payload.get("bindings") or []:
        print(f"  binding account_id={b['account_id']} -> group_id={b['group_id']} "
              f"priority={b['priority']}")
    keys = payload.get("api_keys") or []
    if keys:
        print(f"  api_keys: {len(keys)} (names/status only)")
        for k in keys:
            print(f"    key id={k['id']} name={k.get('name')!r} status={k.get('status')} "
                  f"group_id={k.get('group_id')}")
    pecs = payload.get("protocol_endpoint_capabilities") or []
    links = payload.get("pec_links") or []
    if pecs or links:
        print(f"  pecs: {len(pecs)} capability rows; links: {len(links)} accounts")
        for p in pecs:
            ident = p.get("identity") or {}
            print(f"    pec platform={ident.get('platform')!r} "
                  f"profile={ident.get('endpoint_profile')!r} "
                  f"protocols={p.get('supported_protocols')}")


# ---------------------------------------------------------------------------
# build
# ---------------------------------------------------------------------------
def _col_expr(var: str, col: str, dtype: str) -> str:
    if dtype in ("jsonb", "json"):
        return f"({var}->'{col}')"
    base = f"({var}->>'{col}')"
    if dtype == "boolean":
        return f"{base}::boolean"
    if dtype in ("bigint",):
        return f"{base}::bigint"
    if dtype in ("integer", "smallint"):
        return f"{base}::integer"
    if dtype in ("numeric", "double precision", "real"):
        return f"{base}::numeric"
    if dtype.startswith("timestamp"):
        return f"{base}::timestamptz"
    return base  # character varying / text


def _parse_renames(items: list[str]) -> dict[str, str]:
    out: dict[str, str] = {}
    for it in items or []:
        if "=" not in it:
            die(f"rename must be old=new, got {it!r}")
        old, new = it.split("=", 1)
        out[old.strip()] = new.strip()
    return out


def cmd_build(args: argparse.Namespace) -> None:
    local_json = STATE_DIR / "payload.json"
    if not local_json.exists():
        die("no payload.json; run extract first")
    payload = json.loads(local_json.read_text())
    acct_renames = _parse_renames(args.rename)
    group_renames = _parse_renames(args.rename_group)
    preserve_sched = bool(getattr(args, "preserve_schedulable", False))
    replace_target = bool(getattr(args, "replace_target", False))
    api_keys = payload.get("api_keys") or []
    if api_keys and "api_keys" not in (payload.get("schema") or {}):
        die("payload has api_keys rows but schema.api_keys missing; re-extract with --include-api-keys")

    acct_types = {c["column_name"]: c["data_type"] for c in payload["schema"]["accounts"]}
    group_types = {c["column_name"]: c["data_type"] for c in payload["schema"]["groups"]}
    key_types = {
        c["column_name"]: c["data_type"]
        for c in (payload.get("schema") or {}).get("api_keys") or []
    }

    g_insert_cols = [c["column_name"] for c in payload["schema"]["groups"]
                     if c["column_name"] not in SKIP_ALWAYS]

    # Each row is loaded into a single shared `v jsonb` variable, then inserted with
    # per-column type-aware extraction; RETURNING captures the new id into a temp map
    # so bindings + fallback ids can be remapped to the target host's id space.
    sql_parts = ["\\set ON_ERROR_STOP on", "DO $mig$", "DECLARE",
                 "  new_gid bigint;", "  new_aid bigint;", "  new_kid bigint;",
                 "  admin_uid bigint;", "  v jsonb;",
                 "BEGIN",
                 "  CREATE TEMP TABLE _gmap(old_id bigint primary key, new_id bigint) ON COMMIT DROP;",
                 "  CREATE TEMP TABLE _amap(old_id bigint primary key, new_id bigint) ON COMMIT DROP;"]

    if replace_target:
        # Soft-delete existing target fleet so a full clone does not dual-schedule
        # probe leftovers alongside migrated accounts/keys.
        sql_parts.append(
            "  UPDATE api_keys SET deleted_at=now(), status='disabled', updated_at=now() "
            "WHERE deleted_at IS NULL;"
        )
        sql_parts.append(
            "  UPDATE accounts SET deleted_at=now(), schedulable=false, updated_at=now() "
            "WHERE deleted_at IS NULL;"
        )
        sql_parts.append(
            "  DELETE FROM account_groups;"
        )
        sql_parts.append(
            "  UPDATE groups SET deleted_at=now(), updated_at=now() WHERE deleted_at IS NULL;"
        )

    for g in payload.get("groups") or []:
        cols_sql, vals_sql = [], []
        for col in g_insert_cols:
            cols_sql.append(col)
            if col == "name":
                vals_sql.append(sql_lit(group_renames.get(g["name"], g["name"])))
            elif col in ("fallback_group_id", "fallback_group_id_on_invalid_request"):
                vals_sql.append("NULL")
            elif col in ("created_at", "updated_at"):
                vals_sql.append("now()")
            else:
                vals_sql.append(_col_expr("v", col, group_types[col]))
        sql_parts.append(f"  v := {sql_lit(json.dumps(g))}::jsonb;")
        sql_parts.append(f"  INSERT INTO groups ({', '.join(cols_sql)})")
        sql_parts.append(f"    VALUES ({', '.join(vals_sql)}) RETURNING id INTO new_gid;")
        sql_parts.append(f"  INSERT INTO _gmap VALUES ({int(g['id'])}, new_gid);")

    # remap fallback ids (only when the referenced group was also copied)
    for g in payload.get("groups") or []:
        for fb in ("fallback_group_id", "fallback_group_id_on_invalid_request"):
            if g.get(fb) is not None:
                sql_parts.append(
                    f"  UPDATE groups SET {fb} = (SELECT new_id FROM _gmap WHERE old_id={int(g[fb])}) "
                    f"WHERE id=(SELECT new_id FROM _gmap WHERE old_id={int(g['id'])}) "
                    f"AND EXISTS (SELECT 1 FROM _gmap WHERE old_id={int(g[fb])});")

    # --- accounts ---
    a_insert_cols = [c["column_name"] for c in payload["schema"]["accounts"]
                     if c["column_name"] not in SKIP_ALWAYS]
    for a in payload.get("accounts") or []:
        cols_sql, vals_sql = [], []
        for col in a_insert_cols:
            cols_sql.append(col)
            if col == "name":
                vals_sql.append(sql_lit(acct_renames.get(a["name"], a["name"])))
            elif col == "status":
                # Preserve active/error from source when doing a full identical clone;
                # selective migrates still force active for smoke enablement.
                if preserve_sched:
                    vals_sql.append(_col_expr("v", col, acct_types[col]))
                else:
                    vals_sql.append("'active'")
            elif col == "schedulable":
                if preserve_sched:
                    vals_sql.append(_col_expr("v", col, acct_types[col]))
                else:
                    vals_sql.append("false")  # paused on target per selective migration
            elif col in ("created_at", "updated_at"):
                vals_sql.append("now()")
            elif col in ACCOUNT_RESET_NULL:
                vals_sql.append("NULL")
            else:
                vals_sql.append(_col_expr("v", col, acct_types[col]))
        sql_parts.append(f"  v := {sql_lit(json.dumps(a))}::jsonb;")
        sql_parts.append(f"  INSERT INTO accounts ({', '.join(cols_sql)})")
        sql_parts.append(f"    VALUES ({', '.join(vals_sql)}) RETURNING id INTO new_aid;")
        sql_parts.append(f"  INSERT INTO _amap VALUES ({int(a['id'])}, new_aid);")

    # --- bindings ---
    for b in payload.get("bindings") or []:
        sql_parts.append(
            "  INSERT INTO account_groups (account_id, group_id, priority, created_at) "
            f"VALUES ((SELECT new_id FROM _amap WHERE old_id={int(b['account_id'])}), "
            f"(SELECT new_id FROM _gmap WHERE old_id={int(b['group_id'])}), "
            f"{int(b['priority'])}, now());")

    # --- PEC upsert by capability_key, then relink accounts by name ---
    # protocol_endpoint_capability_id is host-local (ACCOUNT_RESET_NULL); without
    # this step, governed platforms return support_error / no_available_accounts.
    pecs = payload.get("protocol_endpoint_capabilities") or []
    pec_links = payload.get("pec_links") or []
    for p in pecs:
        row = {
            "capability_key": p["capability_key"],
            "identity": p.get("identity") or {},
            "supported_protocols": p.get("supported_protocols") or [],
            "probe_evidence": p.get("probe_evidence") or {},
            "revision": p.get("revision") or 1,
            "identity_conflict": bool(p.get("identity_conflict") or False),
        }
        sql_parts.append(f"  v := {sql_lit(json.dumps(row))}::jsonb;")
        sql_parts.append(
            "  INSERT INTO protocol_endpoint_capabilities AS c "
            "(capability_key, identity, supported_protocols, probe_evidence, revision, "
            "identity_conflict, created_at, updated_at) "
            "VALUES ("
            "(v->>'capability_key'), (v->'identity'), "
            "COALESCE(v->'supported_protocols', '[]'::jsonb), "
            "COALESCE(v->'probe_evidence', '{}'::jsonb), "
            "COALESCE((v->>'revision')::int, 1), "
            "COALESCE((v->>'identity_conflict')::boolean, false), "
            "now(), now()) "
            "ON CONFLICT (capability_key) DO UPDATE SET "
            "identity = EXCLUDED.identity, "
            "supported_protocols = EXCLUDED.supported_protocols, "
            "probe_evidence = EXCLUDED.probe_evidence, "
            "revision = EXCLUDED.revision, "
            "identity_conflict = EXCLUDED.identity_conflict, "
            "updated_at = now();"
        )
    for link in pec_links:
        aname = acct_renames.get(link["account_name"], link["account_name"])
        sql_parts.append(
            "  UPDATE accounts SET protocol_endpoint_capability_id = "
            f"(SELECT id FROM protocol_endpoint_capabilities WHERE capability_key = "
            f"{sql_lit(link['capability_key'])}), updated_at = now() "
            f"WHERE name = {sql_lit(aname)} AND deleted_at IS NULL;"
        )

    # --- api_keys (optional): remap user → target admin; group → _gmap ---
    if api_keys:
        sql_parts.append(
            "  SELECT id INTO admin_uid FROM users "
            "WHERE role='admin' AND deleted_at IS NULL ORDER BY id LIMIT 1;"
        )
        sql_parts.append(
            "  IF admin_uid IS NULL THEN "
            "RAISE EXCEPTION 'migrate: no admin user on target for api_keys.user_id'; "
            "END IF;"
        )
        k_insert_cols = [c for c in key_types if c not in SKIP_ALWAYS]
        for k in api_keys:
            # Key material stays inside the jsonb bind only (never RAISE NOTICE / verify).
            cols_sql, vals_sql = [], []
            for col in k_insert_cols:
                cols_sql.append(col)
                if col == "user_id":
                    vals_sql.append("admin_uid")
                elif col == "group_id":
                    if k.get("group_id") is None:
                        vals_sql.append("NULL")
                    else:
                        vals_sql.append(
                            f"(SELECT new_id FROM _gmap WHERE old_id={int(k['group_id'])})"
                        )
                elif col in ("created_at", "updated_at"):
                    vals_sql.append("now()")
                elif col in ("quota_used", "usage_5h", "usage_1d", "usage_7d"):
                    vals_sql.append("0")
                elif col in (
                    "last_used_at", "expires_at",
                    "window_5h_start", "window_1d_start", "window_7d_start",
                ):
                    vals_sql.append("NULL")
                else:
                    vals_sql.append(_col_expr("v", col, key_types[col]))
            sql_parts.append(f"  v := {sql_lit(json.dumps(k))}::jsonb;")
            sql_parts.append(f"  INSERT INTO api_keys ({', '.join(cols_sql)})")
            sql_parts.append(f"    VALUES ({', '.join(vals_sql)}) RETURNING id INTO new_kid;")

    # --- snapshot rebuild trigger ---
    sql_parts.append("  INSERT INTO scheduler_outbox (event_type, payload, created_at) "
                     "VALUES ('full_rebuild', NULL, now());")
    sql_parts.append("  RAISE NOTICE 'migrate: groups=% accounts=% bindings=% keys=%', "
                     "(SELECT count(*) FROM _gmap), (SELECT count(*) FROM _amap), "
                     f"{len(payload.get('bindings') or [])}, {len(api_keys)};")
    sql_parts.append("END")
    sql_parts.append("$mig$;")
    # verification SELECTs appended after the DO block (non-secret)
    sql_parts.append(
        "SELECT 'verify_account' AS k, a.id, a.name, a.platform, a.type, a.status, "
        "a.schedulable, a.channel_type, (a.credentials IS NOT NULL) AS has_creds "
        "FROM accounts a WHERE a.deleted_at IS NULL ORDER BY a.id;")
    if api_keys:
        sql_parts.append(
            "SELECT 'verify_key' AS k, id, name, status, group_id, "
            "(key IS NOT NULL AND length(key)>0) AS has_key "
            "FROM api_keys WHERE deleted_at IS NULL ORDER BY id;"
        )

    out_sql = STATE_DIR / "migrate.sql"
    out_sql.write_text("\n".join(sql_parts) + "\n")

    print("=== build summary (sanitized) ===")
    if replace_target:
        print("  replace_target: soft-delete existing api_keys/accounts/groups on load")
    for g in payload.get("groups") or []:
        nn = group_renames.get(g["name"], g["name"])
        print(f"  group  {g['name']!r} -> {nn!r} (platform={g['platform']})")
    for a in payload.get("accounts") or []:
        nn = acct_renames.get(a["name"], a["name"])
        sched = a.get("schedulable") if preserve_sched else False
        print(f"  account {a['name']!r} -> {nn!r} (platform={a['platform']} type={a['type']} "
              f"channel_type={a.get('channel_type')}) schedulable={sched}")
    if api_keys:
        print(f"  api_keys: {len(api_keys)} (user_id→target admin; group_id remapped)")
        for k in api_keys:
            print(f"    {k.get('name')!r} status={k.get('status')}")
    if pecs or pec_links:
        print(f"  pecs: {len(pecs)} upsert by capability_key; "
              f"relink {len(pec_links)} accounts")
    print(f"  + 1 full_rebuild scheduler_outbox row")
    print(f"  bindings: {len(payload.get('bindings') or [])}")
    log(f"wrote {out_sql}. review, then: load --to edge:<id>|hetzner:<id> [--execute]")


# ---------------------------------------------------------------------------
# load
# ---------------------------------------------------------------------------
def cmd_load(args: argparse.Namespace) -> None:
    out_sql = STATE_DIR / "migrate.sql"
    if not out_sql.exists():
        die("no migrate.sql; run build first")
    if not args.execute:
        print("=== DRY RUN (migrate.sql NOT applied; pass --execute to apply) ===")
        print(out_sql.read_text())
        return
    region, iid = resolve_edge(args.to_target)
    stamp = run(["date", "-u", "+%Y%m%dT%H%M%SZ"]).stdout.strip()
    key = f"{S3_PREFIX}/load/{stamp}.sql.gz"
    gz = STATE_DIR / "migrate.sql.gz"
    with open(out_sql, "rb") as fi, gzip.open(gz, "wb") as fo:
        fo.write(fi.read())
    run(["aws", "s3", "cp", "--region", S3_REGION, str(gz), f"s3://{S3_BUCKET}/{key}"])
    gz.unlink(missing_ok=True)
    get_url = presign("get_object", key)
    get_b64 = base64.b64encode(get_url.encode()).decode()
    remote = f"/tmp/migrate-load-{stamp}.sql"
    commands = [
        "set -euo pipefail",
        f"echo {get_b64} | base64 -d > /tmp/migrate-get.url",
        f"curl -fS --max-time 600 -o {remote}.gz \"$(cat /tmp/migrate-get.url)\"",
        "rm -f /tmp/migrate-get.url",
        f"gunzip -f {remote}.gz",
        f"docker cp {remote} tokenkey-postgres:{remote}",
        f"docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -f {remote}",
        f"docker exec tokenkey-postgres rm -f {remote}",
        f"rm -f {remote}",
        "echo LOAD_OK",
    ]
    log(f"load to={args.to_target} region={region} (executing migrate.sql)")
    out = ssm_run(region, iid, commands, f"migrate load {args.to_target}")
    print(out)
    run(["aws", "s3", "rm", "--region", S3_REGION, f"s3://{S3_BUCKET}/{key}"])
    if "LOAD_OK" not in out:
        die("load did not confirm OK")
    log("load complete. next: verify snapshot + smoke")


# ---------------------------------------------------------------------------
# set-schedulable (smoke temp-enable / restore) + soft-delete (teardown)
# ---------------------------------------------------------------------------
def cmd_set_schedulable(args: argparse.Namespace) -> None:
    region, iid = resolve_edge(args.to_target)
    val = "true" if args.value == "true" else "false"
    name = args.account_name
    sql = (
        f"UPDATE accounts SET schedulable={val}, updated_at=now() "
        f"WHERE name={sql_lit(name)} AND deleted_at IS NULL; "
        "INSERT INTO scheduler_outbox (event_type, payload, created_at) "
        "VALUES ('full_rebuild', NULL, now());"
    )
    if not args.execute:
        print(f"DRY RUN set schedulable={val} for {name!r} on {args.to_target}")
        print(sql)
        return
    verify_sql = (f"SELECT id||' '||name||' schedulable='||schedulable "
                  f"FROM accounts WHERE name={sql_lit(name)} AND deleted_at IS NULL")
    out = ssm_run(region, iid, [
        "set -euo pipefail",
        f"{PG} -c {shq(sql)}",
        f"{PG} -c {shq(verify_sql)}",
        "echo SET_OK",
    ], f"set-schedulable {name}={val}")
    print(out)


def cmd_soft_delete(args: argparse.Namespace) -> None:
    region, iid = resolve_edge(args.from_target)
    acct_ids = [int(x) for x in args.account_ids.split(",") if x.strip()]
    id_list = ",".join(str(i) for i in acct_ids)
    sql = (
        f"UPDATE accounts SET deleted_at=now(), schedulable=false, updated_at=now() "
        f"WHERE id IN ({id_list}) AND deleted_at IS NULL; "
        f"DELETE FROM account_groups WHERE account_id IN ({id_list}); "
        "INSERT INTO scheduler_outbox (event_type, payload, created_at) "
        "VALUES ('full_rebuild', NULL, now());"
    )
    if not args.execute:
        print(f"DRY RUN soft-delete accounts {id_list} on {args.from_target}")
        print(sql)
        return
    verify_sql = (f"SELECT id||' deleted_at='||COALESCE(deleted_at::text,'NULL') "
                  f"FROM accounts WHERE id IN ({id_list})")  # ops-allow-soft-deleted: verifies the soft-delete set deleted_at — must read the now-deleted rows
    out = ssm_run(region, iid, [
        "set -euo pipefail",
        f"{PG} -c {shq(sql)}",
        f"{PG} -c {shq(verify_sql)}",
        "echo DELETE_OK",
    ], f"soft-delete {id_list}")
    print(out)


# ---------------------------------------------------------------------------
# small SQL literal helpers
# ---------------------------------------------------------------------------
def sql_lit(s: str) -> str:
    """Single-quoted SQL string literal with quotes doubled."""
    return "'" + s.replace("'", "''") + "'"


def shq(s: str) -> str:
    """Shell single-quote for embedding inside an SSM command string."""
    return "'" + s.replace("'", "'\\''") + "'"


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)

    pe = sub.add_parser("extract")
    pe.add_argument("--from", dest="from_target", required=True)
    pe.add_argument("--account-ids", default="", help="comma ids; omit when using --all")
    pe.add_argument("--all", action="store_true", help="extract every live account+group")
    pe.add_argument("--include-api-keys", action="store_true",
                    help="also copy api_keys (needed for prod mirror stubs)")
    pe.set_defaults(func=cmd_extract)

    pb = sub.add_parser("build")
    pb.add_argument("--rename", action="append", default=[], help="account oldname=newname")
    pb.add_argument("--rename-group", action="append", default=[], help="group oldname=newname")
    pb.add_argument("--preserve-schedulable", action="store_true",
                    help="keep source status/schedulable (full identical clone)")
    pb.add_argument("--replace-target", action="store_true",
                    help="soft-delete target api_keys/accounts/groups before insert")
    pb.set_defaults(func=cmd_build)

    pl = sub.add_parser("load")
    pl.add_argument("--to", dest="to_target", required=True)
    pl.add_argument("--execute", action="store_true")
    pl.set_defaults(func=cmd_load)

    ps = sub.add_parser("set-schedulable")
    ps.add_argument("--to", dest="to_target", required=True)
    ps.add_argument("--account-name", required=True)
    ps.add_argument("--value", choices=["true", "false"], required=True)
    ps.add_argument("--execute", action="store_true")
    ps.set_defaults(func=cmd_set_schedulable)

    pd = sub.add_parser("soft-delete")
    pd.add_argument("--from", dest="from_target", required=True)
    pd.add_argument("--account-ids", required=True)
    pd.add_argument("--execute", action="store_true")
    pd.set_defaults(func=cmd_soft_delete)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
