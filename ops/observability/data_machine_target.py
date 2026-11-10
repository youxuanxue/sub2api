#!/usr/bin/env python3
"""Resolve and verify a site's data machine SSM identity.

After the data-plane split (docs/approved/design-data-plane-split.md) Postgres
and Redis run on a data machine per site. Its SSM identity lives in the site
matrix, never in a caller:
  prod     deploy/tencent/candidate-usw-20261022.json  `data_machine`
  cn-prod  deploy/domestic/prod-target.json            `data_ssm_*`

`--verify` asks SSM for the instance and requires Online, Linux and the
registered Name before printing anything, so a re-registered or reused mi-*
never receives a command meant for the database. Callers: run-probe.sh
(--target prod-data / cn-prod-data) and the log_dump job in
ops-daily-diagnostics.yml.

stdlib-only.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
MATRICES = {
    "prod": ROOT / "deploy" / "tencent" / "candidate-usw-20261022.json",
    "cn-prod": ROOT / "deploy" / "domestic" / "prod-target.json",
}
INSTANCE = re.compile(r"^mi-[0-9a-f]{17}$")
REGION = re.compile(r"^[a-z0-9-]+$")


class TargetError(ValueError):
    """The matrix has no usable data-machine identity (exit 1)."""


class NotReady(RuntimeError):
    """SSM lookup failed or the instance is not the registered data machine (exit 2)."""


def resolve(site: str, matrix: Path | None = None) -> dict[str, str]:
    """Return {id, ssm_region, ssm_name} for the site's data machine."""
    if site not in MATRICES:
        raise TargetError(f"unknown site: {site}")
    path = matrix or MATRICES[site]
    doc = json.loads(path.read_text(encoding="utf-8"))
    if site == "prod":
        data = doc.get("data_machine") or {}
        fields = (data.get("expected_ssm_instance_id"), data.get("ssm_region"), data.get("ssm_name"))
    else:
        fields = (doc.get("data_ssm_instance_id"), doc.get("data_ssm_region"), doc.get("data_ssm_name"))
    instance, region, name = (str(value or "") for value in fields)
    if not INSTANCE.match(instance) or not REGION.match(region) or not name:
        raise TargetError(f"{site} data machine SSM identity missing from {path}")
    return {"id": instance, "ssm_region": region, "ssm_name": name}


def verify(target: dict[str, str], run=subprocess.run) -> None:
    """Fail unless SSM reports this instance Online, Linux, with the registered Name."""
    proc = run(
        ["aws", "ssm", "describe-instance-information", "--region", target["ssm_region"],
         "--filters", f"Key=InstanceIds,Values={target['id']}", "--output", "json"],
        capture_output=True, text=True, check=False,
    )
    if proc.returncode:
        raise NotReady("SSM managed instance lookup failed: " + (proc.stderr or proc.stdout).strip()[-500:])
    try:
        items = json.loads(proc.stdout).get("InstanceInformationList", [])
    except ValueError as exc:
        raise NotReady("SSM managed instance lookup returned invalid JSON") from exc
    item = items[0] if len(items) == 1 else {}
    seen = {key: str(item.get(key) or "") for key in ("PingStatus", "Name", "PlatformType")}
    if seen != {"PingStatus": "Online", "Name": target["ssm_name"], "PlatformType": "Linux"}:
        raise NotReady(
            f"data machine SSM identity is not ready (name={seen['Name'] or '<empty>'} "
            f"want={target['ssm_name']} status={seen['PingStatus'] or '<empty>'} "
            f"platform={seen['PlatformType'] or '<empty>'})")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--site", required=True, choices=sorted(MATRICES))
    parser.add_argument("--verify", action="store_true", help="require SSM Online + registered Name")
    parser.add_argument("--github-output", default="", help="append id= and ssm_region= to this file")
    args = parser.parse_args()
    try:
        target = resolve(args.site)
        if args.verify:
            verify(target)
    except (OSError, TargetError) as exc:
        print(f"[data-machine-target] ERROR: {exc}", file=sys.stderr)
        return 1
    except NotReady as exc:
        print(f"[data-machine-target] ERROR: {args.site} {exc}", file=sys.stderr)
        return 2
    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as out:
            out.write(f"id={target['id']}\nssm_region={target['ssm_region']}\n")
    print(f"ssm_region={target['ssm_region']}\ninstance_id={target['id']}\nssm_name={target['ssm_name']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
