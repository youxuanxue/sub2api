#!/usr/bin/env python3
"""Apply tk_069 qa_archive_shards migration on prod Postgres via SSM."""

from __future__ import annotations

import argparse
import base64
import json
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "stage0"))
from ssm_execution import PROD_REGION, resolve_prod_instance, run_shell_b64  # noqa: E402

MIGRATION = ROOT / "backend" / "migrations" / "tk_069_create_qa_archive_shards.sql"
REMOTE = r"""
set -euo pipefail
cd /var/lib/tokenkey
PGPASS="$(sudo grep '^POSTGRES_PASSWORD=' .env | cut -d= -f2-)"
NET="$(sudo docker inspect tokenkey-postgres --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}')"
SQL=$(sudo docker exec -e PAGER=cat tokenkey-postgres psql -U tokenkey -d tokenkey -Atqc \
  "SELECT to_regclass('public.qa_archive_shards') IS NOT NULL" 2>/dev/null || echo f)
if [ "$SQL" = "t" ]; then
  echo "qa_archive_shards already exists"
  exit 0
fi
sudo docker run --rm --network "$NET" -e PGPASSWORD="$PGPASS" \
  -v /tmp/tk069.sql:/tmp/tk069.sql:ro postgres:16-alpine \
  psql -h tokenkey-postgres -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -f /tmp/tk069.sql
echo "tk_069 applied"
"""


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    if not MIGRATION.is_file():
        raise SystemExit(f"migration missing: {MIGRATION}")
    sql_b64 = base64.b64encode(MIGRATION.read_bytes()).decode()
    instance_id = resolve_prod_instance()
    remote = (
        f"echo {json.dumps(sql_b64)} | base64 -d | sudo tee /tmp/tk069.sql >/dev/null\n"
        + REMOTE
    )
    out = run_shell_b64(instance_id, base64.b64encode(remote.encode()).decode(), "apply tk_069")
    payload = {"region": PROD_REGION, "instance_id": instance_id, "stdout": out.strip()}
    if args.json:
        print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    else:
        print(out.strip())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
