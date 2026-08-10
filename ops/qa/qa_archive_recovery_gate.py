#!/usr/bin/env python3
"""Validate independent recovery evidence and authorize break-glass retirement (script removed post-closeout)."""
from __future__ import annotations

import argparse
import hashlib
import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any


REQUIRED_COMMANDS = ("inspect", "verify", "restore")


def _read_json(path: Path, label: str) -> tuple[dict[str, Any] | None, str | None]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        return None, f"{label} unavailable or invalid: {exc}"
    if not isinstance(value, dict):
        return None, f"{label} must be a JSON object"
    return value, None


def _parse_utc(value: object) -> datetime | None:
    if not isinstance(value, str):
        return None
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        return None
    return parsed.astimezone(timezone.utc)


def _production_approval_failures(
    approval_path: Path | None,
    evidence_bytes: bytes,
    expected_window_start: str,
    expected_bucket: str,
    expected_recovery_role_arn: str,
    latest_receipt_at: datetime | None,
    expected_approval_issuer: str = "",
) -> list[str]:
    if approval_path is None:
        return ["production_approval"]
    approval, error = _read_json(approval_path, "production approval")
    if error is not None or approval is None:
        return ["production_approval"]
    failures: list[str] = []
    expected = {
        "schema_version": 1,
        "approval_kind": "tokenkey-prod-qa-archive-retirement-v1",
        "approval_source": "human-high-risk-gate",
        "evidence_sha256": hashlib.sha256(evidence_bytes).hexdigest(),
        "expected_window_start": expected_window_start,
        "expected_bucket": expected_bucket,
        "expected_recovery_role_arn": expected_recovery_role_arn,
    }
    for field, value in expected.items():
        if not value or approval.get(field) != value:
            failures.append(f"production_approval.{field}")
    approved_by = approval.get("approved_by")
    if not isinstance(approved_by, str) or not approved_by.strip() or approved_by.strip().lower() == "pending":
        failures.append("production_approval.approved_by")
    approved_at = _parse_utc(approval.get("approved_at"))
    expires_at = _parse_utc(approval.get("expires_at"))
    now = datetime.now(timezone.utc)
    if approved_at is None or approved_at > now:
        failures.append("production_approval.approved_at")
    elif latest_receipt_at is None or approved_at < latest_receipt_at:
        failures.append("production_approval.receipt_order")
    if expires_at is None or expires_at <= now or (approved_at is not None and expires_at <= approved_at):
        failures.append("production_approval.expires_at")
    issuer = approval.get("approval_issuer")
    if expected_approval_issuer:
        if issuer != expected_approval_issuer:
            failures.append("production_approval.approval_issuer")
    elif issuer not in {None, ""} and not isinstance(issuer, str):
        failures.append("production_approval.approval_issuer")
    try:
        if approval_path.stat().st_mode & 0o022:
            failures.append("production_approval.permissions")
    except OSError:
        failures.append("production_approval.permissions")
    return failures


