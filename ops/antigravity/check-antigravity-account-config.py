#!/usr/bin/env python3
"""TokenKey post-rollout antigravity config check (read-only).

Verifies the gemini-only operator policy (antigravity serves gemini only; claude
routed to anthropic, gpt-oss off antigravity) across all deployable edges + prod,
on BOTH surfaces the backend ``AntigravityConfigReconciler`` self-heals:

  1. **accounts** — every ``platform=antigravity`` account carries a gemini-only
     ``credentials.model_mapping`` (no ``claude-*`` / ``gpt-oss-*`` keys, no
     PR #921 structural-dead Antigravity aliases), AND any active+schedulable
     account is bound to an antigravity group (account_groups).
  2. **groups** — every active ``platform=antigravity`` group carries gemini-only
     ``supported_model_scopes`` (exactly ``[gemini_text, gemini_image]``), so
     ``/antigravity/v1/models`` + the API key usage guide hide claude.

The reconciler self-heals the gemini-only config on every node (boot + tick); the
group binding is a one-time provisioning step the reconciler does NOT heal, so this
tool is the only safety net for it. This tool is the post-rollout *verification*.

A **violation** is any antigravity account whose ``model_mapping`` is null/empty
(an empty map falls back to ``DefaultAntigravityModelMapping``, which still
includes claude + gpt-oss) or contains any ``claude-`` / ``gpt-oss-`` key or
PR #921 structural-dead alias, OR an active+schedulable account with no
antigravity-group binding (account_groups missing → scheduler "No available
accounts" 429: looks ready but silently never serves); OR any active antigravity
group whose ``supported_model_scopes`` is empty (unrestricted → advertises claude)
or not exactly the gemini-only set.

Exit codes (mirrors the anthropic post-release check): ``0`` = all gemini-only
(green); ``1`` = violations found (yellow, non-blocking at rollout); ``2`` = could
not run (yellow). Read-only — never mutates. stdlib-only; reuses the shared
``ops/stage0`` routing + SSM identity helpers (single source for the edge matrix).
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]

# prod is pinned (not an entry in the edge matrix), same as manage-anthropic-config.py.
PROD_TARGET = {"region": "us-east-1", "stack": "tokenkey-prod-stage0", "label": "prod"}

# Read-only snapshot of antigravity accounts and their model_mapping, aggregated
# as one JSON blob per target. `deleted_at IS NULL` is mandatory (soft-delete gate
# + correctness: soft-deleted rows are not served).
ANTIGRAVITY_ACCOUNTS_SQL = (
    "SELECT COALESCE(json_agg(json_build_object("
    "'id', a.id, 'name', a.name, 'model_mapping', a.credentials->'model_mapping', "
    "'status', a.status, 'schedulable', a.schedulable, "
    "'bound', EXISTS(SELECT 1 FROM account_groups ag JOIN groups g ON g.id = ag.group_id "
    "WHERE ag.account_id = a.id AND g.platform = 'antigravity' AND g.deleted_at IS NULL)"
    "))::text, '[]') "
    "FROM accounts a WHERE a.platform = 'antigravity' AND a.deleted_at IS NULL;"
)

# Active antigravity groups + their supported_model_scopes. status='active' mirrors
# the reconciler's ListActiveByPlatform (same口径: only groups the reconciler heals
# are asserted). `deleted_at IS NULL` mandatory (soft-delete gate).
ANTIGRAVITY_GROUPS_SQL = (
    "SELECT COALESCE(json_agg(json_build_object("
    "'id', id, 'name', name, 'scopes', supported_model_scopes))::text, '[]') "
    "FROM groups WHERE platform = 'antigravity' AND status = 'active' AND deleted_at IS NULL;"
)

# Canonical gemini-only group scopes — mirrors domain.GeminiOnlyAntigravityModelScopes
# and the reconciler's antigravityGroupScopesNeedGeminiOnly predicate.
GEMINI_ONLY_SCOPES = {"gemini_text", "gemini_image"}

ANTIGRAVITY_STRUCTURAL_DEAD_MODEL_MAPPING_KEYS = {
    "gemini-2.5-flash-image-preview",
    "gemini-3-flash-preview",
    "gemini-3-pro-high",
    "gemini-3-pro-image-preview",
    "gemini-3-pro-low",
    "gemini-3-pro-preview",
    "gemini-3.1-pro-high",
    "gemini-3.1-pro-preview",
}

# ops-sql-coverage gate: ssm_run_sql ships SQL over SSM, it does not build it.
SELF_CHECK_EXEMPT: dict[str, str] = {
    "ssm_run_sql": "executes SQL over SSM, does not build it",
}


def iter_self_check_sql() -> list[tuple[str, str]]:
    """(label, rendered_sql) for the ops-sql-coverage real-Postgres self-check."""
    return [
        ("ANTIGRAVITY_ACCOUNTS_SQL", ANTIGRAVITY_ACCOUNTS_SQL),
        ("ANTIGRAVITY_GROUPS_SQL", ANTIGRAVITY_GROUPS_SQL),
    ]


def fail(msg: str) -> None:
    print(f"error: {msg}", file=sys.stderr)
    raise SystemExit(2)


def _load(rel: str, name: str):
    spec = importlib.util.spec_from_file_location(name, REPO_ROOT / rel)
    if spec is None or spec.loader is None:
        fail(f"cannot load {rel}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules.setdefault(name, mod)
    spec.loader.exec_module(mod)  # type: ignore[union-attr]
    return mod


# edge_ssm_execution self-bootstraps ops/stage0 onto sys.path for its sibling
# import of edge_routing_matrix, so loading it first is enough; routing is
# boto-free and safe to importlib-load directly for the matrix helpers.
_SSM = _load("ops/stage0/edge_ssm_execution.py", "tk_edge_ssm_execution")
_ROUTING = _load("ops/stage0/edge_routing_matrix.py", "tk_edge_routing_matrix")


def ssm_run_sql(region: str, instance_id: str, sql: str, comment: str) -> str:
    """Pipe SQL via SSM into the target's tokenkey-postgres. Returns stdout."""
    remote = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -t -A -v ON_ERROR_STOP=1"
    command = f"set -euo pipefail\n{remote} <<'SQL'\n{sql}\nSQL"
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            [
                "aws", "ssm", "send-command",
                "--region", region,
                "--instance-ids", instance_id,
                "--document-name", "AWS-RunShellScript",
                "--comment", comment,
                "--parameters", params,
                "--query", "Command.CommandId",
                "--output", "text",
            ],
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


