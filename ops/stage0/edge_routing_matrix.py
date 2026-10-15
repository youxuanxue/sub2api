#!/usr/bin/env python3
"""Matrix-only routing for Lightsail / Hetzner edges; AWS calls live in edge_ssm_execution."""
from __future__ import annotations

import json
import pathlib
from typing import Literal

Platform = Literal["auto", "ec2", "lightsail", "hetzner"]
Transport = Literal["lightsail", "hetzner"]


def load_matrix(path: pathlib.Path) -> dict:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or not isinstance(data.get("targets"), dict):
        raise ValueError(f"invalid edge target matrix: {path}")
    if any(not isinstance(target, dict) for target in data["targets"].values()):
        raise ValueError(f"invalid edge target entry: {path}")
    return data


def load_lightsail_targets(repo_root: pathlib.Path | str) -> dict[str, dict]:
    return load_matrix(
        pathlib.Path(repo_root).resolve() / "deploy/aws/lightsail/edge-targets-lightsail.json"
    )["targets"]


def materialize_hetzner_target(edge_id: str, matrix: dict, raw: dict | None) -> dict:
    """Merge target_defaults + derived names; explicit raw fields win."""
    merged = dict(matrix.get("target_defaults") or {})
    merged.update(raw or {})
    merged.setdefault("profile", matrix.get("default_profile") or "edge-hetzner-cax21")
    merged.setdefault("domain", f"api-{edge_id}.tokenkey.dev")
    merged.setdefault("staging_domain", f"api-{edge_id}-hz.tokenkey.dev")
    merged.setdefault("instance_name", f"tokenkey-edge-{edge_id}-hz-cax21")
    merged.setdefault("floating_ip_name", f"tokenkey-edge-{edge_id}-hz-fip")
    merged.setdefault("ssm_prefix", f"/tokenkey/hetzner/{edge_id}")
    merged.setdefault("ssm_region", "eu-west-2")
    return merged


def load_hetzner_matrix(repo_root: pathlib.Path | str) -> dict:
    return load_matrix(
        pathlib.Path(repo_root).resolve() / "deploy/hetzner/edge-targets-hetzner.json"
    )


def load_hetzner_targets(repo_root: pathlib.Path | str) -> dict[str, dict]:
    data = load_hetzner_matrix(repo_root)
    return {
        eid: materialize_hetzner_target(eid, data, raw)
        for eid, raw in (data.get("targets") or {}).items()
    }


def edge_deployable(target: dict | None) -> bool:
    """Lightsail deployable predicate (historical name kept for callers)."""
    return bool(
        target
        and target.get("deployable") is True
        and target.get("lightsail_region")
        and target.get("ssm_prefix")
    )


def edge_hetzner_deployable(target: dict | None) -> bool:
    """Expects a materialized Hetzner row (defaults already applied)."""
    return bool(
        target
        and target.get("deployable") is True
        and target.get("location")
        and target.get("server_type")
        and target.get("ssm_prefix")
        and target.get("instance_name")
    )


def deployable_edge_ids(targets: dict[str, dict]) -> list[str]:
    """Lightsail-only deployable ids (historical; prefer live_deployable_edge_ids for ops fan-out)."""
    return sorted(eid for eid, target in targets.items() if edge_deployable(target))


def live_deployable_edge_ids(
    repo_root: pathlib.Path | str,
    *,
    lightsail_targets: dict[str, dict] | None = None,
    hetzner_targets: dict[str, dict] | None = None,
) -> list[str]:
    """Only deployable rows enter automatic fan-out; dual activation fails closed."""
    root = pathlib.Path(repo_root).resolve()
    hz = hetzner_targets if hetzner_targets is not None else load_hetzner_targets(root)
    ls = lightsail_targets if lightsail_targets is not None else load_lightsail_targets(root)
    hz_ids = {eid for eid, target in hz.items() if edge_hetzner_deployable(target)}
    ls_ids = {eid for eid, target in ls.items() if edge_deployable(target)}
    if hz_ids & ls_ids:
        raise SystemExit("ambiguous active edge inventory")
    return sorted(hz_ids | ls_ids)


def resolve_route_tab(
    repo_root: pathlib.Path | str,
    edge_id: str,
    platform: Platform = "auto",
) -> tuple[Transport, str, None]:
    """Return (transport, region_or_location, empty stack).

    ``auto`` requires exactly one deployable platform; it never selects inactive rows.
    Explicit ``lightsail`` / ``hetzner`` force that matrix (planned rows allowed).
    """
    if platform == "ec2":
        raise SystemExit("EC2 edge routing is retired; edges use Lightsail or Hetzner")

    eid = edge_id.strip()
    root = pathlib.Path(repo_root).resolve()

    if platform in ("auto", "hetzner"):
        hz_targets = load_hetzner_targets(root)
        hz = hz_targets.get(eid)
        if platform == "hetzner":
            if hz is None:
                raise SystemExit(f"unknown Hetzner edge_id: {eid}")
            if not hz.get("location"):
                raise SystemExit(f"hetzner target {eid} missing location")
            return "hetzner", str(hz["location"]), None
        if edge_hetzner_deployable(hz) and edge_deployable(load_lightsail_targets(root).get(eid)):
            raise SystemExit("ambiguous active edge inventory")
        if edge_hetzner_deployable(hz):
            return "hetzner", str(hz["location"]), None
        candidate = load_lightsail_targets(root).get(eid)
        if edge_deployable(candidate):
            return "lightsail", str(candidate["lightsail_region"]), None
        raise SystemExit(f"edge {eid} is not deployable in the active fleet")

    if platform not in ("auto", "lightsail"):
        raise SystemExit(f"unsupported platform preference: {platform}")

    target = load_lightsail_targets(root).get(eid)
    if target is None:
        raise SystemExit(f"unknown Lightsail edge_id: {eid}")
    if not target.get("lightsail_region"):
        raise SystemExit(f"lightsail target {eid} missing lightsail_region")
    if platform == "auto" and not edge_deployable(target):
        raise SystemExit(f"edge {eid} is not deployable")
    return "lightsail", str(target["lightsail_region"]), None


def load_live_targets(repo_root: pathlib.Path | str) -> dict[str, dict]:
    hz = load_hetzner_targets(repo_root)
    ls = load_lightsail_targets(repo_root)
    ids = live_deployable_edge_ids(repo_root, hetzner_targets=hz, lightsail_targets=ls)
    return {eid: (hz[eid] if edge_hetzner_deployable(hz.get(eid)) else ls[eid]) for eid in ids}


def validate_live_fleet(repo_root):
    """Require every contracted edge on exactly one platform, including raw flags."""
    hz = load_hetzner_targets(repo_root)
    ls = load_lightsail_targets(repo_root)
    expected = {"uk1", "uk2", "us4", "us5"}
    for platform, rows in (("hetzner", hz), ("lightsail", ls)):
        predicate = edge_hetzner_deployable if platform == "hetzner" else edge_deployable
        for eid, row in rows.items():
            if row.get("deployable") is True and (eid not in expected or not predicate(row)):
                raise ValueError(f"invalid active {platform} edge: {eid}")
    actual = set(live_deployable_edge_ids(repo_root, hetzner_targets=hz, lightsail_targets=ls))
    if actual != expected:
        raise ValueError(f"live fleet mismatch: missing={sorted(expected - actual)}, extra={sorted(actual - expected)}")
    return sorted(actual)


if __name__ == "__main__":
    validate_live_fleet(pathlib.Path(__file__).resolve().parents[2])
    print("cutover_matrix_guard=ok")