def evaluate(
    evidence_path: Path,
    production_approval_path: Path | None = None,
    expected_window_start: str = "",
    expected_bucket: str = "",
    expected_recovery_role_arn: str = "",
    expected_approval_issuer: str = "",
) -> tuple[int, dict[str, Any]]:
    result: dict[str, Any] = {
        "ok": False,
        "planned_transition_authorized": False,
        "production_success_claimed": False,
        "script_action": "preserve",
        "break_glass_state": "retired",
    }
    try:
        evidence_bytes = evidence_path.read_bytes()
    except OSError as exc:
        result["error"] = f"recovery evidence unavailable or invalid: {exc}"
        return 2, result
    try:
        evidence = json.loads(evidence_bytes)
    except json.JSONDecodeError as exc:
        result["error"] = f"recovery evidence unavailable or invalid: {exc}"
        return 2, result
    if not isinstance(evidence, dict):
        result["error"] = "recovery evidence must be a JSON object"
        return 2, result

    failures: list[str] = []
    if evidence.get("schema_version") != 1:
        failures.append("schema_version")
    scope = evidence.get("scope")
    if scope not in {"synthetic", "production"}:
        failures.append("scope")
    if evidence.get("source") != "ops-workstation-s3":
        failures.append("source")
    receipt_ids: set[str] = set()
    recovery_run_id = ""
    for command in REQUIRED_COMMANDS:
        receipt = evidence.get(command)
        if not isinstance(receipt, dict) or receipt.get("ok") is not True or receipt.get("verified") is not True:
            failures.append(f"{command}.verified")
            continue
        if receipt.get("database_accessed") is not False:
            failures.append(f"{command}.database_accessed")
        if receipt.get("source") != "ops-workstation-s3":
            failures.append(f"{command}.source")
        if receipt.get("command") != command:
            failures.append(f"{command}.command")
        receipt_id = receipt.get("receipt_id")
        if not isinstance(receipt_id, str) or not receipt_id.strip() or receipt_id in receipt_ids:
            failures.append(f"{command}.receipt_id")
        else:
            receipt_ids.add(receipt_id)
        command_run_id = receipt.get("recovery_run_id")
        if not isinstance(command_run_id, str) or not command_run_id.strip():
            failures.append(f"{command}.recovery_run_id")
        elif recovery_run_id and command_run_id != recovery_run_id:
            failures.append(f"{command}.recovery_run_id_mismatch")
        else:
            recovery_run_id = command_run_id
        for field in ("window_start", "bucket", "recovery_role_arn"):
            if not isinstance(receipt.get(field), str) or not receipt[field].strip():
                failures.append(f"{command}.{field}")
    restore = evidence.get("restore")
    if not isinstance(restore, dict) or restore.get("privacy_confirmed") is not True:
        failures.append("restore.privacy_confirmed")
    inspect = evidence.get("inspect")
    if isinstance(inspect, dict):
        for command in ("verify", "restore"):
            receipt = evidence.get(command)
            if not isinstance(receipt, dict):
                continue
            for field in ("window_start", "bucket", "recovery_role_arn"):
                if receipt.get(field) != inspect.get(field):
                    failures.append(f"{command}.{field}_mismatch")
    if failures:
        result["error"] = "recovery evidence mismatch"
        result["mismatches"] = failures
        return 2, result

    if scope == "production":
        now = datetime.now(timezone.utc)
        latest_receipt_at: datetime | None = None
        for command in REQUIRED_COMMANDS:
            captured_at = _parse_utc(evidence[command].get("captured_at"))
            if captured_at is None or captured_at > now or now-captured_at > timedelta(hours=24):
                failures.append(f"{command}.captured_at_freshness")
                continue
            if latest_receipt_at is None or captured_at > latest_receipt_at:
                latest_receipt_at = captured_at
        for field, actual, expected in (
            ("window_start", inspect.get("window_start"), expected_window_start),
            ("bucket", inspect.get("bucket"), expected_bucket),
            ("recovery_role_arn", inspect.get("recovery_role_arn"), expected_recovery_role_arn),
        ):
            if not expected or actual != expected:
                failures.append(f"production_expected.{field}")
        failures.extend(_production_approval_failures(
            production_approval_path,
            evidence_bytes,
            expected_window_start,
            expected_bucket,
            expected_recovery_role_arn,
            latest_receipt_at,
            expected_approval_issuer,
        ))
        if failures:
            result["error"] = "production recovery evidence is not independently approved"
            result["mismatches"] = failures
            return 2, result
        result.update({
            "ok": True,
            "planned_transition_authorized": True,
            "production_evidence_validated": True,
            "production_success_claimed": False,
            "script_action": "planned_removal_only",
            "note": "production evidence is hash-bound to an unexpired human high-risk approval; the gate authorizes retirement without claiming operational success",
        })
        return 0, result

    result.update({
        "ok": True,
        "planned_transition_authorized": True,
        "production_evidence_validated": False,
        "script_action": "planned_removal_only",
        "note": "synthetic evidence validates the repository transition shape only; production evidence and approval remain required",
    })
    return 0, result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    plan = subparsers.add_parser("plan-retirement")
    plan.add_argument("--evidence", required=True, type=Path)
    plan.add_argument("--production-approval", type=Path)
    plan.add_argument("--expected-window-start", default="")
    plan.add_argument("--expected-bucket", default="")
    plan.add_argument("--expected-recovery-role-arn", default="")
    plan.add_argument("--expected-approval-issuer", default="")
    args = parser.parse_args()
    code, result = evaluate(
        args.evidence,
        args.production_approval,
        args.expected_window_start,
        args.expected_bucket,
        args.expected_recovery_role_arn,
        args.expected_approval_issuer,
    )
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return code


if __name__ == "__main__":
    raise SystemExit(main())
