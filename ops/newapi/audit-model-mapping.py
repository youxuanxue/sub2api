#!/usr/bin/env python3
"""TokenKey newapi account model_mapping audit (read-only, prod-only).

The non-symmetric model_mapping invariant (docs/approved/universal-key-routing.md):
``platform=newapi`` is a **multi-vendor** platform (deepseek / Qwen / volcengine /
google-vertex / grok / … share one platform, split by ``channel_type``). An empty
``credentials.model_mapping`` on a newapi account is a configuration gap: the
universal-key resolver cannot tell which models that account serves, so it routes
by platform name and mis-routes (the grok account #65 with no mapping was exactly
this). Native single-vendor platforms (anthropic/openai/gemini/antigravity) keep
"empty = pass-through" and are NOT subject to this invariant.

Three enforcement gates: (1) write-time validation in AccountService
(ErrNewapiModelMappingRequired), (2) route-time reject in the resolver
(universal_routing_tk_serving.go), (3) **this** live audit for pre-existing rows
the write-time gate can't see. newapi accounts live only on the prod control-plane
DB (edges are anthropic relays), so this is prod-only.

A **violation** is any non-deleted ``platform=newapi`` account whose
``credentials.model_mapping`` is absent or has zero keys.

Exit codes (mirror the post-release checks): ``0`` = no violations (green);
``1`` = violations found (yellow); ``2`` = could not run (yellow). Read-only —
never mutates. stdlib-only; reuses ops/stage0 SSM identity helper.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import subprocess
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
PROD_TARGET = {"region": "us-east-1", "stack": "tokenkey-prod-stage0", "label": "prod"}

# Read-only snapshot: each newapi account + its model_mapping key count.
NEWAPI_ACCOUNTS_SQL = (
    "SELECT COALESCE(json_agg(json_build_object("
    "'id', a.id, 'name', a.name, 'channel_type', a.channel_type, "
    "'status', a.status, 'schedulable', a.schedulable, "
    "'mapping_keys', COALESCE((SELECT count(*) FROM jsonb_object_keys("
    "COALESCE(a.credentials->'model_mapping', '{}'::jsonb))), 0)"
    "))::text, '[]') "
    "FROM accounts a WHERE a.platform = 'newapi' AND a.deleted_at IS NULL;"
)

# ops-sql-coverage gate (scripts/checks/ops-sql-coverage.py): every SQL-shaped
# symbol must be enumerated for the real-Postgres self-check or exempted.
SELF_CHECK_EXEMPT: dict[str, str] = {
    "ssm_run_sql": "executes SQL over SSM, does not build it",
}


def iter_self_check_sql() -> list[tuple[str, str]]:
    """(label, rendered_sql) for the ops-sql-coverage real-Postgres self-check."""
    return [
        ("NEWAPI_ACCOUNTS_SQL", NEWAPI_ACCOUNTS_SQL),
    ]


def fail(msg: str) -> None:
    print(f"audit-model-mapping: {msg}", file=sys.stderr)
    sys.exit(2)


def _load(rel: str, name: str):
    spec = importlib.util.spec_from_file_location(name, REPO_ROOT / rel)
    if spec is None or spec.loader is None:
        fail(f"cannot load {rel}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules.setdefault(name, mod)
    spec.loader.exec_module(mod)  # type: ignore[union-attr]
    return mod


_SSM = _load("ops/stage0/edge_ssm_execution.py", "tk_edge_ssm_execution")


def ssm_run_sql(region: str, instance_id: str, sql: str, comment: str) -> str:
    remote = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -t -A -v ON_ERROR_STOP=1"
    command = f"set -euo pipefail\n{remote} <<'SQL'\n{sql}\nSQL"
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            ["aws", "ssm", "send-command", "--region", region,
             "--instance-ids", instance_id, "--document-name", "AWS-RunShellScript",
             "--comment", comment, "--parameters", params,
             "--query", "Command.CommandId", "--output", "text"],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"ssm send-command failed ({comment}): {e}")
    subprocess.run(
        ["aws", "ssm", "wait", "command-executed", "--region", region,
         "--command-id", cid, "--instance-id", instance_id],
        check=False,
    )
    inv = json.loads(
        subprocess.check_output(
            ["aws", "ssm", "get-command-invocation", "--region", region,
             "--command-id", cid, "--instance-id", instance_id, "--output", "json"],
            text=True,
        )
    )
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:1200]
        fail(f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')} ({comment})\n  stderr: {err}")
    return (inv.get("StandardOutputContent") or "").strip()


def main() -> int:
    ap = argparse.ArgumentParser(description="audit newapi account model_mapping invariant (prod-only)")
    ap.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    args = ap.parse_args()

    inst = _SSM.cfn_resolve_instance_id(PROD_TARGET["region"], PROD_TARGET["stack"])
    out = ssm_run_sql(PROD_TARGET["region"], inst, NEWAPI_ACCOUNTS_SQL, "newapi model_mapping audit")
    try:
        accounts = json.loads(out) if out else []
    except json.JSONDecodeError:
        fail(f"unexpected psql output: {out[:300]}")

    violations = [a for a in accounts if int(a.get("mapping_keys", 0)) == 0]
    result = {
        "target": "prod",
        "newapi_account_count": len(accounts),
        "violation_count": len(violations),
        "violations": [
            {"id": a["id"], "name": a["name"], "channel_type": a.get("channel_type"),
             "status": a.get("status"), "schedulable": a.get("schedulable")}
            for a in violations
        ],
    }

    if args.json:
        print(json.dumps(result, ensure_ascii=False, indent=2))
    else:
        print(f"newapi accounts: {len(accounts)}  violations (empty model_mapping): {len(violations)}")
        for a in violations:
            print(f"  VIOLATION account={a['id']} ({a['name']}) ct={a.get('channel_type')} "
                  f"status={a.get('status')} schedulable={a.get('schedulable')}")
        if not violations:
            print("  OK: every newapi account declares a non-empty model_mapping")

    return 1 if violations else 0


if __name__ == "__main__":
    sys.exit(main())
