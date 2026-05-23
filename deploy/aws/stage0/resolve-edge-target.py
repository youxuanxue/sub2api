#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
DEFAULT_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
LIGHTSAIL_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"


def fail(message: str) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def gha_quote(value: object) -> str:
    return str(value).replace("%", "%25").replace("\n", "%0A").replace("\r", "%0D")


def load_matrix(path: str) -> dict:
    matrix_path = pathlib.Path(path)
    if not matrix_path.is_file():
        fail(f"edge target matrix not found: {matrix_path}")
    return json.loads(matrix_path.read_text(encoding="utf-8"))


def resolve_target(data: dict, edge_id: str, *, confirm_stack: str = "", profile: str = "", allow_planned: bool = False) -> dict:
    targets = data.get("targets") or {}
    target = targets.get(edge_id)
    if target is None:
        fail(f"unknown edge_id {edge_id}; known edges: {', '.join(sorted(targets))}")

    deployable = bool(target.get("deployable"))
    if not deployable and not allow_planned:
        fail(
            f"edge_id {edge_id} is planned but not deployable; "
            "set deployable=true in deploy/aws/stage0/edge-targets.json when ready"
        )

    if bool(target.get("drift_locked")):
        reason = str(target.get("drift_reason") or "no reason recorded")
        fail(
            f"edge_id {edge_id} is drift_locked; refuse to resolve target. "
            f"Reason: {reason} "
            "Clear drift_locked in deploy/aws/stage0/edge-targets.json only after running the recovery runbook."
        )

    target_profile = str(target.get("profile") or "")
    requested_profile = profile.strip()
    if requested_profile and requested_profile != target_profile:
        fail(f"profile mismatch for {edge_id}: requested {requested_profile}, matrix has {target_profile}")

    default_profile = str(data.get("default_profile") or "edge-minimal")
    if target_profile != default_profile:
        fail(f"edge_id {edge_id} profile {target_profile} is not the default allowed profile {default_profile}")

    budget = int(target.get("monthly_budget_usd", 0))
    max_budget = int(data.get("max_monthly_budget_usd", 16))
    if budget > max_budget:
        fail(f"edge_id {edge_id} budget ${budget} exceeds max ${max_budget}")

    stack = str(target.get("stack") or "")
    if confirm_stack and confirm_stack != stack:
        fail(f"confirm_stack mismatch: got {confirm_stack}, expected {stack}")

    required = [
        "region",
        "domain",
        "stack",
        "instance_type",
        "root_volume_gib",
        "data_volume_gib",
        "swap_gib",
        "snapshot_schedule",
        "monthly_budget_usd",
        "ssm_prefix",
    ]
    missing = [key for key in required if key not in target or target[key] in (None, "")]
    if missing:
        fail(f"edge_id {edge_id} missing required fields: {', '.join(missing)}")

    return {
        "edge_id": edge_id,
        "deployable": str(deployable).lower(),
        "profile": target_profile,
        "region": target["region"],
        "domain": target["domain"],
        "stack": stack,
        "instance_type": target["instance_type"],
        "root_volume_gib": target["root_volume_gib"],
        "data_volume_gib": target["data_volume_gib"],
        "swap_gib": target["swap_gib"],
        "snapshot_schedule": target["snapshot_schedule"],
        "monthly_budget_usd": budget,
        "ssm_prefix": target["ssm_prefix"],
        "purpose": target.get("purpose", ""),
    }


