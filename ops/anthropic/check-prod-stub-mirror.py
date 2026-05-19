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
     - Pull the edge's anthropic `default` group OAuth members.
     - Verify `stub.concurrency == absorb_zero_sum(oauth.concurrency)`.
       A stub fronts the *entire* edge default group, so a multi-OAuth
       edge is the SUM of its OAuth concurrencies.

3. Group-level mirror (R3; every prod anthropic group that contains
   at least one apikey stub member):
     - For each member stub in the group, contribute to fan-out:
         * self-edge stub  → upstream edge default_group.rpm_limit
         * external stub   → 0  (unknown capacity ⇒ unlimited)
     - Verify `prod_group.rpm_limit == absorb_zero_sum(fan-out)`.
     - Mixed groups (containing any external stub) thus aggregate to
       0 (unlimited) — the operator is declaring the group accepts
       no RPM ceiling because external capacity is unmodeled.

Capacity aggregation operator: **absorb-zero SUM**.  Runtime treats 0
as "unlimited" for `account.concurrency` (concurrency_service.go) and
`account.extra.base_rpm` / `group.rpm_limit` (account.go / ent schema).
Naive SUM would silently treat unlimited as a 0 datum; absorb-zero
propagates the unlimited semantic — any 0 term forces the aggregate
to 0.  See SKILL.md §"prod 控制面：anthropic stub 主路径镜像规则"
的"容量约定".

External fallback stubs (`base_url` not matching the self-edge
pattern, e.g. `tokensea-*.4` → `agent.tokensea.ai`) have independent
capacity not recorded in our schema:
  - R1: NOT mirrored (operator declares concurrency independently).
  - R3: contributes 0 (unlimited) to fan-out (treated as "unknown =
    unlimited"; any external presence in a group ⇒ group goes
    unlimited via absorb-zero).
  - Must satisfy the common baseline.

Exit codes:
  0  all checked stubs and groups aligned (or only external stubs)
  1  one or more baseline / account-mirror / group-mirror violations
  2  schema / SSM / target-resolution error

Usage:
  python3 ops/anthropic/check-prod-stub-mirror.py
  python3 ops/anthropic/check-prod-stub-mirror.py --json
  python3 ops/anthropic/check-prod-stub-mirror.py --account-id 42
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
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


def absorb_zero_sum(values: list[int]) -> int:
    """absorb-zero SUM: any 0 term ⇒ 0 (unlimited), else SUM of positives.

    Empty input returns 0 (unlimited) — there is no capacity to bound.
    """
    if any(v == 0 for v in values):
        return 0
    return sum(values)


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

# Per-edge: anthropic 'default' group + active OAuth members' concurrency.
# Used by both R1 (sum concurrencies) and R3 (group rpm_limit).
EDGE_DEFAULT_SQL = """
SELECT COALESCE(jsonb_build_object(
  'group_id', g.id,
  'group_name', g.name,
  'rpm_limit', g.rpm_limit,
  'oauth_members', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', a.id,
      'name', a.name,
      'concurrency', a.concurrency,
      'stability_tier', a.extra->>'stability_tier'
    ) ORDER BY a.id)
    FROM account_groups ag
    JOIN accounts a ON a.id = ag.account_id
    WHERE ag.group_id = g.id
      AND a.platform = 'anthropic'
      AND a.type = 'oauth'
      AND a.status = 'active'
      AND a.deleted_at IS NULL
  ), '[]'::jsonb)
), '{}'::jsonb)
FROM groups g
WHERE g.platform = 'anthropic'
  AND g.name = 'default'
  AND g.deleted_at IS NULL
ORDER BY g.id
LIMIT 1;
"""