def _account_violation(row: dict) -> str | None:
    """Return a human reason if the account is misconfigured, else None.

    Two independent checks (both reported if both fail):
      1. gemini-only model_mapping (no claude-* / gpt-oss-* keys, non-empty).
      2. group binding — an active+schedulable account MUST be bound to an
         antigravity group via account_groups, else the scheduler finds no
         account for that group and fast-fails every request with
         "No available accounts" 429 (the account looks ready but silently
         never serves). This binding is a provisioning step the reconciler does
         NOT self-heal, so the post-rollout check is the only safety net.
    """
    reasons: list[str] = []

    mm = row.get("model_mapping")
    if not mm or not isinstance(mm, dict):
        reasons.append("empty/missing model_mapping (falls back to default → serves claude/gpt-oss)")
    else:
        leaked = sorted(k for k in mm if k.startswith("claude-") or k.startswith("gpt-oss-"))
        if leaked:
            reasons.append("serves excluded models: " + ", ".join(leaked))
        stale = sorted(k for k in mm if k in ANTIGRAVITY_STRUCTURAL_DEAD_MODEL_MAPPING_KEYS)
        if stale:
            reasons.append("contains structural-dead aliases: " + ", ".join(stale))

    if row.get("status") == "active" and row.get("schedulable") and not row.get("bound"):
        reasons.append("active+schedulable but NOT bound to any antigravity group "
                       "(account_groups missing → scheduler 'No available accounts' 429)")

    return "; ".join(reasons) if reasons else None


