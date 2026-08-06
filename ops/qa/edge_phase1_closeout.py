#!/usr/bin/env python3
"""One-shot Edge QA Phase 1 closeout: purge stale QA data and remove host QA wiring."""
from __future__ import annotations

import argparse
import base64
import json
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "stage0"))
from edge_ssm_execution import resolve_edge_execution_identity  # noqa: E402

REMOTE = r"""set -euo pipefail
cd /var/lib/tokenkey
apply="__APPLY__"
if [ "${apply}" != "true" ]; then
  qa_timer=$(systemctl show -p ActiveState --value tokenkey-qa-stale-cleanup.timer 2>/dev/null || echo missing)
  qa_cap=$(sudo docker compose -f docker-compose.yml --env-file .env exec -T tokenkey printenv QA_CAPTURE_ENABLED 2>/dev/null || echo missing)
  rec_count=0
  if sudo docker ps --format '{{.Names}}' | grep -qx tokenkey-postgres; then
    PGPASS="$(sudo grep '^POSTGRES_PASSWORD=' /var/lib/tokenkey/.env | cut -d= -f2- || true)"
    NET="$(sudo docker inspect tokenkey-postgres --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' 2>/dev/null || true)"
    if [ -n "${PGPASS}" ] && [ -n "${NET}" ]; then
      rec_count=$(sudo docker run --rm --network "${NET}" -e PGPASSWORD="${PGPASS}" postgres:16-alpine \
        psql -h tokenkey-postgres -U tokenkey -d tokenkey -Atqc "SELECT COUNT(*) FROM qa_records;" 2>/dev/null || echo 0)
    fi
  fi
  blob_count=0
  for d in qa_blobs qa_dlq qa_exports_tmp; do
    [ -d "$d" ] || continue
    blob_count=$((blob_count + $(find "$d" -type f 2>/dev/null | wc -l | tr -d ' ')))
  done
  printf 'DRY_RUN=true\nQA_TIMER=%s\nCONTAINER_QA_CAPTURE_ENABLED=%s\nQA_RECORDS=%s\nBLOB_FILES=%s\n' \
    "$qa_timer" "$qa_cap" "$rec_count" "$blob_count"
  exit 0
fi
sudo systemctl stop tokenkey-qa-stale-cleanup.timer 2>/dev/null || true
sudo systemctl disable tokenkey-qa-stale-cleanup.timer 2>/dev/null || true
sudo systemctl stop tokenkey-qa-stale-cleanup.service 2>/dev/null || true
sudo rm -f /etc/systemd/system/tokenkey-qa-stale-cleanup.service /etc/systemd/system/tokenkey-qa-stale-cleanup.timer
sudo rm -f /usr/local/bin/tokenkey-qa-stale-cleanup.sh /etc/tokenkey/qa-stale-retention.env
sudo systemctl daemon-reload
sudo systemctl reset-failed tokenkey-qa-stale-cleanup.service 2>/dev/null || true
if sudo docker ps --format '{{.Names}}' | grep -qx tokenkey-postgres; then
  PGPASS="$(sudo grep '^POSTGRES_PASSWORD=' /var/lib/tokenkey/.env | cut -d= -f2-)"
  NET="$(sudo docker inspect tokenkey-postgres --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}')"
  if [ -n "${PGPASS}" ] && [ -n "${NET}" ]; then
    sudo docker run --rm --network "${NET}" -e PGPASSWORD="${PGPASS}" postgres:16-alpine \
      psql -h tokenkey-postgres -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 \
      -c "TRUNCATE qa_records;"
  fi
fi
sudo rm -rf qa_blobs qa_dlq qa_exports_tmp
sudo install -d -m 0755 qa_blobs qa_dlq 2>/dev/null || true
qa_timer=$(systemctl show -p ActiveState --value tokenkey-qa-stale-cleanup.timer 2>/dev/null || echo missing)
qa_cap=$(sudo docker compose -f docker-compose.yml --env-file .env exec -T tokenkey printenv QA_CAPTURE_ENABLED 2>/dev/null || echo missing)
rec_count=0
if sudo docker ps --format '{{.Names}}' | grep -qx tokenkey-postgres; then
  PGPASS="$(sudo grep '^POSTGRES_PASSWORD=' /var/lib/tokenkey/.env | cut -d= -f2- || true)"
  NET="$(sudo docker inspect tokenkey-postgres --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' 2>/dev/null || true)"
  if [ -n "${PGPASS}" ] && [ -n "${NET}" ]; then
    rec_count=$(sudo docker run --rm --network "${NET}" -e PGPASSWORD="${PGPASS}" postgres:16-alpine \
      psql -h tokenkey-postgres -U tokenkey -d tokenkey -Atqc "SELECT COUNT(*) FROM qa_records;" 2>/dev/null || echo 0)
  fi
fi
blob_count=0
for d in qa_blobs qa_dlq qa_exports_tmp; do
  [ -d "$d" ] || continue
  blob_count=$((blob_count + $(find "$d" -type f 2>/dev/null | wc -l | tr -d ' ')))
done
printf 'DRY_RUN=false\nQA_TIMER=%s\nCONTAINER_QA_CAPTURE_ENABLED=%s\nQA_RECORDS=%s\nBLOB_FILES=%s\n' \
  "$qa_timer" "$qa_cap" "$rec_count" "$blob_count"
"""


