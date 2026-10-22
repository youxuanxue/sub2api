#!/usr/bin/env python3
"""Collect prod QA Phase 2 live facts and evaluate the current archive contract."""
from __future__ import annotations

import argparse
import base64
import datetime as dt
import json
import shlex
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "stage0"))
sys.path.insert(0, str(ROOT / "ops" / "qa"))

import ssm_execution  # noqa: E402
from ssm_execution import resolve_prod_instance, run_shell_b64  # noqa: E402
import qa_phase2_health  # noqa: E402
import verify_raw_archive_iam_contract as iam_contract  # noqa: E402

PROBE = ROOT / "ops" / "observability/probe-qa-phase2-live-health.sh"
CONTAINER_RESOLVER = ROOT / "ops/lib/resolve-app-container.sh"
POLICY = ROOT / "ops/qa/policy.yaml"


def probe_script_bytes() -> bytes:
    """Probe body with the canonical container resolver defined ahead of it.

    Direct SSM delivery ships one file, so the resolver run-probe.sh uploads via
    --with is inlined here; the probe reuses already-defined resolver functions.
    """
    return CONTAINER_RESOLVER.read_bytes() + b"\n" + PROBE.read_bytes()


def live_archive_coordinates() -> dict[str, str]:
    """Read literal values from the same owner shipped to blue/green hosts."""
    owner = ROOT / "ops/stage0/qa-runtime-environment.sh"
    values = {}
    for line in owner.read_text(encoding="utf-8").splitlines():
        if line.startswith("QA_LIVE_ARCHIVE_STORAGE_"):
            key, value = shlex.split(line)[0].split("=", 1)
            values[key.replace("QA_LIVE_ARCHIVE_", "QA_ARCHIVE_", 1)] = value
    if not values:
        raise ValueError("live QA archive coordinates are unavailable")
    return values


def evaluate_storage(storage: Any) -> dict[str, Any]:
    failures = []
    if not isinstance(storage, dict) or storage.get("status") != "ok":
        return {"ok": False, "failures": ["qa_runtime_storage_unavailable"]}
    if storage.get("runtime_contract") != "independent-v1" or storage.get("runtime_stopped") is not True:
        failures.append("qa_runtime_pin_invalid")
    environment = storage.get("environment")
    if not isinstance(environment, dict):
        environment = {}
    if environment.get("QA_ARCHIVE_ENABLED") != "true":
        failures.append("qa_archive_disabled_or_unknown")
    for key, expected in live_archive_coordinates().items():
        if environment.get(key) != expected:
            failures.append("qa_archive_coordinate_drift:" + key)
    if storage.get("credentials_file_present") is not False:
        failures.append("qa_archive_static_credentials_unexpected")
    if storage.get("instance_role_available") is not True:
        failures.append("qa_archive_instance_role_unavailable")
    return {"ok": not failures, "failures": failures}


def _partition_owner(qa_records: dict[str, Any]) -> str | None:
    default_rows = qa_records.get("default_rows")
    non_default_rows = qa_records.get("non_default_rows")
    counts_are_valid = all(
        isinstance(value, int) and not isinstance(value, bool) and value >= 0
        for value in (default_rows, non_default_rows)
    )
    if counts_are_valid:
        if default_rows > 0:
            return "mixed" if non_default_rows > 0 else "default_only"
        return "partitioned"
    owner = qa_records.get("partition_owner")
    return owner if owner in {"partitioned", "mixed", "default_only"} else None


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
        if prefix == "PHASE2STORAGE":
            snapshot["storage"] = json.loads(payload)
        elif prefix == "PHASE2SYSTEMD":
            snapshot["systemd"] = json.loads(payload)
        elif prefix == "PHASE2RECEIPT":
            snapshot["host_receipt"] = None if payload == "null" else json.loads(payload)
        elif prefix == "PHASE2HEARTBEAT":
            snapshot["database_heartbeat"] = None if payload == "null" else json.loads(payload)
        elif prefix == "PHASE2ARCHIVE":
            snapshot["archive_control"] = json.loads(payload)
        elif prefix == "PHASE2BOUNDARYSYSTEMD":
            snapshot["boundary_systemd"] = json.loads(payload)
        elif prefix == "PHASE2BOUNDARYRECEIPT":
            snapshot["boundary_host_receipt"] = None if payload == "null" else json.loads(payload)
        elif prefix == "PHASE2BOUNDARYHEARTBEAT":
            snapshot["boundary_database_heartbeat"] = None if payload == "null" else json.loads(payload)
        elif prefix == "PHASE2QARECORDS":
            qa_records = json.loads(payload)
    if qa_records is not None:
        owner = _partition_owner(qa_records)
        if owner is not None:
            qa_records["partition_owner"] = owner
        snapshot["qa_records"] = qa_records
    return snapshot


