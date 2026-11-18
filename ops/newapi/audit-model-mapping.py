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

New mapping floors converge through a reviewed bundle and
``ops/pricing/modelops.py activate``. Emergency removal only is available through
``ops/newapi/apply-model-mapping-live.py remove-live``.
"""
from __future__ import annotations

import argparse
import base64
import importlib.util
import json
import pathlib
import subprocess
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
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


_SSM = _load("ops/stage0/ssm_execution.py", "tk_ssm_execution_audit_mapping")


def ssm_run_sql(region: str, instance_id: str, sql: str, comment: str) -> str:
    del region  # TAT path uses PROD_SSM_NODE; SSM break-glass still needs instance_id pin.
    remote = 'sudo docker exec -i "$TK_PG" psql -U tokenkey -d tokenkey -t -A -v ON_ERROR_STOP=1'
    command = f"set -euo pipefail\n{_SSM.data_container_preamble()}{remote} <<'SQL'\n{sql}\nSQL"
    shell_b64 = base64.b64encode(command.encode()).decode()
    try:
        out = _SSM.run_shell_b64(instance_id, shell_b64, comment)
    except SystemExit as exc:
        fail(f"remote sql failed ({comment}): {exc}")
    return out.strip()


def main() -> int:
    ap = argparse.ArgumentParser(description="audit newapi account model_mapping invariant (prod-only)")
    ap.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    args = ap.parse_args()

    inst, region = _SSM.resolve_prod_identity()
    out = ssm_run_sql(region, inst, NEWAPI_ACCOUNTS_SQL, "newapi model_mapping audit")
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
