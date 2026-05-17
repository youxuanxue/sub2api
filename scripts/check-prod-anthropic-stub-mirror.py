#!/usr/bin/env python3
"""
Prod anthropic stub mirror-edge guard.

For every active anthropic forward stub on the prod stage0 control
plane (`platform=anthropic AND type=apikey`), verify two layers:

1. Common baseline (every stub must satisfy these regardless of where
   it forwards to):
     - channel_type = 0
     - rate_multiplier = 1.0
     - auto_pause_on_expired = true
     - status = 'active' (skip otherwise)

2. Mirror-edge layer (only stubs whose `credentials.base_url` matches
   `https://api-<edge_id>.tokenkey.dev`):
     - Resolve <edge_id> against `deploy/aws/stage0/edge-targets.json`.
     - Pull the edge's active anthropic OAuth account.
     - Verify `prod_stub.concurrency == edge_oauth.concurrency`.

Stubs whose base_url does NOT match the self-edge pattern (e.g.
`tokensea-*.4` → `agent.tokensea.ai`) are treated as **external
fallback** — they are allowed independent capacity (concurrency is
not compared) but still must satisfy the common baseline.

Why this rule:
TokenKey's anthropic forward stubs are the prod-side front of an
edge OAuth account. The edge's OAuth account is the actual upstream
throughput contract (Anthropic per-account RPM/concurrency). Having
prod stub concurrency exceed the edge OAuth concurrency causes
wasted upstream calls (overflow gets 429'd at the edge); having it
fall short under-uses the edge's quota. Mirror = pre-edge
backpressure aligned with reality.

Exit codes:
  0  all checked stubs aligned (or only external stubs)
  1  one or more mirror or baseline violations
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

    # Edge OAuth lookups are cached per edge_id; only deployable edges by default.
    edge_oauth_cache: dict[str, list[dict]] = {}

    def edge_oauth_for(edge_id: str) -> tuple[list[dict] | None, str | None]:
        if edge_id in edge_oauth_cache:
            return edge_oauth_cache[edge_id], None
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
            tgt["region"], inst, EDGE_OAUTH_SQL,
            f"prod stub mirror: edge {edge_id} oauth accounts",
        )
        try:
            data = json.loads(raw) if raw else []
        except json.JSONDecodeError as e:
            return None, f"parse edge {edge_id} payload: {e}"
        edge_oauth_cache[edge_id] = data
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

    report = {
        "prod_ssm_command_id": stubs_cid,
        "stubs_checked": len(results),
        "self_edge_stubs": sum(1 for r in results if r["kind"] == "self-edge"),
        "external_stubs": sum(1 for r in results if r["kind"] == "external"),
        "violation_count": sum(
            1 for r in results
            if r["baseline_violations"] or r["mirror_violations"]
        ),
        "results": results,
    }

    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        print(
            f"stubs_checked={report['stubs_checked']} "
            f"self_edge={report['self_edge_stubs']} external={report['external_stubs']} "
            f"violations={report['violation_count']}"
        )
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

    return 1 if has_violation else 0


if __name__ == "__main__":
    sys.exit(main())
