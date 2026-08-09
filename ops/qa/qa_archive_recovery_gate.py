#!/usr/bin/env python3
"""Validate independent recovery evidence and plan break-glass retirement without deleting it."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any


REQUIRED_COMMANDS = ("inspect", "verify", "restore")


def evaluate(evidence_path: Path) -> tuple[int, dict[str, Any]]:
    result: dict[str, Any] = {
        "ok": False,
        "planned_transition_authorized": False,
        "production_success_claimed": False,
        "script_action": "preserve",
        "break_glass_path": "ops/prod/fetch-qa-dump.sh",
    }
    try:
        evidence = json.loads(evidence_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        result["error"] = f"recovery evidence unavailable or invalid: {exc}"
        return 2, result

    failures: list[str] = []
    if evidence.get("schema_version") != 1:
        failures.append("schema_version")
    scope = evidence.get("scope")
    if scope not in {"synthetic", "production"}:
        failures.append("scope")
    if evidence.get("source") != "ops-workstation-s3":
        failures.append("source")
    for command in REQUIRED_COMMANDS:
        receipt = evidence.get(command)
        if not isinstance(receipt, dict) or receipt.get("ok") is not True or receipt.get("verified") is not True:
            failures.append(f"{command}.verified")
            continue
        if receipt.get("database_accessed") is not False:
            failures.append(f"{command}.database_accessed")
        if receipt.get("source") != "ops-workstation-s3":
            failures.append(f"{command}.source")
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

    result.update({
        "ok": True,
        "planned_transition_authorized": True,
        "script_action": "planned_removal_only",
        "note": "synthetic evidence validates the repository transition shape only; production evidence and approval remain required",
    })
    if scope == "production":
        result["production_evidence_validated"] = True
        result["production_success_claimed"] = True
    return 0, result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    plan = subparsers.add_parser("plan-retirement")
    plan.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    code, result = evaluate(args.evidence)
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return code


if __name__ == "__main__":
    raise SystemExit(main())