# Prod anthropic groups + ALL apikey stub members (self-edge + external).
# Classification (self-edge vs external) happens in Python via base_url regex
# so a single membership query feeds both contribution paths.
PROD_GROUPS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', g.id,
  'name', g.name,
  'rpm_limit', g.rpm_limit,
  'stubs', COALESCE((
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
  ), '[]'::jsonb)
) ORDER BY g.id), '[]'::jsonb)
FROM groups g
WHERE g.platform = 'anthropic'
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
    ap.add_argument("--account-id", type=int, help="check only this prod account id (skips group-level R3)")
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

    # Per-edge default group + OAuth members; cached.
    edge_default_cache: dict[str, dict | None] = {}

    def edge_default_for(edge_id: str) -> tuple[dict | None, str | None]:
        if edge_id in edge_default_cache:
            return edge_default_cache[edge_id], None
        tgt = edge_targets.get(edge_id)
        if not tgt:
            return None, f"unknown edge_id {edge_id!r} (not in edge-targets.json)"
        if not tgt.get("deployable") and not args.allow_planned:
            return None, f"edge_id {edge_id} is planned (use --allow-planned to include)"
        for key in ("region", "stack"):
            if key not in tgt:
                return None, f"edge {edge_id} missing required field {key}"
        inst = resolve_instance_id(tgt["region"], tgt["stack"])
        raw, _ = run_remote(
            tgt["region"], inst, EDGE_DEFAULT_SQL,
            f"prod stub mirror: edge {edge_id} default group + oauth members",
        )
        try:
            data = json.loads(raw) if raw else {}
        except json.JSONDecodeError as e:
            return None, f"parse edge {edge_id} payload: {e}"
        # Empty result means no 'default' anthropic group on edge.
        if not data or not data.get("group_id"):
            edge_default_cache[edge_id] = None
            return None, f"edge {edge_id} has no anthropic 'default' group"
        edge_default_cache[edge_id] = data
        return data, None

    # ---------------- Account-level (R1) + baseline ----------------
    results: list[dict] = []
    has_violation = False
    for stub in stubs:
        rec: dict = {
            "account_id": stub["id"],
            "name": stub["name"],
            "concurrency": stub.get("concurrency"),
            "base_url": stub.get("base_url"),
            "kind": None,             # "self-edge" | "external"
            "edge_id": None,
            "edge_default_group_id": None,
            "edge_oauth_account_ids": [],
            "expected_concurrency": None,
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

        edge_default, err = edge_default_for(edge_id)
        if err is not None:
            rec["skipped_reason"] = err
            rec["mirror_violations"].append({"reason": err})
            has_violation = True
            results.append(rec)
            continue

        oauths = edge_default.get("oauth_members") or []
        if not oauths:
            rec["mirror_violations"].append(
                {"reason": f"edge {edge_id} default group has no active anthropic OAuth members"}
            )
            has_violation = True
            results.append(rec)
            continue

        rec["edge_default_group_id"] = edge_default.get("group_id")
        rec["edge_oauth_account_ids"] = [o["id"] for o in oauths]
        rec["edge_oauth_concurrencies"] = [o.get("concurrency") for o in oauths]
        rec["edge_oauth_tiers"] = [o.get("stability_tier") for o in oauths]

        expected = absorb_zero_sum([o.get("concurrency") or 0 for o in oauths])
        rec["expected_concurrency"] = expected

        if stub.get("concurrency") != expected:
            rec["mirror_violations"].append({
                "field": "concurrency",
                "prod_stub": stub.get("concurrency"),
                "expected": expected,
                "expected_formula": "absorb_zero_sum(edge.default.oauth.concurrency)",
                "edge_oauth_concurrencies": rec["edge_oauth_concurrencies"],
            })
            has_violation = True

        if rec["baseline_violations"]:
            has_violation = True

        results.append(rec)

    # ---------------- Group-level (R3) ----------------
    # Skipped when --account-id is given (stub-scoped invocation).
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
            stubs_in_group = g.get("stubs") or []
            self_edge_in_group = [
                s for s in stubs_in_group
                if SELF_EDGE_BASE_URL_RE.match(s.get("base_url") or "")
            ]
            external_in_group = [
                s for s in stubs_in_group
                if not SELF_EDGE_BASE_URL_RE.match(s.get("base_url") or "")
            ]

            grec: dict = {
                "group_id": g["id"],
                "group_name": g["name"],
                "prod_rpm_limit": g.get("rpm_limit"),
                "stub_count": len(stubs_in_group),
                "self_edge_count": len(self_edge_in_group),
                "external_count": len(external_in_group),
                "fanout": [],
                "expected_rpm_limit": None,
                "mirror_violations": [],
                "skipped_reason": None,
            }

            if not stubs_in_group:
                grec["skipped_reason"] = "no apikey stubs in this group"
                group_results.append(grec)
                continue

            # Build fan-out: self-edge stubs contribute upstream edge default rpm;
            # external stubs contribute 0 (unlimited, absorb-zero). Deduplicate
            # self-edge edges so a multi-stub edge does not double-count.
            seen_edge_ids: set[str] = set()
            fan_rpms: list[int] = []
            fanout_errors: list[str] = []

            for stub in self_edge_in_group:
                m = SELF_EDGE_BASE_URL_RE.match(stub.get("base_url") or "")
                edge_id = m.group("edge_id")
                if edge_id in seen_edge_ids:
                    continue
                seen_edge_ids.add(edge_id)

                edge_default, err = edge_default_for(edge_id)
                fan = {
                    "stub_account_id": stub["account_id"],
                    "stub_account_name": stub["account_name"],
                    "kind": "self-edge",
                    "edge_id": edge_id,
                    "edge_default_group_id": None,
                    "edge_default_rpm_limit": None,
                    "contribution": None,
                    "lookup_error": err,
                }
                if err is not None:
                    fanout_errors.append(f"edge {edge_id}: {err}")
                    grec["fanout"].append(fan)
                    continue
                rpm = int(edge_default.get("rpm_limit") or 0)
                fan["edge_default_group_id"] = edge_default.get("group_id")
                fan["edge_default_rpm_limit"] = edge_default.get("rpm_limit")
                fan["contribution"] = rpm
                grec["fanout"].append(fan)
                fan_rpms.append(rpm)

            for stub in external_in_group:
                fan = {
                    "stub_account_id": stub["account_id"],
                    "stub_account_name": stub["account_name"],
                    "kind": "external",
                    "base_url": stub.get("base_url"),
                    "contribution": 0,
                    "lookup_error": None,
                }
                grec["fanout"].append(fan)
                fan_rpms.append(0)  # external = unknown ⇒ unlimited contribution

            if fanout_errors:
                for err in fanout_errors:
                    grec["mirror_violations"].append({"reason": err})
                has_violation = True
                group_results.append(grec)
                continue

            expected = absorb_zero_sum(fan_rpms)
            grec["expected_rpm_limit"] = expected

            if (g.get("rpm_limit") or 0) != expected:
                grec["mirror_violations"].append({
                    "field": "rpm_limit",
                    "prod_group_rpm": g.get("rpm_limit"),
                    "expected": expected,
                    "expected_formula": "absorb_zero_sum(self-edge contributions + external contributions)",
                    "fanout_rpms": fan_rpms,
                })
                has_violation = True

            group_results.append(grec)

    report = {
        "prod_ssm_command_id": stubs_cid,
        "stubs_checked": len(results),
        "self_edge_stubs": sum(1 for r in results if r["kind"] == "self-edge"),
        "external_stubs": sum(1 for r in results if r["kind"] == "external"),
        "groups_checked": len(group_results),
        "groups_with_stubs": sum(
            1 for gr in group_results if gr.get("stub_count", 0) > 0
        ),
        "groups_mixed": sum(
            1 for gr in group_results
            if gr.get("self_edge_count", 0) > 0 and gr.get("external_count", 0) > 0
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

    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        print(
            f"stubs_checked={report['stubs_checked']} "
            f"self_edge={report['self_edge_stubs']} external={report['external_stubs']} "
            f"groups_checked={report['groups_checked']} "
            f"groups_with_stubs={report['groups_with_stubs']} "
            f"groups_mixed={report['groups_mixed']} "
            f"stub_violations={report['stub_violation_count']} "
            f"group_violations={report['group_violation_count']}"
        )
        print("--- account-level (R1) ---")
        for r in results:
            tag = "OK" if not (r["baseline_violations"] or r["mirror_violations"]) else "FAIL"
            if r["kind"] == "external" and not r["baseline_violations"]:
                tag = "external"
            print(f"  [{tag}] id={r['account_id']} name={r['name']} kind={r['kind']} base_url={r['base_url']!r}")
            if r["kind"] == "self-edge" and r.get("expected_concurrency") is not None:
                print(
                    f"      edge_id={r['edge_id']} default_group={r['edge_default_group_id']}"
                    f" oauth_ids={r['edge_oauth_account_ids']}"
                    f" oauth_conc={r.get('edge_oauth_concurrencies')}"
                    f" → prod_conc={r['concurrency']} vs expected={r['expected_concurrency']}"
                    f" (absorb-zero SUM)"
                )
            for v in r["baseline_violations"]:
                print(f"      baseline FAIL: {v['field']} expected={v['expected']} actual={v['actual']}")
            for v in r["mirror_violations"]:
                if "field" in v:
                    print(
                        f"      mirror FAIL: {v['field']}"
                        f" prod_stub={v.get('prod_stub')} expected={v.get('expected')}"
                        f" upstream={v.get('edge_oauth_concurrencies')}"
                    )
                else:
                    print(f"      mirror FAIL: {v.get('reason')}")
        if args.account_id is None:
            print("--- group-level (R3) ---")
            for gr in group_results:
                tag = "OK" if not gr["mirror_violations"] else "FAIL"
                if gr["skipped_reason"]:
                    tag = "skip"
                fanout_parts: list[str] = []
                for f in gr.get("fanout") or []:
                    if f.get("kind") == "self-edge" and f.get("edge_default_group_id") is not None:
                        fanout_parts.append(
                            f"{f['edge_id']}:rpm={f.get('edge_default_rpm_limit')}"
                        )
                    elif f.get("kind") == "external":
                        fanout_parts.append(
                            f"external({f.get('stub_account_name')}):rpm=0"
                        )
                fanout_desc = ",".join(fanout_parts)
                mixed_marker = " mixed" if (
                    gr.get("self_edge_count", 0) > 0 and gr.get("external_count", 0) > 0
                ) else ""
                print(
                    f"  [{tag}] group_id={gr['group_id']} name={gr['group_name']!r}"
                    f" prod_rpm={gr['prod_rpm_limit']}"
                    f" stubs={gr['stub_count']}(self_edge={gr['self_edge_count']}, external={gr['external_count']}){mixed_marker}"
                    f" fanout=[{fanout_desc}]"
                    f" expected={gr.get('expected_rpm_limit')}"
                )
                if gr["skipped_reason"]:
                    print(f"      skip: {gr['skipped_reason']}")
                for v in gr["mirror_violations"]:
                    if "field" in v:
                        print(
                            f"      mirror FAIL: {v['field']}"
                            f" prod_group_rpm={v.get('prod_group_rpm')}"
                            f" expected={v.get('expected')}"
                            f" fanout={v.get('fanout_rpms')}"
                        )
                    else:
                        print(f"      mirror FAIL: {v.get('reason')}")

    return 1 if has_violation else 0


if __name__ == "__main__":
    sys.exit(main())
