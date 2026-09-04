#!/usr/bin/env python3
"""Classify one host restore-canary receipt for unified diagnostics."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import re
import sys

STAGE0 = pathlib.Path(__file__).resolve().parents[1] / "stage0"
sys.path.insert(0, str(STAGE0))

from pgdump_restore_canary_contract import precious_counts_valid  # noqa: E402


MAX_RECEIPT_AGE = dt.timedelta(days=8)
OBJECT_URI_RE = re.compile(r"s3://[^/]+/(?:prod|edge/[a-z][a-z0-9]{1,15})/pgdump/tokenkey-\d{8}T\d{6}Z\.sql\.gz")
REQUIRED_FIELDS = {
    "schema_version",
    "mode",
    "target",
    "completed_at",
    "source_s3_uri",
    "source_last_modified",
    "compressed_bytes",
    "uncompressed_bytes",
    "required_free_bytes",
    "observed_free_bytes",
    "artifact_sha256",
    "restore_image",
    "live_counts",
    "restored_counts",
    "cleanup_verified",
    "source_mutated",
    "deletion_authorized",
}
TARGET_RE = re.compile(r"(?:prod|edge:[a-z][a-z0-9]{1,15})")


def _parse_time(value: object) -> dt.datetime | None:
    if not isinstance(value, str):
        return None
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        return None
    return parsed.astimezone(dt.timezone.utc)


def _finding(
    target_id: str,
    *,
    healthy: bool,
    summary: str,
    status: str | None = None,
    severity: str | None = None,
) -> dict[str, str]:
    if status is None:
        status = "ok" if healthy else "issue_candidate"
    if severity is None:
        severity = "info" if healthy else "error"
    if status == "warning":
        title = f"pg_dump restore canary self-healed for {target_id}"
    elif healthy:
        title = f"pg_dump restore canary is current for {target_id}"
    else:
        title = f"pg_dump restore canary needs attention for {target_id}"
    return {
        "target_id": target_id,
        "kind": "pgdump_restore_canary",
        "status": status,
        "severity": severity,
        "title": title,
        "summary": summary,
        "signature": f"pgdump-restore-canary|{target_id}",
    }


def receipt_problem(receipt: object, expected_target: str) -> str | None:
    """Return why recovery evidence is invalid, or None when it is complete."""
    if TARGET_RE.fullmatch(expected_target) is None:
        return f"restore canary expected target is invalid: {expected_target!r}"
    if not isinstance(receipt, dict):
        return "restore canary receipt is malformed"
    if receipt.get("target") != expected_target:
        return f"restore canary receipt has wrong target: {receipt.get('target')!r}"
    missing = sorted(REQUIRED_FIELDS - set(receipt))
    if missing:
        return f"restore canary receipt has incomplete evidence: missing {', '.join(missing)}"

    completed_at = _parse_time(receipt.get("completed_at"))
    source_modified = _parse_time(receipt.get("source_last_modified"))
    numeric_fields = (
        "compressed_bytes",
        "uncompressed_bytes",
        "required_free_bytes",
        "observed_free_bytes",
    )
    numeric_evidence_valid = all(
        not isinstance(receipt.get(field), bool)
        and isinstance(receipt.get(field), int)
        and receipt[field] > 0
        for field in numeric_fields
    )
    evidence_valid = (
        receipt.get("schema_version") == 2
        and receipt.get("mode") == "pgdump_restore_canary"
        and completed_at is not None
        and source_modified is not None
        and OBJECT_URI_RE.fullmatch(str(receipt.get("source_s3_uri", ""))) is not None
        and numeric_evidence_valid
        and receipt["observed_free_bytes"] >= receipt["required_free_bytes"]
        and re.fullmatch(r"[0-9a-f]{64}", str(receipt.get("artifact_sha256", ""))) is not None
        and isinstance(receipt.get("restore_image"), str)
        and bool(receipt["restore_image"])
        and precious_counts_valid(receipt.get("live_counts"))
        and precious_counts_valid(receipt.get("restored_counts"))
        and receipt.get("cleanup_verified") is True
        and receipt.get("source_mutated") is False
        and receipt.get("deletion_authorized") is False
    )
    expected_prefix = (
        "/prod/pgdump/"
        if expected_target == "prod"
        else f"/edge/{expected_target.split(':', 1)[1]}/pgdump/"
    )
    if not evidence_valid or expected_prefix not in str(receipt.get("source_s3_uri", "")):
        return "restore canary receipt has incomplete or invalid evidence"
    return None


def evaluate_receipt(
    raw: str,
    expected_target: str,
    target_id: str,
    *,
    now: dt.datetime | None = None,
) -> dict[str, str]:
    current_time = now or dt.datetime.now(dt.timezone.utc)
    if not raw.strip():
        return _finding(target_id, healthy=False, summary="restore canary receipt is missing")
    try:
        receipt = json.loads(raw)
    except json.JSONDecodeError:
        return _finding(target_id, healthy=False, summary="restore canary receipt is malformed JSON")
    problem = receipt_problem(receipt, expected_target)
    if problem is not None:
        return _finding(target_id, healthy=False, summary=problem)
    completed_at = _parse_time(receipt["completed_at"])
    assert completed_at is not None
    age = current_time.astimezone(dt.timezone.utc) - completed_at
    if age < dt.timedelta(0) or age > MAX_RECEIPT_AGE:
        return _finding(target_id, healthy=False, summary=f"restore canary receipt is stale: age={age}")
    if receipt.get("healed_from_stale_dump") is True:
        return _finding(
            target_id,
            healthy=True,
            status="warning",
            severity="warning",
            summary=(
                "restore canary self-healed after a stale S3 dump; "
                "verify tokenkey-pgdump.timer still publishes hourly objects"
            ),
        )
    return _finding(
        target_id,
        healthy=True,
        summary="real S3 pg_dump restore and cleanup evidence are current",
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=pathlib.Path, required=True)
    parser.add_argument("--expected-target", required=True)
    parser.add_argument("--target-id", required=True)
    args = parser.parse_args(argv)
    try:
        raw = args.receipt.read_text(encoding="utf-8")
    except FileNotFoundError:
        raw = ""
    finding = evaluate_receipt(raw, args.expected_target, args.target_id)
    print(json.dumps(finding, ensure_ascii=True, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
