#!/usr/bin/env python3
"""Resolve multi-app fleet nodes for run-probe fan-out.

Site matrices:
  prod     → deploy/tencent/candidate-usw-20261022.json  (apps[])
  cn-prod  → deploy/domestic/prod-target.json            (apps[])

Each app that can be probed over SSM must declare ``ssm_instance_id``.
Apps missing that field are listed so callers fail closed instead of
silently diagnosing a single host.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys
from typing import Any

REPO = pathlib.Path(__file__).resolve().parents[2]
MATRICES = {
    "prod": REPO / "deploy/tencent/candidate-usw-20261022.json",
    "cn-prod": REPO / "deploy/domestic/prod-target.json",
}
_SSM_RE = re.compile(r"^mi-[0-9a-f]{8,}$")
_NAME_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,30}$")


def fail(message: str, code: int = 1) -> None:
    print(f"fleet_app_target: {message}", file=sys.stderr)
    raise SystemExit(code)


def load_apps(site: str) -> tuple[list[dict[str, Any]], str]:
    path = MATRICES.get(site)
    if path is None:
        fail(f"site must be prod|cn-prod, got {site!r}")
    data = json.loads(path.read_text(encoding="utf-8"))
    if site == "prod":
        apps = data.get("apps")
        region = str((data.get("ssm_region") or data.get("target", {}).get("ssm_region") or "")).strip()
    else:
        apps = data.get("apps")
        region = str(data.get("ssm_region") or "").strip()
    if not isinstance(apps, list) or not apps:
        fail(f"{path}: apps[] missing or empty")
    if not region:
        fail(f"{path}: ssm_region missing")
    return apps, region


def normalize_app(app: dict[str, Any], *, region: str) -> dict[str, str]:
    name = str(app.get("name") or "").strip()
    if not _NAME_RE.match(name):
        fail(f"invalid app name: {name!r}")
    ssm = str(app.get("ssm_instance_id") or "").strip()
    out = {
        "name": name,
        "ssm_region": region,
        "ssm_instance_id": ssm,
        "private_ip": str(app.get("private_ip") or ""),
        "lighthouse_instance_id": str(app.get("lighthouse_instance_id") or ""),
    }
    return out


def list_apps(site: str) -> list[dict[str, str]]:
    apps, region = load_apps(site)
    return [normalize_app(app, region=region) for app in apps]


def require_ssm(apps: list[dict[str, str]]) -> list[dict[str, str]]:
    missing = [a["name"] for a in apps if not _SSM_RE.match(a["ssm_instance_id"])]
    if missing:
        fail(
            "apps missing ssm_instance_id (cannot fan-out via run-probe SSM): "
            + ",".join(missing)
            + "; register Hybrid on those hosts and update the site matrix, "
            "or pass --node <ssm-capable-name> to acknowledge a single host",
            code=2,
        )
    return apps


def resolve_node(site: str, node: str) -> dict[str, str]:
    if not _NAME_RE.match(node):
        fail(f"invalid --node: {node!r}")
    apps = list_apps(site)
    matches = [a for a in apps if a["name"] == node]
    if len(matches) != 1:
        names = ",".join(a["name"] for a in apps)
        fail(f"unknown --node {node!r} for {site}; known: {names}")
    return require_ssm(matches)[0]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--site", required=True, choices=sorted(MATRICES))
    parser.add_argument("--node", default="", help="single apps[].name")
    parser.add_argument("--list", action="store_true", help="print all apps as JSON")
    parser.add_argument(
        "--format",
        choices=("json", "kv"),
        default="kv",
        help="kv for run-probe; json for --list",
    )
    args = parser.parse_args(argv)

    if args.list and args.node:
        fail("--list and --node are mutually exclusive")
    if not args.list and not args.node:
        fail("pass --list or --node")

    if args.list:
        apps = require_ssm(list_apps(args.site))
        print(json.dumps(apps, ensure_ascii=False))
        return 0

    app = resolve_node(args.site, args.node)
    if args.format == "json":
        print(json.dumps(app, ensure_ascii=False))
    else:
        for key in ("name", "ssm_region", "ssm_instance_id", "private_ip", "lighthouse_instance_id"):
            print(f"{key}={app[key]}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
