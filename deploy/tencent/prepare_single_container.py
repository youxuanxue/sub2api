#!/usr/bin/env python3
"""Prepare a prod app host for Phase C single-container rolling (no cutover).

Stages compose.yml / override / local / node.yml under /var/lib/tokenkey and
runs ``docker compose config`` validation. Does **not** stop blue/green,
entry-origin, or change the published :8080 listener — safe while traffic
still uses Stage0 bluegreen.

Cutover (drain → replace containers) is a separate operator step after both
app nodes are prepared and reviewed.

  python3 deploy/tencent/prepare_single_container.py --node app-cand --dry-run
  python3 deploy/tencent/prepare_single_container.py --node app-cand --apply
"""
from __future__ import annotations

import argparse
import json
import shutil
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
MATRIX = HERE / "candidate-usw-20261022.json"
COMPOSE_SRC = REPO / "deploy" / "aws" / "stage0" / "docker-compose.yml"
OVERRIDE_SRC = HERE / "compose.override.yml"
LOCAL_SRC = HERE / "compose.local.yml"
NODE_COMPOSE = REPO / "deploy" / "domestic" / "node_compose.py"
ROOT = Path("/var/lib/tokenkey")


def load_app(name: str) -> dict:
    data = json.loads(MATRIX.read_text())
    for app in data.get("apps") or []:
        if app.get("name") == name:
            return app
    raise SystemExit(f"unknown apps[].name {name!r} in {MATRIX}")


def render_node(node: str, out: Path) -> None:
    import subprocess
    # node_compose prints a one-line receipt; keep stdout quiet for our plan JSON.
    subprocess.run(
        [sys.executable, str(NODE_COMPOSE), "--target", str(MATRIX),
         "--node", node, "--out", str(out)],
        check=True, stdout=subprocess.DEVNULL,
    )


def assert_target_host(private_ip: str) -> None:
    import subprocess

    try:
        output = subprocess.run(
            ["hostname", "-I"], check=True, capture_output=True, text=True
        ).stdout
    except (OSError, subprocess.CalledProcessError) as exc:
        raise SystemExit(
            f"cannot verify host identity for {private_ip}: hostname -I failed: {exc}"
        ) from exc
    if private_ip not in output.split():
        raise SystemExit(
            f"refused: hostname -I does not include target {private_ip}: "
            f"{output.split()}"
        )


def main(argv: list[str] | None = None) -> int:
    import subprocess
    import tempfile

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--node", required=True, choices=("app-cand", "app-old"))
    parser.add_argument("--apply", action="store_true",
                        help="Write files under /var/lib/tokenkey (host must be the target)")
    parser.add_argument("--dry-run", action="store_true",
                        help="Render locally and print the plan (default)")
    args = parser.parse_args(argv)
    if not args.apply:
        args.dry_run = True

    app = load_app(args.node)
    if not app.get("external_network"):
        raise SystemExit(f"{args.node}: external_network required in matrix")
    if args.apply:
        assert_target_host(app["private_ip"])
    scratch = Path(tempfile.mkdtemp(prefix="phase-c-prepare-"))
    generated = scratch / "compose.node.yml"
    render_node(args.node, generated)
    sources = {
        "compose.yml": COMPOSE_SRC,
        "compose.override.yml": OVERRIDE_SRC,
        "compose.local.yml": LOCAL_SRC,
        "compose.node.yml": generated,
    }
    plan = {
        "node": args.node,
        "private_ip": app["private_ip"],
        "root": str(ROOT),
        "memory_limit": app["memory_limit"],
        "external_network": app["external_network"],
        "files": {name: str(src) for name, src in sources.items()},
        "apply": bool(args.apply),
        "traffic_impact": "none (prepare only; bluegreen keeps serving)",
    }
    print(json.dumps(plan, indent=2))
    node_text = generated.read_text()
    if f'{app["private_ip"]}:8080:8080' not in node_text:
        raise SystemExit(f"rendered compose.node.yml missing {app['private_ip']}:8080 bind")
    if f'prod-{args.node}' not in node_text:
        raise SystemExit(f"rendered compose.node.yml missing TOKENKEY_NODE_NAME prod-{args.node}")
    if args.dry_run and not args.apply:
        return 0

    ROOT.mkdir(parents=True, exist_ok=True)
    for name, src in sources.items():
        dest = ROOT / name
        shutil.copy2(src, dest)
        dest.chmod(0o644)
        print(json.dumps({"written": str(dest), "bytes": dest.stat().st_size}))
    cfg = subprocess.run(
        ["docker", "compose", "--project-directory", str(ROOT),
         "-f", "compose.yml", "-f", "compose.override.yml",
         "-f", "compose.local.yml", "-f", "compose.node.yml", "config"],
        cwd=ROOT, capture_output=True, text=True,
    )
    if cfg.returncode != 0:
        print(cfg.stderr, file=sys.stderr)
        return 1
    # Compose v2 prints host_ip / published on separate lines.
    if f"host_ip: {app['private_ip']}" not in cfg.stdout:
        print(f"refused: compose config missing host_ip {app['private_ip']}", file=sys.stderr)
        return 1
    print(json.dumps({"compose_config": "ok", "node": args.node}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
