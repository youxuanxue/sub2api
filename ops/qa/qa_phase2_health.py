#!/usr/bin/env python3
"""Correlate QA Phase 2 systemd, host, heartbeat, and archive-control facts."""

from __future__ import annotations

import argparse
import datetime as dt
import email.utils
import json
import pathlib
import sys
from typing import Any

MAX_RECEIPT_AGE = dt.timedelta(hours=2)
MAX_CORRELATION_SKEW = dt.timedelta(minutes=5)
DEFAULT_CATCHUP_GAP_POLICY = "accepted_terminal"
TERMINAL_CATCHUP_ERROR = "source_unavailable_after_retention"
FAILED_ARCHIVE_ERROR = "archive_failed"
HOURLY_HORIZON = 72


def _evaluate_qa_records_lifecycle(
    reasons: list[str],
    qa_records: dict[str, Any] | None,
    archive: dict[str, Any] | None,
    now: dt.datetime,
) -> tuple[bool, str]:
    """Return the lifecycle/catalog failure flag and the derived cutover phase."""
    failed = False
    if qa_records is None:
        reasons.append("qa_records_catalog_missing")
        return True, "unknown"
    active = qa_records.get("hourly_cutover_active") is True
    finalized = qa_records.get("hourly_cutover_finalized") is True
    finalize_receipt_present = qa_records.get("hourly_cutover_finalize_receipt_present")
    activate_t0 = _timestamp(qa_records.get("activate_t0_utc"))
    activate_applied_at = _timestamp(qa_records.get("activate_applied_at"))
    activate_plan_hash = qa_records.get("activate_plan_hash")
    finalize_t0 = _timestamp(qa_records.get("finalize_t0_utc"))
    finalize_applied_at = _timestamp(qa_records.get("finalize_applied_at"))
    finalize_plan_hash = qa_records.get("finalize_plan_hash")
    receipts_inconsistent = (
        (finalized and not active)
        or (
            isinstance(finalize_receipt_present, bool)
            and finalize_receipt_present != finalized
        )
        or (active and not isinstance(finalize_receipt_present, bool))
        or (active and activate_t0 is None)
        or (active and not _valid_plan_hash(activate_plan_hash))
        or (active and activate_applied_at is None)
        or (activate_applied_at is not None and activate_applied_at > now)
        or (
            finalized
            and (
                finalize_t0 is None
                or finalize_t0 != activate_t0
                or not _valid_plan_hash(finalize_plan_hash)
                or finalize_applied_at is None
                or finalize_applied_at > now
                or (
                    activate_applied_at is not None
                    and finalize_applied_at is not None
                    and finalize_applied_at < activate_applied_at
                )
            )
        )
    )
    if receipts_inconsistent:
        reasons.append("qa_records_cutover_receipts_inconsistent")
        failed = True

    if not active:
        phase = "pre_activate"
    elif finalized:
        phase = "finalized"
    elif activate_t0 is not None and now < activate_t0:
        phase = "scheduled_activation"
    else:
        phase = "draining"

    if phase in {"scheduled_activation", "draining"}:
        if qa_records.get("default_present") is not True:
            reasons.append("qa_records_default_missing_before_finalize")
            failed = True
        if qa_records.get("default_rows_after_t0") != 0:
            reasons.append("qa_records_default_growth_after_t0")
            failed = True

    if phase == "scheduled_activation":
        required = qa_records.get("future_coverage_required_hours")
        canonical = qa_records.get("future_coverage_canonical_hours")
        gap = qa_records.get("future_coverage_gap_hours")
        coverage_start = _timestamp(qa_records.get("future_coverage_start_utc"))
        coverage_end = _timestamp(qa_records.get("future_coverage_end_utc"))
        if (
            activate_t0 is None
            or required != HOURLY_HORIZON
            or canonical != HOURLY_HORIZON
            or gap != 0
            or coverage_start != activate_t0
            or coverage_end != activate_t0 + dt.timedelta(hours=HOURLY_HORIZON)
        ):
            reasons.append("qa_records_activation_partition_gap")
            failed = True

    if phase == "draining" and qa_records.get("current_hour_partition_missing") is not False:
        reasons.append("qa_records_current_partition_missing")
        failed = True

    if phase == "finalized":
        default_present = qa_records.get("default_present")
        if default_present is True:
            reasons.append("qa_records_default_after_hourly_cutover")
            failed = True
        elif default_present is not False:
            reasons.append("qa_records_default_presence_unknown")
            failed = True
        required = qa_records.get("future_coverage_required_hours")
        canonical = qa_records.get("future_coverage_canonical_hours")
        gap = qa_records.get("future_coverage_gap_hours")
        coverage_start = _timestamp(qa_records.get("future_coverage_start_utc"))
        coverage_end = _timestamp(qa_records.get("future_coverage_end_utc"))
        current_hour = now.replace(minute=0, second=0, microsecond=0)
        if (
            required != HOURLY_HORIZON
            or canonical != HOURLY_HORIZON
            or gap != 0
            or coverage_start != current_hour
            or coverage_end != current_hour + dt.timedelta(hours=HOURLY_HORIZON)
        ):
            reasons.append("qa_records_future_partition_gap")
            failed = True
        if qa_records.get("current_hour_partition_missing") is not False:
            reasons.append("qa_records_current_partition_missing")
            failed = True
        overdue = qa_records.get("expired_partitions_attached")
        if not _is_nonnegative_int(overdue):
            reasons.append("qa_records_expired_partitions_fact_missing")
            failed = True
        elif overdue > 0:
            reasons.append("qa_records_expired_partitions_attached")
            failed = True
        noncanonical = qa_records.get("noncanonical_partitions_attached")
        if not _is_nonnegative_int(noncanonical):
            reasons.append("qa_records_noncanonical_partitions_fact_missing")
            failed = True
        elif noncanonical > 0:
            reasons.append("qa_records_noncanonical_partitions_attached")
            failed = True
        backlog = qa_records.get("hot_cleanup_backlog")
        cleanup_pending = qa_records.get("hot_files_cleanup_pending")
        if not _is_nonnegative_int(backlog) or not isinstance(cleanup_pending, bool):
            reasons.append("qa_records_hot_cleanup_fact_missing")
            failed = True
        elif cleanup_pending or backlog > 0:
            reasons.append("qa_records_hot_files_cleanup_pending")
            failed = True
    if archive is not None:
        for key in ("failed_shards", "archive_failed_windows"):
            entries = archive.get(key)
            if not isinstance(entries, list):
                continue
            for entry in entries:
                if not isinstance(entry, dict):
                    continue
                code = entry.get("verification_error_code")
                if code == FAILED_ARCHIVE_ERROR:
                    reasons.append("archive_failed")
                    failed = True
                elif code not in {None, "", TERMINAL_CATCHUP_ERROR}:
                    reasons.append("archive_verification_failure")
                    failed = True
    return failed, phase


