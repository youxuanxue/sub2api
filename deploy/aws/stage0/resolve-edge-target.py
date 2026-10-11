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
    hz_targets = _EDGE_ROUTING.load_hetzner_targets(REPO_ROOT)
    include = []
    excluded = []

    def include_live_edge(edge_id: str) -> None:
        # Live fleet: prefer deployable Hetzner Hybrid; else Lightsail.
        # INSTANCE_ID comes from ssm_prefix/ssm_managed_instance_id at diagnostics time.
        mode, _, _ = _EDGE_ROUTING.resolve_route_tab(REPO_ROOT, edge_id, "auto")
        if mode == "hetzner":
            target = hz_targets[edge_id]
            include.append(
                {
                    "target_id": f"edge-{edge_id}-hz",
                    "target_kind": "edge",
                    "platform": "hetzner",
                    "edge_id": edge_id,
                    "region": str(target.get("ssm_region") or "eu-west-2"),
                    "stack": "",
                    "domain": str(target.get("domain") or f"api-{edge_id}.tokenkey.dev"),
                    "ssm_prefix": str(target.get("ssm_prefix") or ""),
                    "purpose": str(target.get("purpose") or ""),
                }
            )
            return
        target = lightsail_targets[edge_id]
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

    live_ids = set(
        _EDGE_ROUTING.live_deployable_edge_ids(
            REPO_ROOT,
            lightsail_targets=lightsail_targets,
            hetzner_targets=hz_targets,
        )
    )

    if selector in ("all", "edge:*"):
        for edge_id in sorted(live_ids):
            include_live_edge(edge_id)
        for edge_id, ls_target in sorted(lightsail_targets.items()):
            if edge_id in live_ids:
                if _EDGE_ROUTING.edge_deployable(ls_target) and _EDGE_ROUTING.edge_hetzner_deployable(
                    hz_targets.get(edge_id)
                ):
                    excluded.append(
                        {
                            "target_id": f"edge-{edge_id}-ls",
                            "reason": "lightsail standby; live fleet prefers Hetzner",
                        }
                    )
                continue
            if _EDGE_ROUTING.edge_deployable(ls_target):
                excluded.append({"target_id": f"edge-{edge_id}-ls", "reason": "not in live fleet"})
            else:
                excluded.append({"target_id": f"edge-{edge_id}-ls", "reason": "lightsail deployable=false"})
    elif selector.startswith("edge:"):
        edge_id = selector.split(":", 1)[1].strip()
        if not edge_id:
            fail("target_selector edge: requires an edge id")
        if edge_id.endswith("-ls") or edge_id.endswith("-hz"):
            edge_id = edge_id[:-3]
        if edge_id not in live_ids:
            fail(f"target_selector {selector} is not in the live deployable fleet")
        include_live_edge(edge_id)
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
            "Print one live ops edge id per line (Hetzner deployable preferred; "
            "Lightsail-only deployable when no HZ row). "
            "With a non-default --lightsail-matrix, lists Lightsail deployable "
            "from that file only (fixture override). "
            "Mutually exclusive with --prod-ops-matrix. "
            "Exits 0 even when no edges are deployable (prints nothing)."
        ),
    )
    args = parser.parse_args()

    lightsail_path = pathlib.Path(args.lightsail_matrix).resolve()
    data = _EDGE_ROUTING.load_matrix(lightsail_path)

    if args.list_deployable:
        if args.prod_ops_matrix:
            fail("--list-deployable is mutually exclusive with --prod-ops-matrix")
        if lightsail_path == LIGHTSAIL_MATRIX.resolve():
            ids = _EDGE_ROUTING.live_deployable_edge_ids(REPO_ROOT)
        else:
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
