#!/usr/bin/env python3
"""Matrix-only routing: which transport (EC2 CFN vs Lightsail) owns an edge_id.

AWS calls live in ``edge_ssm_execution.py``.  This module is imported by CLI
helpers and orchestrators — keep it boto-free."""

from __future__ import annotations

import json
import pathlib
import sys
from typing import Literal


def _fail(msg: str, code: int = 2) -> None:
    print(msg, file=sys.stderr)
    raise SystemExit(code)


def load_matrix(path: pathlib.Path) -> dict:
    raw = path.read_text(encoding="utf-8")
    return json.loads(raw)


def resolve_route_tab(
    repo_root: pathlib.Path | str,
    edge_id: str,
    platform: Literal["auto", "ec2", "lightsail"] = "auto",
) -> tuple[Literal["ec2", "lightsail"], str, str | None]:
    """Return ``(mode, region, ec2_stack_or_empty)``.

    * ``mode=lightsail`` → ``region`` is ``lightsail_region``; stack is omitted (``None``).
    * ``mode=ec2`` → ``region`` + ``stack`` come from EC2 matrix.
    """
    root = pathlib.Path(repo_root).resolve()
    eid = edge_id.strip()
    pref = platform

    ec2_path = root / "deploy/aws/stage0/edge-targets.json"
    if not ec2_path.is_file():
        _fail(f"EC2 edge matrix not found: {ec2_path}")

    ls_path = root / "deploy/aws/lightsail/edge-targets-lightsail.json"
    ls_target: dict | None = None
    if ls_path.is_file():
        try:
            ls_data = load_matrix(ls_path)
        except (OSError, json.JSONDecodeError) as exc:
            _fail(f"invalid lightsail matrix ({ls_path}): {exc}")
        ls_target = (ls_data.get("targets") or {}).get(eid)

    ec2_data = load_matrix(ec2_path)
    ec2_target = (ec2_data.get("targets") or {}).get(eid)

    if pref == "lightsail":
        if not ls_target:
            _fail(
                f"edge_id={eid} not in lightsail matrix or matrix missing ({ls_path})",
            )
        region = ls_target.get("lightsail_region")
        if not region:
            _fail(f"lightsail target {eid} missing lightsail_region")
        return ("lightsail", str(region), None)

    if pref == "ec2":
        if not ec2_target:
            _fail(f"unknown edge_id in EC2 matrix: {eid}")
        region = ec2_target.get("region")
        stack = ec2_target.get("stack")
        if not region or not stack:
            _fail(f"edge {eid} missing region/stack in EC2 matrix")
        return ("ec2", str(region), str(stack))

    # auto
    if (
        ls_target
        and ls_target.get("deployable") is True
        and ls_target.get("lightsail_region")
    ):
        return ("lightsail", str(ls_target["lightsail_region"]), None)

    if not ec2_target:
        _fail(
            f"edge {eid}: no Lightsail deployable route and unknown in EC2 matrix",
        )
    region = ec2_target.get("region")
    stack = ec2_target.get("stack")
    if not region or not stack:
        _fail(f"edge {eid} missing region/stack for EC2 fallback")
    return ("ec2", str(region), str(stack))


def load_lightsail_targets(repo_root: pathlib.Path | str) -> dict[str, dict]:
    root = pathlib.Path(repo_root).resolve()
    ls_path = root / "deploy/aws/lightsail/edge-targets-lightsail.json"
    if not ls_path.is_file():
        return {}
    try:
        data = load_matrix(ls_path)
    except (OSError, json.JSONDecodeError):
        return {}
    targets = data.get("targets")
    return targets if isinstance(targets, dict) else {}


def edge_effective_deployable(
    ec2_target: dict | None,
    ls_target: dict | None,
) -> bool:
    """True when this edge should be treated as an active Stage0 edge target.

    Lightsail matrix wins when ``deployable=true`` + region; otherwise EC2
    ``deployable=true`` applies (exclusivity gate in CI keeps a single live
    platform per edge_id in normal operation).
    """
    ls_ok = bool(
        ls_target
        and ls_target.get("deployable") is True
        and ls_target.get("lightsail_region")
        and ls_target.get("ssm_prefix"),
    )
    ec2_ok = bool(
        ec2_target
        and ec2_target.get("deployable") is True
        and ec2_target.get("region")
        and ec2_target.get("stack"),
    )
    return ls_ok or ec2_ok


def merged_edge_ids(ec2_matrix: dict, ls_targets: dict[str, dict]) -> list[str]:
    ec2_keys = set((ec2_matrix.get("targets") or {}).keys())
    ls_keys = set(ls_targets.keys())
    return sorted(ec2_keys | ls_keys)


def iter_effective_deployable_edge_ids(
    ec2_matrix: dict,
    ls_targets: dict[str, dict],
) -> list[str]:
    out: list[str] = []
    for eid in merged_edge_ids(ec2_matrix, ls_targets):
        ec2_t = (ec2_matrix.get("targets") or {}).get(eid)
        ls_t = ls_targets.get(eid)
        if edge_effective_deployable(ec2_t, ls_t):
            out.append(eid)
    return sorted(out)
