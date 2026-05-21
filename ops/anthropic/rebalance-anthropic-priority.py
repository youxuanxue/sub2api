#!/usr/bin/env python3
"""
TokenKey Anthropic OAuth priority rebalance orchestrator.

Ranks each edge's anthropic+oauth accounts by remaining 5h/7d usage
window and rewrites accounts.priority so that accounts with more
remaining quota schedule first (smaller priority wins). Same-tier
only — never crosses stability_tier boundaries.

Write surface
-------------
ONE field: accounts.priority on the local edge DB.

Tier baseline (concurrency / base_rpm / sticky_buffer / max_sessions /
window_cost_limit / stability_tier) remains the write surface of
ops/anthropic/manage-anthropic-config.py and MUST NOT be co-written.

Re-apply after every tier-baseline apply — the tier-baseline template
resets priority to the tier base (l1=10, l2=20, l3=30, l4=40, l5=50).

Stages
------
  1. snapshot — pull each deployable edge's anthropic OAuth accounts +
                live utilization (session_window_utilization,
                passive_usage_7d_utilization, passive_usage_sampled_at,
                session_window_end) into one JSON.
  2. plan     — per (edge, stability_tier) bucket: compute remaining
                score, rank, assign new_priority = tier_base + offset
                (0..9). Stale buckets fall to the back. Emit plan JSON.
  3. apply    — render priority-rebalance template per action, run via
                SSM, fail-stop.
  4. verify   — re-snapshot, diff expected_after vs live.

Freshness gate (per user decision, 2026-05-21)
----------------------------------------------
Stale account (passive_usage_sampled_at older than --stale-minutes,
default 120, OR session_window_end in the past, OR utilization fields
missing on an account whose status=active) → treated as FULL load
(remaining_score = 0) and sorted to the tier's back. Inactive accounts
(status != active) are skipped entirely (priority untouched).

Exit codes
----------
  0  command succeeded; for verify, no drift
  1  command ran but produced action-needed report:
     - plan: some accounts were marked stale (still emits plan)
     - apply: at least one step failed
     - verify: at least one drift
  2  setup / SSM / target-resolution / schema error

Usage
-----
  rebalance-anthropic-priority.py snapshot --out snap.json
  rebalance-anthropic-priority.py plan --edge all \\
      --snapshot snap.json --out plan.json [--stale-minutes 120]
  rebalance-anthropic-priority.py apply --plan plan.json \\
      --confirm yes-rebalance-anthropic-priority
  rebalance-anthropic-priority.py verify --plan plan.json
"""
from __future__ import annotations

import argparse
import base64
import datetime as _dt
import json
import os
import pathlib
import subprocess
import sys
from typing import Any

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
EDGE_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
TIER_BASELINES = REPO_ROOT / "deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json"
TEMPLATE_DIR = REPO_ROOT / "deploy/aws/stage0"
APPLY_TEMPLATE_NAME = "anthropic-oauth-priority-rebalance-apply-template.sql"

CONFIRM_CODE = "yes-rebalance-anthropic-priority"

PLAN_VERSION = 1
SNAPSHOT_VERSION = 1

# Tier band geometry: tiers are spaced 10 apart in
# anthropic-oauth-stability-baselines-tiered.json. Allow at most 10
# accounts per tier per edge so offset 0..9 never spills into the next
# tier's band.
MAX_PER_TIER_PER_EDGE = 10


def fail(msg: str, code: int = 2) -> None:
    print(f"::error::{msg}", file=sys.stderr)
    sys.exit(code)


def warn(msg: str) -> None:
    print(f"::warning::{msg}", file=sys.stderr)


def now_utc() -> _dt.datetime:
    return _dt.datetime.now(_dt.timezone.utc)


def now_utc_iso() -> str:
    return now_utc().strftime("%Y-%m-%dT%H:%M:%SZ")


def load_json_file(path: pathlib.Path, what: str) -> Any:
    if not path.exists():
        fail(f"{what} not found: {path}")
    try:
        return json.loads(path.read_text())
    except json.JSONDecodeError as e:
        fail(f"{what} parse error ({path}): {e}")
    return None  # unreachable


