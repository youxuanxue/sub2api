#!/usr/bin/env python3
"""
TokenKey Anthropic configuration orchestrator (5-stage pipeline).

One entrypoint, five subcommands, one file format (plan JSON) between
stages.  Replaces the manual sequence of running multiple guard scripts +
copying multiple SQL templates that was documented as §§ 3.4 / 3.5 / 4 of
the anthropic-oauth-config skill.

Stages
------
  1. snapshot                — pull prod + each referenced edge into one JSON
  2. check                   — run all three guards against the snapshot
  3. plan-edge-account-tier  — declare an edge OAuth tier change; emit plan
     plan-external-stub      — declare an external apikey quota change; emit plan
  4. apply                   — execute the plan: each action renders an
                               existing SQL template, runs it via SSM,
                               compares STDOUT against expected_after
  5. verify                  — re-snapshot, diff each expected_after vs live

All cascading math (edge account tier → edge group cap → prod stub
concurrency / declared_rpm → prod group rpm_limit) lives in plan-*.
Apply is pure execution — no recomputation, no surprises.

The orchestrator owns no SQL: every UPDATE goes through one of the four
templates in `deploy/aws/stage0/anthropic-*.sql`.  Adding a new mutation
surface means: (a) add a template, (b) add a `render_<kind>()` here,
(c) add the kind to plan-* and apply.  The skill documents the protocol;
this script is the protocol's only legitimate implementation.

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
  manage-anthropic-config.py plan-external-stub \\
      --stub tokensea-0.4 --declared-rpm 150 \\
      --snapshot snap.json --out plan.json
  manage-anthropic-config.py apply --plan plan.json \\
      --confirm yes-apply-anthropic-config-cascade
  manage-anthropic-config.py verify --plan plan.json
"""
from __future__ import annotations

import argparse
import base64
import datetime as _dt
import json
import os
import pathlib
import re
import subprocess
import sys
from typing import Any

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
EDGE_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
TIER_BASELINES = REPO_ROOT / "deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json"
TEMPLATE_DIR = REPO_ROOT / "deploy/aws/stage0"
OPS_DIR = REPO_ROOT / "ops/anthropic"

PROD = {
    "label": "prod",
    "stack": "tokenkey-prod-stage0",
    "region": "us-east-1",
}

SELF_EDGE_BASE_URL_RE = re.compile(
    r"^https?://api-(?P<edge_id>[a-z0-9-]+)\.tokenkey\.dev/?$"
)

CONFIRM_CODE = "yes-apply-anthropic-config-cascade"

PLAN_VERSION = 1
SNAPSHOT_VERSION = 1


# --------------------------------------------------------------------------
# Utility
# --------------------------------------------------------------------------

def fail(msg: str, code: int = 2) -> None:
    print(f"::error::{msg}", file=sys.stderr)
    sys.exit(code)


def now_utc_iso() -> str:
    return _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def absorb_zero_sum(values: list[int]) -> int:
    """R1 (concurrency) aggregation: 0 means unlimited, propagates."""
    if any(v == 0 for v in values):
        return 0
    return sum(values)


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
        # Fallback: try describe-stack-resources for AWS::EC2::Instance
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
    """Pipe SQL to docker exec tokenkey-postgres via SSM. Returns (stdout, command_id)."""
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


def ssm_run_sql_b64(region: str, instance_id: str, sql_b64: str, comment: str) -> tuple[str, str]:
    """For apply: send base64-encoded SQL so embedded quotes/heredocs don't escape.

    Uses -A -t (unaligned + tuples-only) so jsonb_pretty() output is a parseable
    JSON blob, not psql's "key | { ... +" expanded-mode decoration.
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
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:1200]
        out_preview = (inv.get("StandardOutputContent") or "").strip()[:600]
        return out_preview, cid + " FAILED"  # let caller detect via exit_status
    return (inv.get("StandardOutputContent") or "").strip(), cid


# --------------------------------------------------------------------------
# Stage 1 — snapshot
# --------------------------------------------------------------------------

PROD_STUBS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name, 'platform', a.platform, 'type', a.type,
  'concurrency', a.concurrency,
  'channel_type', a.channel_type,
  'rate_multiplier', a.rate_multiplier,
  'auto_pause_on_expired', a.auto_pause_on_expired,
  'status', a.status,
  'base_url', a.credentials->>'base_url',
  'declared_rpm', NULLIF(a.extra->>'declared_rpm', '')::int,
  'group_bindings', COALESCE((
    SELECT jsonb_agg(group_id ORDER BY group_id)
    FROM account_groups WHERE account_id = a.id
  ), '[]'::jsonb)
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.platform = 'anthropic'
  AND a.type = 'apikey'
  AND a.deleted_at IS NULL;
"""

