#!/usr/bin/env python3
"""
Prod anthropic stub mirror-edge guard.

For every active anthropic forward stub on the prod stage0 control
plane (`platform=anthropic AND type=apikey`), and for every prod
anthropic group that contains at least one such stub, verify three
layers:

1. Common baseline (every stub must satisfy these regardless of where
   it forwards to):
     - channel_type = 0
     - rate_multiplier = 1.0
     - auto_pause_on_expired = true
     - status = 'active' (skip otherwise)

2. Account-level mirror (R1; only stubs whose `credentials.base_url`
   matches `https://api-<edge_id>.tokenkey.dev`):
     - Resolve <edge_id> against `deploy/aws/stage0/edge-targets.json`.
     - Pull the edge's active anthropic OAuth account.
     - Verify `prod_stub.concurrency == edge_oauth.concurrency`.

3. Group-level mirror (R3; every prod anthropic group that contains
   at least one self-edge stub):
     - For each member self-edge stub, resolve its <edge_id> and pull
       that edge's anthropic `default` group `rpm_limit` (stubs
       passthrough into edge `default`).
     - Compute `max_edge_rpm_limit` across the group's self-edge stubs
       (NULL / 0 on the edge side is treated as +∞ — unlimited).
     - Require `prod_group.rpm_limit == 0` (unlimited) OR
       `prod_group.rpm_limit >= max_edge_rpm_limit`.
     - Violating this means prod is the tighter layer and will 429 /
       503 ahead of the edge's true ceiling, defeating the
       passthrough topology.

Stubs whose base_url does NOT match the self-edge pattern (e.g.
`tokensea-*.4` → `agent.tokensea.ai`) are treated as **external
fallback** — they are allowed independent capacity (concurrency is
not compared) but still must satisfy the common baseline.  External
stubs do NOT contribute to a group's edge fan-out set (their
upstream capacity is independent).

Why this rule:
TokenKey's anthropic forward stubs are the prod-side front of an
edge OAuth account. The edge's OAuth account is the actual upstream
throughput contract (Anthropic per-account RPM/concurrency). Having
prod stub concurrency exceed the edge OAuth concurrency causes
wasted upstream calls (overflow gets 429'd at the edge); having it
fall short under-uses the edge's quota. Mirror = pre-edge
backpressure aligned with reality.  The group-level layer extends
the same logic to per-minute throughput: prod must not be a stricter
RPM ceiling than the edge it forwards to.

Exit codes:
  0  all checked stubs and groups aligned (or only external stubs)
  1  one or more baseline / account-mirror / group-mirror violations
  2  schema / SSM / target-resolution error

Usage:
  python3 scripts/check-prod-anthropic-stub-mirror.py
  python3 scripts/check-prod-anthropic-stub-mirror.py --json
  python3 scripts/check-prod-anthropic-stub-mirror.py --account-id 42
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
EDGE_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"

PROD = {
    "label": "prod",
    "stack": "tokenkey-prod-stage0",
    "region": "us-east-1",
}

SELF_EDGE_BASE_URL_RE = re.compile(
    r"^https?://api-(?P<edge_id>[a-z0-9-]+)\.tokenkey\.dev/?$"
)


def fail(msg: str, code: int = 2) -> None:
    print(f"::error::{msg}", file=sys.stderr)
    sys.exit(code)


def resolve_instance_id(region: str, stack: str) -> str:
    try:
        out = subprocess.check_output(
            [
                "aws",
                "cloudformation",
                "describe-stacks",
                "--region",
                region,
                "--stack-name",
                stack,
                "--query",
                "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue",
                "--output",
                "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"describe-stacks failed for {stack}/{region}: {e}")
    if not out:
        fail(f"no InstanceId output on stack {stack}/{region}")
    return out


def run_remote(region: str, inst: str, sql: str, comment: str) -> tuple[str, str]:
    remote = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -t -A -v ON_ERROR_STOP=1"
    command = f"set -euo pipefail\n{remote} <<'SQL'\n{sql}\nSQL"
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            [
                "aws", "ssm", "send-command",
                "--region", region,
                "--instance-ids", inst,
                "--document-name", "AWS-RunShellScript",
                "--comment", comment,
                "--parameters", params,
                "--query", "Command.CommandId",
                "--output", "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"ssm send-command failed: {e}")
    subprocess.run(
        [
            "aws", "ssm", "wait", "command-executed",
            "--region", region,
            "--command-id", cid,
            "--instance-id", inst,
        ],
        check=False,
    )
    inv = json.loads(
        subprocess.check_output(
            [
                "aws", "ssm", "get-command-invocation",
                "--region", region,
                "--command-id", cid,
                "--instance-id", inst,
                "--output", "json",
            ],
            text=True,
        )
    )
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:600]
        fail(f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')}: {err}")
    return (inv.get("StandardOutputContent") or "").strip(), cid


PROD_STUBS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name,
  'concurrency', a.concurrency,
  'channel_type', a.channel_type,
  'rate_multiplier', a.rate_multiplier,
  'auto_pause_on_expired', a.auto_pause_on_expired,
  'base_url', a.credentials->>'base_url'
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.platform = 'anthropic'
  AND a.type = 'apikey'
  AND a.status = 'active'
  AND a.deleted_at IS NULL;
"""

