#!/usr/bin/env python3
"""
TokenKey Anthropic OAuth tier-baseline + prod stub pool-mode orchestrator.

One entrypoint, plan JSON as the only file between stages.  Two write
surfaces, both JSON-derived (no static SQL templates, no operator-written
SQL):

  A. edge OAuth account tier baseline
     — per ``anthropic-oauth-stability-baselines-tiered.json``
     — action.kind = ``edge_account_tier``
  B. prod anthropic api-key stub pool_mode (mirror stubs, base_url = api-*.tokenkey.dev)
     — per ``anthropic-stub-pool-baselines.json``
     — action.kind = ``prod_stub_pool``

Stages
------
  1. snapshot — pull each deployable edge's anthropic OAuth accounts AND
                prod's anthropic api-key mirror stubs into one JSON
  2. check    — invoke check-edge-oauth-stability.py for each edge × account
  3a. plan-edge-account-tier — declare an edge OAuth tier change
  3b. plan-tier-bump         — re-apply a tier baseline to every matching edge account
  3c. plan-stub-pool         — enable pool_mode on every prod stub matching the
                               base_url policy (idempotent; live-matched stubs skip)
  4. apply    — render apply SQL from JSON, run via SSM, parse output
  5. verify   — re-snapshot, diff each expected_after vs live

History
-------
Prior to 2026-05-21 this orchestrator also covered prod-side cascading
writes (stub concurrency mirror, stub `extra.declared_rpm`, group
`rpm_limit` derived as Σ stub.declared_rpm, edge group cap derived as
Σ(base_rpm + rpm_sticky_buffer)).  That entire "account → group
aggregation" model was retired because layered SUM caps left no
headroom for sticky-buffer burst on the upstream OAuth pool — upstream
quota was being throttled before real traffic could exercise it.
Group `rpm_limit` is now set independently in the admin UI; this
orchestrator no longer writes to any group nor to any prod surface.

Each successful edge ``apply`` transaction also sets ``users.id=1``
``concurrency`` to the sum of ``concurrency`` on every ``anthropic`` account row
(not soft-deleted) on that same database — oauth and api-key types —
so operator default tracks total Anthropic account capacity.

Exit codes
----------
  0  command succeeded; for check/verify, no violations / no drift
  1  command ran but reported violations / drift / apply step failure
  2  setup / SSM / target-resolution / schema error

Usage
-----
  manage-anthropic-config.py snapshot --out snap.json
  manage-anthropic-config.py check --snapshot snap.json
  manage-anthropic-config.py plan-edge-account-tier \\
      --edge uk1 --account en-ld-ec2-16-1-b --tier l2 \\
      --snapshot snap.json --out plan.json
  manage-anthropic-config.py apply --plan plan.json \\
      --confirm yes-apply-anthropic-config-cascade
  manage-anthropic-config.py verify --plan plan.json
"""
from __future__ import annotations

import argparse
import base64
import datetime as _dt
import importlib.util
import json
import os
import pathlib
import subprocess
import sys
from typing import Any

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
EDGE_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
TIER_BASELINES = REPO_ROOT / "deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json"
STUB_POOL_BASELINES = REPO_ROOT / "deploy/aws/stage0/anthropic-stub-pool-baselines.json"
OPS_DIR = REPO_ROOT / "ops/anthropic"

# prod is not an entry in edge-targets.json (which is the edge matrix).
# Pin it explicitly so plan-stub-pool / apply / verify can resolve the prod
# Postgres without operators discovering CFN stack names from memory.
PROD_TARGET = {
    "region": "us-east-1",
    "stack": "tokenkey-prod-stage0",
    "domain": "api.tokenkey.dev",
    "label": "prod",
}