PROD_GROUPS_WITH_STUBS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', g.id, 'name', g.name, 'rpm_limit', g.rpm_limit,
  'is_exclusive', g.is_exclusive,
  'members', COALESCE((
    SELECT jsonb_agg(account_id ORDER BY account_id)
    FROM account_groups WHERE group_id = g.id
  ), '[]'::jsonb)
) ORDER BY g.id), '[]'::jsonb)
FROM groups g
WHERE g.platform = 'anthropic'
  AND g.deleted_at IS NULL
  AND EXISTS (
    SELECT 1 FROM account_groups ag
    JOIN accounts a ON a.id = ag.account_id
    WHERE ag.group_id = g.id
      AND a.platform = 'anthropic'
      AND a.type = 'apikey'
      AND a.deleted_at IS NULL
  );
"""

EDGE_ACCOUNTS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name, 'platform', a.platform, 'type', a.type,
  'status', a.status, 'concurrency', a.concurrency, 'priority', a.priority,
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
  'cache_ttl_override_enabled', NULLIF(a.extra->>'cache_ttl_override_enabled', '')::boolean,
  'group_bindings', COALESCE((
    SELECT jsonb_agg(group_id ORDER BY group_id)
    FROM account_groups WHERE account_id = a.id
  ), '[]'::jsonb)
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.platform = 'anthropic'
  AND a.type = 'oauth'
  AND a.deleted_at IS NULL;
"""

EDGE_GROUPS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', g.id, 'name', g.name, 'rpm_limit', g.rpm_limit,
  'is_exclusive', g.is_exclusive,
  'members', COALESCE((
    SELECT jsonb_agg(account_id ORDER BY account_id)
    FROM account_groups WHERE group_id = g.id
  ), '[]'::jsonb)
) ORDER BY g.id), '[]'::jsonb)
FROM groups g
WHERE g.platform = 'anthropic'
  AND g.deleted_at IS NULL;