EDGE_OAUTH_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name,
  'concurrency', a.concurrency,
  'stability_tier', a.extra->>'stability_tier'
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.platform = 'anthropic'
  AND a.type = 'oauth'
  AND a.deleted_at IS NULL;
"""

PROD_GROUPS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', g.id,
  'name', g.name,
  'rpm_limit', g.rpm_limit,
  'self_edge_stubs', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'account_id', a.id,
      'account_name', a.name,
      'base_url', a.credentials->>'base_url'
    ) ORDER BY a.id)
    FROM account_groups ag
    JOIN accounts a ON a.id = ag.account_id
    WHERE ag.group_id = g.id
      AND a.platform = 'anthropic'
      AND a.type = 'apikey'
      AND a.status = 'active'
      AND a.deleted_at IS NULL
      AND a.credentials->>'base_url' ~ '^https?://api-[a-z0-9-]+\\.tokenkey\\.dev/?$'
  ), '[]'::jsonb)
) ORDER BY g.id), '[]'::jsonb)
FROM groups g
WHERE g.platform = 'anthropic'
  AND g.deleted_at IS NULL;
"""

EDGE_DEFAULT_GROUP_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', g.id,
  'name', g.name,
  'rpm_limit', g.rpm_limit
) ORDER BY g.id), '[]'::jsonb)
FROM groups g
WHERE g.platform = 'anthropic'
  AND g.name = 'default'
  AND g.deleted_at IS NULL;
