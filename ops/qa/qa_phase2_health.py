#!/usr/bin/env python3
"""Correlate QA Phase 2 systemd, host, heartbeat, and archive-control facts."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import sys
from typing import Any

MAX_RECEIPT_AGE = dt.timedelta(hours=2)
MAX_CORRELATION_SKEW = dt.timedelta(minutes=5)
DEFAULT_CATCHUP_GAP_POLICY = "accepted_terminal"


def _mapping(value: Any) -> dict[str, Any] | None:
    return value if isinstance(value, dict) else None


def _timestamp(value: Any) -> dt.datetime | None:
    if not isinstance(value, str) or not value.strip():
        return None
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        parsed = dt.datetime.fromisoformat(text)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        return None
    return parsed.astimezone(dt.timezone.utc)


def _last_result(value: Any) -> dict[str, str]:
    if not isinstance(value, str):
        return {}
    result: dict[str, str] = {}
    for item in value.split():
        key, separator, field = item.partition("=")
        if separator and key:
            result[key] = field
    return result


def _same_fact(
    reasons: list[str],
    label: str,
    receipt: dict[str, Any],
    heartbeat: dict[str, str],
    control: dict[str, Any] | None,
) -> None:
    prefix = "normal" if label == "normal" else "compensation"
    if control is None:
        reasons.append(f"{label}_control_missing")
        return
    for field in ("window_start", "commit_etag", "state"):
        expected = receipt.get(field)
        if control.get(field) != expected:
            reasons.append(f"{label}_{field}_control_mismatch")
        heartbeat_field = "window" if field == "window_start" else field
        if heartbeat.get(f"{prefix}_{heartbeat_field}") != str(expected):
            reasons.append(f"{label}_{field}_heartbeat_mismatch")
    restore = receipt.get("restore_verified") is True
    if control.get("restore_verified") is not restore:
        reasons.append(f"{label}_restore_control_mismatch")
    if heartbeat.get(f"{prefix}_restore_verified") != str(restore).lower():
        reasons.append(f"{label}_restore_heartbeat_mismatch")
    if receipt.get("state") != "committed" or not restore:
        reasons.append(f"{label}_not_committed_restore_verified")
    if receipt.get("cleanup_eligible") is not False or control.get("cleanup_eligible") is not False:
        reasons.append(f"{label}_cleanup_not_denied")


def _evaluate_catchup(
    reasons: list[str],
    *,
    receipt: dict[str, Any] | None,
    heartbeat: dict[str, str],
    archive: dict[str, Any],
    catchup_gap_policy: str,
) -> bool:
    failed = False
    raw_compensation = receipt.get("compensation") if receipt is not None else None
    compensation = _mapping(raw_compensation)
    if compensation is not None:
        _same_fact(reasons, "compensation", compensation, heartbeat, _mapping(archive.get("compensation")))
    elif raw_compensation is not None:
        reasons.append("compensation_receipt_invalid")
        failed = True
    else:
        if archive.get("compensation") is not None:
            reasons.append("compensation_control_without_receipt")
            failed = True
        if any(key.startswith("compensation_") for key in heartbeat):
            reasons.append("compensation_heartbeat_without_receipt")
            failed = True

    terminal = archive.get("terminal_failures_after_cutover")
    if not isinstance(terminal, list):
        reasons.append("terminal_failure_inventory_missing")
        failed = True
    elif terminal:
        if catchup_gap_policy == "accepted_terminal":
            reasons.append("catchup_terminal_gaps_present")
        else:
            reasons.append("terminal_failures_after_cutover")
            failed = True
    return failed


def evaluate(
    snapshot: dict[str, Any],
    *,
    now: dt.datetime | None = None,
    catchup_gap_policy: str = DEFAULT_CATCHUP_GAP_POLICY,
) -> dict[str, Any]:
    now = (now or dt.datetime.now(dt.timezone.utc)).astimezone(dt.timezone.utc)
    forward_reasons: list[str] = []
    catchup_reasons: list[str] = []
    failed = False
    if not isinstance(snapshot, dict):
        return {
            "healthy": False,
            "status": "failed",
            "reasons": ["snapshot_invalid"],
            "forward_reasons": ["snapshot_invalid"],
            "catchup_reasons": [],
        }

    systemd = _mapping(snapshot.get("systemd"))
    receipt = _mapping(snapshot.get("host_receipt"))
    database = _mapping(snapshot.get("database_heartbeat"))
    archive = _mapping(snapshot.get("archive_control"))
    if systemd is None:
        forward_reasons.append("systemd_missing")
    else:
        if systemd.get("timer_enabled") is not True:
            forward_reasons.append("timer_not_enabled")
        if systemd.get("timer_active") is not True:
            forward_reasons.append("timer_not_active")
        if systemd.get("service_result") != "success":
            forward_reasons.append("systemd_service_failed")
            failed = True
    if receipt is None:
        forward_reasons.append("host_receipt_missing")
    if database is None:
        forward_reasons.append("database_heartbeat_missing")
    if archive is None:
        forward_reasons.append("archive_control_missing")

    if receipt is not None:
        if receipt.get("schema_version") != "qa-maintenance-runner-v1":
            forward_reasons.append("host_receipt_schema_invalid")
        if not isinstance(receipt.get("run_id"), str) or not receipt.get("run_id"):
            forward_reasons.append("host_receipt_run_id_missing")
        if receipt.get("trigger") != "timer":
            forward_reasons.append("host_receipt_trigger_not_timer")
        if not isinstance(receipt.get("active_container"), str) or not receipt.get("active_container"):
            forward_reasons.append("host_receipt_container_missing")
        if not isinstance(receipt.get("image"), str) or not receipt.get("image", "").startswith("sha256:"):
            forward_reasons.append("host_receipt_image_invalid")
        if receipt.get("runner_uid") != 1000 or receipt.get("runner_gid") != 1000:
            forward_reasons.append("host_receipt_runner_identity_invalid")
        if receipt.get("deletion_authorized") is not False:
            forward_reasons.append("host_receipt_deletion_not_denied")
        started = _timestamp(receipt.get("started_at"))
        if started is None:
            forward_reasons.append("host_receipt_started_at_invalid")
        finished = _timestamp(receipt.get("finished_at"))
        if finished is None:
            forward_reasons.append("host_receipt_finished_at_invalid")
        elif finished > now or now - finished > MAX_RECEIPT_AGE:
            forward_reasons.append("host_receipt_stale")
        elif started is not None and started > finished:
            forward_reasons.append("host_receipt_time_order_invalid")
        if receipt.get("runner_exit_code") != 0:
            forward_reasons.append("host_runner_failed")
            failed = True
        if receipt.get("child_exit_code") != 0:
            forward_reasons.append("maintenance_child_failed")
            failed = True
        if receipt.get("error_code") not in {None, ""}:
            forward_reasons.append("host_receipt_success_has_error")
            failed = True
    else:
        started = None
        finished = None

    heartbeat = _last_result(database.get("last_result")) if database is not None else {}
    if database is not None:
        if heartbeat.get("status") != "committed":
            forward_reasons.append("database_heartbeat_not_committed")
            failed = True
        if heartbeat.get("deletion_authorized") != "false":
            forward_reasons.append("database_heartbeat_deletion_not_denied")
        if receipt is not None and heartbeat.get("run_id") != receipt.get("run_id"):
            forward_reasons.append("run_id_mismatch")
            failed = True
        if receipt is not None and heartbeat.get("trigger") != receipt.get("trigger"):
            forward_reasons.append("trigger_mismatch")
            failed = True
        heartbeat_run = _timestamp(database.get("last_run_at"))
        if heartbeat_run is None:
            forward_reasons.append("database_last_run_invalid")
        elif started is not None and abs(started - heartbeat_run) > MAX_CORRELATION_SKEW:
            forward_reasons.append("receipt_heartbeat_run_time_mismatch")
            failed = True
        heartbeat_success = _timestamp(database.get("last_success_at"))
        if heartbeat_success is None:
            forward_reasons.append("database_last_success_invalid")
        elif finished is not None and abs(finished - heartbeat_success) > MAX_CORRELATION_SKEW:
            forward_reasons.append("receipt_heartbeat_time_mismatch")
            failed = True
        last_error = _timestamp(database.get("last_error_at"))
        if last_error is not None and (heartbeat_success is None or last_error > heartbeat_success):
            forward_reasons.append("database_has_newer_failure")
            failed = True

    if systemd is not None and finished is not None:
        systemd_finished = _timestamp(systemd.get("finished_at"))
        if systemd_finished is None:
            forward_reasons.append("systemd_finished_at_invalid")
        elif abs(systemd_finished - finished) > MAX_CORRELATION_SKEW:
            forward_reasons.append("systemd_receipt_time_mismatch")
            failed = True

    if receipt is not None and archive is not None:
        normal = _mapping(receipt.get("normal"))
        if normal is None:
            forward_reasons.append("normal_receipt_missing")
            failed = True
        else:
            _same_fact(forward_reasons, "normal", normal, heartbeat, _mapping(archive.get("normal")))
            if any(reason.startswith("normal_") for reason in forward_reasons):
                failed = True
        if _evaluate_catchup(
            catchup_reasons,
            receipt=receipt,
            heartbeat=heartbeat,
            archive=archive,
            catchup_gap_policy=catchup_gap_policy,
        ):
            failed = True

    forward_reasons = sorted(set(forward_reasons))
    catchup_reasons = sorted(set(catchup_reasons))
    reasons = sorted(set(forward_reasons + catchup_reasons))
    if not reasons:
        status = "healthy"
    elif failed:
        status = "failed"
    else:
        status = "degraded"
    return {
        "healthy": not reasons,
        "status": status,
        "reasons": reasons,
        "forward_reasons": forward_reasons,
        "catchup_reasons": catchup_reasons,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("snapshot", type=pathlib.Path, help="structured Phase 2 health snapshot JSON")
    parser.add_argument(
        "--catchup-gap-policy",
        default=DEFAULT_CATCHUP_GAP_POLICY,
        choices=("accepted_terminal", "strict"),
        help="accepted_terminal downgrades historical terminal gaps to degraded",
    )
    args = parser.parse_args()
    try:
        snapshot = json.loads(args.snapshot.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(json.dumps({"healthy": False, "status": "failed", "reasons": [str(exc)]}))
        return 2
    verdict = evaluate(snapshot, catchup_gap_policy=args.catchup_gap_policy)
    print(json.dumps(verdict, ensure_ascii=True, sort_keys=True))
    return 0 if verdict["healthy"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