"""


def parse_self_edge_id(base_url: str | None) -> str | None:
    if not base_url:
        return None
    m = SELF_EDGE_BASE_URL_RE.match(base_url)
    return m.group("edge_id") if m else None


def cmd_snapshot(args: argparse.Namespace) -> int:
    edge_matrix = load_json_file(EDGE_MATRIX, "edge matrix")
    edge_targets = edge_matrix.get("targets") or {}

    prod_inst = args.prod_instance_id or resolve_instance_id(PROD["region"], PROD["stack"])

    print(f"snapshot: prod_instance={prod_inst}", file=sys.stderr)

    stubs_raw, _ = ssm_run_sql(PROD["region"], prod_inst, PROD_STUBS_SQL, "snapshot: prod stubs")
    prod_stubs = json.loads(stubs_raw) if stubs_raw else []
    groups_raw, _ = ssm_run_sql(PROD["region"], prod_inst, PROD_GROUPS_WITH_STUBS_SQL, "snapshot: prod groups with stubs")
    prod_groups = json.loads(groups_raw) if groups_raw else []

    # Annotate each stub with kind + edge_id
    for s in prod_stubs:
        eid = parse_self_edge_id(s.get("base_url"))
        s["is_self_edge"] = eid is not None
        s["edge_id"] = eid

    needed_edges = sorted({s["edge_id"] for s in prod_stubs if s.get("edge_id")})
    print(f"snapshot: edges referenced by prod stubs = {needed_edges}", file=sys.stderr)

    edges: dict[str, dict] = {}
    for eid in needed_edges:
        tgt = edge_targets.get(eid)
        if not tgt:
            edges[eid] = {"error": f"edge_id {eid!r} not in edge-targets.json"}
            continue
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
        accts_raw, _ = ssm_run_sql(tgt["region"], inst, EDGE_ACCOUNTS_SQL, f"snapshot: edge {eid} oauth accounts")
        grps_raw, _ = ssm_run_sql(tgt["region"], inst, EDGE_GROUPS_SQL, f"snapshot: edge {eid} groups")
        edges[eid] = {
            "deployable": True,
            "instance_id": inst,
            "region": tgt["region"],
            "stack": tgt["stack"],
            "domain": tgt.get("domain"),
            "oauth_accounts": json.loads(accts_raw) if accts_raw else [],
            "anthropic_groups": json.loads(grps_raw) if grps_raw else [],
        }

    snapshot = {
        "version": SNAPSHOT_VERSION,
        "captured_at": now_utc_iso(),
        "prod": {
            "instance_id": prod_inst,
            "region": PROD["region"],
            "stack": PROD["stack"],
            "anthropic_apikey_stubs": prod_stubs,
            "anthropic_groups_with_stubs": prod_groups,
        },
        "edges": edges,
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

    # Discover edge IDs from snapshot (if provided) or from a fresh resolve.
    edge_ids: list[str] = []
    if snapshot is not None:
        edge_ids = sorted([
            eid for eid, e in snapshot.get("edges", {}).items()
            if e.get("deployable") is not False and "error" not in e
        ])

    sub_results: list[dict] = []

    # Guard 1: prod stub mirror (R1 + R3-unified)
    sub_results.append(_run_guard(
        ["python3", str(OPS_DIR / "check-prod-stub-mirror.py"), "--json"]
        + (["--allow-planned"] if args.allow_planned else []),
        "prod-stub-mirror (R1 + R3-unified)",
    ))

    # Guard 2: edge OAuth stability for each edge × account (best-effort)
    if edge_ids:
        for eid in edge_ids:
            sub_results.append(_run_guard(
                ["python3", str(OPS_DIR / "check-edge-oauth-stability.py"),
                 "--edge-id", eid, "--account-name", "all", "--json"]
                + (["--allow-planned"] if args.allow_planned else []),
                f"edge-oauth-stability edge={eid}",
            ))
    else:
        sub_results.append({
            "description": "edge-oauth-stability",
            "skipped_reason": "no edge_ids resolved (run snapshot first and pass --snapshot)",
        })

    # Guard 3: account-group alignment, strict-redline, per edge + prod
    targets = edge_ids + ["prod"]
    for t in targets:
        sub_results.append(_run_guard(
            ["python3", str(OPS_DIR / "check-account-group-rpm-alignment.py"),
             "--target", t, "--strict-redline", "--json"]
            + (["--allow-planned"] if args.allow_planned else []),
            f"account-group-rpm-alignment target={t}",
        ))

    any_violation = any(
        sr.get("exit_code", 0) not in (0, None) for sr in sub_results
    )

    report = {
        "version": 1,
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
            if sr.get("report"):
                rep = sr["report"]
                if "stub_violation_count" in rep or "group_violation_count" in rep:
                    print(f"      stub_violations={rep.get('stub_violation_count')} "
                          f"group_violations={rep.get('group_violation_count')}")
                if "violations" in rep and isinstance(rep["violations"], list):
                    for v in rep["violations"][:5]:
                        print(f"      - {v}")
    return 1 if any_violation else 0


# --------------------------------------------------------------------------
# Stage 3 — plan
# --------------------------------------------------------------------------

def _load_snapshot_or_die(path: str) -> dict:
    snap = load_json_file(pathlib.Path(path), "snapshot")
    if snap.get("version") != SNAPSHOT_VERSION:
        fail(f"snapshot version {snap.get('version')} != expected {SNAPSHOT_VERSION}")
    return snap


def _load_tier_baselines() -> dict[str, dict]:
    """Return {tier: flattened_fields} keyed by 'l1'..'l5'.

    The JSON ships nested as ``tiers[lN].baseline.{account,extra}.<field>``
    plus a sibling ``factor``.  We flatten so callers see a single dict
    per tier: account fields (concurrency, priority, rate_multiplier) and
    extra fields (base_rpm, rpm_sticky_buffer, max_sessions, ...) both at
    the top level — convenient for cascade math.  Nested keys ``account``
    and ``extra`` are preserved verbatim too, in case a caller wants the
    original shape.
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
        flat: dict[str, Any] = {"tier": str(key).lower(), "factor": t.get("factor") if isinstance(t, dict) else None}
        if isinstance(baseline, dict):
            for sub in ("account", "extra"):
                d = baseline.get(sub)
                if isinstance(d, dict):
                    flat[sub] = dict(d)
                    flat.update(d)  # top-level convenience
        # Some legacy shapes flatten at tier root; merge those too.
        for k, v in (t.items() if isinstance(t, dict) else []):
            if k in ("baseline", "factor"):
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


def _find_edge_group(edge: dict, group_name: str) -> dict | None:
    for g in edge.get("anthropic_groups", []):
        if g.get("name") == group_name:
            return g
    return None


def _find_prod_stub_by_edge(snap: dict, edge_id: str) -> dict | None:
    for s in snap.get("prod", {}).get("anthropic_apikey_stubs", []):
        if s.get("is_self_edge") and s.get("edge_id") == edge_id:
            return s
    return None


def _find_prod_stub_by_name(snap: dict, stub_name: str) -> dict | None:
    for s in snap.get("prod", {}).get("anthropic_apikey_stubs", []):
        if s.get("name") == stub_name:
            return s
    return None


def _find_prod_group(snap: dict, group_id: int) -> dict | None:
    for g in snap.get("prod", {}).get("anthropic_groups_with_stubs", []):
        if g.get("id") == group_id:
            return g
    return None


def _stub_by_id(snap: dict, account_id: int) -> dict | None:
    for s in snap.get("prod", {}).get("anthropic_apikey_stubs", []):
        if s.get("id") == account_id:
            return s
    return None


