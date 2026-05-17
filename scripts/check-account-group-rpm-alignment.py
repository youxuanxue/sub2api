#!/usr/bin/env python3
"""
S2 guard: account → group RPM alignment.

For every active anthropic OAuth account that declares
`extra.base_rpm`, verify that every group it is bound to has
`rpm_limit >= base_rpm`. A group with `rpm_limit=0` (or NULL) is
treated as "unlimited" and always passes.

Why this exists:
A tier landed on an account (`extra.base_rpm=N`) is useless if its
group caps RPM below N — the group becomes the bottleneck silently.
H1 (2026-05-17) tripped exactly this: edge uk1/fra1 `default.rpm_limit=3`
masked `base_rpm=6` from tier l1.

Targets a single stack at a time:
  --target prod       → tokenkey-prod-stage0 (us-east-1)
  --target <edge_id>  → matches deploy/aws/stage0/edge-targets.json
                       (uk1, us1, fra1, sg1, ...)

Exit codes:
  0  alignment OK (or no checkable pairs)
  1  one or more violations
  2  schema / SSM / target-resolution error

Usage:
  python3 scripts/check-account-group-rpm-alignment.py --target us1
  python3 scripts/check-account-group-rpm-alignment.py --target prod --json
"""
from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
EDGE_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"

PROD = {
    "label": "prod",
    "stack": "tokenkey-prod-stage0",
    "region": "us-east-1",
}

QUERY = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'account_id',     a.id,
  'account_name',   a.name,
  'group_id',       g.id,
  'group_name',     g.name,
  'base_rpm',       NULLIF(a.extra->>'base_rpm','')::int,
  'group_rpm_limit', g.rpm_limit
) ORDER BY a.name, g.name), '[]'::jsonb)
FROM accounts a
JOIN account_groups ag ON ag.account_id = a.id
JOIN groups g          ON g.id          = ag.group_id AND g.deleted_at IS NULL
WHERE a.platform = 'anthropic'
  AND a.type     = 'oauth'
  AND a.deleted_at IS NULL
  AND NULLIF(a.extra->>'base_rpm', '') IS NOT NULL;
"""


def fail(msg: str, code: int = 2) -> None:
    print(f"::error::{msg}", file=sys.stderr)
    sys.exit(code)


def resolve_target(name: str) -> dict[str, str]:
    if name == "prod":
        return dict(PROD)
    if not EDGE_MATRIX.exists():
        fail(f"edge matrix not found: {EDGE_MATRIX}")
    matrix = json.loads(EDGE_MATRIX.read_text())
    targets = matrix.get("targets") or {}
    if name not in targets:
        known = ", ".join(["prod"] + sorted(targets))
        fail(f"unknown target {name!r}; known: {known}")
    tgt = targets[name]
    for key in ("region", "stack"):
        if key not in tgt:
            fail(f"edge target {name} missing required field {key}")
    return {"label": name, "stack": tgt["stack"], "region": tgt["region"]}


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
        return ""  # unreachable
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
                "aws",
                "ssm",
                "send-command",
                "--region",
                region,
                "--instance-ids",
                inst,
                "--document-name",
                "AWS-RunShellScript",
                "--comment",
                comment,
                "--parameters",
                params,
                "--query",
                "Command.CommandId",
                "--output",
                "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"ssm send-command failed: {e}")
        return "", ""  # unreachable
    subprocess.run(
        [
            "aws",
            "ssm",
            "wait",
            "command-executed",
            "--region",
            region,
            "--command-id",
            cid,
            "--instance-id",
            inst,
        ],
        check=False,
    )
    inv = json.loads(
        subprocess.check_output(
            [
                "aws",
                "ssm",
                "get-command-invocation",
                "--region",
                region,
                "--command-id",
                cid,
                "--instance-id",
                inst,
                "--output",
                "json",
            ],
            text=True,
        )
    )
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:600]
        fail(f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')}: {err}")
    return (inv.get("StandardOutputContent") or "").strip(), cid


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__.split("\n\n", 1)[0] if __doc__ else "",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("--target", required=True, help="edge_id (e.g. us1) or 'prod'")
    ap.add_argument("--instance-id", help="override SSM instance id (skips CFN describe-stacks)")
    ap.add_argument("--json", action="store_true", help="emit JSON report only (no human summary)")
    args = ap.parse_args()

    tgt = resolve_target(args.target)
    inst = args.instance_id or resolve_instance_id(tgt["region"], tgt["stack"])
    out, cid = run_remote(
        tgt["region"], inst, QUERY, f"S2 rpm alignment check {tgt['label']}"
    )
    try:
        rows = json.loads(out) if out else []
    except json.JSONDecodeError as e:
        fail(f"failed to parse alignment payload: {e}\n{out[:600]}")
        return 2

    violations = []
    for r in rows:
        base = r.get("base_rpm")
        gl = r.get("group_rpm_limit")
        if base is None:
            continue
        if gl in (0, None):
            continue
        if gl < base:
            violations.append(r)

    report = {
        "target": tgt["label"],
        "region": tgt["region"],
        "stack": tgt["stack"],
        "instance_id": inst,
        "ssm_command_id": cid,
        "pairs_checked": len(rows),
        "violation_count": len(violations),
        "violations": violations,
    }

    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        print(
            f"target={tgt['label']} pairs_checked={len(rows)} "
            f"violations={len(violations)} ssm_cmd={cid}"
        )
        if violations:
            for v in violations:
                print(
                    f"  - account={v['account_name']} (base_rpm={v['base_rpm']}) "
                    f"bound to group={v['group_name']} (rpm_limit={v['group_rpm_limit']}) "
                    "← bottleneck"
                )
            print(
                "fix: raise group.rpm_limit to >= base_rpm, "
                "or lower account base_rpm via tier change."
            )
        else:
            print(
                "OK: every checked anthropic OAuth account.base_rpm "
                "<= bound group.rpm_limit (or rpm_limit=0/unlimited)"
            )

    return 1 if violations else 0


if __name__ == "__main__":
    sys.exit(main())
