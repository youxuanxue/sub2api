#!/usr/bin/env python3
"""Read-only prod QA Phase 2 baseline probe (local QA footprint + raw archive bucket)."""
from __future__ import annotations

import argparse
import base64
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "stage0"))
from ssm_execution import PROD_REGION, resolve_prod_instance, run_shell_b64  # noqa: E402

REMOTE = r"""set -uo pipefail
cd /var/lib/tokenkey
app_container=tokenkey
if [ -r active-color ]; then
  color=$(sed -n '1p' active-color 2>/dev/null | tr -d '[:space:]')
  case "${color}" in
    blue|green) app_container="tokenkey-${color}" ;;
  esac
fi
qa_cap=$(sudo docker exec "${app_container}" printenv QA_CAPTURE_ENABLED 2>/dev/null || true)
if [ -z "${qa_cap}" ]; then
  qa_cap=$(grep -E '^QA_CAPTURE_ENABLED=' .env 2>/dev/null | cut -d= -f2- || echo missing)
fi
[ -n "${qa_cap}" ] || qa_cap=missing
qa_archive=$(sudo docker exec "${app_container}" printenv QA_ARCHIVE_ENABLED 2>/dev/null || echo missing)
export_driver=$(sudo docker exec "${app_container}" printenv QA_CAPTURE_EXPORT_STORAGE_DRIVER 2>/dev/null || echo missing)
export_bucket=$(sudo docker exec "${app_container}" printenv QA_CAPTURE_EXPORT_STORAGE_BUCKET 2>/dev/null || echo missing)
rec_count=0
rec_oldest=
rec_newest=
blob_bytes=0
if sudo docker ps --format '{{.Names}}' | grep -qx tokenkey-postgres; then
  PGPASS="$(sudo grep '^POSTGRES_PASSWORD=' /var/lib/tokenkey/.env | cut -d= -f2- || true)"
  NET="$(sudo docker inspect tokenkey-postgres --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' 2>/dev/null || true)"
  if [ -n "${PGPASS}" ] && [ -n "${NET}" ]; then
    rec_count=$(sudo docker run --rm --network "${NET}" -e PGPASSWORD="${PGPASS}" postgres:16-alpine \
      psql -h tokenkey-postgres -U tokenkey -d tokenkey -Atqc "SELECT COUNT(*) FROM qa_records;" 2>/dev/null || echo 0)
    rec_oldest=$(sudo docker run --rm --network "${NET}" -e PGPASSWORD="${PGPASS}" postgres:16-alpine \
      psql -h tokenkey-postgres -U tokenkey -d tokenkey -Atq -c "SELECT MIN(created_at) FROM qa_records;" 2>/dev/null | head -1)
    rec_newest=$(sudo docker run --rm --network "${NET}" -e PGPASSWORD="${PGPASS}" postgres:16-alpine \
      psql -h tokenkey-postgres -U tokenkey -d tokenkey -Atq -c "SELECT MAX(created_at) FROM qa_records;" 2>/dev/null | head -1)
  fi
fi
if [ -d qa_blobs ] || [ -d qa_dlq ]; then
  blob_bytes=$(du -sb qa_blobs qa_dlq 2>/dev/null | awk '{s+=$1} END {print s+0}')
fi
printf 'QA_CAPTURE_ENABLED=%s\nQA_ARCHIVE_ENABLED=%s\nEXPORT_DRIVER=%s\nEXPORT_BUCKET=%s\nQA_RECORDS=%s\nQA_OLDEST=%s\nQA_NEWEST=%s\nBLOB_BYTES=%s\n' \
  "$qa_cap" "$qa_archive" "$export_driver" "$export_bucket" "$rec_count" "$rec_oldest" "$rec_newest" "$blob_bytes"
"""


def _account_id() -> str:
    out = subprocess.check_output(
        ["aws", "sts", "get-caller-identity", "--query", "Account", "--output", "text"],
        text=True,
    ).strip()
    if not re.fullmatch(r"\d{12}", out):
        raise SystemExit(f"unexpected aws account id: {out!r}")
    return out


def _raw_bucket_status(account_id: str) -> dict[str, str]:
    bucket = f"tokenkey-prod-qa-raw-archive-{account_id}"
    probe = subprocess.run(
        ["aws", "s3api", "head-bucket", "--bucket", bucket],
        capture_output=True,
        text=True,
        check=False,
    )
    if probe.returncode == 0:
        return {"bucket": bucket, "status": "exists"}
    err = (probe.stderr or probe.stdout or "").strip()
    if "404" in err or "Not Found" in err or "NoSuchBucket" in err:
        return {"bucket": bucket, "status": "missing"}
    return {"bucket": bucket, "status": "unknown", "error": err[:500]}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    account_id = _account_id()
    bucket = _raw_bucket_status(account_id)
    instance_id = resolve_prod_instance()
    remote_out = run_shell_b64(
        instance_id,
        base64.b64encode(REMOTE.encode()).decode(),
        "prod qa phase2 baseline",
    )
    payload = {
        "region": PROD_REGION,
        "instance_id": instance_id,
        "account_id": account_id,
        "raw_archive_bucket": bucket,
        "stdout": remote_out.strip(),
    }
    if args.json:
        print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    else:
        print(f"prod instance={instance_id} account={account_id}")
        print(f"raw_archive: {bucket}")
        print(remote_out.strip())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
