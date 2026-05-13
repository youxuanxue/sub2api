#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
DEFAULT_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"


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


def build_prod_ops_matrix(data: dict, *, selector: str, prod_region: str, prod_stack: str) -> tuple[dict, list[dict]]:
    selector = selector.strip() or "all"
    targets = data.get("targets") or {}
    include = []
    excluded = []

    def include_edge(edge_id: str) -> None:
        resolved = resolve_target(data, edge_id)
        include.append(
            {
                "target_id": f"edge-{edge_id}",
                "target_kind": "edge",
                "edge_id": edge_id,
                "region": resolved["region"],
                "stack": resolved["stack"],
                "domain": resolved["domain"],
                "ssm_prefix": resolved["ssm_prefix"],
                "purpose": resolved.get("purpose", ""),
            }
        )

    if selector in ("all", "prod"):
        include.append(
            {
                "target_id": "prod",
                "target_kind": "prod",
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
    elif selector.startswith("edge:"):
        edge_id = selector.split(":", 1)[1].strip()
        if not edge_id:
            fail("target_selector edge: requires an edge id")
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
    args = parser.parse_args()

    data = load_matrix(args.matrix)

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
