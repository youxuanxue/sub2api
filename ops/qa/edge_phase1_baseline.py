#!/usr/bin/env python3
"""Read-only edge QA Phase 1 baseline probe (capture env, timer, blob footprint)."""
from __future__ import annotations

import argparse
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
tag=$(sudo docker compose -f docker-compose.yml --env-file .env ps --format json 2>/dev/null | python3 -c "import json,sys; rows=[json.loads(l) for l in sys.stdin if l.strip()]; imgs=[r.get('Image','') for r in rows if r.get('Service')=='tokenkey']; print(imgs[0].split(':')[-1] if imgs else 'unknown')" 2>/dev/null || echo unknown)
qa_env=$(grep -E '^(QA_CAPTURE_ENABLED|QA_BUNDLE_)' .env 2>/dev/null | tr '\n' ';' || true)
qa_timer=$(systemctl show -p ActiveState --value tokenkey-qa-stale-cleanup.timer 2>/dev/null || echo missing)
qa_cap=$(sudo docker compose -f docker-compose.yml --env-file .env exec -T tokenkey printenv QA_CAPTURE_ENABLED 2>/dev/null || echo missing)
blob_count=0
blob_bytes=0
if [ -d qa_blobs ] || [ -d qa_dlq ]; then
  blob_count=$(find qa_blobs qa_dlq -type f 2>/dev/null | wc -l | tr -d ' ')
  blob_bytes=$(du -sb qa_blobs qa_dlq 2>/dev/null | awk '{s+=$1} END {print s+0}')
fi
printf 'TAG=%s\nQA_ENV=%s\nQA_TIMER=%s\nCONTAINER_QA_CAPTURE_ENABLED=%s\nBLOB_FILES=%s\nBLOB_BYTES=%s\n' "$tag" "$qa_env" "$qa_timer" "$qa_cap" "$blob_count" "$blob_bytes"
"""


def _send(region: str, instance_id: str, edge_id: str, comment: str) -> str:
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
            json.dumps({"commands": [REMOTE]}),
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
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    ident = resolve_edge_execution_identity(ROOT, args.edge_id)
    command_id = _send(ident.region, ident.instance_id, ident.edge_id, f"edge qa phase1 baseline {ident.edge_id}")
    status, stdout, stderr = _poll(ident.region, command_id, ident.instance_id)
    payload = {
        "edge_id": ident.edge_id,
        "region": ident.region,
        "instance_id": ident.instance_id,
        "command_id": command_id,
        "status": status,
        "stdout": stdout.strip(),
        "stderr": stderr.strip(),
    }
    if args.json:
        print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    else:
        print(f"edge={ident.edge_id} status={status}")
        print(stdout.strip())
        if stderr.strip():
            print(stderr.strip(), file=sys.stderr)
    return 0 if status == "Success" else 1


if __name__ == "__main__":
    raise SystemExit(main())
