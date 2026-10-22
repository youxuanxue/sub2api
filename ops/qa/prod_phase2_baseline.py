#!/usr/bin/env python3
"""Read-only prod QA maintenance pin, storage and lifecycle baseline."""
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
import ssm_execution  # noqa: E402
from ssm_execution import resolve_prod_instance, run_shell_b64  # noqa: E402

import prod_phase2_live_health as live_health  # noqa: E402


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


def _archive_bucket_status(*, legacy_aws: bool) -> tuple[str | None, dict[str, str]]:
    """Inspect the historical AWS bucket only when explicitly requested."""
    if not legacy_aws:
        return None, {"status": "skipped", "reason": "control-plane bucket audit not requested"}
    account_id = _account_id()
    return account_id, _raw_bucket_status(account_id)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--legacy-aws",
        action="store_true",
        help="also inspect the historical AWS raw archive bucket",
    )
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    account_id, bucket = _archive_bucket_status(legacy_aws=args.legacy_aws)
    instance_id = resolve_prod_instance()
    remote_out = run_shell_b64(
        instance_id,
        base64.b64encode(live_health.probe_script_bytes()).decode(),
        "prod qa phase2 baseline",
    )
    snapshot = live_health._parse_probe_output(remote_out)
    storage = live_health.evaluate_storage(snapshot.get("storage"))
    payload = {
        "region": ssm_execution.PROD_REGION,
        "instance_id": instance_id,
        "account_id": account_id,
        "raw_archive_bucket": bucket,
        "raw_archive_storage": snapshot.get("storage"),
        "storage_contract": storage,
        "qa_records": snapshot.get("qa_records"),
        "stdout": remote_out.strip(),
    }
    if args.json:
        print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    else:
        print(f"prod instance={instance_id} account={account_id}")
        print(f"raw_archive: {bucket}")
        print(remote_out.strip())
    return 0 if storage["ok"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