def _group_violation(row: dict) -> str | None:
    """Return a human reason if the group scopes are NOT gemini-only, else None."""
    scopes = row.get("scopes")
    if not scopes or not isinstance(scopes, list):
        return "empty/missing supported_model_scopes (unrestricted → advertises claude on /models + usage guide)"
    got = {str(s).strip() for s in scopes}
    if got == GEMINI_ONLY_SCOPES:
        return None
    parts = []
    extra = sorted(got - GEMINI_ONLY_SCOPES)
    missing = sorted(GEMINI_ONLY_SCOPES - got)
    if extra:
        parts.append("unexpected: " + ", ".join(extra))
    if missing:
        parts.append("missing: " + ", ".join(missing))
    return "scopes not gemini-only (" + "; ".join(parts) + ")"


def _parse_rows(label: str, out: str) -> list:
    try:
        return json.loads(out or "[]")
    except json.JSONDecodeError:
        fail(f"{label}: could not parse antigravity snapshot JSON: {out[:300]!r}")
        return []  # unreachable (fail raises)


def _check_target(label: str, region: str, instance_id: str) -> list[dict]:
    bad: list[dict] = []
    acc_out = ssm_run_sql(region, instance_id, ANTIGRAVITY_ACCOUNTS_SQL, f"antigravity-account-check {label}")
    for r in _parse_rows(label, acc_out):
        reason = _account_violation(r)
        if reason:
            bad.append({"target": label, "kind": "account", "id": r.get("id"), "name": r.get("name"), "reason": reason})
    grp_out = ssm_run_sql(region, instance_id, ANTIGRAVITY_GROUPS_SQL, f"antigravity-group-check {label}")
    for r in _parse_rows(label, grp_out):
        reason = _group_violation(r)
        if reason:
            bad.append({"target": label, "kind": "group", "id": r.get("id"), "name": r.get("name"), "reason": reason})
    return bad


def _resolve_targets(skip_prod: bool) -> list[tuple[str, str, str]]:
    ec2_matrix = _ROUTING.load_matrix(REPO_ROOT / "deploy/aws/stage0/edge-targets.json")
    ls_targets = _ROUTING.load_lightsail_targets(REPO_ROOT)
    edge_ids = _ROUTING.iter_effective_deployable_edge_ids(ec2_matrix, ls_targets)
    targets: list[tuple[str, str, str]] = []
    for eid in edge_ids:
        ident = _SSM.resolve_edge_execution_identity(REPO_ROOT, eid)
        targets.append((eid, ident.region, ident.instance_id))
    if not skip_prod:
        prod_inst = _SSM.cfn_resolve_instance_id(PROD_TARGET["region"], PROD_TARGET["stack"])
        targets.append((PROD_TARGET["label"], PROD_TARGET["region"], prod_inst))
    return targets


def main() -> int:
    ap = argparse.ArgumentParser(description="Post-rollout antigravity account gemini-only config check (read-only).")
    ap.add_argument("--json", action="store_true", help="machine-readable output")
    ap.add_argument("--skip-prod", action="store_true", help="check edges only")
    ap.add_argument("--parallel", type=int, default=6, help="parallel SSM workers")
    args = ap.parse_args()

    targets = _resolve_targets(args.skip_prod)
    violations: list[dict] = []
    with ThreadPoolExecutor(max_workers=max(1, args.parallel)) as ex:
        futs = {ex.submit(_check_target, *t): t for t in targets}
        for fut in as_completed(futs):
            violations.extend(fut.result())

    def _sort_key(v: dict):
        return (str(v["target"]), v.get("kind", ""), v.get("id") or 0)

    if args.json:
        print(json.dumps({
            "targets": [t[0] for t in targets],
            "violation_count": len(violations),
            "violations": sorted(violations, key=_sort_key),
        }, indent=2, ensure_ascii=False))
    elif violations:
        print(f"FAIL: {len(violations)} antigravity config violation(s) not gemini-only across {len(targets)} target(s):")
        for v in sorted(violations, key=_sort_key):
            print(f"  [{v['target']}] {v.get('kind', 'account')} {v['id']} ({v['name']}): {v['reason']}")
    else:
        print(f"OK: all antigravity accounts + groups gemini-only across {len(targets)} target(s)")

    return 1 if violations else 0


if __name__ == "__main__":
    raise SystemExit(main())