def _valid_plan_hash(value: Any) -> bool:
    return (
        isinstance(value, str)
        and len(value) == 64
        and all(character in "0123456789abcdef" for character in value)
    )


def _is_nonnegative_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and value >= 0


def _evaluate_boundary_disabled(reasons: list[str], snapshot: dict[str, Any]) -> bool:
    systemd = _mapping(snapshot.get("boundary_systemd"))
    if systemd is None:
        reasons.append("boundary_systemd_missing")
        return True
    if systemd.get("timer_enabled") is not False:
        reasons.append("boundary_timer_enabled_before_finalize")
    if systemd.get("timer_active") is not False:
        reasons.append("boundary_timer_active_before_finalize")
    return any(reason.startswith("boundary_") for reason in reasons)


def _evaluate_boundary(
    reasons: list[str],
    snapshot: dict[str, Any],
    now: dt.datetime,
) -> bool:
    systemd = _mapping(snapshot.get("boundary_systemd"))
    receipt = _mapping(snapshot.get("boundary_host_receipt"))
    database = _mapping(snapshot.get("boundary_database_heartbeat"))
    if systemd is None:
        reasons.append("boundary_systemd_missing")
    if receipt is None:
        reasons.append("boundary_host_receipt_missing")
    if database is None:
        reasons.append("boundary_database_heartbeat_missing")
    if systemd is None or receipt is None or database is None:
        return True

    if systemd.get("timer_enabled") is not True:
        reasons.append("boundary_timer_not_enabled")
    if systemd.get("timer_active") is not True:
        reasons.append("boundary_timer_not_active")
    if systemd.get("service_result") != "success":
        reasons.append("boundary_systemd_service_failed")

    if receipt.get("schema_version") != "qa-boundary-runner-v1":
        reasons.append("boundary_host_receipt_schema_invalid")
    run_id = receipt.get("run_id")
    if not isinstance(run_id, str) or not run_id:
        reasons.append("boundary_host_receipt_run_id_missing")
    if receipt.get("trigger") != "timer":
        reasons.append("boundary_host_receipt_trigger_not_timer")
    if not isinstance(receipt.get("active_container"), str) or not receipt.get("active_container"):
        reasons.append("boundary_host_receipt_container_missing")
    if not isinstance(receipt.get("image"), str) or not receipt.get("image", "").startswith("sha256:"):
        reasons.append("boundary_host_receipt_image_invalid")
    if receipt.get("runner_uid") != 1000 or receipt.get("runner_gid") != 1000:
        reasons.append("boundary_host_receipt_runner_identity_invalid")
    started = _timestamp(receipt.get("started_at"))
    finished = _timestamp(receipt.get("finished_at"))
    if started is None:
        reasons.append("boundary_host_receipt_started_at_invalid")
    if finished is None:
        reasons.append("boundary_host_receipt_finished_at_invalid")
    elif finished > now or now - finished > MAX_RECEIPT_AGE:
        reasons.append("boundary_host_receipt_stale")
    elif started is not None and started > finished:
        reasons.append("boundary_host_receipt_time_order_invalid")
    if receipt.get("runner_exit_code") != 0:
        reasons.append("boundary_host_runner_failed")
    if receipt.get("child_exit_code") != 0:
        reasons.append("boundary_maintenance_child_failed")
    if receipt.get("error_code") not in {None, ""}:
        reasons.append("boundary_host_receipt_success_has_error")

    boundary = _mapping(receipt.get("boundary"))
    provision = _mapping(boundary.get("provision")) if boundary is not None else None
    if provision is None:
        reasons.append("boundary_provision_receipt_missing")
        covered = None
        required = None
        provision_attempts = None
        provision_lock_retries = None
        provision_attempts_present = False
        provision_lock_retries_present = False
    else:
        covered = provision.get("ranges_covered")
        required = provision.get("ranges_required")
        if covered != HOURLY_HORIZON or required != HOURLY_HORIZON:
            reasons.append("boundary_provision_coverage_incomplete")
        provision_attempts = provision.get("attempts")
        provision_lock_retries = provision.get("lock_retries")
        provision_attempts_present = "attempts" in provision
        provision_lock_retries_present = "lock_retries" in provision

    heartbeat = _last_result(database.get("last_result"))
    heartbeat_attempts_present = "provision_attempts" in heartbeat
    heartbeat_lock_retries_present = "provision_lock_retries" in heartbeat
    retry_contract_present = any(
        (
            provision_attempts_present,
            provision_lock_retries_present,
            heartbeat_attempts_present,
            heartbeat_lock_retries_present,
        )
    )
    if retry_contract_present:
        if type(provision_attempts) is not int or provision_attempts < 1:
            reasons.append("boundary_provision_attempts_invalid")
        if type(provision_lock_retries) is not int or provision_lock_retries < 0:
            reasons.append("boundary_provision_lock_retries_invalid")
        if (
            type(provision_attempts) is int
            and type(provision_lock_retries) is int
            and provision_lock_retries != provision_attempts - 1
        ):
            reasons.append("boundary_provision_retry_accounting_invalid")
        if heartbeat.get("provision_attempts") != str(provision_attempts):
            reasons.append("boundary_provision_attempts_heartbeat_mismatch")
        if heartbeat.get("provision_lock_retries") != str(provision_lock_retries):
            reasons.append("boundary_provision_lock_retries_heartbeat_mismatch")

    if heartbeat.get("status") != "ok" or heartbeat.get("phase") != "boundary":
        reasons.append("boundary_database_heartbeat_not_ok")
    if heartbeat.get("run_id") != run_id:
        reasons.append("boundary_run_id_mismatch")
    if heartbeat.get("trigger") != receipt.get("trigger"):
        reasons.append("boundary_trigger_mismatch")
    if heartbeat.get("provision_covered") != f"{covered}/{required}":
        reasons.append("boundary_provision_heartbeat_mismatch")
    deletion_authorized = str(receipt.get("deletion_authorized") is True).lower()
    if heartbeat.get("deletion_authorized") != deletion_authorized:
        reasons.append("boundary_deletion_authorized_mismatch")

    heartbeat_run = _timestamp(database.get("last_run_at"))
    heartbeat_success = _timestamp(database.get("last_success_at"))
    if heartbeat_run is None:
        reasons.append("boundary_database_last_run_invalid")
    elif started is not None and abs(started - heartbeat_run) > MAX_CORRELATION_SKEW:
        reasons.append("boundary_receipt_heartbeat_run_time_mismatch")
    if heartbeat_success is None:
        reasons.append("boundary_database_last_success_invalid")
    elif finished is not None and abs(finished - heartbeat_success) > MAX_CORRELATION_SKEW:
        reasons.append("boundary_receipt_heartbeat_time_mismatch")
    last_error = _timestamp(database.get("last_error_at"))
    if last_error is not None and (heartbeat_success is None or last_error > heartbeat_success):
        reasons.append("boundary_database_has_newer_failure")
    if _correlate_systemd_receipt(
        reasons,
        "boundary_",
        systemd,
        started,
        finished,
    ):
        return True
    return bool(reasons)


