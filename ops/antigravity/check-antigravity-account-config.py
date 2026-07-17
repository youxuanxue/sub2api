#!/usr/bin/env python3
"""TokenKey post-rollout antigravity config check (read-only).

Verifies Antigravity account/group configuration across prod and all deployable
edges. Model and scope policy is loaded from the generated model-surface bundle;
this checker owns only the independent account-to-group binding invariant:

  1. **accounts** — prod accounts cover the complete bundle mapping floor
     with correct targets and no bundle-forbidden key/prefix. On every target, any
     active+schedulable account is bound to an antigravity group. Edge account
     mappings remain passthrough-empty and are not treated as drift.
  2. **groups** — every active ``platform=antigravity`` group carries exactly
     the bundle ``supported_model_scopes`` projection.

Account mappings and group scopes are not self-healed by server startup/ticks;
operators review the diff and then run the explicit apply flow. The group
binding is a one-time provisioning step the apply flow does NOT infer, so this
tool remains the safety net for it. This tool is the post-rollout
*verification*.

A **violation** is a prod antigravity account whose ``model_mapping`` is
null/empty, misses or misroutes the bundle floor, or contains a bundle
forbidden key/prefix; OR, on any target, an
active+schedulable account with no antigravity-group binding; OR any active
antigravity group whose scopes do not equal the bundle projection.

Exit codes (mirrors the anthropic post-release check): ``0`` = all configured
(green); ``1`` = violations found (yellow, non-blocking at rollout); ``2`` = could
not run (yellow). Read-only - never mutates. Python has no third-party
dependencies; only the release bundle is required. Routing and SSM identity
reuse the shared ``ops/stage0`` helpers.
"""
from __future__ import annotations

import argparse
import functools
import importlib.util
import json
import pathlib
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]

_bundle_spec = importlib.util.spec_from_file_location(
    "tk_model_surface_bundle", REPO_ROOT / "ops" / "pricing" / "model_surface_bundle.py")
_BUNDLE = importlib.util.module_from_spec(_bundle_spec)
_bundle_spec.loader.exec_module(_BUNDLE)
DEFAULT_BUNDLE_PATH = _BUNDLE.DEFAULT_BUNDLE_PATH
_BUNDLE_PATH = DEFAULT_BUNDLE_PATH

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
# the account model_mapping apply flow (same口径: only active groups are asserted).
# `deleted_at IS NULL` mandatory (soft-delete gate).
ANTIGRAVITY_GROUPS_SQL = (
    "SELECT COALESCE(json_agg(json_build_object("
    "'id', id, 'name', name, 'scopes', supported_model_scopes))::text, '[]') "
    "FROM groups WHERE platform = 'antigravity' AND status = 'active' AND deleted_at IS NULL;"
)

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


def _set_bundle_path(raw: str | None) -> pathlib.Path:
    global _BUNDLE_PATH
    if raw is not None and str(raw).strip():
        _BUNDLE_PATH = pathlib.Path(str(raw)).expanduser().resolve()
    _antigravity_policy.cache_clear()
    return _BUNDLE_PATH


@functools.lru_cache(maxsize=4)
def _antigravity_policy() -> dict:
    try:
        bundle = _BUNDLE.load_bundle(_BUNDLE_PATH)
    except RuntimeError as e:
        fail(str(e))
    doc = bundle["account_model_mapping"]

    mapping = (doc.get("platforms") or {}).get("antigravity")
    scopes = doc.get("antigravity_group_scopes")
    if not isinstance(mapping, dict) or not mapping:
        fail("model surface bundle omitted antigravity platform mapping")
    if not isinstance(scopes, list) or not scopes:
        fail("model surface bundle omitted antigravity group scopes")
    return {
        "floor_sha256": bundle["floor_sha256"],
        "mapping": mapping,
        "scopes": {str(scope).strip() for scope in scopes if str(scope).strip()},
        "forbidden_keys": set((doc.get("forbidden_model_mapping_keys") or {}).get("antigravity") or []),
        "forbidden_prefixes": tuple(
            str(prefix)
            for prefix in ((doc.get("forbidden_model_mapping_prefixes") or {}).get("antigravity") or [])
            if str(prefix)
        ),
    }


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