def _load_lightsail_targets() -> dict:
    if not LIGHTSAIL_MATRIX.is_file():
        return {}
    try:
        payload = json.loads(LIGHTSAIL_MATRIX.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return {}
    return payload.get("targets") or {}


def build_prod_ops_matrix(data: dict, *, selector: str, prod_region: str, prod_stack: str) -> tuple[dict, list[dict]]:
    selector = selector.strip() or "all"
    targets = data.get("targets") or {}
    lightsail_targets = _load_lightsail_targets()
    include = []
    excluded = []

    def include_edge(edge_id: str) -> None:
        resolved = resolve_target(data, edge_id)
        include.append(
            {
                "target_id": f"edge-{edge_id}",
                "target_kind": "edge",
                "platform": "ec2",
                "edge_id": edge_id,
                "region": resolved["region"],
                "stack": resolved["stack"],
                "domain": resolved["domain"],
                "ssm_prefix": resolved["ssm_prefix"],
                "purpose": resolved.get("purpose", ""),
            }
        )

    def include_lightsail_edge(edge_id: str, target: dict) -> None:
        # Lightsail edges have no CloudFormation stack — INSTANCE_ID is resolved
        # from ssm_prefix/ssm_managed_instance_id at diagnostics time. ApiUrl
        # likewise has no CFN output; the workflow falls back to https://<domain>.
        include.append(
            {
                "target_id": f"edge-{edge_id}-ls",
                "target_kind": "edge",
                "platform": "lightsail",
                "edge_id": edge_id,
                "region": str(target.get("lightsail_region") or ""),
                "stack": "",
                "domain": str(target.get("domain") or ""),
                "ssm_prefix": str(target.get("ssm_prefix") or ""),
                "purpose": str(target.get("purpose") or ""),
            }
        )

    if selector in ("all", "prod"):
        include.append(
            {
                "target_id": "prod",
                "target_kind": "prod",
                "platform": "ec2",
                "edge_id": "",
                "region": prod_region,
                "stack": prod_stack,
                "domain": "api.tokenkey.dev",
                "ssm_prefix": "/tokenkey/prod",
                "purpose": "primary-prod",
            }
        )
    elif selector.startswith("prod:"):
        fail(f"unsupported target_selector {selector}; use prod")

    if selector in ("all", "edge:*"):
        for edge_id, target in sorted(targets.items()):
            if bool(target.get("deployable")):
                include_edge(edge_id)
            else:
                excluded.append({"target_id": f"edge-{edge_id}", "reason": "deployable=false"})
        for edge_id, ls_target in sorted(lightsail_targets.items()):
            if bool(ls_target.get("deployable")):
                include_lightsail_edge(edge_id, ls_target)
            else:
                excluded.append({"target_id": f"edge-{edge_id}-ls", "reason": "lightsail deployable=false"})
    elif selector.startswith("edge:"):
        edge_id = selector.split(":", 1)[1].strip()
        if not edge_id:
            fail("target_selector edge: requires an edge id")
        # Allow lightsail-specific id "<edge_id>-ls" form too.
        if edge_id.endswith("-ls"):
            ls_id = edge_id[:-3]
            ls_target = lightsail_targets.get(ls_id)
            if ls_target is None:
                fail(f"unknown lightsail edge target_selector {selector}; known: {', '.join(sorted(lightsail_targets))}")
            if not bool(ls_target.get("deployable")):
                fail(f"target_selector {selector} is planned but not deployable")
            include_lightsail_edge(ls_id, ls_target)
        else:
            target = targets.get(edge_id)
            if target is None:
                fail(f"unknown edge target_selector {selector}; known edges: {', '.join(sorted(targets))}")
            if not bool(target.get("deployable")):
                fail(f"target_selector {selector} is planned but not deployable")
            include_edge(edge_id)
    elif selector not in ("all", "prod"):
        fail(f"unsupported target_selector {selector}; expected all, prod, edge:*, or edge:<id>")

    return {"include": include}, excluded


def write_outputs(path: str, outputs: dict) -> None:
    if not path:
        return
    with open(path, "a", encoding="utf-8") as fh:
        for key, value in outputs.items():
            fh.write(f"{key}={gha_quote(value)}\n")


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve a TokenKey Edge Stage0 target.")
    parser.add_argument("--edge-id", default="")
    parser.add_argument("--confirm-stack", default="")
    parser.add_argument("--profile", default="")
    parser.add_argument("--matrix", default=str(DEFAULT_MATRIX))
    parser.add_argument("--allow-planned", action="store_true")
    parser.add_argument("--github-output", default="")
    parser.add_argument("--prod-ops-matrix", action="store_true")
    parser.add_argument("--target-selector", default="all")
    parser.add_argument("--prod-region", default="us-east-1")
    parser.add_argument("--prod-stack", default="tokenkey-prod-stage0")
    parser.add_argument(
        "--list-deployable",
        action="store_true",
        help=(
            "Print one deployable edge id per line (deployable=true in the matrix). "
            "Mutually exclusive with --edge-id / --prod-ops-matrix. "
            "Stable output for shell consumers in skills/scripts; "
            "exits 0 even when no edges are deployable (prints nothing)."
        ),
    )
    args = parser.parse_args()

    data = load_matrix(args.matrix)

    if args.list_deployable:
        if args.edge_id or args.prod_ops_matrix:
            fail("--list-deployable is mutually exclusive with --edge-id / --prod-ops-matrix")
        targets = data.get("targets") or {}
        for edge_id in sorted(targets):
            if bool(targets[edge_id].get("deployable")):
                print(edge_id)
        return 0

    if args.prod_ops_matrix:
        matrix, excluded = build_prod_ops_matrix(
            data,
            selector=args.target_selector,
            prod_region=args.prod_region,
            prod_stack=args.prod_stack,
        )
        outputs = {
            "matrix": json.dumps(matrix, separators=(",", ":")),
            "excluded": json.dumps(excluded, separators=(",", ":")),
            "has_targets": "true" if matrix["include"] else "false",
        }
        print(json.dumps({"matrix": matrix, "excluded": excluded}, indent=2, sort_keys=True))
        write_outputs(args.github_output, outputs)
        return 0

    if not args.edge_id:
        fail("--edge-id is required unless --prod-ops-matrix is set")

    outputs = resolve_target(
        data,
        args.edge_id,
        confirm_stack=args.confirm_stack,
        profile=args.profile,
        allow_planned=args.allow_planned,
    )

    for key, value in outputs.items():
        print(f"{key}={value}")

    write_outputs(args.github_output, outputs)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
