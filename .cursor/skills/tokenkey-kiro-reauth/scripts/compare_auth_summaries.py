#!/usr/bin/env python3
"""Compare a local Kiro auth summary with an edge auth summary."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any


def read_json(path: str) -> dict[str, Any]:
    raw = sys.stdin.read() if path == "-" else Path(path).read_text(encoding="utf-8")
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"invalid JSON in {path}: {exc}")
    if not isinstance(data, dict):
        raise SystemExit(f"expected JSON object in {path}")
    return data


def compare_pair(left: dict[str, Any], right: dict[str, Any], key: str) -> dict[str, Any]:
    a = left.get(key)
    b = right.get(key)
    return {"match": a == b, "local": a, "edge": b}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--local", required=True, help="local summary JSON path or -")
    parser.add_argument("--edge", required=True, help="edge summary JSON path or -")
    parser.add_argument(
        "--allow-access-mismatch",
        action="store_true",
        help="treat access token mismatch as non-fatal when other control-plane fields match",
    )
    args = parser.parse_args()

    local = read_json(args.local)
    edge = read_json(args.edge)

    checks: dict[str, Any] = {
        "auth_method": compare_pair(local, edge, "auth_method"),
        "region": compare_pair(local, edge, "region"),
        "refresh_token": compare_pair(local, edge, "refresh_md5_16"),
        "access_token": compare_pair(local, edge, "access_md5_16"),
    }

    auth_method = str(local.get("auth_method") or edge.get("auth_method") or "").lower()
    if auth_method == "idc":
        checks["client_id"] = compare_pair(local, edge, "client_id_md5_16")
        checks["client_secret"] = compare_pair(local, edge, "client_secret_md5_16")

    edge_ready = {
        "active": edge.get("status") == "active",
        "schedulable": edge.get("schedulable") is True,
        "no_error_message": not str(edge.get("error_message") or "").strip(),
        "has_access_token": bool(edge.get("has_access_token", True)),
        "has_refresh_token": bool(edge.get("has_refresh_token", True)),
    }
    if auth_method == "idc":
        edge_ready["has_client_id"] = bool(edge.get("has_client_id", True))
        edge_ready["has_client_secret"] = bool(edge.get("has_client_secret", True))

    required_check_names = [name for name in checks if name != "access_token"]
    control_plane_match = all(bool(checks[name]["match"]) for name in required_check_names)
    access_match = bool(checks["access_token"]["match"])
    edge_ready_ok = all(edge_ready.values())

    if control_plane_match and access_match and edge_ready_ok:
        verdict = "exact_match_ready"
    elif control_plane_match and access_match and not edge_ready_ok:
        verdict = "exact_match_edge_not_ready"
    elif control_plane_match and not access_match and args.allow_access_mismatch:
        verdict = "control_plane_match_access_diff_allowed"
    elif control_plane_match and not access_match:
        verdict = "control_plane_match_access_diff_only"
    else:
        verdict = "mismatch"

    output = {
        "verdict": verdict,
        "control_plane_match": control_plane_match,
        "access_match": access_match,
        "edge_ready": edge_ready_ok,
        "checks": checks,
        "edge_state": edge_ready,
        "local_identity": {
            "auth_method": local.get("auth_method"),
            "region": local.get("region"),
            "token_source": local.get("token_source"),
            "refreshed_at_utc": local.get("refreshed_at_utc", ""),
        },
        "edge_identity": {
            "account_id": edge.get("id"),
            "account_name": edge.get("name"),
            "status": edge.get("status"),
            "schedulable": edge.get("schedulable"),
            "token_version": edge.get("token_version", ""),
        },
    }
    json.dump(output, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
