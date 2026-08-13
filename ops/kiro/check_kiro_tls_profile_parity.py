#!/usr/bin/env python3
"""Compare the captured Kiro TLS canonical profile with a read-only DB snapshot.

This proves production-configured parity only. It does not observe the deployed
ClientHello and never writes the database.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

import capture_kiro_fingerprint as capture

DEFAULT_CANONICAL = capture.KIRO_TLS_PROFILE_JSON


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def extract_snapshot_row(snapshot: Any) -> dict[str, Any] | None:
    """Accept a bare row or run-probe stdout containing one JSON row."""
    if snapshot is None:
        return None
    if isinstance(snapshot, dict):
        if snapshot.get("status") == "missing" and "row" not in snapshot:
            return None
        if "row" in snapshot:
            return extract_snapshot_row(snapshot["row"])
        return snapshot
    if isinstance(snapshot, list):
        rows = [extract_snapshot_row(item) for item in snapshot]
        rows = [row for row in rows if row is not None]
        if len(rows) > 1:
            raise ValueError("snapshot contains more than one canonical row")
        return rows[0] if rows else None
    if isinstance(snapshot, str):
        rows: list[dict[str, Any]] = []
        for line in snapshot.splitlines():
            line = line.strip()
            if not line:
                continue
            parsed = json.loads(line)
            row = extract_snapshot_row(parsed)
            if row is not None:
                rows.append(row)
        if len(rows) > 1:
            raise ValueError("snapshot contains more than one canonical row")
        return rows[0] if rows else None
    raise ValueError("snapshot must be a JSON object, array, or JSON-lines string")


def compare_profiles(canonical: dict[str, Any], row: dict[str, Any] | None) -> dict[str, Any]:
    canonical_projection = capture.runtime_profile_projection(canonical)
    canonical_digest = capture.runtime_profile_digest(canonical)
    if row is None:
        return {
            "schema_version": 1,
            "evidence_level": "production_configured",
            "status": "missing",
            "profile_name": capture.KIRO_PROFILE_NAME,
            "canonical_digest": canonical_digest,
            "database_digest": None,
            "field_diffs": {},
        }
    if str(row.get("name") or "") != capture.KIRO_PROFILE_NAME:
        raise ValueError(f"snapshot row name must be {capture.KIRO_PROFILE_NAME}")
    database_projection = capture.runtime_profile_projection(row)
    database_digest = capture.runtime_profile_digest(row)
    field_diffs = {
        field: {"canonical": canonical_projection[field], "database": database_projection[field]}
        for field in capture.RUNTIME_PROFILE_FIELDS
        if canonical_projection[field] != database_projection[field]
    }
    return {
        "schema_version": 1,
        "evidence_level": "production_configured",
        "status": "healthy" if not field_diffs else "drift",
        "profile_name": capture.KIRO_PROFILE_NAME,
        "canonical_digest": canonical_digest,
        "database_digest": database_digest,
        "field_diffs": field_diffs,
    }


def observer_failure(message: str) -> dict[str, Any]:
    return {
        "schema_version": 1,
        "evidence_level": "production_configured",
        "status": "observer_failed",
        "profile_name": capture.KIRO_PROFILE_NAME,
        "error": message,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--canonical", type=Path, default=DEFAULT_CANONICAL)
    parser.add_argument("--snapshot", type=Path)
    parser.add_argument("--snapshot-stdin", action="store_true")
    parser.add_argument("--report-json", type=Path)
    args = parser.parse_args(argv)

    try:
        canonical = load_json(args.canonical)
        if args.snapshot and args.snapshot_stdin:
            raise ValueError("choose only one of --snapshot or --snapshot-stdin")
        if args.snapshot:
            raw_snapshot: Any = args.snapshot.read_text(encoding="utf-8")
        elif args.snapshot_stdin:
            raw_snapshot = sys.stdin.read()
        else:
            result = {
                "schema_version": 1,
                "evidence_level": "capture_baseline",
                "status": "healthy",
                "profile_name": capture.KIRO_PROFILE_NAME,
                "canonical_digest": capture.runtime_profile_digest(canonical),
                "runtime_projection": capture.runtime_profile_projection(canonical),
            }
            raw_snapshot = None
        if args.snapshot or args.snapshot_stdin:
            result = compare_profiles(canonical, extract_snapshot_row(raw_snapshot))
    except (OSError, json.JSONDecodeError, ValueError, TypeError) as exc:
        result = observer_failure(str(exc))

    rendered = json.dumps(result, ensure_ascii=False, indent=2) + "\n"
    if args.report_json:
        args.report_json.parent.mkdir(parents=True, exist_ok=True)
        args.report_json.write_text(rendered, encoding="utf-8")
    sys.stdout.write(rendered)
    return {"healthy": 0, "drift": 1, "missing": 1, "observer_failed": 2}.get(str(result.get("status")), 2)


if __name__ == "__main__":
    raise SystemExit(main())