def _send(region: str, instance_id: str, edge_id: str, apply: bool, comment: str) -> str:
    remote = REMOTE.replace("__APPLY__", "true" if apply else "false")
    args = ["aws", "ssm", "send-command", "--region", region]
    if instance_id.startswith("mi-"):
        args.extend(
            [
                "--targets",
                f"Key=tag:EdgeId,Values={edge_id}",
                "Key=tag:Platform,Values=lightsail",
            ]
        )
    else:
        args.extend(["--instance-ids", instance_id])
    args.extend(
        [
            "--document-name",
            "AWS-RunShellScript",
            "--comment",
            comment,
            "--parameters",
            json.dumps({"commands": [remote]}),
            "--query",
            "Command.CommandId",
            "--output",
            "text",
        ]
    )
    completed = subprocess.run(args, capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        raise SystemExit(completed.stderr.strip() or "ssm send-command failed")
    return completed.stdout.strip()


def _resolve_instance_id(region: str, command_id: str, fallback: str) -> str:
    for _ in range(60):
        inv = subprocess.run(
            [
                "aws",
                "ssm",
                "list-command-invocations",
                "--region",
                region,
                "--command-id",
                command_id,
                "--output",
                "json",
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        if inv.returncode != 0:
            time.sleep(2)
            continue
        rows = json.loads(inv.stdout).get("CommandInvocations") or []
        if len(rows) == 1:
            resolved = str(rows[0].get("InstanceId") or "").strip()
            if resolved:
                return resolved
        time.sleep(2)
    return fallback


def _poll(region: str, command_id: str, instance_id: str) -> tuple[str, str, str]:
    effective_id = _resolve_instance_id(region, command_id, instance_id)
    for _ in range(40):
        time.sleep(3)
        inv = subprocess.run(
            [
                "aws",
                "ssm",
                "get-command-invocation",
                "--region",
                region,
                "--command-id",
                command_id,
                "--instance-id",
                effective_id,
                "--output",
                "json",
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        if inv.returncode != 0:
            continue
        data = json.loads(inv.stdout)
        status = data.get("Status", "")
        if status in {"Success", "Failed", "Cancelled", "TimedOut"}:
            return status, data.get("StandardOutputContent", ""), data.get("StandardErrorContent", "")
    raise SystemExit("ssm poll timeout")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--edge-id", required=True)
    parser.add_argument("--apply", action="store_true", help="execute closeout (default is dry-run)")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    ident = resolve_edge_execution_identity(ROOT, args.edge_id)
    mode = "apply" if args.apply else "dry-run"
    command_id = _send(
        ident.region,
        ident.instance_id,
        ident.edge_id,
        args.apply,
        f"edge qa phase1 closeout {mode} {ident.edge_id}",
    )
    status, stdout, stderr = _poll(ident.region, command_id, ident.instance_id)
    payload = {
        "edge_id": ident.edge_id,
        "region": ident.region,
        "instance_id": ident.instance_id,
        "command_id": command_id,
        "apply": args.apply,
        "status": status,
        "stdout": stdout.strip(),
        "stderr": stderr.strip(),
    }
    if args.json:
        print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    else:
        print(f"edge={ident.edge_id} mode={mode} status={status}")
        print(stdout.strip())
        if stderr.strip():
            print(stderr.strip(), file=sys.stderr)
    return 0 if status == "Success" else 1


if __name__ == "__main__":
    raise SystemExit(main())