def _account_violation(row: dict, *, allow_empty_mapping: bool = False) -> str | None:
    """Return a human reason if the account is misconfigured, else None.

    Two independent checks (both reported if both fail):
      1. explicit live model_mapping (complete bundle floor coverage and
         forbidden key/prefix policy, non-empty; non-forbidden extras allowed).
      2. group binding — an active+schedulable account MUST be bound to an
         antigravity group via account_groups, else the scheduler finds no
         account for that group and fast-fails every request with
         "No available accounts" 429 (the account looks ready but silently
         never serves). This binding is a provisioning step the account
         model_mapping apply flow does NOT infer, so the post-rollout check is
         the only safety net.
    """
    reasons: list[str] = []
    mm = row.get("model_mapping")
    if allow_empty_mapping:
        if mm is not None and mm != {}:
            reasons.append("edge model_mapping must remain empty passthrough")
    else:
        policy = _antigravity_policy()
        expected_mapping = policy["mapping"]
        forbidden_keys = policy["forbidden_keys"]
        forbidden_prefixes = policy["forbidden_prefixes"]

        if not mm or not isinstance(mm, dict):
            reasons.append("empty/missing prod model_mapping")
        else:
            missing_floor_keys = sorted(k for k in expected_mapping if k not in mm)
            if missing_floor_keys:
                reasons.append("missing bundle floor keys: " + ", ".join(missing_floor_keys))
            bad_floor_targets = sorted(
                k for k, v in expected_mapping.items()
                if k in mm and mm.get(k) != v
            )
            if bad_floor_targets:
                reasons.append("bad bundle floor remaps: " + ", ".join(
                    f"{k}->{mm.get(k)!r} want {expected_mapping[k]!r}" for k in bad_floor_targets
                ))
            leaked = sorted(
                k for k in mm
                if any(k.startswith(prefix) for prefix in forbidden_prefixes)
            )
            if leaked:
                reasons.append("serves unsupported models: " + ", ".join(leaked))
            forbidden = sorted(k for k in mm if k in forbidden_keys)
            if forbidden:
                reasons.append("contains forbidden model_mapping keys from bundle: " + ", ".join(forbidden))

    if row.get("status") == "active" and row.get("schedulable") and not row.get("bound"):
        reasons.append("active+schedulable but NOT bound to any antigravity group "
                       "(account_groups missing → scheduler 'No available accounts' 429)")

    return "; ".join(reasons) if reasons else None


def _group_violation(row: dict) -> str | None:
    """Return a human reason if the group scopes are not canonical, else None."""
    scopes = row.get("scopes")
    if not scopes or not isinstance(scopes, list):
        return "empty/missing supported_model_scopes (unrestricted on /models + usage guide)"
    canonical_scopes = _antigravity_policy()["scopes"]
    got = {str(s).strip() for s in scopes}
    if got == canonical_scopes:
        return None
    parts = []
    extra = sorted(got - canonical_scopes)
    missing = sorted(canonical_scopes - got)
    if extra:
        parts.append("unexpected: " + ", ".join(extra))
    if missing:
        parts.append("missing: " + ", ".join(missing))
    return "scopes not canonical (" + "; ".join(parts) + ")"


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
        reason = _account_violation(r, allow_empty_mapping=(label != PROD_TARGET["label"]))
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
    ap = argparse.ArgumentParser(description="Post-rollout antigravity explicit model_mapping config check (read-only).")
    ap.add_argument("--json", action="store_true", help="machine-readable output")
    ap.add_argument("--skip-prod", action="store_true", help="check edges only")
    ap.add_argument("--bundle", help="generated model-surface bundle to check against")
    ap.add_argument("--parallel", type=int, default=6, help="parallel SSM workers")
    args = ap.parse_args()

    _set_bundle_path(args.bundle)
    policy = _antigravity_policy()  # Validate the bundle before target workers fan out.
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
            "bundle": str(_BUNDLE_PATH),
            "floor_sha256": policy["floor_sha256"],
            "targets": [t[0] for t in targets],
            "violation_count": len(violations),
            "violations": sorted(violations, key=_sort_key),
        }, indent=2, ensure_ascii=False))
    elif violations:
        print(f"FAIL: {len(violations)} antigravity config violation(s) across {len(targets)} target(s):")
        for v in sorted(violations, key=_sort_key):
            print(f"  [{v['target']}] {v.get('kind', 'account')} {v['id']} ({v['name']}): {v['reason']}")
    else:
        print(f"OK: all antigravity accounts + groups explicit across {len(targets)} target(s)")

    return 1 if violations else 0


if __name__ == "__main__":
    raise SystemExit(main())
