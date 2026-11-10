#!/usr/bin/env python3
"""Pick the SSM instance that holds a log_dump container.

After the data-plane split (docs/approved/design-data-plane-split.md) Postgres
and Redis run on the prod data machine; the app host keeps only the stopped
originals. The gateway and Caddy stay on the app host. The data machine's SSM
identity comes from the prod matrix, never a constant here.

stdlib-only.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import sys

MATRIX = Path(__file__).resolve().parents[2] / "deploy" / "tencent" / "candidate-usw-20261022.json"
DATA_CONTAINERS = ("tokenkey-postgres", "tokenkey-redis")
APP_CONTAINERS = ("tokenkey", "tokenkey-caddy")
INSTANCE = re.compile(r"^mi-[0-9a-f]{17}$")


def resolve(container: str, app_instance: str, app_region: str, matrix: Path = MATRIX) -> dict[str, str]:
    if container in APP_CONTAINERS:
        return {"id": app_instance, "ssm_region": app_region, "host": "app"}
    if container not in DATA_CONTAINERS:
        raise ValueError(f"unknown log_dump container: {container}")
    data = json.loads(matrix.read_text(encoding="utf-8")).get("data_machine") or {}
    instance, region = data.get("expected_ssm_instance_id") or "", data.get("ssm_region") or ""
    if not INSTANCE.match(instance) or not region:
        raise ValueError(f"data machine SSM identity missing from {matrix}")
    return {"id": instance, "ssm_region": region, "host": "data"}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", required=True)
    parser.add_argument("--app-instance", required=True)
    parser.add_argument("--app-region", required=True)
    parser.add_argument("--github-output", default="")
    args = parser.parse_args()
    try:
        target = resolve(args.container, args.app_instance, args.app_region)
    except (OSError, ValueError) as exc:
        print(f"::error::{exc}", file=sys.stderr)
        return 1
    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as out:
            out.write(f"id={target['id']}\nssm_region={target['ssm_region']}\n")
    print(f"log_dump {args.container} -> {target['host']} host instance={target['id']} ssm_region={target['ssm_region']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