def evaluate_snapshot(
    snapshot: dict[str, Any],
    *,
    skip_iam: bool = True,
    now: dt.datetime | None = None,
    maintenance_pause_reason: str = "",
    boundary_pause_reason: str = "",
) -> dict[str, Any]:
    catchup_gap_policy = _load_catchup_gap_policy()
    health = qa_phase2_health.evaluate(
        snapshot, now=now, catchup_gap_policy=catchup_gap_policy,
        maintenance_paused=bool(maintenance_pause_reason),
        boundary_paused=bool(boundary_pause_reason),
    )
    health["maintenance_pause_reason"] = maintenance_pause_reason
    health["boundary_pause_reason"] = boundary_pause_reason
    storage = evaluate_storage(snapshot.get("storage"))
    if not storage["ok"]:
        health["reasons"] = sorted(set([*health.get("reasons", []), *storage["failures"]]))
        health["forward_reasons"] = sorted(set([*health.get("forward_reasons", []), *storage["failures"]]))
        health.update(status="failed", healthy=False)
    warnings: list[str] = []
    qa_records = snapshot.get("qa_records")
    if isinstance(qa_records, dict):
        owner = _partition_owner(qa_records)
        if owner in {"default_only", "mixed"} and health.get("lifecycle_phase") == "finalized":
            reason = f"qa_records_partition_owner_{owner}"
            warnings.append(reason)
            forward = health.setdefault("forward_reasons", [])
            if reason not in forward:
                forward.append(reason)
            health["forward_reasons"] = sorted(set(forward))
            health.setdefault("reasons", [])
            if reason not in health["reasons"]:
                health["reasons"] = sorted(set([*health["reasons"], reason]))
            health["status"] = "failed"
            health["healthy"] = False
    if skip_iam:
        iam: dict[str, Any] = {"ok": True, "status": "skipped", "failures": []}
    else:
        try:
            account_id = iam_contract._account_id()
            bucket = f"tokenkey-prod-qa-raw-archive-{account_id}"
            app_role_arn = iam_contract.resolve_app_role_arn()
            iam = iam_contract.evaluate(
                bucket=bucket, app_role_arn=app_role_arn, verify_network=True,
            )
        except RuntimeError as exc:
            iam = {"ok": False, "status": "unknown", "failures": [str(exc)]}
    return {
        "health": health,
        "storage_contract": storage,
        "iam_contract": iam,
        "warnings": warnings,
        "catchup_gap_policy": catchup_gap_policy,
        "snapshot": snapshot,
    }


def _load_pause_reason(job: str) -> str:
    # Single pause owner shared with the host watcher (host_watch.paused_jobs).
    inventory = json.loads((ROOT / "deploy/tencent/candidate-usw-20261022.json").read_text())
    pause_reason = inventory.get("host_watch", {}).get("paused_jobs", {}).get(job, "")
    if not isinstance(pause_reason, str) or (pause_reason and not pause_reason.strip()):
        raise ValueError(f"{job} pause requires an explicit reason")
    return pause_reason


def load_maintenance_pause_reason() -> str:
    # Sentinel anchor: get("host_watch", {}).get("paused_jobs", {}).get("tokenkey-qa-maintenance", "")
    return _load_pause_reason("tokenkey-qa-maintenance")


def load_boundary_pause_reason() -> str:
    return _load_pause_reason("tokenkey-qa-boundary")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot-file", type=Path, help="evaluate an existing probe snapshot JSON")
    parser.add_argument(
        "--from-probe-stdin",
        action="store_true",
        help="read probe-qa-phase2-live-health.sh stdout from stdin",
    )
    iam_mode = parser.add_mutually_exclusive_group()
    iam_mode.add_argument(
        "--legacy-aws",
        dest="legacy_aws",
        action="store_true",
        help="also verify the AWS raw archive IAM contract (legacy flag name)",
    )
    iam_mode.add_argument(
        "--skip-iam",
        action="store_true",
        help="skip the optional control-plane IAM policy audit",
    )
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    skip_iam = not args.legacy_aws
    if args.skip_iam:
        skip_iam = True

    pause_reason = load_maintenance_pause_reason()
    boundary_reason = load_boundary_pause_reason()

    if args.snapshot_file is not None:
        snapshot = json.loads(args.snapshot_file.read_text(encoding="utf-8"))
        payload = evaluate_snapshot(snapshot, skip_iam=skip_iam, maintenance_pause_reason=pause_reason,
                                     boundary_pause_reason=boundary_reason)
    elif args.from_probe_stdin:
        snapshot = _parse_probe_output(sys.stdin.read())
        payload = evaluate_snapshot(snapshot, skip_iam=skip_iam, maintenance_pause_reason=pause_reason,
                                     boundary_pause_reason=boundary_reason)
    else:
        instance_id = resolve_prod_instance()
        remote_out = run_shell_b64(
            instance_id,
            base64.b64encode(probe_script_bytes()).decode(),
            "prod qa phase2 live health",
        )
        snapshot = _parse_probe_output(remote_out)
        payload = {
            "region": ssm_execution.PROD_REGION,
            "instance_id": instance_id,
            **evaluate_snapshot(snapshot, skip_iam=skip_iam, maintenance_pause_reason=pause_reason,
                                     boundary_pause_reason=boundary_reason),
        }
    print(json.dumps(payload, ensure_ascii=True, sort_keys=True))
    health_status = payload["health"].get("status", "failed")
    if not skip_iam and not payload.get("iam_contract", {}).get("ok", False):
        return 2
    accepted_terminal_only = (
        health_status == "degraded"
        and payload["health"].get("forward_reasons") == []
        and set(payload["health"].get("catchup_reasons", []))
        == {"catchup_terminal_gaps_present"}
    )
    if health_status == "healthy" or accepted_terminal_only:
        return 0
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
