#!/usr/bin/env python3
"""Render the cn-prod entry machine Caddyfile from prod-target.json.

``entry.domain`` is the public site; ``apps[]`` order and weights become the
``to`` list and ``weighted_round_robin`` weights.
docs/approved/design-cn-prod-rolling-deploy.md §1, M2.

  entry_caddy.py --out PATH
  entry_caddy.py --diff /var/lib/tokenkey/caddy/etc/Caddyfile   # read-only

``--diff`` ignores comments and whitespace and exits 1 on any other difference.
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
TEMPLATE = HERE / "entry" / "Caddyfile.tmpl"
BACKEND_PORT = 8080
DOMAIN = re.compile(r"(?=.{1,253}\Z)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}\Z")


def render(target: dict, template: str | None = None) -> str:
    domain = target["entry"]["domain"]
    if not DOMAIN.fullmatch(domain):
        raise ValueError("entry.domain must be a lowercase DNS name")
    apps = target.get("apps") or []
    if not apps:
        raise ValueError("apps[] is empty")
    backends, weights, seen = [], [], set()
    # apps[] order is the rolling order. Caddy weights are positional, so the
    # pool is listed by private IP: a stable order independent of rollout
    # order, and the order the live file had on 2026-11-15.
    for app in sorted(apps, key=lambda item: ipaddress.ip_address(item["private_ip"])):
        address = ipaddress.ip_address(app["private_ip"])
        weight = app["weight"]
        if not address.is_private or address in seen:
            raise ValueError("apps[].private_ip must be unique private addresses")
        if type(weight) is not int or not 1 <= weight <= 100:
            raise ValueError("apps[].weight must be an integer 1-100")
        seen.add(address)
        backends.append(f"{address}:{BACKEND_PORT}")
        weights.append(str(weight))
    text = TEMPLATE.read_text(encoding="utf-8") if template is None else template
    for key, value in (("BACKENDS", " ".join(backends)), ("WEIGHTS", " ".join(weights)),
                       ("DOMAIN", domain)):
        if "{{" + key + "}}" not in text:
            raise ValueError(f"template lacks {{{{{key}}}}}")
        text = text.replace("{{" + key + "}}", value)
    if "{{" in text:
        raise ValueError("template has an unknown placeholder")
    return text


def normalized(text: str) -> list[str]:
    """Caddyfile tokens per line without comments or whitespace differences."""
    out = []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        out.append(" ".join(line.split()))
    return out


def write_file(path: Path, text: str) -> None:
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
    parser.add_argument("--target", type=Path, default=TARGET)
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--out", type=Path)
    group.add_argument("--diff", type=Path, metavar="EXISTING")
    args = parser.parse_args(argv)
    text = render(json.loads(args.target.read_text(encoding="utf-8")))
    if args.out:
        write_file(args.out, text)
        print(json.dumps({"written": str(args.out)}))
        return 0
    existing = args.diff.read_text(encoding="utf-8")
    if normalized(existing) == normalized(text):
        print(json.dumps({"equivalent": True}))
        return 0
    sys.stdout.writelines(line + "\n" for line in difflib.unified_diff(
        normalized(existing), normalized(text), str(args.diff), "rendered", lineterm=""))
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