def _correlate_systemd_receipt(
    reasons: list[str],
    prefix: str,
    systemd: dict[str, Any],
    started: dt.datetime | None,
    finished: dt.datetime | None,
) -> bool:
    failed = False
    last_trigger = _timestamp(systemd.get("last_trigger_at"))
    if last_trigger is None:
        reasons.append(f"{prefix}systemd_last_trigger_at_invalid")
        failed = True
    elif started is not None and abs(last_trigger - started) > MAX_CORRELATION_SKEW:
        reasons.append(f"{prefix}systemd_receipt_start_time_mismatch")
        failed = True

    systemd_finished = _timestamp(systemd.get("finished_at"))
    if (
        systemd_finished is not None
        and finished is not None
        and abs(systemd_finished - finished) > MAX_CORRELATION_SKEW
    ):
        reasons.append(f"{prefix}systemd_receipt_time_mismatch")
        failed = True
    return failed


def _mapping(value: Any) -> dict[str, Any] | None:
    return value if isinstance(value, dict) else None


def _parse_systemd_timestamp(text: str) -> dt.datetime | None:
    for fmt in ("%a %Y-%m-%d %H:%M:%S UTC", "%a %Y-%m-%d %H:%M:%S.%f UTC"):
        try:
            parsed = dt.datetime.strptime(text, fmt)
        except ValueError:
            continue
        return parsed.replace(tzinfo=dt.timezone.utc)
    return None