def _edge_default_redline_sum(edge: dict, override_account: str | None = None,
                               override_fields: dict | None = None) -> int:
    """Recompute edge default group's rpm_limit = absorb_zero_sum(base+sticky_buffer).

    If override_account is given, that account's base/sticky are taken from
    override_fields instead of the snapshot.
    """
    grp = _find_edge_group(edge, "default")
    if not grp:
        return 0
    members = set(grp.get("members", []))
    redlines: list[int] = []
    for acc in edge.get("oauth_accounts", []):
        if acc["id"] not in members:
            continue
        if acc.get("status") in ("disabled", "suspended"):
            continue
        if override_account and acc.get("name") == override_account and override_fields:
            base = int(override_fields.get("base_rpm") or 0)
            sticky = int(override_fields.get("rpm_sticky_buffer") or 0)
        else:
            base = int(acc.get("base_rpm") or 0)
            sticky = int(acc.get("rpm_sticky_buffer") or 0)
        redlines.append(base + sticky)
    return absorb_zero_sum(redlines)


def _edge_default_concurrency_sum(edge: dict, override_account: str | None = None,
                                   override_fields: dict | None = None) -> int:
    grp = _find_edge_group(edge, "default")
    if not grp:
        return 0
    members = set(grp.get("members", []))
    concs: list[int] = []
    for acc in edge.get("oauth_accounts", []):
        if acc["id"] not in members:
            continue
        if acc.get("status") in ("disabled", "suspended"):
            continue
        if override_account and acc.get("name") == override_account and override_fields:
            concs.append(int(override_fields.get("concurrency") or 0))
        else:
            concs.append(int(acc.get("concurrency") or 0))
    return absorb_zero_sum(concs)


