#!/usr/bin/env python3
from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
LIGHTSAIL_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"

_ROUT_SPEC = importlib.util.spec_from_file_location(
    "deploy_edge_routing_matrix",
    REPO_ROOT / "ops/stage0/edge_routing_matrix.py",
)
if _ROUT_SPEC is None or _ROUT_SPEC.loader is None:
    raise RuntimeError("cannot load ops/stage0/edge_routing_matrix.py")
_EDGE_ROUTING = importlib.util.module_from_spec(_ROUT_SPEC)
sys.modules.setdefault(_ROUT_SPEC.name, _EDGE_ROUTING)
_ROUT_SPEC.loader.exec_module(_EDGE_ROUTING)


def fail(message: str) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def gha_quote(value: object) -> str:
    return str(value).replace("%", "%25").replace("\n", "%0A").replace("\r", "%0D")


def build_prod_ops_matrix(data: dict, *, selector: str, prod_region: str, prod_stack: str) -> tuple[dict, list[dict]]:
    selector = selector.strip() or "all"
    lightsail_targets = data["targets"]
    include = []
    excluded = []

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
        for edge_id, ls_target in sorted(lightsail_targets.items()):
            if _EDGE_ROUTING.edge_deployable(ls_target):
                include_lightsail_edge(edge_id, ls_target)
            else:
                excluded.append({"target_id": f"edge-{edge_id}-ls", "reason": "lightsail deployable=false"})
    elif selector.startswith("edge:"):
        edge_id = selector.split(":", 1)[1].strip()
        if not edge_id:
            fail("target_selector edge: requires an edge id")
        if edge_id.endswith("-ls"):
            edge_id = edge_id[:-3]
        target = lightsail_targets.get(edge_id)
        if target is None:
            fail(f"unknown edge target_selector {selector}; known: {', '.join(sorted(lightsail_targets))}")
        if not _EDGE_ROUTING.edge_deployable(target):
            fail(f"target_selector {selector} is planned but not deployable")
        include_lightsail_edge(edge_id, target)
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
    parser.add_argument("--github-output", default="")
    parser.add_argument("--prod-ops-matrix", action="store_true")
    parser.add_argument("--target-selector", default="all")
    parser.add_argument("--prod-region", default="us-east-1")
    parser.add_argument("--prod-stack", default="tokenkey-prod-stage0")
    parser.add_argument(
        "--lightsail-matrix",
        default=str(LIGHTSAIL_MATRIX),
        help=(
            "Lightsail edges JSON (defaults to shipped deploy/aws/lightsail path). "
            "Used for both --list-deployable and --prod-ops-matrix."
        ),
    )
    parser.add_argument(
        "--list-deployable",
        action="store_true",
        help=(
            "Print one deployable Lightsail edge id per line. "
            "Mutually exclusive with --prod-ops-matrix. "
            "Stable output for shell consumers in skills/scripts; "
            "exits 0 even when no edges are deployable (prints nothing)."
        ),
    )
    args = parser.parse_args()

    data = _EDGE_ROUTING.load_matrix(pathlib.Path(args.lightsail_matrix))

    if args.list_deployable:
        if args.prod_ops_matrix:
            fail("--list-deployable is mutually exclusive with --prod-ops-matrix")
        ids = _EDGE_ROUTING.deployable_edge_ids(data["targets"])
        for edge_id in ids:
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

    fail("--list-deployable or --prod-ops-matrix is required; deploy targets use resolve-edge-lightsail-target.py")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