def _timestamp(value: Any) -> dt.datetime | None:
    if not isinstance(value, str) or not value.strip():
        return None
    text = value.strip()
    if text.lower() in {"n/a", "none", "[n/a]"}:
        return None
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    parsed: dt.datetime | None = None
    try:
        parsed = dt.datetime.fromisoformat(text)
    except ValueError:
        parsed = _parse_systemd_timestamp(text)
        if parsed is None:
            try:
                parsed = email.utils.parsedate_to_datetime(text)
            except (TypeError, ValueError):
                return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
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


def _is_terminal_compensation(plan: dict[str, Any]) -> bool:
    return (
        plan.get("state") == "failed"
        and plan.get("verification_error_code") == TERMINAL_CATCHUP_ERROR
    )


def _terminal_inventory(archive: dict[str, Any]) -> list[dict[str, Any]]:
    terminal = archive.get("terminal_failures_after_cutover")
    if not isinstance(terminal, list):
        return []
    return [item for item in terminal if isinstance(item, dict)]


def _is_correlatable_terminal_catchup_attempt(
    receipt: dict[str, Any] | None,
    heartbeat: dict[str, str],
) -> bool:
    if receipt is None:
        return False
    normal = _mapping(receipt.get("normal"))
    compensation = _mapping(receipt.get("compensation"))
    return bool(
        receipt.get("failure_stage") == "compensation_terminal"
        and receipt.get("failure_code") == TERMINAL_CATCHUP_ERROR
        and receipt.get("error_code") == "child_failed"
        and receipt.get("runner_exit_code") != 0
        and receipt.get("child_exit_code") != 0
        and normal is not None
        and normal.get("state") == "committed"
        and normal.get("restore_verified") is True
        and compensation is not None
        and _is_terminal_compensation(compensation)
        and heartbeat.get("status") == "failed"
        and heartbeat.get("stage") == "compensation_terminal"
        and heartbeat.get("error_code") == TERMINAL_CATCHUP_ERROR
    )


