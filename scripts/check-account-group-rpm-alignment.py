#!/usr/bin/env python3
"""
S2 guard: account redline ↔ group.rpm_limit alignment.

For every active anthropic group with `rpm_limit > 0`, verify the
group's RPM cap sits within the band of its bound anthropic OAuth
accounts' per-account redline:

  layer A (per-account):  max(redline) ≤ group.rpm_limit
                          — otherwise the group caps a single account
                            below its tier-declared ceiling (silent
                            bottleneck; original S2 case from H1
                            uk1/fra1).

  layer B (group-aggregate): Σ(redline) ≥ group.rpm_limit
                          — otherwise the group's cap exceeds the
                            combined RPM the bound OAuth accounts can
                            actually sustain (the cap is virtual).

Two evaluation modes (the redline definition differs):

  default (legacy base_rpm mode):
      redline = account.extra.base_rpm
    Pre-strict-redline historical behavior.  Ignores sticky_buffer
    space, which means the group cap can be set right at Σ(base_rpm)
    and silently throttle the StickyOnly (yellow-zone) traffic that
    runtime account-level scheduling intends to allow.

  --strict-redline (recommended; matches runtime account-level
  ceiling):
      redline = account.extra.base_rpm
              + account.extra.rpm_sticky_buffer
    Mirrors `(*Account).CheckRPMSchedulability`'s NotSchedulable
    threshold (`backend/internal/service/account.go`).  Each account
    contributes its physical hard ceiling (base_rpm + buffer) instead
    of the green-zone ceiling alone, so the group cap leaves room for
    in-flight StickyOnly traffic.

Strict-mode preconditions (fail-loud, treated as configuration drift):

  - Any OAuth account with `extra.rpm_strategy = 'sticky_exempt'`
    has no finite redline.  Such groups are reported as `skipped`
    with reason; operator must review the cap manually.  TokenKey
    does not enable this strategy today.

  - Any OAuth account missing `extra.rpm_sticky_buffer` (NULL / 0 /
    empty string) is treated as a Layer-C drift violation — strict
    mode requires every account to carry the manual override field
    that baseline tiers (L1..L5) already populate.

Groups with `rpm_limit = 0` (or NULL) are treated as "unlimited" and
skipped entirely. Groups with `rpm_limit > 0` but **no** anthropic
OAuth account bound (e.g. prod `cc-edges` which only holds stubs) are
out of scope — the H4 stub-only design intentionally has no
account.base_rpm to compare against. They are reported as `skipped`
with a clear reason and do not count as violations.

Targets one stack per invocation:
  --target prod       → tokenkey-prod-stage0 (us-east-1)
  --target <edge_id>  → deploy/aws/stage0/edge-targets.json

Exit codes:
  0  all in-scope groups satisfy both layers
  1  one or more violations (layer A / B / C)
  2  schema / SSM / target-resolution error

Usage:
  python3 scripts/check-account-group-rpm-alignment.py --target us1
  python3 scripts/check-account-group-rpm-alignment.py --target prod --json
  python3 scripts/check-account-group-rpm-alignment.py --target us1 --strict-redline
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
  'group_id',   g.id,
  'group_name', g.name,
  'rpm_limit',  g.rpm_limit,
  'oauth_accounts', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'account_id', a.id,
      'account_name', a.name,
      'base_rpm', NULLIF(a.extra->>'base_rpm','')::int,
      'rpm_sticky_buffer', NULLIF(a.extra->>'rpm_sticky_buffer','')::int,
      'rpm_strategy', NULLIF(a.extra->>'rpm_strategy','')
    ) ORDER BY a.id)
    FROM account_groups ag
    JOIN accounts a ON a.id = ag.account_id
    WHERE ag.group_id = g.id
      AND a.platform  = 'anthropic'
      AND a.type      = 'oauth'
      AND a.deleted_at IS NULL
  ), '[]'::jsonb)
) ORDER BY g.id), '[]'::jsonb)
FROM groups g
WHERE g.platform = 'anthropic'
  AND g.deleted_at IS NULL;
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
                "aws", "cloudformation", "describe-stacks",
                "--region", region, "--stack-name", stack,
                "--query", "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue",
                "--output", "text",
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
            "--region", region, "--command-id", cid, "--instance-id", inst,
        ],
        check=False,
    )
    inv = json.loads(
        subprocess.check_output(
            [
                "aws", "ssm", "get-command-invocation",
                "--region", region, "--command-id", cid, "--instance-id", inst,
                "--output", "json",
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
    ap.add_argument("--instance-id", help="override SSM instance id")
    ap.add_argument("--json", action="store_true", help="emit JSON report only")
    ap.add_argument(
        "--strict-redline",
        action="store_true",
        help=(
            "evaluate redline as base_rpm + rpm_sticky_buffer (matches "
            "runtime CheckRPMSchedulability). Fails on accounts missing "
            "rpm_sticky_buffer; skips groups containing sticky_exempt "
            "accounts with explicit reason."
        ),
    )
    args = ap.parse_args()

    tgt = resolve_target(args.target)
    inst = args.instance_id or resolve_instance_id(tgt["region"], tgt["stack"])
    out, cid = run_remote(
        tgt["region"], inst, QUERY, f"S2 rpm alignment check {tgt['label']}"
    )
    try:
        groups = json.loads(out) if out else []
    except json.JSONDecodeError as e:
        fail(f"failed to parse alignment payload: {e}\n{out[:600]}")
        return 2

    strict = args.strict_redline
    results = []
    violation_count = 0
    for g in groups:
        rpm_limit = g.get("rpm_limit") or 0
        accounts = g.get("oauth_accounts") or []

        rec = {
            "group_id": g["group_id"],
            "group_name": g["group_name"],
            "rpm_limit": rpm_limit,
            "oauth_account_count": len(accounts),
            "mode": "strict_redline" if strict else "base_rpm",
            "status": "ok",
            "skip_reason": None,
            "layer_a_violation": None,
            "layer_b_violation": None,
            "layer_c_violation": None,
        }

        if rpm_limit == 0:
            rec["status"] = "skipped"
            rec["skip_reason"] = "rpm_limit=0 (unlimited)"
            results.append(rec)
            continue

        # Strict mode: groups containing sticky_exempt accounts have no
        # finite redline (the strategy bypasses the red zone). Skip with
        # explicit reason instead of silently passing or panicking.
        sticky_exempt = (
            [a for a in accounts if (a.get("rpm_strategy") or "") == "sticky_exempt"]
            if strict
            else []
        )
        if sticky_exempt:
            rec["status"] = "skipped"
            rec["skip_reason"] = (
                f"rpm_limit={rpm_limit}; group contains "
                f"{len(sticky_exempt)} account(s) with rpm_strategy="
                "'sticky_exempt' (no finite redline). Review cap manually."
            )
            rec["sticky_exempt_accounts"] = [
                {"account_id": a["account_id"], "account_name": a["account_name"]}
                for a in sticky_exempt
            ]
            results.append(rec)
            continue

        base_rpm_accounts = [a for a in accounts if a.get("base_rpm") is not None]
        if not base_rpm_accounts:
            rec["status"] = "skipped"
            rec["skip_reason"] = (
                f"rpm_limit={rpm_limit} but no anthropic OAuth account "
                "with extra.base_rpm bound (out of scope: stub-only group)"
            )
            results.append(rec)
            continue

        if strict:
            # Layer C: every account with base_rpm must also carry a
            # manual override rpm_sticky_buffer > 0. Baseline tiers
            # L1-L5 all supply this; absent values are config drift
            # that must be resolved before apply.
            missing_buffer = [
                a
                for a in base_rpm_accounts
                if a.get("rpm_sticky_buffer") is None
                or a["rpm_sticky_buffer"] <= 0
            ]
            if missing_buffer:
                rec["layer_c_violation"] = {
                    "rule": (
                        "every account with extra.base_rpm must also "
                        "carry extra.rpm_sticky_buffer > 0 (strict mode "
                        "requires manual override; baseline tiers L1-L5 "
                        "already supply it)"
                    ),
                    "offenders": [
                        {
                            "account_id": a["account_id"],
                            "account_name": a["account_name"],
                            "base_rpm": a["base_rpm"],
                            "rpm_sticky_buffer": a.get("rpm_sticky_buffer"),
                        }
                        for a in missing_buffer
                    ],
                }
                rec["status"] = "fail"
                violation_count += 1
                results.append(rec)
                continue

            redlines = [
                {
                    "account_id": a["account_id"],
                    "account_name": a["account_name"],
                    "base_rpm": a["base_rpm"],
                    "rpm_sticky_buffer": a["rpm_sticky_buffer"],
                    "redline": a["base_rpm"] + a["rpm_sticky_buffer"],
                }
                for a in base_rpm_accounts
            ]
            rec["max_redline"] = max(r["redline"] for r in redlines)
            rec["sum_redline"] = sum(r["redline"] for r in redlines)
            ceiling_value = rec["max_redline"]
            sum_value = rec["sum_redline"]
            layer_a_rule = (
                "max(account.base_rpm + rpm_sticky_buffer) <= group.rpm_limit"
            )
            layer_b_rule = (
                "sum(account.base_rpm + rpm_sticky_buffer) >= group.rpm_limit"
            )
            ceiling_key = "max_redline"
            sum_key = "sum_redline"
            layer_a_offenders = [r for r in redlines if r["redline"] > rpm_limit]
        else:
            base_rpms = [a["base_rpm"] for a in base_rpm_accounts]
            rec["base_rpm_count"] = len(base_rpms)
            rec["max_base_rpm"] = max(base_rpms)
            rec["sum_base_rpm"] = sum(base_rpms)
            ceiling_value = rec["max_base_rpm"]
            sum_value = rec["sum_base_rpm"]
            layer_a_rule = "max(account.base_rpm) <= group.rpm_limit"
            layer_b_rule = "sum(account.base_rpm) >= group.rpm_limit"
            ceiling_key = "max_base_rpm"
            sum_key = "sum_base_rpm"
            layer_a_offenders = [
                a for a in base_rpm_accounts if a["base_rpm"] > rpm_limit
            ]

        if ceiling_value > rpm_limit:
            rec["layer_a_violation"] = {
                "rule": layer_a_rule,
                "rpm_limit": rpm_limit,
                ceiling_key: ceiling_value,
                "offenders": layer_a_offenders,
            }
            rec["status"] = "fail"

        if sum_value < rpm_limit:
            rec["layer_b_violation"] = {
                "rule": layer_b_rule,
                "rpm_limit": rpm_limit,
                sum_key: sum_value,
            }
            rec["status"] = "fail"

        if rec["status"] == "fail":
            violation_count += 1

        results.append(rec)

    report = {
        "target": tgt["label"],
        "region": tgt["region"],
        "stack": tgt["stack"],
        "instance_id": inst,
        "ssm_command_id": cid,
        "mode": "strict_redline" if strict else "base_rpm",
        "groups_checked": len(results),
        "violation_count": violation_count,
        "results": results,
    }

    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        skipped = sum(1 for r in results if r["status"] == "skipped")
        ok = sum(1 for r in results if r["status"] == "ok")
        mode_label = "strict_redline" if strict else "base_rpm"
        print(
            f"target={tgt['label']} mode={mode_label} "
            f"groups_checked={len(results)} ok={ok} skipped={skipped} "
            f"violations={violation_count} ssm_cmd={cid}"
        )
        for r in results:
            head = (
                f"  [{r['status'].upper()}] group_id={r['group_id']} "
                f"name={r['group_name']!r} rpm_limit={r['rpm_limit']} "
                f"oauth_accounts={r['oauth_account_count']}"
            )
            print(head)
            if r["status"] == "skipped":
                print(f"      skip: {r['skip_reason']}")
                continue

            lc = r.get("layer_c_violation")
            if lc:
                offenders = ", ".join(
                    f"{a['account_name']}"
                    f"(base_rpm={a['base_rpm']},"
                    f"rpm_sticky_buffer={a.get('rpm_sticky_buffer')})"
                    for a in lc["offenders"]
                )
                print(
                    f"      layer C FAIL ({lc['rule']}): "
                    f"offenders=[{offenders}]"
                )
                print(
                    "      fix: set extra.rpm_sticky_buffer per baseline "
                    "tier (L1=2 / L2=4 / L3=6 / L4=8 / L5=12) before apply."
                )
                continue

            if r["mode"] == "strict_redline":
                print(
                    f"      max(redline)={r['max_redline']} "
                    f"sum(redline)={r['sum_redline']} "
                    "(redline = base_rpm + rpm_sticky_buffer)"
                )
            else:
                print(
                    f"      max(base_rpm)={r['max_base_rpm']} "
                    f"sum(base_rpm)={r['sum_base_rpm']}"
                )

            la = r["layer_a_violation"]
            if la:
                if r["mode"] == "strict_redline":
                    offenders = ", ".join(
                        f"{a['account_name']}(redline={a['redline']}"
                        f"=base_rpm({a['base_rpm']})+buffer({a['rpm_sticky_buffer']}))"
                        for a in la["offenders"]
                    )
                    ceiling = la["max_redline"]
                else:
                    offenders = ", ".join(
                        f"{a['account_name']}(base_rpm={a['base_rpm']})"
                        for a in la["offenders"]
                    )
                    ceiling = la["max_base_rpm"]
                print(
                    f"      layer A FAIL ({la['rule']}): "
                    f"max={ceiling} > rpm_limit={la['rpm_limit']} "
                    f"offenders=[{offenders}]"
                )
                print(
                    f"      fix: raise group.rpm_limit to ≥ {ceiling}, "
                    "or lower offending account tier."
                )
            lb = r["layer_b_violation"]
            if lb:
                floor_sum = lb.get("sum_redline", lb.get("sum_base_rpm"))
                print(
                    f"      layer B FAIL ({lb['rule']}): "
                    f"sum={floor_sum} < rpm_limit={lb['rpm_limit']}"
                )
                print(
                    f"      fix: lower group.rpm_limit to ≤ {floor_sum}, "
                    "or add OAuth account capacity to the group."
                )

    return 1 if violation_count > 0 else 0


if __name__ == "__main__":
    sys.exit(main())