def _load_guard_module():
    """Import the sibling stability guard (hyphenated filename → importlib).

    The guard owns the single JSON-derived apply-SQL generator (generate_sql)
    and the shared shared+tier merge (effective_baseline_for_tier). Reusing them
    here keeps the tier baseline values in exactly one place: the JSON file.
    """
    path = OPS_DIR / "check-edge-oauth-stability.py"
    spec = importlib.util.spec_from_file_location("tk_edge_oauth_stability_guard", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load guard module from {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


_GUARD = _load_guard_module()

CONFIRM_CODE = "yes-apply-anthropic-config-cascade"

PLAN_VERSION = 3
SNAPSHOT_VERSION = 3

# After each OAuth tier-baseline apply on an edge Postgres, bump the operator
# (admin/default) user's row concurrency to match Σ anthropic account concurrency
# (all types incl. api-key) on that same DB — avoids drift when Anthropic pool sizing changes.

ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID = 1


# --------------------------------------------------------------------------
# Utility
# --------------------------------------------------------------------------

def fail(msg: str, code: int = 2) -> None:
    print(f"::error::{msg}", file=sys.stderr)
    sys.exit(code)


def now_utc_iso() -> str:
    return _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def load_json_file(path: pathlib.Path, what: str) -> Any:
    if not path.exists():
        fail(f"{what} not found: {path}")
    try:
        return json.loads(path.read_text())
    except json.JSONDecodeError as e:
        fail(f"{what} parse error ({path}): {e}")
    return None  # unreachable


# --------------------------------------------------------------------------
# AWS / SSM plumbing
# --------------------------------------------------------------------------

def resolve_instance_id(region: str, stack: str) -> str:
    try:
        out = subprocess.check_output(
            [
                "aws", "cloudformation", "describe-stacks",
                "--region", region,
                "--stack-name", stack,
                "--query", "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue",
                "--output", "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"describe-stacks failed for {stack}/{region}: {e}")
    if not out or out == "None":
        try:
            out = subprocess.check_output(
                [
                    "aws", "cloudformation", "describe-stack-resources",
                    "--region", region,
                    "--stack-name", stack,
                    "--query", "StackResources[?ResourceType=='AWS::EC2::Instance'].PhysicalResourceId | [0]",
                    "--output", "text",
                ],
                text=True,
            ).strip()
        except subprocess.CalledProcessError as e:
            fail(f"describe-stack-resources fallback failed for {stack}/{region}: {e}")
    if not out or out == "None":
        fail(f"no InstanceId resolvable for stack {stack}/{region}")
    return out


def ssm_run_sql(region: str, instance_id: str, sql: str, comment: str) -> tuple[str, str]:
    """Pipe SQL via SSM. Returns (stdout, command_id)."""
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
        [
            "aws", "ssm", "wait", "command-executed",
            "--region", region,
            "--command-id", cid,
            "--instance-id", instance_id,
        ],
        check=False,
    )
    inv = json.loads(
        subprocess.check_output(
            [
                "aws", "ssm", "get-command-invocation",
                "--region", region,
                "--command-id", cid,
                "--instance-id", instance_id,
                "--output", "json",
            ],
            text=True,
        )
    )
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:1200]
        out_preview = (inv.get("StandardOutputContent") or "").strip()[:600]
        fail(
            f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')} ({comment})\n"
            f"  stderr: {err}\n  stdout: {out_preview}"
        )
    return (inv.get("StandardOutputContent") or "").strip(), cid


def ssm_run_sql_b64(region: str, instance_id: str, sql_b64: str, comment: str
                     ) -> tuple[str, str, bool, str]:
    """Apply path: base64-encoded SQL so embedded quotes / heredocs don't escape.

    Returns (stdout, ssm_command_id, success, stderr_preview).  cid is always
    a valid SSM CommandId, so callers can feed it to
    ``aws ssm get-command-invocation`` for after-the-fact debugging even on
    failure.
    """
    command = (
        "set -euo pipefail\n"
        f"echo {sql_b64} | base64 -d | sudo docker exec -i tokenkey-postgres "
        "psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -X -A -t"
    )
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
        [
            "aws", "ssm", "wait", "command-executed",
            "--region", region,
            "--command-id", cid,
            "--instance-id", instance_id,
        ],
        check=False,
    )
    inv = json.loads(
        subprocess.check_output(
            [
                "aws", "ssm", "get-command-invocation",
                "--region", region,
                "--command-id", cid,
                "--instance-id", instance_id,
                "--output", "json",
            ],
            text=True,
        )
    )
    stdout = (inv.get("StandardOutputContent") or "").strip()
    stderr = (inv.get("StandardErrorContent") or "").strip()[:1200]
    success = inv.get("Status") == "Success" and inv.get("ResponseCode") == 0
    if not success:
        stdout = stdout[:600]
    return stdout, cid, success, stderr


# --------------------------------------------------------------------------
# Stage 1 — snapshot
# --------------------------------------------------------------------------

EDGE_ACCOUNTS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name, 'platform', a.platform, 'type', a.type,
  'status', a.status, 'schedulable', a.schedulable,
  'concurrency', a.concurrency, 'priority', a.priority,
  'channel_type', a.channel_type, 'rate_multiplier', a.rate_multiplier,
  'auto_pause_on_expired', a.auto_pause_on_expired,
  'stability_tier', a.extra->>'stability_tier',
  'rpm_strategy', a.extra->>'rpm_strategy',
  'base_rpm', NULLIF(a.extra->>'base_rpm', '')::int,
  'rpm_sticky_buffer', NULLIF(a.extra->>'rpm_sticky_buffer', '')::int,
  'max_sessions', NULLIF(a.extra->>'max_sessions', '')::int,
  'session_idle_timeout_minutes', NULLIF(a.extra->>'session_idle_timeout_minutes', '')::int,
  'window_cost_limit', NULLIF(a.extra->>'window_cost_limit', '')::int,
  'window_cost_sticky_reserve', NULLIF(a.extra->>'window_cost_sticky_reserve', '')::int,
  'cache_ttl_override_enabled', NULLIF(a.extra->>'cache_ttl_override_enabled', '')::boolean
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.platform = 'anthropic'
  AND a.type = 'oauth'
  AND a.deleted_at IS NULL;
"""

# Prod stubs: anthropic api-key accounts whose credentials.base_url points at
# a tokenkey edge domain. snapshot pulls them so plan-stub-pool can match by
# regex without re-querying. We deliberately include every anthropic api-key
# stub (not just base_url-matching ones); plan-stub-pool filters in Python
# using the JSON-driven policy regex — this keeps the SQL stable when we
# evolve which patterns are in scope.
PROD_STUBS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name, 'platform', a.platform, 'type', a.type,
  'status', a.status, 'schedulable', a.schedulable,
  'concurrency', a.concurrency, 'priority', a.priority,
  'cred_base_url',              a.credentials->>'base_url',
  'cred_pool_mode',             NULLIF(a.credentials->>'pool_mode', '')::boolean,
  'cred_pool_mode_retry_count', NULLIF(a.credentials->>'pool_mode_retry_count', '')::int
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.platform = 'anthropic'
  AND a.type = 'apikey'
  AND a.deleted_at IS NULL;
"""


def cmd_snapshot(args: argparse.Namespace) -> int:
    edge_matrix = load_json_file(EDGE_MATRIX, "edge matrix")
    edge_targets = edge_matrix.get("targets") or {}

    edges: dict[str, dict] = {}
    for eid, tgt in edge_targets.items():
        if not tgt.get("deployable") and not args.allow_planned:
            edges[eid] = {
                "deployable": False,
                "skipped_reason": f"edge {eid} is planned; pass --allow-planned to include",
                "region": tgt.get("region"), "stack": tgt.get("stack"),
            }
            continue
        try:
            inst = resolve_instance_id(tgt["region"], tgt["stack"])
        except SystemExit:
            edges[eid] = {"error": f"could not resolve instance for edge {eid}"}
            continue
        print(f"snapshot: edge {eid} instance={inst}", file=sys.stderr)
        accts_raw, _ = ssm_run_sql(tgt["region"], inst, EDGE_ACCOUNTS_SQL,
                                    f"snapshot: edge {eid} oauth accounts")
        edges[eid] = {
            "deployable": True,
            "instance_id": inst,
            "region": tgt["region"],
            "stack": tgt["stack"],
            "domain": tgt.get("domain"),
            "oauth_accounts": json.loads(accts_raw) if accts_raw else [],
        }

    prod_view: dict[str, Any]
    if getattr(args, "skip_prod", False):
        prod_view = {"skipped_reason": "--skip-prod passed"}
    else:
        try:
            prod_inst = resolve_instance_id(PROD_TARGET["region"], PROD_TARGET["stack"])
            print(f"snapshot: prod instance={prod_inst}", file=sys.stderr)
            stubs_raw, _ = ssm_run_sql(PROD_TARGET["region"], prod_inst, PROD_STUBS_SQL,
                                        "snapshot: prod anthropic stubs")
            prod_view = {
                "instance_id": prod_inst,
                "region": PROD_TARGET["region"],
                "stack": PROD_TARGET["stack"],
                "domain": PROD_TARGET["domain"],
                "anthropic_stubs": json.loads(stubs_raw) if stubs_raw else [],
            }
        except SystemExit:
            print("snapshot: prod failed to resolve instance (skipping prod view; "
                  "plan-stub-pool will fail-loud if invoked)", file=sys.stderr)
            prod_view = {"error": "could not resolve instance for prod"}

    snapshot = {
        "version": SNAPSHOT_VERSION,
        "captured_at": now_utc_iso(),
        "edges": edges,
        "prod": prod_view,
    }

    out_str = json.dumps(snapshot, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"snapshot: written {args.out} ({len(out_str)} bytes)", file=sys.stderr)
    else:
        print(out_str)
    return 0


# --------------------------------------------------------------------------
# Stage 2 — check
# --------------------------------------------------------------------------

def _run_guard(argv: list[str], description: str) -> dict[str, Any]:
    """Run a sub-guard with --json and return parsed dict + exit code."""
    try:
        proc = subprocess.run(argv, capture_output=True, text=True, check=False)
    except FileNotFoundError as e:
        return {"error": str(e), "exit_code": 127, "raw_stdout": "", "raw_stderr": ""}
    stdout = proc.stdout.strip()
    stderr = proc.stderr.strip()
    out: dict[str, Any] = {"exit_code": proc.returncode, "description": description}
    if stdout:
        try:
            out["report"] = json.loads(stdout)
        except json.JSONDecodeError:
            out["raw_stdout"] = stdout[:2000]
    if stderr:
        out["raw_stderr"] = stderr[:2000]
    return out


def cmd_check(args: argparse.Namespace) -> int:
    snapshot = load_json_file(pathlib.Path(args.snapshot), "snapshot") if args.snapshot else None

    if snapshot is not None:
        edge_ids = sorted([
            eid for eid, e in snapshot.get("edges", {}).items()
            if e.get("deployable") is not False and "error" not in e
        ])
    else:
        matrix = load_json_file(EDGE_MATRIX, "edge matrix")
        edge_ids = sorted([
            eid for eid, t in (matrix.get("targets") or {}).items()
            if t.get("deployable") or args.allow_planned
        ])

    sub_results: list[dict] = []
    for eid in edge_ids:
        sub_results.append(_run_guard(
            ["python3", str(OPS_DIR / "check-edge-oauth-stability.py"),
             "--edge-id", eid, "--account-name", "all", "--json"]
            + (["--allow-planned"] if args.allow_planned else []),
            f"edge-oauth-stability edge={eid}",
        ))

    any_violation = any(
        sr.get("exit_code", 0) not in (0, None) for sr in sub_results
    )

    report = {
        "version": 2,
        "checked_at": now_utc_iso(),
        "edges_in_scope": edge_ids,
        "any_violation": any_violation,
        "guards": sub_results,
    }
    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        print(f"check: any_violation={any_violation} guards_run={len(sub_results)} edges={edge_ids}")
        for sr in sub_results:
            ec = sr.get("exit_code")
            status = "OK" if ec == 0 else (sr.get("skipped_reason", "?") if ec is None else f"FAIL exit={ec}")
            print(f"  [{status}] {sr.get('description')}")
    return 1 if any_violation else 0


# --------------------------------------------------------------------------
# Stage 3 — plan
# --------------------------------------------------------------------------

def _load_snapshot_or_die(path: str) -> dict:
    snap = load_json_file(pathlib.Path(path), "snapshot")
    v = snap.get("version")
    if v != SNAPSHOT_VERSION:
        fail(f"snapshot version {v} != expected {SNAPSHOT_VERSION} "
             f"(snapshot v1 cascaded prod stub fields aggregated from edges; "
             f"v2 dropped that, v3 re-added a prod READ view + stub pool-mode WRITE)")
    return snap


def _load_stub_pool_policy() -> dict:
    """Parse anthropic-stub-pool-baselines.json once. The policy is the single
    source of truth for which prod anthropic stubs get pool_mode enabled — both
    the regex matcher and the retry-count value live here, so apply SQL is
    derived (not hand-written) and verify can compare against the same field
    set the plan declared. Schema is checked strictly to catch typos early."""
    raw = load_json_file(STUB_POOL_BASELINES, "stub pool baselines")
    if not isinstance(raw, dict):
        fail("stub pool baselines: top-level must be an object")
    if raw.get("schema_version") != 1:
        fail(f"stub pool baselines: schema_version {raw.get('schema_version')!r} != 1")
    policy = raw.get("policy")
    if not isinstance(policy, dict):
        fail("stub pool baselines: missing 'policy' object")
    required = ("base_url_pattern", "platform", "account_type",
                "pool_mode_enabled", "pool_mode_retry_count")
    for k in required:
        if k not in policy:
            fail(f"stub pool baselines: policy missing required field {k!r}")
    if policy["platform"] != "anthropic":
        fail(f"stub pool baselines: policy.platform must be 'anthropic', got {policy['platform']!r} "
             "(this orchestrator handles anthropic only)")
    if policy["account_type"] != "apikey":
        fail(f"stub pool baselines: policy.account_type must be 'apikey', got {policy['account_type']!r} "
             "(pool_mode is only meaningful for IsAPIKeyOrBedrock() accounts; oauth cannot enable it)")
    retry = policy["pool_mode_retry_count"]
    if not isinstance(retry, int) or retry < 0 or retry > 10:
        fail(f"stub pool baselines: policy.pool_mode_retry_count must be int in [0,10], got {retry!r}")
    try:
        import re
        policy["_compiled_pattern"] = re.compile(policy["base_url_pattern"])
    except re.error as e:
        fail(f"stub pool baselines: policy.base_url_pattern is not a valid regex: {e}")
    return policy


def _load_tier_baselines() -> dict[str, dict]:
    """Return {tier: flattened_fields} keyed by 'l1'..'l5'.

    Flattens the nested ``tiers[lN].baseline.{account,extra}.<field>`` shape
    so callers see one dict per tier with both account fields and extra
    fields at the top level (the keys used by the tier baseline template).
    """
    raw = load_json_file(TIER_BASELINES, "tier baselines")
    out: dict[str, dict] = {}
    items = raw.get("tiers") if isinstance(raw, dict) and "tiers" in raw else raw
    src_iter = items.items() if isinstance(items, dict) else (
        ((t.get("stability_tier") or t.get("tier")), t) for t in items
    )
    for key, t in src_iter:
        if not key:
            continue
        baseline = t.get("baseline") if isinstance(t, dict) else None
        flat: dict[str, Any] = {"tier": str(key).lower()}
        if isinstance(baseline, dict):
            for sub in ("account", "extra"):
                d = baseline.get(sub)
                if isinstance(d, dict):
                    flat[sub] = dict(d)
                    flat.update(d)
        for k, v in (t.items() if isinstance(t, dict) else []):
            if k == "baseline":
                continue
            flat.setdefault(k, v)
        out[str(key).lower()] = flat
    return out


def _find_edge(snap: dict, edge_id: str) -> dict:
    edges = snap.get("edges", {})
    if edge_id not in edges:
        fail(f"edge {edge_id!r} not in snapshot; re-run snapshot")
    edge = edges[edge_id]
    if edge.get("error") or edge.get("skipped_reason"):
        fail(f"edge {edge_id!r} not snapshotted: {edge.get('error') or edge.get('skipped_reason')}")
    return edge


def _find_edge_account(edge: dict, account_name: str) -> dict:
    for a in edge.get("oauth_accounts", []):
        if a.get("name") == account_name:
            return a
    fail(f"account {account_name!r} not found in edge oauth_accounts")
    return {}  # unreachable


# Tier-baseline value fields that the apply SQL writes AND that this pipeline
# owns end-to-end. The skip-as-noop gate (_tier_fields_match) and Stage-5 verify
# must both cover this WHOLE set — otherwise a bump touching only one of them
# (e.g. window_cost_limit-only) is silently skipped by plan-tier-bump and then
# falsely verified clean, defeating the "no account left at the old value"
# guarantee. snapshot already carries all of these.
#
# `priority` is deliberately EXCLUDED: it is owned by the separate
# rebalance-anthropic-priority pipeline. Pulling it in here would make
# plan-tier-bump fight the rebalancer (spurious actions on every rebalanced
# account) and make verify flag false drift whenever priority was rebalanced.
# (apply still writes the baseline priority — an existing cross-pipeline
# interaction, out of scope here.) rate_multiplier is fixed at 1.0 by policy and
# is likewise not a field anyone bumps, so it is left out to avoid float-equality
# fragility. credentials-side fields (e.g. temp_unschedulable_rules) are not
# tracked by snapshot/verify either — that is what --force-template-rewrite covers.
_TIER_BASELINE_FIELDS = (
    "base_rpm", "rpm_sticky_buffer", "concurrency", "max_sessions",
    "window_cost_limit", "session_idle_timeout_minutes", "window_cost_sticky_reserve",
)
_ACCOUNT_BEFORE_FIELDS = (
    "id", "name", "concurrency", "stability_tier", "base_rpm",
    "rpm_sticky_buffer", "max_sessions", "session_idle_timeout_minutes",
    "window_cost_limit", "window_cost_sticky_reserve", "status",
)


def _tier_fields_match(account: dict, baseline: dict) -> bool:
    return all(account.get(k) == baseline.get(k) for k in _TIER_BASELINE_FIELDS)


def _tier_expected_after(baseline: dict, tier_key: str) -> dict:
    """The post-apply field values Stage-5 verify compares against live. Carries
    stability_tier + every field this pipeline owns (_TIER_BASELINE_FIELDS), so
    verify can confirm each one actually landed."""
    exp = {"stability_tier": tier_key}
    exp.update({k: baseline.get(k) for k in _TIER_BASELINE_FIELDS})
    return exp


def _build_tier_action(edge_id: str, account_name: str, baseline: dict,
                       tier_key: str, step: int) -> dict:
    """One apply action re-rendering ``account_name`` at ``tier_key``. The SQL is
    derived from the JSON baseline at apply time; ``expected_after`` is what Stage
    5 verify compares against live."""
    return {
        "step": step,
        "kind": "edge_account_tier",
        "target": {"env": "edge", "edge_id": edge_id, "account_name": account_name},
        "sql_source": "rendered-from-anthropic-oauth-stability-baselines-tiered.json",
        "variables": {"account_name": account_name, "stability_tier": tier_key},
        "expected_after": _tier_expected_after(baseline, tier_key),
    }


def _account_before(account: dict) -> dict:
    return {k: account.get(k) for k in _ACCOUNT_BEFORE_FIELDS}


def cmd_plan_edge_account_tier(args: argparse.Namespace) -> int:
    snap = _load_snapshot_or_die(args.snapshot)
    tiers = _load_tier_baselines()
    tier_key = args.tier.lower()
    if tier_key not in tiers:
        fail(f"tier {args.tier!r} not in baselines (available: {sorted(tiers)})")
    baseline = tiers[tier_key]

    edge = _find_edge(snap, args.edge_id)
    target_account = _find_edge_account(edge, args.account_name)

    current_tier = (target_account.get("stability_tier") or "").lower()
    fields_match = _tier_fields_match(target_account, baseline)
    force_rewrite = bool(getattr(args, "force_template_rewrite", False))
    if current_tier == tier_key and fields_match and not force_rewrite:
        print(
            f"plan: account {args.account_name!r} on edge {args.edge_id} is already at "
            f"tier {tier_key} with matching baseline fields — emitting empty plan.",
            file=sys.stderr,
        )
        empty_plan = {
            "version": PLAN_VERSION,
            "kind": "edge_account_tier_change",
            "confirm_code": CONFIRM_CODE,
            "intent": {"edge_id": args.edge_id, "account_name": args.account_name,
                       "new_tier": tier_key,
                       "force_template_rewrite": force_rewrite},
            "snapshot_captured_at": snap.get("captured_at"),
            "plan_built_at": now_utc_iso(),
            "noop": True,
            "noop_reason": "current tier and baseline fields already match target",
            "summary": {"total_steps": 0, "edge_changes": 0},
            "live_inputs": {"edge_account_before": target_account},
            "actions": [],
        }
        out_str = json.dumps(empty_plan, indent=2, ensure_ascii=False)
        if args.out:
            pathlib.Path(args.out).write_text(out_str)
            print(f"plan: written {args.out} (noop)", file=sys.stderr)
        else:
            print(out_str)
        return 0

    action = _build_tier_action(args.edge_id, args.account_name, baseline, tier_key, 1)

    plan = {
        "version": PLAN_VERSION,
        "kind": "edge_account_tier_change",
        "confirm_code": CONFIRM_CODE,
        "intent": {"edge_id": args.edge_id, "account_name": args.account_name,
                   "new_tier": tier_key,
                   "force_template_rewrite": force_rewrite},
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "summary": {"total_steps": 1, "edge_changes": 1},
        "live_inputs": {
            "edge_account_before": _account_before(target_account),
        },
        "actions": [action],
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan: written {args.out} (1 step)", file=sys.stderr)
    else:
        print(out_str)
    return 0


def cmd_plan_tier_bump(args: argparse.Namespace) -> int:
    """Re-apply a (just-edited) tier baseline value to *every* account currently
    on that tier across all snapshotted deployable edges, in one multi-action
    plan. This is the correct shape for a tier-VALUE bump (e.g. L5 max_sessions
    50→60): edit the baseline JSON first, then this enumerates the tier's whole
    live population so no account is silently left at the old value. apply/verify
    already iterate the actions list, so one apply + one verify covers the batch.
    Contrast plan-edge-account-tier, which MOVES a single account to a tier."""
    snap = _load_snapshot_or_die(args.snapshot)
    tiers = _load_tier_baselines()
    tier_key = args.tier.lower()
    if tier_key not in tiers:
        fail(f"tier {args.tier!r} not in baselines (available: {sorted(tiers)})")
    baseline = tiers[tier_key]
    force = bool(getattr(args, "force_template_rewrite", False))

    actions: list[dict] = []
    befores: list[dict] = []
    skipped: list[dict] = []
    for edge_id, edge in sorted(snap.get("edges", {}).items()):
        if edge.get("error") or edge.get("skipped_reason"):
            continue
        for a in edge.get("oauth_accounts", []):
            if (a.get("stability_tier") or "").lower() != tier_key:
                continue
            if _tier_fields_match(a, baseline) and not force:
                skipped.append({"edge_id": edge_id, "account_name": a.get("name"),
                                "reason": "live fields already match baseline"})
                continue
            step = len(actions) + 1
            actions.append(_build_tier_action(edge_id, a.get("name"), baseline, tier_key, step))
            befores.append({"edge_id": edge_id, **_account_before(a)})

    plan = {
        "version": PLAN_VERSION,
        "kind": "edge_account_tier_change",
        "confirm_code": CONFIRM_CODE,
        "intent": {"tier_bump": tier_key, "scope": "all-snapshotted-deployable-edges",
                   "force_template_rewrite": force},
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "noop": len(actions) == 0,
        "summary": {"total_steps": len(actions), "edge_changes": len(actions),
                    "skipped": len(skipped)},
        "live_inputs": {"edge_accounts_before": befores, "skipped": skipped},
        "actions": actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan: written {args.out} ({len(actions)} step(s), {len(skipped)} skipped)",
              file=sys.stderr)
    else:
        print(out_str)
    if not actions:
        print(f"plan: no account on tier {tier_key} needs rewriting "
              f"(skipped={len(skipped)}; pass --force-template-rewrite to rewrite anyway)",
              file=sys.stderr)
    return 0


# --------------------------------------------------------------------------
# Stage 3c — plan-stub-pool (prod anthropic mirror stubs)
# --------------------------------------------------------------------------

_STUB_POOL_FIELDS = ("cred_pool_mode", "cred_pool_mode_retry_count")


def _stub_pool_expected_after(policy: dict) -> dict:
    """Field values Stage-5 verify compares against live after pool_mode is set.
    Carries exactly what apply writes; verify reads them straight off the
    re-snapshot's prod.anthropic_stubs entries (same JSON keys)."""
    return {
        "cred_pool_mode": bool(policy["pool_mode_enabled"]),
        "cred_pool_mode_retry_count": int(policy["pool_mode_retry_count"]),
    }


def _stub_pool_fields_match(stub: dict, policy: dict) -> bool:
    exp = _stub_pool_expected_after(policy)
    return all(stub.get(k) == exp[k] for k in _STUB_POOL_FIELDS)


def _stub_before(stub: dict) -> dict:
    keep = ("id", "name", "type", "status", "schedulable", "concurrency",
            "cred_base_url", "cred_pool_mode", "cred_pool_mode_retry_count")
    return {k: stub.get(k) for k in keep}


def cmd_plan_stub_pool(args: argparse.Namespace) -> int:
    """Enumerate every prod anthropic stub whose credentials.base_url matches
    the policy regex; emit one plan action per stub that is not already at the
    declared (pool_mode_enabled, pool_mode_retry_count) tuple. Idempotent — a
    second run after apply produces noop=true. Per skill design 2026-05-23:
    no edge-fan-out gate (see policy.notes.no_min_account_gate in the baseline)."""
    snap = _load_snapshot_or_die(args.snapshot)
    policy = _load_stub_pool_policy()
    pattern = policy["_compiled_pattern"]
    force = bool(getattr(args, "force_template_rewrite", False))

    prod = snap.get("prod") or {}
    if prod.get("error") or prod.get("skipped_reason"):
        fail(f"snapshot.prod not captured: {prod.get('error') or prod.get('skipped_reason')}; "
             "re-run snapshot without --skip-prod")
    stubs = prod.get("anthropic_stubs") or []

    actions: list[dict] = []
    befores: list[dict] = []
    skipped_unmatched: list[dict] = []
    skipped_noop: list[dict] = []
    for stub in stubs:
        base_url = stub.get("cred_base_url") or ""
        if not pattern.match(base_url):
            skipped_unmatched.append({
                "id": stub.get("id"), "name": stub.get("name"),
                "cred_base_url": base_url,
                "reason": "base_url does not match policy.base_url_pattern",
            })
            continue
        if _stub_pool_fields_match(stub, policy) and not force:
            skipped_noop.append({
                "id": stub.get("id"), "name": stub.get("name"),
                "reason": "pool_mode + retry_count already match policy",
            })
            continue
        step = len(actions) + 1
        actions.append({
            "step": step,
            "kind": "prod_stub_pool",
            "target": {
                "env": "prod",
                "account_id": stub.get("id"),
                "account_name": stub.get("name"),
            },
            "sql_source": "rendered-from-anthropic-stub-pool-baselines.json",
            "variables": {
                "account_id": stub.get("id"),
                "pool_mode_enabled": bool(policy["pool_mode_enabled"]),
                "pool_mode_retry_count": int(policy["pool_mode_retry_count"]),
            },
            "expected_after": _stub_pool_expected_after(policy),
        })
        befores.append(_stub_before(stub))

    plan = {
        "version": PLAN_VERSION,
        "kind": "prod_stub_pool_mode",
        "confirm_code": CONFIRM_CODE,
        "intent": {
            "scope": "all-prod-anthropic-stubs-matching-policy",
            "policy_source": STUB_POOL_BASELINES.name,
            "base_url_pattern": policy["base_url_pattern"],
            "pool_mode_enabled": bool(policy["pool_mode_enabled"]),
            "pool_mode_retry_count": int(policy["pool_mode_retry_count"]),
            "force_template_rewrite": force,
        },
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "noop": len(actions) == 0,
        "summary": {
            "total_steps": len(actions),
            "prod_changes": len(actions),
            "skipped_unmatched": len(skipped_unmatched),
            "skipped_noop": len(skipped_noop),
        },
        "live_inputs": {
            "prod_stubs_before": befores,
            "skipped_unmatched": skipped_unmatched,
            "skipped_noop": skipped_noop,
        },
        "actions": actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan-stub-pool: written {args.out} "
              f"({len(actions)} step(s), {len(skipped_noop)} noop, "
              f"{len(skipped_unmatched)} unmatched)", file=sys.stderr)
    else:
        print(out_str)
    return 0


# --------------------------------------------------------------------------
# Stage 4 — apply
# --------------------------------------------------------------------------

def render_admin_operator_concurrency_sync_sql() -> str:
    """Sync ``users.concurrency`` for the operator account to summed Anthropic pool.

    All non-soft-deleted ``accounts`` rows with ``platform='anthropic'`` including
    ``oauth`` and ``api_key`` rows. Runs in the same transaction as tier-baseline SQL.
    """
    uid = ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID
    return (
        f"-- Align users.id={uid} concurrency to Σ all anthropic account concurrency\n"
        "UPDATE users u SET concurrency = agg.total::int, updated_at = NOW()\n"
        "FROM (\n"
        "  SELECT COALESCE(SUM(a.concurrency), 0)::bigint AS total\n"
        "  FROM accounts a\n"
        "  WHERE a.platform = 'anthropic'\n"
        "    AND a.deleted_at IS NULL\n"
        ") agg\n"
        f"WHERE u.id = {uid} AND u.deleted_at IS NULL;"
    )

def _inject_sql_before_commit(transaction_sql: str, fragment: str) -> str:
    """Append ``fragment`` immediately before the final ``COMMIT;``."""
    sentinel = "\nCOMMIT;"
    pos = transaction_sql.rfind(sentinel)
    if pos == -1:
        fail("generated tier SQL missing final COMMIT; cannot inject admin concurrency sync")
    sep = "\n" if fragment and not fragment.startswith("\n") else ""
    return transaction_sql[:pos] + sep + fragment.rstrip() + sentinel + transaction_sql[pos + len(sentinel):]


def render_edge_account_tier_sql(account_name: str, stability_tier: str, edge_id: str = "") -> str:
    """Render the apply SQL for one account at one tier, fully derived from the
    JSON baseline (the single source of truth). Reuses the guard's effective-baseline
    merge + generate_sql so the tier values live in exactly one file."""
    baseline_json = load_json_file(TIER_BASELINES, "tier baselines")
    effective = _GUARD.effective_baseline_for_tier(baseline_json, stability_tier)
    header = (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"-- source of truth: {TIER_BASELINES.name} (tier={stability_tier})\n"
    )
    body = _GUARD.generate_sql({"edge_id": edge_id or "(orchestrator)"}, account_name, effective)
    body = _inject_sql_before_commit(body, render_admin_operator_concurrency_sync_sql())
    return header + body


def _resolve_edge_target(edge_id: str, edge_matrix: dict) -> tuple[str, str, str]:
    e = edge_matrix.get("targets", {}).get(edge_id)
    if not e:
        fail(f"edge {edge_id!r} not in edge-targets.json")
    return e["region"], resolve_instance_id(e["region"], e["stack"]), f"edge:{edge_id}"


def _resolve_prod_target() -> tuple[str, str, str]:
    return (
        PROD_TARGET["region"],
        resolve_instance_id(PROD_TARGET["region"], PROD_TARGET["stack"]),
        PROD_TARGET["label"],
    )


def render_prod_stub_pool_sql(account_id: int, account_name: str,
                              pool_mode_enabled: bool, pool_mode_retry_count: int) -> str:
    """Render the apply SQL for one prod anthropic stub. credentials is jsonb;
    we merge two keys with ``||`` so any sibling keys (api_key, base_url, …)
    survive untouched. ``id + name + platform + type`` form the WHERE so a
    typo never silently lands on a different row. ON_ERROR_STOP=1 is set by
    the SSM wrapper. Reason embedded in a comment for audit. (No
    ``users.id=1`` concurrency-sum sync here — that runs after edge tier-baseline
    apply only; this surface does not touch concurrency.)"""
    # Defence-in-depth: name/account_id are PK-typed but we still parameterise
    # via a constant-ish SQL literal because the orchestrator owns both ends.
    # If you find yourself wanting to f"" untrusted strings here, stop and add
    # a quoting helper instead.
    if not isinstance(account_id, int):
        fail(f"render_prod_stub_pool_sql: account_id must be int, got {type(account_id).__name__}")
    if not isinstance(account_name, str) or not account_name:
        fail(f"render_prod_stub_pool_sql: account_name required, got {account_name!r}")
    # SQL-quote the name: replace ' with '' (defensive only — stub names are
    # admin-set ascii identifiers in practice).
    quoted_name = account_name.replace("'", "''")
    enabled_lit = "true" if pool_mode_enabled else "false"
    return (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"-- source of truth: {STUB_POOL_BASELINES.name}\n"
        f"-- kind: prod_stub_pool, account_id={account_id}, name='{quoted_name}'\n"
        "BEGIN;\n"
        "UPDATE accounts SET\n"
        "  credentials = credentials || jsonb_build_object(\n"
        f"    'pool_mode', {enabled_lit}::boolean,\n"
        f"    'pool_mode_retry_count', {int(pool_mode_retry_count)}::int\n"
        "  ),\n"
        "  updated_at = NOW()\n"
        f"WHERE id = {int(account_id)}\n"
        f"  AND name = '{quoted_name}'\n"
        "  AND platform = 'anthropic'\n"
        "  AND type = 'apikey'\n"
        "  AND deleted_at IS NULL\n"
        "RETURNING id, name,\n"
        "  credentials->>'pool_mode' AS after_pool_mode,\n"
        "  credentials->>'pool_mode_retry_count' AS after_retry;\n"
        "COMMIT;"
    )


def cmd_apply(args: argparse.Namespace) -> int:
    plan_path = pathlib.Path(args.plan)
    plan = load_json_file(plan_path, "plan")
    if plan.get("version") != PLAN_VERSION:
        fail(f"plan version {plan.get('version')} != expected {PLAN_VERSION}")
    if args.confirm != CONFIRM_CODE:
        fail(
            f"--confirm mismatch.\n  Got:      {args.confirm!r}\n  Required: {CONFIRM_CODE!r}",
            code=2,
        )

    edge_matrix = load_json_file(EDGE_MATRIX, "edge matrix")

    job_dir = pathlib.Path(args.job_dir) if args.job_dir else pathlib.Path(
        f"/tmp/anthropic-apply-{_dt.datetime.now().strftime('%Y%m%d-%H%M%S')}-{os.getpid()}"
    )
    job_dir.mkdir(parents=True, exist_ok=True)
    print(f"apply: job_dir={job_dir}", file=sys.stderr)

    results: list[dict] = []
    actions = plan.get("actions") or []
    for action in actions:
        step = action["step"]
        kind = action["kind"]
        tgt = action["target"]
        v = action.get("variables", {})
        if kind == "edge_account_tier":
            edge_id = tgt["edge_id"]
            account_name = tgt["account_name"]
            label = f"step{step:02d}-edge-{edge_id}-{kind}-{account_name}".replace("/", "-")
            sql = render_edge_account_tier_sql(v["account_name"], v["stability_tier"], edge_id)
            region, instance_id, target_label = _resolve_edge_target(edge_id, edge_matrix)
        elif kind == "prod_stub_pool":
            account_id = v["account_id"]
            account_name = tgt["account_name"]
            label = f"step{step:02d}-prod-{kind}-{account_name}".replace("/", "-")
            sql = render_prod_stub_pool_sql(
                int(account_id), account_name,
                bool(v["pool_mode_enabled"]), int(v["pool_mode_retry_count"]),
            )
            region, instance_id, target_label = _resolve_prod_target()
        else:
            fail(f"unknown action.kind {kind!r} (orchestrator handles edge_account_tier | prod_stub_pool)")
            return 2  # unreachable, pacifies static analysis

        sql_path = job_dir / f"{label}.sql"
        sql_path.write_text(sql)
        sql_b64 = base64.b64encode(sql.encode("utf-8")).decode("ascii")
        print(f"apply: step{step:02d} {kind} → {target_label}  (sql={sql_path})",
              file=sys.stderr)
        stdout, cid, ssm_ok, stderr = ssm_run_sql_b64(
            region, instance_id, sql_b64,
            f"apply step {step} {kind} on {target_label}",
        )
        # edge_account_tier template returns relation rows, not jsonb — we
        # trust the transaction commit and defer field-level verification
        # to Stage 5 verify.
        result = {
            "step": step,
            "kind": kind,
            "target_label": target_label,
            "sql_path": str(sql_path),
            "ssm_command_id": cid,
            "ssm_ok": ssm_ok,
            "stdout_preview": stdout[-1200:],
            "stderr_preview": stderr,
            "expected_after": action.get("expected_after"),
            "ok": ssm_ok,
        }
        if not ssm_ok:
            result["error"] = (
                "SSM ResponseCode != 0; remote SQL likely failed (DO-block RAISE or "
                "psql ON_ERROR_STOP). Inspect via: "
                f"aws ssm get-command-invocation --region {region} "
                f"--instance-id {instance_id} --command-id {cid}"
            )
        results.append(result)
        if not result["ok"]:
            print(f"apply: step{step:02d} FAILED — stopping. cid={cid}", file=sys.stderr)
            break

    success = all(r["ok"] for r in results) and len(results) == len(actions)
    report = {
        "version": 2,
        "applied_at": now_utc_iso(),
        "job_dir": str(job_dir),
        "plan_path": str(plan_path),
        "plan_kind": plan.get("kind"),
        "intent": plan.get("intent"),
        "total_steps": len(actions),
        "completed_steps": len(results),
        "success": success,
        "results": results,
    }
    out_str = json.dumps(report, indent=2, ensure_ascii=False)
    (job_dir / "apply-report.json").write_text(out_str)
    if args.json:
        print(out_str)
    else:
        print(f"apply: success={success} completed={len(results)}/{len(actions)} job_dir={job_dir}")
        for r in results:
            tag = "OK" if r["ok"] else "FAIL"
            print(f"  [{tag}] step{r['step']:02d} {r['kind']} → {r['target_label']}  cid={r['ssm_command_id']}")
    return 0 if success else 1


# --------------------------------------------------------------------------
# Stage 5 — verify
# --------------------------------------------------------------------------

def cmd_verify(args: argparse.Namespace) -> int:
    plan_path = pathlib.Path(args.plan)
    plan = load_json_file(plan_path, "plan")

    snap_path = pathlib.Path(args.snapshot_out) if args.snapshot_out else pathlib.Path(
        f"/tmp/anthropic-verify-snap-{_dt.datetime.now().strftime('%Y%m%d-%H%M%S')}.json"
    )
    print(f"verify: capturing fresh snapshot → {snap_path}", file=sys.stderr)
    plan_needs_prod = any(
        (a.get("kind") == "prod_stub_pool") for a in (plan.get("actions") or [])
    )
    snap_args = argparse.Namespace(
        out=str(snap_path),
        allow_planned=args.allow_planned,
        skip_prod=not plan_needs_prod and bool(getattr(args, "skip_prod", False)),
    )
    rc = cmd_snapshot(snap_args)
    if rc != 0:
        fail(f"verify: re-snapshot exited {rc}")
    snap = load_json_file(snap_path, "verify snapshot")

    drift: list[dict] = []
    for action in plan.get("actions") or []:
        kind = action.get("kind")
        tgt = action["target"]
        exp = action.get("expected_after") or {}
        live: dict | None = None
        diffs: list[str] = []
        if kind == "edge_account_tier":
            edge = snap.get("edges", {}).get(tgt["edge_id"], {})
            for a in edge.get("oauth_accounts", []):
                if a.get("name") == tgt["account_name"]:
                    live = a
                    break
        elif kind == "prod_stub_pool":
            prod = snap.get("prod") or {}
            if prod.get("error") or prod.get("skipped_reason"):
                diffs.append(f"verify snapshot lacks prod view: "
                             f"{prod.get('error') or prod.get('skipped_reason')}")
            else:
                # account_id is the trustworthy PK; name is also checked downstream
                # in apply WHERE, but here we match by id since it cannot collide.
                want_id = tgt.get("account_id")
                for s in prod.get("anthropic_stubs", []):
                    if s.get("id") == want_id:
                        live = s
                        break
        else:
            diffs.append(f"verify: unknown action.kind {kind!r}")
        if live is None and not diffs:
            diffs.append("target not found in live snapshot")
        if live is not None:
            for k, want in exp.items():
                if live.get(k) != want:
                    diffs.append(f"{k}: live={live.get(k)} expected={want}")
        if diffs:
            drift.append({
                "step": action["step"],
                "kind": kind,
                "target": tgt,
                "diffs": diffs,
            })

    report = {
        "version": 2,
        "verified_at": now_utc_iso(),
        "plan_path": str(plan_path),
        "snapshot_path": str(snap_path),
        "total_actions": len(plan.get("actions") or []),
        "drift_count": len(drift),
        "drift": drift,
    }
    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        print(f"verify: drift_count={report['drift_count']}/{report['total_actions']}")
        for d in drift:
            tgt = d["target"]
            print(f"  [DRIFT] step{d['step']:02d} {d['kind']} edge={tgt['edge_id']} account={tgt['account_name']}")
            for diff in d["diffs"]:
                print(f"      {diff}")
    return 1 if drift else 0


# --------------------------------------------------------------------------
# Dispatch
# --------------------------------------------------------------------------

def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__.split("\n\n", 1)[0] if __doc__ else "",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    sub = ap.add_subparsers(dest="cmd", required=True)

    sp = sub.add_parser("snapshot",
                        help="pull each deployable edge's anthropic OAuth accounts + prod anthropic api-key stubs into one JSON")
    sp.add_argument("--out", help="write snapshot JSON to this path (otherwise stdout)")
    sp.add_argument("--allow-planned", action="store_true",
                    help="include planned edges (per edge-targets.json)")
    sp.add_argument("--skip-prod", action="store_true",
                    help="skip the prod stub query (offline / lab runs that only need edge data)")
    sp.set_defaults(handler=cmd_snapshot)

    sp = sub.add_parser("check", help="run edge OAuth stability guard for each edge in scope")
    sp.add_argument("--snapshot", help="snapshot JSON path; used to discover edge IDs in scope")
    sp.add_argument("--allow-planned", action="store_true")
    sp.add_argument("--json", action="store_true")
    sp.set_defaults(handler=cmd_check)

    sp = sub.add_parser("plan-edge-account-tier",
                        help="declare an edge OAuth tier change; emit plan JSON")
    sp.add_argument("--edge-id", "--edge", dest="edge_id", required=True)
    sp.add_argument("--account-name", "--account", dest="account_name", required=True)
    sp.add_argument("--tier", required=True, help="l1 / l2 / l3 / l4 / l5")
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.add_argument(
        "--force-template-rewrite",
        action="store_true",
        help=(
            "skip the fields_match noop short-circuit and always emit an action. "
            "Required when re-applying the same tier to overwrite credentials-side "
            "fields (e.g. temp_unschedulable_rules) that snapshot/verify do not "
            "track but the apply template rewrites unconditionally."
        ),
    )
    sp.set_defaults(handler=cmd_plan_edge_account_tier)

    sp = sub.add_parser(
        "plan-tier-bump",
        help="re-apply a tier's baseline to every account on that tier (one multi-action plan)")
    sp.add_argument("--tier", required=True, help="l1 / l2 / l3 / l4 / l5")
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.add_argument(
        "--force-template-rewrite",
        action="store_true",
        help=(
            "include accounts whose live fields already match the baseline "
            "(otherwise they are skipped as no-ops). Use when only credentials-side "
            "fields changed."
        ),
    )
    sp.set_defaults(handler=cmd_plan_tier_bump)

    sp = sub.add_parser(
        "plan-stub-pool",
        help="enable pool_mode on every prod anthropic stub matching the base_url policy")
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.add_argument(
        "--force-template-rewrite",
        action="store_true",
        help=(
            "skip the live-fields-match noop short-circuit and re-emit an action "
            "for every matching stub. Use if you suspect the snapshot is stale "
            "or want to rewrite credentials regardless of the live JSONB shape."
        ),
    )
    sp.set_defaults(handler=cmd_plan_stub_pool)

    sp = sub.add_parser("apply",
                        help="execute a plan: render apply SQL from JSON, run via SSM")
    sp.add_argument("--plan", required=True)
    sp.add_argument("--confirm", required=True,
                    help=f"must be exactly: {CONFIRM_CODE}")
    sp.add_argument("--job-dir", help="where to write rendered SQL + apply-report.json")
    sp.add_argument("--json", action="store_true")
    sp.set_defaults(handler=cmd_apply)

    sp = sub.add_parser("verify",
                        help="re-snapshot and compare every action's expected_after vs live")
    sp.add_argument("--plan", required=True)
    sp.add_argument("--snapshot-out", help="path to write the fresh snapshot used for verify")
    sp.add_argument("--allow-planned", action="store_true")
    sp.add_argument("--skip-prod", action="store_true",
                    help="skip the re-snapshot's prod query when the plan has no prod_stub_pool actions")
    sp.add_argument("--json", action="store_true")
    sp.set_defaults(handler=cmd_verify)

    args = ap.parse_args()
    return args.handler(args)


if __name__ == "__main__":
    sys.exit(main())