def _evaluate_terminal_compensation(
    reasons: list[str],
    compensation: dict[str, Any],
    heartbeat: dict[str, str],
    archive: dict[str, Any],
) -> bool:
    """Return True when terminal compensation facts contradict across sources."""
    failed = False
    window = compensation.get("window_start")
    terminal = _terminal_inventory(archive)
    if window and not any(entry.get("window_start") == window for entry in terminal):
        reasons.append("compensation_window_not_in_terminal_inventory")
        failed = True
    if heartbeat.get("compensation_window") != str(window):
        reasons.append("compensation_window_heartbeat_mismatch")
        failed = True
    if heartbeat.get("compensation_state") != "failed":
        reasons.append("compensation_state_heartbeat_mismatch")
        failed = True
    if heartbeat.get("compensation_error_code") != TERMINAL_CATCHUP_ERROR:
        reasons.append("compensation_error_code_heartbeat_mismatch")
        failed = True

    control = _mapping(archive.get("compensation"))
    if control is None:
        reasons.append("compensation_control_missing")
        failed = True
    elif control.get("window_start") != window:
        reasons.append("compensation_window_control_mismatch")
        failed = True
    else:
        if control.get("state") != "failed":
            reasons.append("compensation_state_control_mismatch")
            failed = True
        if control.get("verification_error_code") != TERMINAL_CATCHUP_ERROR:
            reasons.append("compensation_error_code_control_mismatch")
            failed = True
    return failed