# --------------------------------------------------------------------------
# AWS / SSM plumbing  (shape mirrors manage-anthropic-config.py so an
# operator who knows one knows the other; intentionally NOT a shared
# library — these two orchestrators stay decoupled per OPC principle of
# minimum shared mutable surface)
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
                    "--query",
                    "StackResources[?ResourceType=='AWS::EC2::Instance'].PhysicalResourceId | [0]",
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
    remote = (
        "sudo docker exec -i tokenkey-postgres "
        "psql -U tokenkey -d tokenkey -t -A -v ON_ERROR_STOP=1"
    )
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
# Stage 1 — snapshot  (extended SQL with live utilization fields)
# --------------------------------------------------------------------------

EDGE_ACCOUNTS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id,
  'name', a.name,
  'platform', a.platform,
  'type', a.type,
  'status', a.status,
  'priority', a.priority,
  'concurrency', a.concurrency,
  'stability_tier', a.extra->>'stability_tier',
  'session_window_end',
    CASE
      WHEN a.session_window_end IS NULL THEN NULL
      ELSE to_char(a.session_window_end AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
    END,
  'session_window_utilization',
    NULLIF(a.extra->>'session_window_utilization', '')::float8,
  'passive_usage_7d_utilization',
    NULLIF(a.extra->>'passive_usage_7d_utilization', '')::float8,
  'passive_usage_7d_reset',
    NULLIF(a.extra->>'passive_usage_7d_reset', '')::bigint,
  'passive_usage_sampled_at', a.extra->>'passive_usage_sampled_at'
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.platform = 'anthropic'
  AND a.type = 'oauth'
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
        accts_raw, _ = ssm_run_sql(
            tgt["region"], inst, EDGE_ACCOUNTS_SQL,
            f"snapshot: edge {eid} oauth accounts + utilization",
        )
        edges[eid] = {
            "deployable": True,
            "instance_id": inst,
            "region": tgt["region"],
            "stack": tgt["stack"],
            "domain": tgt.get("domain"),
            "oauth_accounts": json.loads(accts_raw) if accts_raw else [],
        }

    snapshot = {
        "version": SNAPSHOT_VERSION,
        "captured_at": now_utc_iso(),
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
# Stage 2 — plan
# --------------------------------------------------------------------------

def _load_snapshot_or_die(path: str) -> dict:
    snap = load_json_file(pathlib.Path(path), "snapshot")
    v = snap.get("version")
    if v != SNAPSHOT_VERSION:
        fail(f"snapshot version {v} != expected {SNAPSHOT_VERSION}")
    return snap


def _load_tier_base_priority() -> dict[str, int]:
    """Return {tier: base_priority} drawn from the canonical tier baseline."""
    raw = load_json_file(TIER_BASELINES, "tier baselines")
    tiers = raw.get("tiers") if isinstance(raw, dict) else None
    if not isinstance(tiers, dict):
        fail(f"unexpected tier baseline shape at {TIER_BASELINES}")
    out: dict[str, int] = {}
    for key, t in tiers.items():
        if not isinstance(t, dict):
            continue
        baseline = t.get("baseline") or {}
        account = baseline.get("account") or {}
        prio = account.get("priority")
        if not isinstance(prio, int):
            fail(f"tier {key!r} has non-integer baseline priority {prio!r}")
        out[str(key).lower()] = prio
    return out


def _parse_utc_iso(raw: str | None) -> _dt.datetime | None:
    """Parse an RFC3339 / ISO-8601 UTC timestamp.

    Used for both ``passive_usage_sampled_at`` and ``session_window_end``;
    they share format (UTC string with trailing Z).
    """
    if not raw:
        return None
    s = raw.replace("Z", "+00:00") if raw.endswith("Z") else raw
    try:
        dt = _dt.datetime.fromisoformat(s)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=_dt.timezone.utc)
    return dt


def _score_account(
    acct: dict, snapshot_at: _dt.datetime, stale_minutes: int
) -> dict[str, Any]:
    """Compute remaining_score and stale flags for one account.

    Returns a dict with: remaining_score (float 0..1, higher = more
    remaining), remaining_5h, remaining_7d, stale (bool), stale_reasons.
    Inactive accounts get stale=True with reason 'status_inactive' so the
    caller can decide to skip them.
    """
    status = (acct.get("status") or "").lower()
    if status != "active":
        return {
            "remaining_score": 0.0,
            "remaining_5h": None,
            "remaining_7d": None,
            "stale": True,
            "stale_reasons": [f"status_inactive:{status or 'unknown'}"],
        }

    reasons: list[str] = []

    util_5h = acct.get("session_window_utilization")
    util_7d = acct.get("passive_usage_7d_utilization")
    sampled_at = _parse_utc_iso(acct.get("passive_usage_sampled_at"))
    window_end = _parse_utc_iso(acct.get("session_window_end"))

    # Freshness gate: stale sampled_at OR sampled_at missing entirely
    if sampled_at is None:
        reasons.append("never_sampled")
    else:
        age_minutes = (snapshot_at - sampled_at).total_seconds() / 60.0
        if age_minutes > stale_minutes:
            reasons.append(f"sampled_at_age_min={age_minutes:.0f}>{stale_minutes}")

    # Window-end gate: if the 5h window already rolled over, the
    # persisted 5h utilization belongs to a past window and is likely
    # stale even if sampled_at is fresh
    if window_end is not None and window_end < snapshot_at:
        reasons.append("session_window_expired")
    elif window_end is None:
        reasons.append("session_window_end_missing")

    # Per user decision (2026-05-21): stale → full load → remaining_5h=0
    if reasons:
        remaining_5h = 0.0
    elif util_5h is None:
        # active + fresh sampled_at + window still in future, but
        # utilization field absent — be conservative
        reasons.append("util_5h_missing")
        remaining_5h = 0.0
    else:
        remaining_5h = max(0.0, min(1.0, 1.0 - float(util_5h)))

    # 7d window is independent — it does not get the same freshness
    # penalty (a 7d util reading from 2h ago is still meaningful).
    # Missing 7d util is treated as full remaining (1.0) because the
    # 5h signal is the dominant input; the 7d signal only constrains
    # accounts that are visibly approaching the weekly cap.
    if util_7d is None:
        remaining_7d = 1.0
    else:
        remaining_7d = max(0.0, min(1.0, 1.0 - float(util_7d)))

    # Composite: take the tighter of the two constraints
    remaining_score = min(remaining_5h, remaining_7d)

    return {
        "remaining_score": remaining_score,
        "remaining_5h": remaining_5h,
        "remaining_7d": remaining_7d,
        "stale": bool(reasons),
        "stale_reasons": reasons,
    }


def _plan_one_edge(
    edge_id: str,
    edge: dict,
    tier_base: dict[str, int],
    snapshot_at: _dt.datetime,
    stale_minutes: int,
) -> tuple[list[dict], list[dict], list[dict]]:
    """Return (actions, skipped_accounts, tier_summaries) for one edge.

    actions: list of plan actions for accounts whose priority changes
    skipped_accounts: inactive or unschedulable accounts
    tier_summaries: per (edge, tier) ranking diagnostic
    """
    accounts = edge.get("oauth_accounts") or []

    # Bucket by stability_tier
    buckets: dict[str, list[dict]] = {}
    skipped: list[dict] = []
    for a in accounts:
        tier = (a.get("stability_tier") or "").lower()
        status = (a.get("status") or "").lower()
        if status != "active":
            skipped.append({
                "edge_id": edge_id, "account_id": a.get("id"),
                "account_name": a.get("name"), "reason": f"status={status or 'unknown'}",
                "current_priority": a.get("priority"),
            })
            continue
        if tier not in tier_base:
            skipped.append({
                "edge_id": edge_id, "account_id": a.get("id"),
                "account_name": a.get("name"),
                "reason": f"unknown_or_missing_stability_tier={tier!r}",
                "current_priority": a.get("priority"),
            })
            continue
        buckets.setdefault(tier, []).append(a)

    actions: list[dict] = []
    summaries: list[dict] = []

    for tier in sorted(buckets):
        bucket = buckets[tier]
        if len(bucket) > MAX_PER_TIER_PER_EDGE:
            fail(
                f"edge {edge_id} tier {tier}: {len(bucket)} accounts exceeds "
                f"MAX_PER_TIER_PER_EDGE={MAX_PER_TIER_PER_EDGE}; rebalance would "
                f"spill into next tier band. Split into a separate tier or "
                f"raise the band geometry first."
            )

        # Score each
        scored = []
        for a in bucket:
            s = _score_account(a, snapshot_at, stale_minutes)
            scored.append({"account": a, **s})

        # Sort: stale accounts always after fresh ones; within same
        # staleness, higher remaining_score first; tie-break by id for
        # determinism so repeated plans are stable
        scored.sort(
            key=lambda r: (
                1 if r["stale"] else 0,
                -float(r["remaining_score"]),
                int(r["account"].get("id") or 0),
            )
        )

        base = tier_base[tier]
        for rank, r in enumerate(scored):
            a = r["account"]
            new_priority = base + rank
            old_priority = a.get("priority")
            if old_priority == new_priority:
                # no-op for this account, but still surface it in
                # diagnostics so operator sees the full ordering
                continue
            actions.append({
                "step": 0,  # filled in by caller after merging across edges
                "kind": "account_priority",
                "target": {
                    "env": "edge",
                    "edge_id": edge_id,
                    "account_id": a.get("id"),
                    "account_name": a.get("name"),
                },
                "ranking": {
                    "stability_tier": tier,
                    "tier_base_priority": base,
                    "tier_rank": rank,
                    "remaining_score": round(r["remaining_score"], 4),
                    "remaining_5h": r["remaining_5h"],
                    "remaining_7d": r["remaining_7d"],
                    "stale": r["stale"],
                    "stale_reasons": r["stale_reasons"],
                },
                "current": {"priority": old_priority},
                "expected_after": {"priority": new_priority},
            })

        summaries.append({
            "edge_id": edge_id,
            "stability_tier": tier,
            "tier_base_priority": base,
            "account_count": len(scored),
            "stale_count": sum(1 for r in scored if r["stale"]),
            "ordering": [
                {
                    "rank": i,
                    "account_id": r["account"].get("id"),
                    "account_name": r["account"].get("name"),
                    "remaining_score": round(r["remaining_score"], 4),
                    "old_priority": r["account"].get("priority"),
                    "new_priority": base + i,
                    "stale": r["stale"],
                    "stale_reasons": r["stale_reasons"],
                }
                for i, r in enumerate(scored)
            ],
        })

    return actions, skipped, summaries


def cmd_plan(args: argparse.Namespace) -> int:
    snap = _load_snapshot_or_die(args.snapshot)
    tier_base = _load_tier_base_priority()

    requested_edges: list[str]
    if args.edge_id == "all":
        requested_edges = sorted([
            eid for eid, e in (snap.get("edges") or {}).items()
            if e.get("deployable") is not False and "error" not in e
        ])
    else:
        requested_edges = [args.edge_id]

    snapshot_at = _parse_utc_iso(snap.get("captured_at")) or now_utc()

    all_actions: list[dict] = []
    all_skipped: list[dict] = []
    all_summaries: list[dict] = []

    for eid in requested_edges:
        edge = (snap.get("edges") or {}).get(eid)
        if not edge:
            fail(f"edge {eid!r} not present in snapshot; re-run snapshot")
        if edge.get("error") or edge.get("skipped_reason"):
            fail(
                f"edge {eid!r} not snapshotted: "
                f"{edge.get('error') or edge.get('skipped_reason')}"
            )
        actions, skipped, summaries = _plan_one_edge(
            eid, edge, tier_base, snapshot_at, args.stale_minutes,
        )
        all_actions.extend(actions)
        all_skipped.extend(skipped)
        all_summaries.extend(summaries)

    # Assign step numbers across the merged action list
    for i, a in enumerate(all_actions, start=1):
        a["step"] = i

    any_stale = any(a["ranking"]["stale"] for a in all_actions)

    plan = {
        "version": PLAN_VERSION,
        "kind": "anthropic_priority_rebalance",
        "confirm_code": CONFIRM_CODE,
        "intent": {
            "edges": requested_edges,
            "stale_minutes": args.stale_minutes,
            "max_per_tier_per_edge": MAX_PER_TIER_PER_EDGE,
        },
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "summary": {
            "total_actions": len(all_actions),
            "skipped_accounts": len(all_skipped),
            "tier_buckets": len(all_summaries),
            "any_stale": any_stale,
        },
        "tier_summaries": all_summaries,
        "skipped_accounts": all_skipped,
        "actions": all_actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(
            f"plan: written {args.out}  edges={requested_edges} "
            f"actions={len(all_actions)} skipped={len(all_skipped)} "
            f"any_stale={any_stale}",
            file=sys.stderr,
        )
    else:
        print(out_str)

    return 1 if any_stale else 0


# --------------------------------------------------------------------------
# Stage 3 — apply
# --------------------------------------------------------------------------

def _read_template() -> str:
    path = TEMPLATE_DIR / APPLY_TEMPLATE_NAME
    if not path.exists():
        fail(f"apply template not found at {path}")
    return path.read_text()


def render_apply_sql(account_name: str, new_priority: int) -> str:
    body = _read_template()
    header = (
        f"-- Auto-generated by rebalance-anthropic-priority.py at {now_utc_iso()}\n"
        f"\\set account_name '{account_name}'\n"
        f"\\set new_priority {int(new_priority)}\n"
    )
    return header + body


def _resolve_edge_target(edge_id: str, edge_matrix: dict) -> tuple[str, str, str]:
    e = (edge_matrix.get("targets") or {}).get(edge_id)
    if not e:
        fail(f"edge {edge_id!r} not in edge-targets.json")
    return e["region"], resolve_instance_id(e["region"], e["stack"]), f"edge:{edge_id}"


def cmd_apply(args: argparse.Namespace) -> int:
    plan_path = pathlib.Path(args.plan)
    plan = load_json_file(plan_path, "plan")
    if plan.get("version") != PLAN_VERSION:
        fail(f"plan version {plan.get('version')} != expected {PLAN_VERSION}")
    if plan.get("kind") != "anthropic_priority_rebalance":
        fail(f"plan kind {plan.get('kind')!r} != expected 'anthropic_priority_rebalance'")
    if args.confirm != CONFIRM_CODE:
        fail(
            f"--confirm mismatch.\n  Got:      {args.confirm!r}\n  Required: {CONFIRM_CODE!r}",
            code=2,
        )

    edge_matrix = load_json_file(EDGE_MATRIX, "edge matrix")

    job_dir = pathlib.Path(args.job_dir) if args.job_dir else pathlib.Path(
        f"/tmp/anthropic-priority-apply-"
        f"{_dt.datetime.now().strftime('%Y%m%d-%H%M%S')}-{os.getpid()}"
    )
    job_dir.mkdir(parents=True, exist_ok=True)
    print(f"apply: job_dir={job_dir}", file=sys.stderr)

    actions = plan.get("actions") or []
    results: list[dict] = []
    for action in actions:
        step = action["step"]
        if action.get("kind") != "account_priority":
            fail(
                f"unknown action.kind {action.get('kind')!r} "
                f"(this orchestrator only handles 'account_priority')"
            )
        tgt = action["target"]
        edge_id = tgt["edge_id"]
        account_name = tgt["account_name"]
        new_priority = int(action["expected_after"]["priority"])
        label = (
            f"step{step:02d}-edge-{edge_id}-{account_name}-priority"
        ).replace("/", "-")
        sql_path = job_dir / f"{label}.sql"
        sql = render_apply_sql(account_name, new_priority)
        sql_path.write_text(sql)

        region, instance_id, target_label = _resolve_edge_target(edge_id, edge_matrix)
        sql_b64 = base64.b64encode(sql.encode("utf-8")).decode("ascii")
        print(
            f"apply: step{step:02d} account_priority → {target_label} "
            f"account={account_name} new_priority={new_priority}",
            file=sys.stderr,
        )
        stdout, cid, ssm_ok, stderr = ssm_run_sql_b64(
            region, instance_id, sql_b64,
            f"apply step {step} account_priority on {target_label} ({account_name})",
        )
        result = {
            "step": step,
            "kind": "account_priority",
            "target_label": target_label,
            "account_name": account_name,
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
                "SSM ResponseCode != 0; remote SQL likely failed "
                "(DO-block RAISE or psql ON_ERROR_STOP). Inspect via: "
                f"aws ssm get-command-invocation --region {region} "
                f"--instance-id {instance_id} --command-id {cid}"
            )
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
        print(
            f"apply: success={success} completed={len(results)}/{len(actions)} "
            f"job_dir={job_dir}"
        )
        for r in results:
            tag = "OK" if r["ok"] else "FAIL"
            print(
                f"  [{tag}] step{r['step']:02d} {r['target_label']} "
                f"account={r['account_name']} cid={r['ssm_command_id']}"
            )
    return 0 if success else 1


# --------------------------------------------------------------------------
# Stage 4 — verify
# --------------------------------------------------------------------------

def cmd_verify(args: argparse.Namespace) -> int:
    plan_path = pathlib.Path(args.plan)
    plan = load_json_file(plan_path, "plan")

    snap_path = pathlib.Path(args.snapshot_out) if args.snapshot_out else pathlib.Path(
        f"/tmp/anthropic-priority-verify-snap-"
        f"{_dt.datetime.now().strftime('%Y%m%d-%H%M%S')}.json"
    )
    print(f"verify: capturing fresh snapshot → {snap_path}", file=sys.stderr)
    snap_args = argparse.Namespace(out=str(snap_path), allow_planned=args.allow_planned)
    rc = cmd_snapshot(snap_args)
    if rc != 0:
        fail(f"verify: re-snapshot exited {rc}")
    snap = load_json_file(snap_path, "verify snapshot")

    drift: list[dict] = []
    for action in plan.get("actions") or []:
        if action.get("kind") != "account_priority":
            continue
        tgt = action["target"]
        edge = (snap.get("edges") or {}).get(tgt["edge_id"], {})
        live: dict | None = None
        for a in edge.get("oauth_accounts", []):
            if a.get("name") == tgt["account_name"]:
                live = a
                break
        exp_priority = (action.get("expected_after") or {}).get("priority")
        if live is None:
            drift.append({
                "step": action["step"],
                "target": tgt,
                "diff": "target not found in live snapshot",
            })
            continue
        if live.get("priority") != exp_priority:
            drift.append({
                "step": action["step"],
                "target": tgt,
                "diff": f"priority: live={live.get('priority')} expected={exp_priority}",
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
            print(
                f"  [DRIFT] step{d['step']:02d} edge={tgt['edge_id']} "
                f"account={tgt['account_name']} — {d['diff']}"
            )
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

    sp = sub.add_parser(
        "snapshot",
        help="pull each deployable edge's anthropic OAuth accounts + utilization into one JSON",
    )
    sp.add_argument("--out", help="write snapshot JSON (otherwise stdout)")
    sp.add_argument("--allow-planned", action="store_true",
                    help="include planned edges (per edge-targets.json)")
    sp.set_defaults(handler=cmd_snapshot)

    sp = sub.add_parser(
        "plan",
        help="rank accounts by remaining window, emit plan with new priority per account",
    )
    sp.add_argument("--edge", "--edge-id", dest="edge_id", required=True,
                    help="'all' (every deployable edge in snapshot) or a single edge id")
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--stale-minutes", type=int, default=120,
                    help="treat accounts whose passive_usage_sampled_at is older "
                         "than this as full-load (default 120)")
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.set_defaults(handler=cmd_plan)

    sp = sub.add_parser(
        "apply",
        help="execute a plan: render the priority-rebalance template, run via SSM, fail-stop",
    )
    sp.add_argument("--plan", required=True)
    sp.add_argument("--confirm", required=True,
                    help=f"must be exactly: {CONFIRM_CODE}")
    sp.add_argument("--job-dir", help="where to write rendered SQL + apply-report.json")
    sp.add_argument("--json", action="store_true")
    sp.set_defaults(handler=cmd_apply)

    sp = sub.add_parser(
        "verify",
        help="re-snapshot and compare each action's expected_after vs live",
    )
    sp.add_argument("--plan", required=True)
    sp.add_argument("--snapshot-out", help="path to write the fresh snapshot used for verify")
    sp.add_argument("--allow-planned", action="store_true")
    sp.add_argument("--json", action="store_true")
    sp.set_defaults(handler=cmd_verify)

    args = ap.parse_args()
    return args.handler(args)


if __name__ == "__main__":
    sys.exit(main())
