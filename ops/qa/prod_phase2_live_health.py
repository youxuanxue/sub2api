#!/usr/bin/env python3
"""Collect prod QA Phase 2 live facts and evaluate correlated health + IAM contract."""
from __future__ import annotations

import argparse
import base64
import datetime as dt
import json
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "stage0"))
sys.path.insert(0, str(ROOT / "ops" / "qa"))

from ssm_execution import PROD_REGION, resolve_prod_instance, run_shell_b64  # noqa: E402
import qa_phase2_health  # noqa: E402
import verify_raw_archive_iam_contract as iam_contract  # noqa: E402

PROBE = ROOT / "ops" / "observability/probe-qa-phase2-live-health.sh"
POLICY = ROOT / "ops/qa/policy.yaml"


def _load_catchup_gap_policy() -> str:
    try:
        import yaml

        policy = yaml.safe_load(POLICY.read_text(encoding="utf-8"))
        archive = policy.get("prod", {}).get("archive", {}) if isinstance(policy, dict) else {}
        value = archive.get("catchup_gap_policy") if isinstance(archive, dict) else None
        if value in {"accepted_terminal", "strict"}:
            return value
    except (OSError, ImportError, ValueError):
        pass
    return qa_phase2_health.DEFAULT_CATCHUP_GAP_POLICY


def _parse_probe_output(text: str) -> dict[str, Any]:
    snapshot: dict[str, Any] = {}
    qa_records: dict[str, Any] | None = None
    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        prefix, _, payload = line.partition(" ")
        if prefix == "PHASE2SYSTEMD":
            snapshot["systemd"] = json.loads(payload)
        elif prefix == "PHASE2RECEIPT":
            snapshot["host_receipt"] = None if payload == "null" else json.loads(payload)
        elif prefix == "PHASE2HEARTBEAT":
            snapshot["database_heartbeat"] = None if payload == "null" else json.loads(payload)
        elif prefix == "PHASE2ARCHIVE":
            snapshot["archive_control"] = json.loads(payload)
        elif prefix == "PHASE2QARECORDS":
            qa_records = json.loads(payload)
    if qa_records is not None:
        snapshot["qa_records"] = qa_records
    return snapshot


def evaluate_snapshot(
    snapshot: dict[str, Any],
    *,
    skip_iam: bool = False,
    now: dt.datetime | None = None,
) -> dict[str, Any]:
    catchup_gap_policy = _load_catchup_gap_policy()
    health = qa_phase2_health.evaluate(snapshot, now=now, catchup_gap_policy=catchup_gap_policy)
    warnings: list[str] = []
    qa_records = snapshot.get("qa_records")
    if isinstance(qa_records, dict) and qa_records.get("partition_owner") == "default_only":
        warnings.append("qa_records_partition_owner_default_only")
    if skip_iam:
        iam: dict[str, Any] = {"ok": True, "status": "skipped", "failures": []}
    else:
        try:
            account_id = iam_contract._account_id()
            bucket = f"tokenkey-prod-qa-raw-archive-{account_id}"
            app_role_arn = iam_contract.resolve_app_role_arn()
            iam = iam_contract.evaluate(bucket=bucket, app_role_arn=app_role_arn)
        except RuntimeError as exc:
            iam = {"ok": False, "status": "unknown", "failures": [str(exc)]}
        if not iam.get("ok"):
            health.setdefault("reasons", []).append("raw_archive_iam_contract_drift")
            health["reasons"] = sorted(set(health["reasons"]))
            if health.get("status") == "healthy":
                health["status"] = "degraded"
            health["healthy"] = False
    return {
        "health": health,
        "iam_contract": iam,
        "warnings": warnings,
        "catchup_gap_policy": catchup_gap_policy,
        "snapshot": snapshot,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot-file", type=Path, help="evaluate an existing probe snapshot JSON")
    parser.add_argument(
        "--from-probe-stdin",
        action="store_true",
        help="read probe-qa-phase2-live-health.sh stdout from stdin",
    )
    parser.add_argument("--skip-iam", action="store_true", help="skip live IAM contract verification")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    if args.snapshot_file is not None:
        snapshot = json.loads(args.snapshot_file.read_text(encoding="utf-8"))
        payload = evaluate_snapshot(snapshot, skip_iam=args.skip_iam)
    elif args.from_probe_stdin:
        snapshot = _parse_probe_output(sys.stdin.read())
        payload = evaluate_snapshot(snapshot, skip_iam=args.skip_iam)
    else:
        probe_script = PROBE.read_text(encoding="utf-8")
        instance_id = resolve_prod_instance()
        remote_out = run_shell_b64(
            instance_id,
            base64.b64encode(probe_script.encode()).decode(),
            "prod qa phase2 live health",
        )
        snapshot = _parse_probe_output(remote_out)
        payload = {
            "region": PROD_REGION,
            "instance_id": instance_id,
            **evaluate_snapshot(snapshot, skip_iam=args.skip_iam),
        }
    print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    health_status = payload["health"].get("status", "failed")
    if not args.skip_iam and not payload.get("iam_contract", {}).get("ok", False):
        return 2
    if health_status in {"healthy", "degraded"}:
        return 0
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