"""


def baseline_violations(stub: dict) -> list[dict]:
    out = []
    if stub.get("channel_type") != 0:
        out.append({"field": "channel_type", "expected": 0, "actual": stub.get("channel_type")})
    if stub.get("rate_multiplier") != 1.0:
        out.append({"field": "rate_multiplier", "expected": 1.0, "actual": stub.get("rate_multiplier")})
    if stub.get("auto_pause_on_expired") is not True:
        out.append({"field": "auto_pause_on_expired", "expected": True, "actual": stub.get("auto_pause_on_expired")})
    return out


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__.split("\n\n", 1)[0] if __doc__ else "",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("--account-id", type=int, help="check only this prod account id")
    ap.add_argument("--json", action="store_true", help="emit JSON report only")
    ap.add_argument("--prod-instance-id", help="override prod EC2 instance id")
    ap.add_argument("--allow-planned", action="store_true",
                    help="allow self-edge resolution against planned edges in edge-targets.json")
    args = ap.parse_args()

    if not EDGE_MATRIX.exists():
        fail(f"edge matrix not found: {EDGE_MATRIX}")
    matrix = json.loads(EDGE_MATRIX.read_text())
    edge_targets = matrix.get("targets") or {}

    prod_inst = args.prod_instance_id or resolve_instance_id(PROD["region"], PROD["stack"])
    stubs_raw, stubs_cid = run_remote(
        PROD["region"], prod_inst, PROD_STUBS_SQL,
        "prod stub mirror: list anthropic apikey stubs",
    )
    try:
        stubs = json.loads(stubs_raw) if stubs_raw else []
    except json.JSONDecodeError as e:
        fail(f"parse prod stubs payload: {e}\n{stubs_raw[:600]}")
        return 2

    if args.account_id is not None:
        stubs = [s for s in stubs if s.get("id") == args.account_id]
        if not stubs:
            fail(f"prod account_id={args.account_id} not found (or not anthropic apikey active)", code=2)

    # Edge lookups are cached per edge_id; only deployable edges by default.
    edge_oauth_cache: dict[str, list[dict]] = {}
    edge_default_group_cache: dict[str, list[dict]] = {}
    edge_instance_cache: dict[str, tuple[str, str]] = {}  # edge_id -> (region, instance_id)

    def edge_runtime_for(edge_id: str) -> tuple[tuple[str, str] | None, str | None]:
        """Resolve (region, instance_id) for an edge; cached."""
        if edge_id in edge_instance_cache:
            return edge_instance_cache[edge_id], None
        tgt = edge_targets.get(edge_id)
        if not tgt:
            return None, f"unknown edge_id {edge_id!r} (not in edge-targets.json)"
        if not tgt.get("deployable") and not args.allow_planned:
            return None, f"edge_id {edge_id} is planned (use --allow-planned to include)"
        for key in ("region", "stack"):
            if key not in tgt:
                return None, f"edge {edge_id} missing required field {key}"
        inst = resolve_instance_id(tgt["region"], tgt["stack"])
        edge_instance_cache[edge_id] = (tgt["region"], inst)
        return edge_instance_cache[edge_id], None

    def edge_oauth_for(edge_id: str) -> tuple[list[dict] | None, str | None]:
        if edge_id in edge_oauth_cache:
            return edge_oauth_cache[edge_id], None
        rt, err = edge_runtime_for(edge_id)
        if err:
            return None, err
        region, inst = rt
        raw, _ = run_remote(
            region, inst, EDGE_OAUTH_SQL,
            f"prod stub mirror: edge {edge_id} oauth accounts",
        )
        try:
            data = json.loads(raw) if raw else []
        except json.JSONDecodeError as e:
            return None, f"parse edge {edge_id} payload: {e}"
        edge_oauth_cache[edge_id] = data
        return data, None

    def edge_default_group_for(edge_id: str) -> tuple[list[dict] | None, str | None]:
        if edge_id in edge_default_group_cache:
            return edge_default_group_cache[edge_id], None
        rt, err = edge_runtime_for(edge_id)
        if err:
            return None, err
        region, inst = rt
        raw, _ = run_remote(
            region, inst, EDGE_DEFAULT_GROUP_SQL,
            f"prod stub mirror: edge {edge_id} default group",
        )
        try:
            data = json.loads(raw) if raw else []
        except json.JSONDecodeError as e:
            return None, f"parse edge {edge_id} default-group payload: {e}"
        edge_default_group_cache[edge_id] = data
        return data, None

    results = []
    has_violation = False
    for stub in stubs:
        rec: dict = {
            "account_id": stub["id"],
            "name": stub["name"],
            "concurrency": stub.get("concurrency"),
            "base_url": stub.get("base_url"),
            "kind": None,             # "self-edge" | "external"
            "edge_id": None,
            "edge_oauth_account_id": None,
            "edge_oauth_concurrency": None,
            "baseline_violations": [],
            "mirror_violations": [],
            "skipped_reason": None,
        }

        rec["baseline_violations"] = baseline_violations(stub)

        url = stub.get("base_url") or ""
        m = SELF_EDGE_BASE_URL_RE.match(url)
        if not m:
            rec["kind"] = "external"
            rec["skipped_reason"] = "base_url does not match api-<edge>.tokenkey.dev"
            results.append(rec)
            if rec["baseline_violations"]:
                has_violation = True
            continue

        rec["kind"] = "self-edge"
        edge_id = m.group("edge_id")
        rec["edge_id"] = edge_id

        edge_oauths, err = edge_oauth_for(edge_id)
        if err is not None:
            rec["skipped_reason"] = err
            rec["mirror_violations"].append({"reason": err})
            has_violation = True
            results.append(rec)
            continue

        if not edge_oauths:
            rec["mirror_violations"].append({"reason": f"no active anthropic oauth account on edge {edge_id}"})
            has_violation = True
            results.append(rec)
            continue

        if len(edge_oauths) > 1:
            rec["mirror_violations"].append({
                "reason": f"edge {edge_id} has {len(edge_oauths)} anthropic oauth accounts; mirror is ambiguous",
                "edge_account_ids": [a["id"] for a in edge_oauths],
            })
            has_violation = True
            results.append(rec)
            continue

        edge_oauth = edge_oauths[0]
        rec["edge_oauth_account_id"] = edge_oauth["id"]
        rec["edge_oauth_concurrency"] = edge_oauth["concurrency"]
        rec["edge_oauth_tier"] = edge_oauth.get("stability_tier")

        if stub.get("concurrency") != edge_oauth["concurrency"]:
            rec["mirror_violations"].append({
                "field": "concurrency",
                "prod_stub": stub.get("concurrency"),
                "edge_oauth": edge_oauth["concurrency"],
            })
            has_violation = True

        if rec["baseline_violations"]:
            has_violation = True

        results.append(rec)

    # ---------------- Group-level mirror (R3) ----------------
    # Skip group-level checks when --account-id is given (account-scoped invocation).
    group_results: list[dict] = []
    if args.account_id is None:
        groups_raw, _ = run_remote(
            PROD["region"], prod_inst, PROD_GROUPS_SQL,
            "prod stub mirror: list anthropic groups + self-edge stubs",
        )
        try:
            prod_groups = json.loads(groups_raw) if groups_raw else []
        except json.JSONDecodeError as e:
            fail(f"parse prod groups payload: {e}\n{groups_raw[:600]}")
            return 2

        for g in prod_groups:
            grec: dict = {
                "group_id": g["id"],
                "group_name": g["name"],
                "prod_rpm_limit": g.get("rpm_limit"),
                "self_edge_count": len(g.get("self_edge_stubs") or []),
                "edge_fanout": [],
                "max_edge_rpm_limit": None,
                "max_edge_unlimited": False,  # True if any edge's default group is rpm_limit=0/null
                "mirror_violations": [],
                "skipped_reason": None,
            }

            stubs_in_group = g.get("self_edge_stubs") or []
            if not stubs_in_group:
                grec["skipped_reason"] = "no self-edge stubs in this group"
                group_results.append(grec)
                continue

            # Collect edge fan-out: for each self-edge stub, resolve edge_id and
            # look up that edge's anthropic 'default' group.rpm_limit.
            seen_edge_ids: set[str] = set()
            fanout_errors: list[str] = []
            max_rpm: int | None = None
            any_unlimited = False

            for stub in stubs_in_group:
                url = stub.get("base_url") or ""
                m = SELF_EDGE_BASE_URL_RE.match(url)
                if not m:
                    # Should not happen — SQL already filtered, but guard anyway.
                    continue
                edge_id = m.group("edge_id")
                if edge_id in seen_edge_ids:
                    continue
                seen_edge_ids.add(edge_id)

                edge_groups, err = edge_default_group_for(edge_id)
                fan = {
                    "stub_account_id": stub["account_id"],
                    "stub_account_name": stub["account_name"],
                    "edge_id": edge_id,
                    "edge_default_group_id": None,
                    "edge_default_rpm_limit": None,
                    "lookup_error": err,
                }
                if err is not None:
                    fanout_errors.append(f"edge {edge_id}: {err}")
                    grec["edge_fanout"].append(fan)
                    continue
                if not edge_groups:
                    fanout_errors.append(
                        f"edge {edge_id} has no anthropic 'default' group"
                    )
                    grec["edge_fanout"].append(fan)
                    continue
                # Take the first (typically only) default group.
                eg = edge_groups[0]
                fan["edge_default_group_id"] = eg["id"]
                fan["edge_default_rpm_limit"] = eg.get("rpm_limit")
                grec["edge_fanout"].append(fan)

                rpm = eg.get("rpm_limit")
                # On the edge side: 0 / NULL is unlimited (per alignment guard
                # convention).  An unlimited edge means prod cannot bound it,
                # so prod must also be unlimited (rpm_limit=0).
                if rpm is None or rpm == 0:
                    any_unlimited = True
                else:
                    if max_rpm is None or rpm > max_rpm:
                        max_rpm = rpm

            grec["max_edge_rpm_limit"] = max_rpm
            grec["max_edge_unlimited"] = any_unlimited

            if fanout_errors:
                for err in fanout_errors:
                    grec["mirror_violations"].append({"reason": err})
                has_violation = True
                group_results.append(grec)
                continue

            # ---- Verify R3 constraint ----
            prod_rpm = grec["prod_rpm_limit"]
            prod_unlimited = prod_rpm is None or prod_rpm == 0

            if prod_unlimited:
                # 0 / NULL on prod always satisfies (prod is +∞).
                pass
            elif any_unlimited:
                # An edge default group is unlimited (rpm=0/NULL) — prod cannot
                # be a tighter ceiling than +∞.  Prod must also be unlimited.
                grec["mirror_violations"].append({
                    "field": "rpm_limit",
                    "prod_group_rpm": prod_rpm,
                    "required": "0 (unlimited; at least one edge default is unlimited)",
                })
                has_violation = True
            elif max_rpm is not None and prod_rpm < max_rpm:
                grec["mirror_violations"].append({
                    "field": "rpm_limit",
                    "prod_group_rpm": prod_rpm,
                    "max_edge_rpm": max_rpm,
                    "required": f">= {max_rpm} (or 0 for unlimited)",
                })
                has_violation = True

            group_results.append(grec)

    report = {
        "prod_ssm_command_id": stubs_cid,
        "stubs_checked": len(results),
        "self_edge_stubs": sum(1 for r in results if r["kind"] == "self-edge"),
        "external_stubs": sum(1 for r in results if r["kind"] == "external"),
        "groups_checked": len(group_results),
        "groups_with_self_edge": sum(
            1 for gr in group_results if gr["self_edge_count"] > 0
        ),
        "stub_violation_count": sum(
            1 for r in results
            if r["baseline_violations"] or r["mirror_violations"]
        ),
        "group_violation_count": sum(
            1 for gr in group_results if gr["mirror_violations"]
        ),
        "results": results,
        "group_results": group_results,
    }
    report["violation_count"] = (
        report["stub_violation_count"] + report["group_violation_count"]
    )

    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        print(
            f"stubs_checked={report['stubs_checked']} "
            f"self_edge={report['self_edge_stubs']} external={report['external_stubs']} "
            f"groups_checked={report['groups_checked']} "
            f"groups_with_self_edge={report['groups_with_self_edge']} "
            f"violations={report['violation_count']} "
            f"(stub={report['stub_violation_count']} group={report['group_violation_count']})"
        )
        print("--- account-level (R1) ---")
        for r in results:
            tag = "OK" if not (r["baseline_violations"] or r["mirror_violations"]) else "FAIL"
            if r["kind"] == "external" and not r["baseline_violations"]:
                tag = "external"
            print(f"  [{tag}] id={r['account_id']} name={r['name']} kind={r['kind']} base_url={r['base_url']!r}")
            if r["kind"] == "self-edge":
                print(
                    f"      edge_id={r['edge_id']} edge_oauth_id={r['edge_oauth_account_id']}"
                    f" tier={r.get('edge_oauth_tier')}"
                    f" → prod_conc={r['concurrency']} vs edge_oauth_conc={r['edge_oauth_concurrency']}"
                )
            for v in r["baseline_violations"]:
                print(f"      baseline FAIL: {v['field']} expected={v['expected']} actual={v['actual']}")
            for v in r["mirror_violations"]:
                if "field" in v:
                    print(
                        f"      mirror FAIL: {v['field']}"
                        f" prod_stub={v['prod_stub']} edge_oauth={v['edge_oauth']}"
                    )
                else:
                    print(f"      mirror FAIL: {v.get('reason')}")
        if group_results:
            print("--- group-level (R3) ---")
            for gr in group_results:
                tag = "OK"
                if gr["mirror_violations"]:
                    tag = "FAIL"
                elif gr["skipped_reason"]:
                    tag = "SKIPPED"
                fanout_desc = (
                    "[" + ", ".join(
                        f"{fan['edge_id']}:rpm={fan['edge_default_rpm_limit']}"
                        for fan in gr["edge_fanout"]
                    ) + "]"
                ) if gr["edge_fanout"] else "[]"
                print(
                    f"  [{tag}] group_id={gr['group_id']} name={gr['group_name']!r}"
                    f" prod_rpm={gr['prod_rpm_limit']}"
                    f" self_edge_stubs={gr['self_edge_count']}"
                    f" edge_fanout={fanout_desc}"
                )
                if gr["skipped_reason"]:
                    print(f"      skip: {gr['skipped_reason']}")
                for v in gr["mirror_violations"]:
                    if "field" in v:
                        bits = [
                            f"prod_group_rpm={v.get('prod_group_rpm')}",
                            f"max_edge_rpm={v.get('max_edge_rpm')}"
                            if "max_edge_rpm" in v else None,
                            f"required={v.get('required')}",
                        ]
                        print("      group mirror FAIL: " + " ".join(b for b in bits if b))
                    else:
                        print(f"      group mirror FAIL: {v.get('reason')}")

    return 1 if has_violation else 0


if __name__ == "__main__":
    sys.exit(main())
