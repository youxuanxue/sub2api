#!/usr/bin/env python3
"""Render one cn-prod app node's compose.node.yml from prod-target.json apps[].

The node file is the last ``-f`` after compose.yml / compose.override.yml /
compose.local.yml. It pins the gateway to ``<private_ip>:8080`` (the entry
machine's backend address) and ``127.0.0.1:18080`` (local smoke and watch),
and carries the per-node memory cap and env.
docs/approved/design-cn-prod-rolling-deploy.md §7.

  node_compose.py --node app-b --out /opt/tokenkey/compose.node.yml
  node_compose.py --node app-b --diff /opt/tokenkey/compose.appb.yml   # read-only

``--diff`` exits 1 when the existing file is not equivalent.
"""
from __future__ import annotations

import argparse
import difflib
import ipaddress
import json
import os
import re
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
TARGET = HERE / "prod-target.json"
OVERRIDE = HERE / "compose.override.yml"
ENV_KEY = re.compile(r"[A-Z][A-Z0-9_]*\Z")
MEMORY = re.compile(r"[1-9][0-9]*[mg]\Z")


def load_target(path: Path = TARGET) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def node_name_prefix(target: dict) -> str:
    """TOKENKEY_NODE_NAME prefix: prod- / cn-prod- from role or rolling_confirm_site."""
    for key in ("rolling_confirm_site", "role"):
        value = target.get(key)
        if value in ("prod", "cn-prod"):
            return value + "-"
    return "cn-prod-"


def find_app(target: dict, name: str) -> dict:
    for app in target.get("apps", []):
        if app.get("name") == name:
            return app
    raise ValueError(f"unknown app node: {name}")


def default_max_in_flight(path: Path = OVERRIDE) -> str:
    """GATEWAY_MAX_IN_FLIGHT_REQUESTS from the shared override, the single owner."""
    found = re.findall(r'^\s+GATEWAY_MAX_IN_FLIGHT_REQUESTS:\s*"(\d+)"\s*$',
                       path.read_text(encoding="utf-8"), re.M)
    if len(found) != 1:
        raise ValueError("compose.override.yml must set GATEWAY_MAX_IN_FLIGHT_REQUESTS exactly once")
    return found[0]


def _quote(value: str) -> str:
    return json.dumps(str(value))


def render(app: dict, max_in_flight: str | None = None, *, prefix: str = "cn-prod-") -> str:
    name = app.get("name", "")
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,30}", name):
        raise ValueError("invalid app name")
    address = ipaddress.ip_address(app.get("private_ip", ""))
    if not address.is_private:
        raise ValueError("app private_ip must be a private address")
    memory = app.get("memory_limit", "")
    if not MEMORY.fullmatch(memory):
        raise ValueError("memory_limit must look like 3g or 512m")
    env = {
        "GATEWAY_MAX_IN_FLIGHT_REQUESTS": max_in_flight or default_max_in_flight(),
        "TOKENKEY_NODE_NAME": prefix + name,
    }
    extra = app.get("env", {}) or {}
    if not isinstance(extra, dict):
        raise ValueError("app env must be an object")
    for key, value in extra.items():
        if not ENV_KEY.fullmatch(key) or not isinstance(value, str) or "\n" in value:
            raise ValueError(f"invalid env entry: {key}")
        if key in env:
            raise ValueError(f"app env must not override {key}")
        env[key] = value
    lines = [
        f"# Rendered by deploy/domestic/node_compose.py for {prefix}{name}; do not edit on the host.",
        "services:",
        "  tokenkey:",
        f"    mem_limit: {memory}",
        f"    memswap_limit: {memory}",
        "    ports: !override",
        f'      - "{address}:8080:8080"',
        '      - "127.0.0.1:18080:8080"',
        "    environment:",
    ]
    lines += [f"      {key}: {_quote(value)}" for key, value in env.items()]
    # A host migrated from the blue/green layout already owns the Docker
    # network (created outside compose, no compose label). Compose refuses to
    # adopt it, so such a node declares it external instead of recreating it
    # under the remaining containers (app-a, 2026-11-15 M4).
    network = app.get("external_network")
    if network is not None:
        if not isinstance(network, str) or not re.fullmatch(r"[a-z0-9][a-z0-9_.-]{0,62}", network):
            raise ValueError("external_network must be a docker network name")
        lines += [
            "networks:",
            "  tokenkey-network: !override",
            f"    name: {network}",
            "    external: true",
        ]
    return "\n".join(lines) + "\n"


def normalized(text: str) -> list[str]:
    """Drop comments, blank lines and trailing spaces; keep order and indent."""
    out = []
    for line in text.splitlines():
        stripped = line.split(" #", 1)[0].rstrip() if not line.lstrip().startswith("#") else ""
        if stripped:
            out.append(stripped.replace("'", '"'))
    return out


def semantic(text: str):
    """YAML value with ``!override``/``!reset`` kept as tagged markers, or None without PyYAML."""
    try:
        import yaml  # type: ignore
    except ImportError:
        return None

    class Loader(yaml.SafeLoader):
        pass

    def tagged(loader, suffix, node):
        if isinstance(node, yaml.SequenceNode):
            value = loader.construct_sequence(node)
        elif isinstance(node, yaml.MappingNode):
            value = loader.construct_mapping(node)
        else:
            value = loader.construct_scalar(node)
        return {"!" + suffix: value}

    Loader.add_multi_constructor("!", tagged)
    return yaml.load(text, Loader=Loader)


def equivalent(rendered: str, existing: str) -> bool:
    left, right = semantic(rendered), semantic(existing)
    if left is not None:
        return left == right
    return normalized(rendered) == normalized(existing)


def write_private(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, scratch = tempfile.mkstemp(prefix="." + path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(text)
        os.chmod(scratch, 0o644)
        os.replace(scratch, path)
    except BaseException:
        Path(scratch).unlink(missing_ok=True)
        raise


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--node", required=True, help="apps[].name, e.g. app-b")
    parser.add_argument("--target", type=Path, default=TARGET)
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--out", type=Path)
    group.add_argument("--diff", type=Path, metavar="EXISTING")
    args = parser.parse_args(argv)
    target = load_target(args.target)
    text = render(find_app(target, args.node), prefix=node_name_prefix(target))
    if args.out:
        write_private(args.out, text)
        print(json.dumps({"node": args.node, "written": str(args.out)}))
        return 0
    existing = args.diff.read_text(encoding="utf-8")
    if equivalent(text, existing):
        print(json.dumps({"node": args.node, "equivalent": True}))
        return 0
    sys.stdout.writelines(difflib.unified_diff(
        existing.splitlines(True), text.splitlines(True), str(args.diff), "rendered"))
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