def _evaluate_terminal_inventory_control(
    reasons: list[str],
    control: dict[str, Any],
    heartbeat: dict[str, str],
    archive: dict[str, Any],
) -> bool:
    """Return True when a historical terminal control row contradicts other sources."""
    failed = False
    window = control.get("window_start")
    terminal = _terminal_inventory(archive)
    if window and not any(entry.get("window_start") == window for entry in terminal):
        reasons.append("compensation_window_not_in_terminal_inventory")
        failed = True
    if any(key.startswith("compensation_") for key in heartbeat):
        reasons.append("compensation_heartbeat_without_receipt")
        failed = True
    if control.get("state") != "failed":
        reasons.append("compensation_state_control_mismatch")
        failed = True
    if control.get("verification_error_code") != TERMINAL_CATCHUP_ERROR:
        reasons.append("compensation_error_code_control_mismatch")
        failed = True
    return failed


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
        if catchup_gap_policy == "accepted_terminal" and _is_terminal_compensation(compensation):
            if _evaluate_terminal_compensation(reasons, compensation, heartbeat, archive):
                failed = True
        else:
            _same_fact(reasons, "compensation", compensation, heartbeat, _mapping(archive.get("compensation")))
    elif raw_compensation is not None:
        reasons.append("compensation_receipt_invalid")
        failed = True
    else:
        control = _mapping(archive.get("compensation"))
        heartbeat_checked = False
        if control is not None:
            if catchup_gap_policy == "accepted_terminal" and _is_terminal_compensation(control):
                if _evaluate_terminal_inventory_control(reasons, control, heartbeat, archive):
                    failed = True
                heartbeat_checked = True
            else:
                reasons.append("compensation_control_without_receipt")
                failed = True
        if not heartbeat_checked and any(key.startswith("compensation_") for key in heartbeat):
            reasons.append("compensation_heartbeat_without_receipt")
            failed = True

    terminal = archive.get("terminal_failures_after_cutover")
    if not isinstance(terminal, list):
        reasons.append("terminal_failure_inventory_missing")
        failed = True
    elif terminal:
        terminal_codes_valid = True
        for entry in terminal:
            code = entry.get("verification_error_code") if isinstance(entry, dict) else None
            if code == TERMINAL_CATCHUP_ERROR:
                continue
            terminal_codes_valid = False
            reasons.append("archive_failed" if code == FAILED_ARCHIVE_ERROR else "archive_verification_failure")
        if not terminal_codes_valid:
            failed = True
        elif catchup_gap_policy == "accepted_terminal":
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
    qa_records = _mapping(snapshot.get("qa_records"))
    heartbeat = _last_result(database.get("last_result")) if database is not None else {}
    terminal_catchup_attempt = _is_correlatable_terminal_catchup_attempt(receipt, heartbeat)
    if systemd is None:
        forward_reasons.append("systemd_missing")
    else:
        if systemd.get("timer_enabled") is not True:
            forward_reasons.append("timer_not_enabled")
        if systemd.get("timer_active") is not True:
            forward_reasons.append("timer_not_active")
        if systemd.get("service_result") != "success" and not terminal_catchup_attempt:
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
        if receipt.get("runner_exit_code") != 0 and not terminal_catchup_attempt:
            forward_reasons.append("host_runner_failed")
            failed = True
        if receipt.get("child_exit_code") != 0 and not terminal_catchup_attempt:
            forward_reasons.append("maintenance_child_failed")
            failed = True
        if receipt.get("error_code") not in {None, ""} and not terminal_catchup_attempt:
            forward_reasons.append("host_receipt_success_has_error")
            failed = True
    else:
        started = None
        finished = None

    if database is not None:
        if heartbeat.get("status") != "committed" and not terminal_catchup_attempt:
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
        elif (
            finished is not None
            and not terminal_catchup_attempt
            and abs(finished - heartbeat_success) > MAX_CORRELATION_SKEW
        ):
            forward_reasons.append("receipt_heartbeat_time_mismatch")
            failed = True
        last_error = _timestamp(database.get("last_error_at"))
        if terminal_catchup_attempt:
            if last_error is None or finished is None or abs(finished - last_error) > MAX_CORRELATION_SKEW:
                forward_reasons.append("terminal_catchup_failure_time_mismatch")
                failed = True
        elif last_error is not None and (heartbeat_success is None or last_error > heartbeat_success):
            forward_reasons.append("database_has_newer_failure")
            failed = True

    if systemd is not None and _correlate_systemd_receipt(
        forward_reasons,
        "",
        systemd,
        started,
        finished,
    ):
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

    lifecycle_failed, lifecycle_phase = _evaluate_qa_records_lifecycle(
        forward_reasons, qa_records, archive, now
    )
    if lifecycle_failed:
        failed = True
    if lifecycle_phase == "finalized":
        if _evaluate_boundary(forward_reasons, snapshot, now):
            failed = True
    elif _evaluate_boundary_disabled(forward_reasons, snapshot):
        failed = True

    forward_reasons = sorted(set(forward_reasons))
    catchup_reasons = sorted(set(catchup_reasons))
    reasons = sorted(set(forward_reasons + catchup_reasons))
    accepted_degraded_reasons = {"catchup_terminal_gaps_present"}
    if forward_reasons:
        failed = True
    if catchup_reasons and set(catchup_reasons) != accepted_degraded_reasons:
        failed = True
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
        "lifecycle_phase": lifecycle_phase,
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
    if verdict.get("status") == "failed":
        return 2
    return 0 if verdict.get("healthy") else 2


if __name__ == "__main__":
    raise SystemExit(main())
