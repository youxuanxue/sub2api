#!/usr/bin/env python3
"""Turn data-layer protection signals into independent fail-closed findings."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import sys
from typing import Any


HEARTBEAT_MAX_AGE = dt.timedelta(hours=26)
TELEMETRY_HEARTBEAT_MAX_AGE = dt.timedelta(minutes=3)
PGDUMP_MAX_AGE = dt.timedelta(hours=2)
SNAPSHOT_MAX_AGE = dt.timedelta(hours=36)
HOLD_MAX_AGE = dt.timedelta(days=14)
RESTORE_MAX_AGE = dt.timedelta(days=30)
MAX_FUTURE_SKEW = dt.timedelta(minutes=5)


def _timestamp(value: Any) -> dt.datetime | None:
    if not isinstance(value, str) or not value.strip():
        return None
    try:
        parsed = dt.datetime.fromisoformat(value.strip().replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        return None
    return parsed.astimezone(dt.timezone.utc)


def _finding(kind: str, summary: str, *, severity: str = "error") -> dict[str, str]:
    return {
        "kind": kind,
        "status": "issue_candidate" if severity == "error" else "warning",
        "severity": severity,
        "summary": summary,
    }


def _fresh(
    stamp: dt.datetime | None,
    now: dt.datetime,
    max_age: dt.timedelta,
) -> bool:
    return (
        stamp is not None
        and now - max_age <= stamp
        and stamp <= now + MAX_FUTURE_SKEW
    )


def compute_verdict(signals: dict[str, Any]) -> dict[str, Any]:
    partitions = signals.get("PARTITIONSTATS")
    backups = signals.get("BACKUPSTATS")
    snapshot = signals.get("SNAPSHOTSTATS")
    archive = signals.get("ARCHIVESTATS")
    telemetry = signals.get("TELEMETRYSTATS")
    findings: list[dict[str, str]] = []

    now = _timestamp(partitions.get("server_clock")) if isinstance(partitions, dict) else None
    if now is None:
        findings.append(_finding("data_layer_probe", "partition probe has no valid database clock"))
        now = dt.datetime.now(dt.timezone.utc)

    required_coverage = (
        "ops_error_logs_current_covered",
        "ops_error_logs_future_covered",
        "ops_system_logs_current_covered",
        "ops_system_logs_future_covered",
        "usage_logs_current_covered",
        "usage_logs_future_covered",
    )
    if not isinstance(partitions, dict):
        findings.append(_finding("partition_coverage", "partition coverage probe is missing"))
    else:
        missing = [name for name in required_coverage if partitions.get(name) is not True]
        if missing:
            findings.append(
                _finding(
                    "partition_coverage",
                    "missing current/future partition coverage: " + ", ".join(missing),
                )
            )
        heartbeat = _timestamp(partitions.get("partition_maintenance_last_success_at"))
        heartbeat_error = _timestamp(partitions.get("partition_maintenance_last_error_at"))
        if heartbeat_error is not None and (
            heartbeat is None or heartbeat_error > heartbeat
        ):
            findings.append(
                _finding(
                    "partition_maintenance_error",
                    "partition maintenance has failed since its last success",
                )
            )
        if not _fresh(heartbeat, now, HEARTBEAT_MAX_AGE):
            findings.append(
                _finding(
                    "partition_maintenance_heartbeat",
                    "partition maintenance success heartbeat is missing, stale, or future-dated",
                )
            )

    pgdump = _timestamp(backups.get("latest_pgdump_at")) if isinstance(backups, dict) else None
    if not _fresh(pgdump, now, PGDUMP_MAX_AGE):
        findings.append(
            _finding(
                "pgdump_freshness",
                "latest pgdump is missing, stale, or future-dated",
            )
        )

    snapshot_at = _timestamp(snapshot.get("latest_snapshot_at")) if isinstance(snapshot, dict) else None
    if not _fresh(snapshot_at, now, SNAPSHOT_MAX_AGE):
        findings.append(
            _finding(
                "ebs_snapshot_freshness",
                "latest completed EBS snapshot is missing, stale, or future-dated",
            )
        )

    if not isinstance(telemetry, dict):
        findings.append(
            _finding("telemetry_archive_probe", "telemetry archive enablement signal is missing")
        )
    elif telemetry.get("probe_ok") is not True:
        findings.append(
            _finding("telemetry_archive_probe", "telemetry archive health probe failed")
        )
    elif telemetry.get("enabled") is False:
        pass
    elif telemetry.get("enabled") is not True:
        findings.append(
            _finding("telemetry_archive_probe", "telemetry archive enablement signal is invalid")
        )
    else:
        telemetry_success = _timestamp(telemetry.get("last_success_at"))
        telemetry_error = _timestamp(telemetry.get("last_error_at"))
        if telemetry_error is not None and (
            telemetry_success is None or telemetry_error > telemetry_success
        ):
            findings.append(
                _finding(
                    "telemetry_archive_error",
                    "telemetry archive has failed since its last clean heartbeat",
                )
            )
        if not _fresh(telemetry_success, now, TELEMETRY_HEARTBEAT_MAX_AGE):
            findings.append(
                _finding(
                    "telemetry_archive_heartbeat",
                    "telemetry archive clean heartbeat is missing, stale, or future-dated",
                )
            )
        telemetry_result = telemetry.get("last_result")
        if not isinstance(telemetry_result, dict):
            findings.append(
                _finding("telemetry_archive_stats", "telemetry archive stats are missing or invalid")
            )
        else:
            dropped = telemetry_result.get("dropped")
            failed = telemetry_result.get("failed")
            if (
                not isinstance(dropped, int)
                or isinstance(dropped, bool)
                or dropped < 0
                or not isinstance(failed, int)
                or isinstance(failed, bool)
                or failed < 0
            ):
                findings.append(
                    _finding("telemetry_archive_stats", "telemetry archive loss counters are invalid")
                )
            elif dropped > 0 or failed > 0:
                findings.append(
                    _finding(
                        "telemetry_archive_loss",
                        f"telemetry archive lost records: dropped={dropped}, failed={failed}",
                    )
                )

    ledgers = archive.get("ledgers") if isinstance(archive, dict) else None
    evidence_errors = (
        archive.get("evidence_errors") if isinstance(archive, dict) else None
    )
    closeout_complete = archive.get("closeout_complete") is True if isinstance(archive, dict) else False
    tail_export_complete = (
        archive.get("tail_export_complete") is True if isinstance(archive, dict) else False
    )
    if evidence_errors:
        valid_errors = (
            sorted({str(item) for item in evidence_errors})
            if isinstance(evidence_errors, list)
            else ["invalid evidence error signal"]
        )
        findings.append(
            _finding(
                "archive_evidence",
                "archive evidence failed validation: " + ", ".join(valid_errors),
            )
        )
    elif closeout_complete and tail_export_complete:
        if archive.get("cleanup_release_complete") is not True:
            findings.append(
                _finding(
                    "archive_evidence",
                    "cleanup hold release evidence is missing or invalid",
                )
            )
    elif closeout_complete and not tail_export_complete:
        findings.append(
            _finding(
                "archive_lag",
                "post-legacy tail export is incomplete for one or both ops tables",
            )
        )
    else:
        expected_tables = {"ops_error_logs", "ops_system_logs"}
        ledger_tables = {
            ledger.get("table") for ledger in ledgers if isinstance(ledger, dict)
        } if isinstance(ledgers, list) else set()
        if (
            not isinstance(ledgers, list)
            or len(ledgers) != 2
            or ledger_tables != expected_tables
        ):
            findings.append(
                _finding(
                    "archive_lag",
                    "archive ledger coverage is missing for one or both ops tables",
                )
            )
        else:
            for ledger in ledgers:
                if not isinstance(ledger, dict):
                    findings.append(_finding("archive_lag", "archive ledger signal is invalid"))
                    continue
                table = str(ledger.get("table") or "unknown")
                if ledger.get("more_cold_rows_remaining") is not False:
                    findings.append(
                        _finding("archive_lag", f"{table} legacy export is incomplete")
                    )

    hold_started = _timestamp(archive.get("hold_started_at")) if isinstance(archive, dict) else None
    if hold_started is not None and not closeout_complete and now - hold_started > HOLD_MAX_AGE:
        findings.append(
            _finding(
                "cleanup_hold_stale",
                "cleanup hold is older than 14d without complete archive closeout",
                severity="warning",
            )
        )

    restore_times = archive.get("restore_verified_at") if isinstance(archive, dict) else None
    parsed_restore = [
        stamp for stamp in (_timestamp(value) for value in (restore_times or [])) if stamp
    ]
    if len(parsed_restore) != 2 or any(
        not _fresh(stamp, now, RESTORE_MAX_AGE) for stamp in parsed_restore
    ):
        findings.append(
            _finding(
                "archive_restore_proof",
                "long-term archive restore proof is missing, stale, or future-dated for one or both ops tables",
            )
        )

    return {
        "verdict": "green" if not findings else "unsafe",
        "checked_at": now.isoformat().replace("+00:00", "Z"),
        "findings": findings,
        "summary": "all data-layer protection gates passed" if not findings else f"{len(findings)} protection gate(s) failed",
    }


def parse_signals(text: str) -> dict[str, Any]:
    signals: dict[str, Any] = {}
    for line in text.splitlines():
        line = line.strip()
        for tag in (
            "PARTITIONSTATS",
            "BACKUPSTATS",
            "SNAPSHOTSTATS",
            "ARCHIVESTATS",
            "TELEMETRYSTATS",
        ):
            if not line.startswith(tag + " "):
                continue
            try:
                value = json.loads(line[len(tag) + 1 :])
            except json.JSONDecodeError:
                continue
            if isinstance(value, dict):
                signals[tag] = value
    return signals


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args()
    print(json.dumps(compute_verdict(parse_signals(sys.stdin.read())), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