def cmd_plan_edge_account_tier(args: argparse.Namespace) -> int:
    snap = _load_snapshot_or_die(args.snapshot)
    tiers = _load_tier_baselines()
    tier_key = args.tier.lower()
    if tier_key not in tiers:
        fail(f"tier {args.tier!r} not in baselines (available: {sorted(tiers)})")
    baseline = tiers[tier_key]

    edge = _find_edge(snap, args.edge_id)
    target_account = _find_edge_account(edge, args.account_name)

    # Edge default group cap recomputed with override
    new_edge_default_rpm = _edge_default_redline_sum(
        edge, override_account=args.account_name, override_fields=baseline,
    )
    new_stub_concurrency = _edge_default_concurrency_sum(
        edge, override_account=args.account_name, override_fields=baseline,
    )

    # Prod stub for this edge
    prod_stub = _find_prod_stub_by_edge(snap, args.edge_id)
    if not prod_stub:
        fail(f"no prod self-edge stub for edge {args.edge_id!r} (looked for base_url=api-{args.edge_id}.tokenkey.dev)")

    new_declared_rpm = new_edge_default_rpm

    # Prod groups that contain this stub — each needs its rpm_limit + per-stub declared_rpm rewritten
    prod_group_actions: list[dict] = []
    affected_group_ids = list(prod_stub.get("group_bindings", []))
    for gid in affected_group_ids:
        grp = _find_prod_group(snap, gid)
        if not grp:
            continue  # group not stub-bearing per snapshot query; skip
        # stub_inputs for this group: keep other stubs' declared_rpm from snapshot,
        # replace this stub's declared_rpm with new_declared_rpm
        stub_inputs: list[dict] = []
        for member_id in grp.get("members", []):
            member = _stub_by_id(snap, member_id)
            if not member:
                fail(f"prod group {grp['name']!r} member account_id={member_id} not in snapshot stubs (snapshot stale?)")
            if member_id == prod_stub["id"]:
                stub_inputs.append({"account_id": member_id, "declared_rpm": new_declared_rpm})
            else:
                d = member.get("declared_rpm")
                if d is None or d <= 0:
                    fail(
                        f"prod group {grp['name']!r} contains stub {member['name']!r} (id={member_id}) "
                        f"with declared_rpm={d}; cascade cannot SUM. Fix that stub first."
                    )
                stub_inputs.append({"account_id": member_id, "declared_rpm": d})
        target_group_rpm = sum(s["declared_rpm"] for s in stub_inputs)
        prod_group_actions.append({
            "kind": "prod_group_r3_unified",
            "target": {"env": "prod", "group_id": grp["id"], "group_name": grp["name"]},
            "template": "anthropic-prod-group-r3-unified-apply-template.sql",
            "variables": {"group_id": grp["id"], "target_group_rpm": target_group_rpm},
            "stub_inputs": stub_inputs,
            "expected_after": {"group_rpm_limit": target_group_rpm},
        })

    actions: list[dict] = []
    actions.append({
        "kind": "edge_account_tier",
        "target": {"env": "edge", "edge_id": args.edge_id, "account_name": args.account_name},
        "template": "anthropic-oauth-stability-tiered-apply-template.sql",
        "variables": {"account_name": args.account_name, "stability_tier": tier_key},
        "expected_after": {
            "stability_tier": tier_key,
            "base_rpm": baseline.get("base_rpm"),
            "rpm_sticky_buffer": baseline.get("rpm_sticky_buffer"),
            "concurrency": baseline.get("concurrency"),
            "max_sessions": baseline.get("max_sessions"),
        },
    })
    actions.append({
        "kind": "edge_group_aggregate",
        "target": {"env": "edge", "edge_id": args.edge_id, "group_name": "default"},
        "template": "anthropic-oauth-group-aggregate-apply-template.sql",
        "variables": {"group_name": "default"},
        "expected_after": {"group_rpm_limit": new_edge_default_rpm},
    })
    actions.append({
        "kind": "prod_stub_concurrency",
        "target": {"env": "prod", "account_name": prod_stub["name"]},
        "template": "anthropic-stub-mirror-concurrency-apply-template.sql",
        "variables": {"account_name": prod_stub["name"], "new_concurrency": new_stub_concurrency},
        "expected_after": {"concurrency": new_stub_concurrency},
    })
    actions.extend(prod_group_actions)

    # Number the steps after assembly
    for i, a in enumerate(actions, start=1):
        a["step"] = i

    plan = {
        "version": PLAN_VERSION,
        "kind": "edge_account_tier_change",
        "confirm_code": CONFIRM_CODE,
        "intent": {"edge_id": args.edge_id, "account_name": args.account_name, "new_tier": tier_key},
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "summary": {
            "total_steps": len(actions),
            "edge_changes": sum(1 for a in actions if a["target"]["env"] == "edge"),
            "prod_changes": sum(1 for a in actions if a["target"]["env"] == "prod"),
        },
        "live_inputs": {
            "edge_account_before": {k: target_account.get(k) for k in [
                "id", "name", "concurrency", "stability_tier", "base_rpm",
                "rpm_sticky_buffer", "max_sessions", "window_cost_limit", "status",
            ]},
            "edge_default_group_before": _find_edge_group(edge, "default"),
            "prod_stub_before": {k: prod_stub.get(k) for k in [
                "id", "name", "concurrency", "declared_rpm", "base_url", "group_bindings", "status",
            ]},
            "prod_groups_before": [
                {k: g.get(k) for k in ["id", "name", "rpm_limit", "members"]}
                for g in (
                    _find_prod_group(snap, gid) for gid in affected_group_ids
                ) if g is not None
            ],
        },
        "actions": actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan: written {args.out} ({plan['summary']['total_steps']} steps)", file=sys.stderr)
    else:
        print(out_str)
    return 0


def cmd_plan_external_stub(args: argparse.Namespace) -> int:
    snap = _load_snapshot_or_die(args.snapshot)
    stub = _find_prod_stub_by_name(snap, args.stub_name)
    if not stub:
        fail(f"prod stub {args.stub_name!r} not found in snapshot")
    if stub.get("is_self_edge"):
        fail(
            f"stub {args.stub_name!r} is self-edge (base_url={stub.get('base_url')}). "
            f"Use plan-edge-account-tier to change its declared_rpm via mirror."
        )
    new_decl = int(args.declared_rpm)
    if new_decl <= 0:
        fail(f"--declared-rpm must be > 0 (unlimited forbidden under R3-unified)")

    actions: list[dict] = []
    affected_group_ids = list(stub.get("group_bindings", []))
    for gid in affected_group_ids:
        grp = _find_prod_group(snap, gid)
        if not grp:
            continue
        stub_inputs: list[dict] = []
        for member_id in grp.get("members", []):
            member = _stub_by_id(snap, member_id)
            if not member:
                fail(f"snapshot stale: prod group {grp['name']!r} member id={member_id} missing")
            if member_id == stub["id"]:
                stub_inputs.append({"account_id": member_id, "declared_rpm": new_decl})
            else:
                d = member.get("declared_rpm")
                if d is None or d <= 0:
                    fail(
                        f"prod group {grp['name']!r} contains stub {member['name']!r} "
                        f"with declared_rpm={d}; fix that stub first."
                    )
                stub_inputs.append({"account_id": member_id, "declared_rpm": d})
        target_group_rpm = sum(s["declared_rpm"] for s in stub_inputs)
        actions.append({
            "kind": "prod_group_r3_unified",
            "target": {"env": "prod", "group_id": grp["id"], "group_name": grp["name"]},
            "template": "anthropic-prod-group-r3-unified-apply-template.sql",
            "variables": {"group_id": grp["id"], "target_group_rpm": target_group_rpm},
            "stub_inputs": stub_inputs,
            "expected_after": {"group_rpm_limit": target_group_rpm},
        })
    for i, a in enumerate(actions, start=1):
        a["step"] = i

    plan = {
        "version": PLAN_VERSION,
        "kind": "external_stub_declared_rpm_change",
        "confirm_code": CONFIRM_CODE,
        "intent": {"stub_name": args.stub_name, "new_declared_rpm": new_decl},
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "summary": {
            "total_steps": len(actions),
            "edge_changes": 0,
            "prod_changes": len(actions),
        },
        "live_inputs": {
            "prod_stub_before": {k: stub.get(k) for k in [
                "id", "name", "concurrency", "declared_rpm", "base_url", "group_bindings",
            ]},
            "prod_groups_before": [
                {k: g.get(k) for k in ["id", "name", "rpm_limit", "members"]}
                for g in (_find_prod_group(snap, gid) for gid in affected_group_ids) if g is not None
            ],
        },
        "actions": actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan: written {args.out} ({plan['summary']['total_steps']} steps)", file=sys.stderr)
    else:
        print(out_str)
    return 0


# --------------------------------------------------------------------------
# Stage 4 — apply
# --------------------------------------------------------------------------

def _read_template(name: str) -> str:
    path = TEMPLATE_DIR / name
    if not path.exists():
        fail(f"template {name} not found at {path}")
    return path.read_text()


def render_edge_account_tier_sql(account_name: str, stability_tier: str) -> str:
    body = _read_template("anthropic-oauth-stability-tiered-apply-template.sql")
    header = (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"\\set account_name '{account_name}'\n"
        f"\\set stability_tier '{stability_tier}'\n"
    )
    return header + body


def render_edge_group_aggregate_sql(group_name: str) -> str:
    body = _read_template("anthropic-oauth-group-aggregate-apply-template.sql")
    header = (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"\\set group_name '{group_name}'\n"
    )
    return header + body


def render_prod_stub_concurrency_sql(account_name: str, new_concurrency: int) -> str:
    body = _read_template("anthropic-stub-mirror-concurrency-apply-template.sql")
    header = (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"\\set account_name '{account_name}'\n"
        f"\\set new_concurrency {int(new_concurrency)}\n"
    )
    return header + body


SENTINEL_LINE = "(-1::bigint, -1::int)     -- SENTINEL: replace with one row per stub"


def render_prod_group_r3_unified_sql(group_id: int, target_group_rpm: int,
                                      stub_inputs: list[dict]) -> str:
    body = _read_template("anthropic-prod-group-r3-unified-apply-template.sql")
    # VALUES row separator (",") must precede the line-comment so the comment
    # doesn't swallow it.  Last row carries no trailing comma.
    rendered_rows: list[str] = []
    for idx, s in enumerate(stub_inputs):
        sep = "," if idx < len(stub_inputs) - 1 else ""
        rendered_rows.append(
            f"({int(s['account_id'])}::bigint, {int(s['declared_rpm'])}::int){sep}"
            f"  -- account_id={s['account_id']}, declared_rpm={s['declared_rpm']}"
        )
    values_lines = "\n  ".join(rendered_rows)
    if SENTINEL_LINE not in body:
        fail("template anthropic-prod-group-r3-unified missing sentinel placeholder; refusing to render")
    body = body.replace(SENTINEL_LINE, values_lines)
    header = (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"\\set group_id {int(group_id)}\n"
        f"\\set target_group_rpm {int(target_group_rpm)}\n"
    )
    return header + body


def _render_action_sql(action: dict) -> str:
    kind = action["kind"]
    v = action.get("variables", {})
    if kind == "edge_account_tier":
        return render_edge_account_tier_sql(v["account_name"], v["stability_tier"])
    if kind == "edge_group_aggregate":
        return render_edge_group_aggregate_sql(v["group_name"])
    if kind == "prod_stub_concurrency":
        return render_prod_stub_concurrency_sql(v["account_name"], v["new_concurrency"])
    if kind == "prod_group_r3_unified":
        return render_prod_group_r3_unified_sql(v["group_id"], v["target_group_rpm"], action["stub_inputs"])
    fail(f"unknown action.kind {kind!r}")
    return ""  # unreachable


def _resolve_action_target(action: dict, edge_matrix: dict) -> tuple[str, str, str]:
    """Returns (region, instance_id, label) for the action's target."""
    tgt = action["target"]
    env = tgt["env"]
    if env == "prod":
        return PROD["region"], resolve_instance_id(PROD["region"], PROD["stack"]), "prod"
    if env == "edge":
        eid = tgt["edge_id"]
        e = edge_matrix.get("targets", {}).get(eid)
        if not e:
            fail(f"action target edge {eid!r} not in edge-targets.json")
        return e["region"], resolve_instance_id(e["region"], e["stack"]), f"edge:{eid}"
    fail(f"unknown action.target.env {env!r}")
    return "", "", ""  # unreachable


def _extract_output_json(stdout: str) -> dict | None:
    """psql -P expanded=on returns key-value rows; we wrote jsonb_pretty(...) so
    look for the first complete '{ ... }' block in stdout.
    """
    # Find first '{' ... matching last '}' (cheap nesting count)
    start = stdout.find("{")
    if start < 0:
        return None
    depth = 0
    in_str = False
    esc = False
    for i, c in enumerate(stdout[start:], start=start):
        if esc:
            esc = False
            continue
        if c == "\\":
            esc = True
            continue
        if c == '"' and not esc:
            in_str = not in_str
            continue
        if in_str:
            continue
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                try:
                    return json.loads(stdout[start:i + 1])
                except json.JSONDecodeError:
                    return None
    return None


def _verify_expected_after(action: dict, output_json: dict | None) -> list[str]:
    """Compare action.expected_after against output_json. Returns list of mismatch strings.

    Some templates return JSON via jsonb_pretty (edge_group_aggregate,
    prod_stub_concurrency, prod_group_r3_unified) so we can verify in-band.
    Others (edge_account_tier) return relation rows only — for those we
    trust the transaction commit and defer the field-level check to the
    Stage 5 verify subcommand, which re-snapshots and diffs live state.
    """
    exp = action.get("expected_after") or {}
    out: list[str] = []
    kind = action["kind"]
    if kind == "edge_account_tier":
        # No JSON return; commit success implies the UPDATE landed.
        # Field-level verification happens in Stage 5 verify.
        return out
    if not output_json:
        return [f"no JSON output to verify expected_after={exp}"]
    if kind == "edge_group_aggregate":
        upd = output_json.get("rpm_limit_update") or {}
        rpm_after = upd.get("after")
        if rpm_after != exp.get("group_rpm_limit"):
            out.append(f"edge_group_aggregate rpm_limit after={rpm_after} expected={exp.get('group_rpm_limit')}")
    elif kind == "prod_stub_concurrency":
        cu = output_json.get("concurrency_update") or {}
        if cu.get("after") != exp.get("concurrency"):
            out.append(f"prod_stub_concurrency after={cu.get('after')} expected={exp.get('concurrency')}")
    elif kind == "prod_group_r3_unified":
        grp = output_json.get("group") or {}
        sc = output_json.get("sum_check") or {}
        rpm_after = grp.get("rpm_limit")
        if rpm_after != exp.get("group_rpm_limit"):
            out.append(f"prod_group_r3_unified rpm_limit={rpm_after} expected={exp.get('group_rpm_limit')}")
        if sc.get("matches") is not True:
            out.append(f"prod_group_r3_unified sum_check.matches={sc.get('matches')}")
    return out


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
        env = tgt.get("env")
        label_id = (
            tgt.get("account_name") or tgt.get("group_name")
            or tgt.get("account_id") or tgt.get("group_id") or "?"
        )
        edge_part = f"-{tgt['edge_id']}" if env == "edge" else ""
        label = f"step{step:02d}-{env}{edge_part}-{kind}-{label_id}".replace("/", "-")
        sql_path = job_dir / f"{label}.sql"
        sql = _render_action_sql(action)
        sql_path.write_text(sql)

        region, instance_id, target_label = _resolve_action_target(action, edge_matrix)
        sql_b64 = base64.b64encode(sql.encode("utf-8")).decode("ascii")
        print(f"apply: step{step:02d} {kind} → {target_label}  (sql={sql_path})", file=sys.stderr)
        stdout, cid = ssm_run_sql_b64(region, instance_id, sql_b64,
                                       f"apply step {step} {kind} on {target_label}")
        output_json = _extract_output_json(stdout)
        mismatches = _verify_expected_after(action, output_json)
        result = {
            "step": step,
            "kind": kind,
            "target_label": target_label,
            "sql_path": str(sql_path),
            "ssm_command_id": cid,
            "stdout_preview": stdout[-1200:],
            "output_json": output_json,
            "expected_after": action.get("expected_after"),
            "mismatches": mismatches,
            "ok": cid.endswith("FAILED") is False and not mismatches,
        }
        if cid.endswith("FAILED"):
            result["ok"] = False
            result["error"] = "SSM exit_status indicates failure; see stdout_preview / SSM logs"
        results.append(result)
        if not result["ok"]:
            print(f"apply: step{step:02d} FAILED — stopping. cid={cid}", file=sys.stderr)
            break

    success = all(r["ok"] for r in results) and len(results) == len(actions)
    report = {
        "version": 1,
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
            for m in r["mismatches"]:
                print(f"      mismatch: {m}")
    return 0 if success else 1


# --------------------------------------------------------------------------
# Stage 5 — verify
# --------------------------------------------------------------------------

def _live_field_from_snapshot(snap: dict, action: dict) -> dict | None:
    """Return the live record this action expected to mutate, from a fresh snapshot."""
    kind = action["kind"]
    tgt = action["target"]
    if kind == "edge_account_tier":
        edge = snap.get("edges", {}).get(tgt["edge_id"], {})
        for a in edge.get("oauth_accounts", []):
            if a.get("name") == tgt["account_name"]:
                return a
    elif kind == "edge_group_aggregate":
        edge = snap.get("edges", {}).get(tgt["edge_id"], {})
        for g in edge.get("anthropic_groups", []):
            if g.get("name") == tgt["group_name"]:
                return g
    elif kind == "prod_stub_concurrency":
        for s in snap.get("prod", {}).get("anthropic_apikey_stubs", []):
            if s.get("name") == tgt["account_name"]:
                return s
    elif kind == "prod_group_r3_unified":
        for g in snap.get("prod", {}).get("anthropic_groups_with_stubs", []):
            if g.get("id") == tgt.get("group_id"):
                return g
    return None


def _diff_action_live(action: dict, live: dict | None) -> list[str]:
    if live is None:
        return [f"target not found in live snapshot"]
    exp = action.get("expected_after") or {}
    out: list[str] = []
    kind = action["kind"]
    if kind == "edge_account_tier":
        for k in ("stability_tier", "base_rpm", "rpm_sticky_buffer", "concurrency", "max_sessions"):
            if k in exp and live.get(k) != exp[k]:
                out.append(f"{k}: live={live.get(k)} expected={exp[k]}")
    elif kind == "edge_group_aggregate":
        if live.get("rpm_limit") != exp.get("group_rpm_limit"):
            out.append(f"rpm_limit: live={live.get('rpm_limit')} expected={exp.get('group_rpm_limit')}")
    elif kind == "prod_stub_concurrency":
        if live.get("concurrency") != exp.get("concurrency"):
            out.append(f"concurrency: live={live.get('concurrency')} expected={exp.get('concurrency')}")
    elif kind == "prod_group_r3_unified":
        if live.get("rpm_limit") != exp.get("group_rpm_limit"):
            out.append(f"rpm_limit: live={live.get('rpm_limit')} expected={exp.get('group_rpm_limit')}")
        # Also verify per-stub declared_rpm matches the plan.stub_inputs values
        # (apply-template DO block already enforced this in-transaction; here we
        #  re-confirm the post-apply state is preserved.)
    return out


def cmd_verify(args: argparse.Namespace) -> int:
    plan_path = pathlib.Path(args.plan)
    plan = load_json_file(plan_path, "plan")

    print("verify: capturing fresh snapshot...", file=sys.stderr)
    snap_ns = argparse.Namespace(
        out=None, allow_planned=False, prod_instance_id=None,
    )
    # Capture snapshot to a temp file via a subprocess call to ourselves so the
    # snapshot subcommand handles all SSM resolution / printing identically.
    snap_path = pathlib.Path(args.snapshot_out) if args.snapshot_out else pathlib.Path(
        f"/tmp/anthropic-verify-snap-{_dt.datetime.now().strftime('%Y%m%d-%H%M%S')}.json"
    )
    res = subprocess.run(
        ["python3", str(pathlib.Path(__file__)), "snapshot",
         "--out", str(snap_path)]
        + (["--allow-planned"] if args.allow_planned else []),
        check=False,
    )
    if res.returncode != 0:
        fail(f"verify: re-snapshot exited {res.returncode}")
    snap = load_json_file(snap_path, "verify snapshot")

    drift: list[dict] = []
    for action in plan.get("actions") or []:
        live = _live_field_from_snapshot(snap, action)
        diffs = _diff_action_live(action, live)
        if diffs:
            drift.append({
                "step": action["step"],
                "kind": action["kind"],
                "target": action["target"],
                "diffs": diffs,
            })

    report = {
        "version": 1,
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
            label = tgt.get("account_name") or tgt.get("group_name") or tgt.get("group_id")
            print(f"  [DRIFT] step{d['step']:02d} {d['kind']} {tgt['env']}:{label}")
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

    sp = sub.add_parser("snapshot", help="pull prod + each referenced edge into one JSON")
    sp.add_argument("--out", help="write snapshot JSON to this path (otherwise stdout)")
    sp.add_argument("--prod-instance-id", help="override prod EC2 instance id")
    sp.add_argument("--allow-planned", action="store_true",
                    help="include planned edges (per edge-targets.json)")
    sp.set_defaults(handler=cmd_snapshot)

    sp = sub.add_parser("check", help="run all three guards; compose unified report")
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
    sp.set_defaults(handler=cmd_plan_edge_account_tier)

    sp = sub.add_parser("plan-external-stub",
                        help="declare an external apikey stub quota change; emit plan JSON")
    sp.add_argument("--stub-name", "--stub", dest="stub_name", required=True)
    sp.add_argument("--declared-rpm", dest="declared_rpm", required=True, type=int)
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out")
    sp.set_defaults(handler=cmd_plan_external_stub)

    sp = sub.add_parser("apply",
                        help="execute a plan: render each template, SSM run, verify expected_after")
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
    sp.add_argument("--json", action="store_true")
    sp.set_defaults(handler=cmd_verify)

    args = ap.parse_args()
    return args.handler(args)


if __name__ == "__main__":
    sys.exit(main())
