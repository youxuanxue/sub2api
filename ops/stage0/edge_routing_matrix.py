#!/usr/bin/env python3
"""Matrix-only routing for Lightsail edges; AWS calls live in edge_ssm_execution."""
from __future__ import annotations

import json
import pathlib
from typing import Literal


def load_matrix(path: pathlib.Path) -> dict:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or not isinstance(data.get("targets"), dict):
        raise ValueError(f"invalid edge target matrix: {path}")
    if any(not isinstance(target, dict) for target in data["targets"].values()):
        raise ValueError(f"invalid edge target entry: {path}")
    return data


def load_lightsail_targets(repo_root: pathlib.Path | str) -> dict[str, dict]:
    return load_matrix(pathlib.Path(repo_root).resolve() /
                       "deploy/aws/lightsail/edge-targets-lightsail.json")["targets"]


def edge_deployable(target: dict | None) -> bool:
    return bool(target and target.get("deployable") is True
                and target.get("lightsail_region") and target.get("ssm_prefix"))


def deployable_edge_ids(targets: dict[str, dict]) -> list[str]:
    return sorted(eid for eid, target in targets.items() if edge_deployable(target))


def resolve_route_tab(
    repo_root: pathlib.Path | str,
    edge_id: str,
    platform: Literal["auto", "ec2", "lightsail"] = "auto",
) -> tuple[Literal["lightsail"], str, None]:
    """Return (transport, region, empty stack), preserving the shell output contract."""
    if platform not in ("auto", "lightsail"):
        raise SystemExit("EC2 edge routing is retired; edges use Lightsail")
    eid = edge_id.strip()
    target = load_lightsail_targets(repo_root).get(eid)
    if target is None:
        raise SystemExit(f"unknown Lightsail edge_id: {eid}")
    if not target.get("lightsail_region"):
        raise SystemExit(f"lightsail target {eid} missing lightsail_region")
    if platform == "auto" and not edge_deployable(target):
        raise SystemExit(f"edge {eid} is not deployable")
    return "lightsail", str(target["lightsail_region"]), None
