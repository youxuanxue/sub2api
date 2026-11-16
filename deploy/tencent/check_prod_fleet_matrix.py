#!/usr/bin/env python3
"""Read-only gate for the prod entry+apps matrix (Phase C prep).

Does not touch live hosts. Validates deploy/tencent/candidate-usw-20261022.json
carries the entry/apps shape https-watch and future rolling will consume.
Exit 0 on OK, 1 on shape errors.
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

MATRIX = Path(__file__).resolve().parent / "candidate-usw-20261022.json"
REQUIRED_ENTRY = ("name", "lighthouse_instance_id", "private_ip", "public_ip",
                  "ssm_instance_id", "domain", "domains")
REQUIRED_APP = ("name", "lighthouse_instance_id", "private_ip", "public_ip",
                "ssm_instance_id", "weight", "memory_limit")
ALLOWED_LAYOUT = ("single-container", "stage0-bluegreen")
MEM_RE = re.compile(r"^[0-9]+[gGmM]$")


def main() -> int:
    data = json.loads(MATRIX.read_text())
    errors: list[str] = []
    layout = data.get("layout")
    if not isinstance(layout, dict):
        errors.append("layout object required")
    else:
        kind = layout.get("kind")
        if kind not in ALLOWED_LAYOUT:
            errors.append(f"layout.kind must be one of {ALLOWED_LAYOUT}")
        root = layout.get("root")
        if not isinstance(root, str) or not root.startswith("/"):
            errors.append("layout.root must be an absolute path")
        if kind == "stage0-bluegreen" and root != "/var/lib/tokenkey":
            errors.append("stage0-bluegreen layout.root must be /var/lib/tokenkey")
        # cn-prod uses /opt/tokenkey; USW prod keeps /var/lib/tokenkey (LUKS).
        if kind == "single-container" and root not in ("/opt/tokenkey", "/var/lib/tokenkey"):
            errors.append(
                "single-container layout.root must be /opt/tokenkey or /var/lib/tokenkey")
        if data.get("tat_region") in ("", None):
            errors.append("tat_region required for rolling remote_exec")
        if data.get("ssm_region") in ("", None) and not (
                isinstance(data.get("data_machine"), dict)
                and data["data_machine"].get("ssm_region")):
            errors.append("ssm_region required (top-level or data_machine)")
    entry = data.get("entry")
    if not isinstance(entry, dict):
        errors.append("missing entry object")
    else:
        for key in REQUIRED_ENTRY:
            if key not in entry or entry[key] in ("", None, []):
                errors.append(f"entry.{key} required")
        domains = entry.get("domains") if isinstance(entry, dict) else None
        if isinstance(domains, list) and entry.get("domain") not in domains:
            errors.append("entry.domain must appear in entry.domains")
    apps = data.get("apps")
    if not isinstance(apps, list) or len(apps) < 2:
        errors.append("apps must list at least two nodes")
    else:
        names = set()
        for index, app in enumerate(apps):
            if not isinstance(app, dict):
                errors.append(f"apps[{index}] must be object")
                continue
            for key in REQUIRED_APP:
                if key not in app or app[key] in ("", None):
                    errors.append(f"apps[{index}].{key} required")
            name = app.get("name")
            if name in names:
                errors.append(f"duplicate app name {name}")
            names.add(name)
            weight = app.get("weight")
            if not isinstance(weight, int) or weight < 1:
                errors.append(f"apps[{index}].weight must be int >= 1")
            mem = app.get("memory_limit")
            if isinstance(mem, str) and not MEM_RE.match(mem.strip()):
                errors.append(f"apps[{index}].memory_limit must look like 3g or 512m")
            # Phase C: 2C8G cand must not declare the old-host 8g bluegreen default.
            if name == "app-cand" and isinstance(mem, str) and mem.strip().lower() == "8g":
                errors.append("apps[app-cand].memory_limit must not be 8g on 2C8G")
        ips = [a.get("private_ip") for a in apps if isinstance(a, dict)]
        if len(ips) != len(set(ips)):
            errors.append("apps private_ip values must be unique")
        if isinstance(entry, dict) and entry.get("private_ip") in ips:
            errors.append("entry.private_ip must not collide with apps")
    if errors:
        for err in errors:
            print(f"FAIL {err}", file=sys.stderr)
        return 1
    kind = data["layout"]["kind"]
    print(f"OK entry={data['entry']['public_ip']} apps={len(data['apps'])} layout={kind}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
