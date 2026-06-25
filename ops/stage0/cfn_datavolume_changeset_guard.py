#!/usr/bin/env python3
"""Guard a CloudFormation change set for prod DataVolume-only reconciliation.

The Stage0 prod stack intentionally keeps some CFN drift (ImageTag / AMI) out of
routine updates because those fields can replace the EC2 instance. This guard is
the mechanical check for the narrow safe path: only the DataVolume gp3
properties may change, and CloudFormation must report Replacement=False.
"""

from __future__ import annotations

import argparse
import json
import sys
from typing import Any

ALLOWED_LOGICAL_ID = "DataVolume"
ALLOWED_RESOURCE_TYPE = "AWS::EC2::Volume"
ALLOWED_ACTION = "Modify"
ALLOWED_PROPERTY_NAMES = {"Size", "Iops", "Throughput"}
BLOCKED_LOGICAL_IDS = {"Instance", "EIPAssoc"}


def _resource_change(change: dict[str, Any]) -> dict[str, Any]:
    return change.get("ResourceChange") or {}


def validate_change_set(doc: dict[str, Any]) -> tuple[bool, list[str], list[dict[str, Any]]]:
    violations: list[str] = []
    summaries: list[dict[str, Any]] = []
    changes = doc.get("Changes") or []

    if not changes:
        return True, [], []

    for change in changes:
        rc = _resource_change(change)
        logical_id = rc.get("LogicalResourceId")
        resource_type = rc.get("ResourceType")
        action = rc.get("Action")
        replacement = str(rc.get("Replacement", ""))

        details = rc.get("Details") or []
        property_names: set[str] = set()
        requires_recreation: set[str] = set()
        for detail in details:
            target = detail.get("Target") or {}
            if target.get("Attribute") == "Properties" and target.get("Name"):
                property_names.add(str(target.get("Name")))
            if target.get("RequiresRecreation"):
                requires_recreation.add(str(target.get("RequiresRecreation")))

        summaries.append(
            {
                "logical_id": logical_id,
                "resource_type": resource_type,
                "action": action,
                "replacement": replacement,
                "properties": sorted(property_names),
                "requires_recreation": sorted(requires_recreation),
            }
        )

        if logical_id in BLOCKED_LOGICAL_IDS:
            violations.append(f"{logical_id}: blocked resource appears in change set")
            continue
        if logical_id != ALLOWED_LOGICAL_ID:
            violations.append(f"{logical_id}: only {ALLOWED_LOGICAL_ID} may change")
            continue
        if resource_type != ALLOWED_RESOURCE_TYPE:
            violations.append(f"{logical_id}: expected {ALLOWED_RESOURCE_TYPE}, got {resource_type}")
        if action != ALLOWED_ACTION:
            violations.append(f"{logical_id}: expected action {ALLOWED_ACTION}, got {action}")
        if replacement != "False":
            violations.append(f"{logical_id}: expected Replacement=False, got {replacement}")

        unknown = property_names - ALLOWED_PROPERTY_NAMES
        if unknown:
            violations.append(f"{logical_id}: unexpected property changes {sorted(unknown)}")
        if not property_names:
            violations.append(f"{logical_id}: no property-level details found")
        if "Always" in requires_recreation:
            violations.append(f"{logical_id}: property requires recreation")

    return not violations, violations, summaries


def run_selftest() -> int:
    good = {
        "Changes": [
            {
                "ResourceChange": {
                    "Action": "Modify",
                    "LogicalResourceId": "DataVolume",
                    "ResourceType": "AWS::EC2::Volume",
                    "Replacement": "False",
                    "Details": [
                        {
                            "Target": {
                                "Attribute": "Properties",
                                "Name": "Size",
                                "RequiresRecreation": "Never",
                            }
                        },
                        {
                            "Target": {
                                "Attribute": "Properties",
                                "Name": "Iops",
                                "RequiresRecreation": "Never",
                            }
                        },
                        {
                            "Target": {
                                "Attribute": "Properties",
                                "Name": "Throughput",
                                "RequiresRecreation": "Never",
                            }
                        },
                    ],
                }
            }
        ]
    }
    bad_instance = {
        "Changes": [
            {
                "ResourceChange": {
                    "Action": "Modify",
                    "LogicalResourceId": "Instance",
                    "ResourceType": "AWS::EC2::Instance",
                    "Replacement": "True",
                    "Details": [],
                }
            }
        ]
    }
    bad_volume_replace = {
        "Changes": [
            {
                "ResourceChange": {
                    "Action": "Modify",
                    "LogicalResourceId": "DataVolume",
                    "ResourceType": "AWS::EC2::Volume",
                    "Replacement": "True",
                    "Details": [
                        {
                            "Target": {
                                "Attribute": "Properties",
                                "Name": "AvailabilityZone",
                                "RequiresRecreation": "Always",
                            }
                        }
                    ],
                }
            }
        ]
    }

    cases = [
        ("good", good, True),
        ("blocked instance", bad_instance, False),
        ("volume replacement", bad_volume_replace, False),
        ("empty", {"Changes": []}, True),
    ]
    failed = 0
    for name, doc, want in cases:
        got, violations, _ = validate_change_set(doc)
        if got != want:
            failed += 1
            print(f"SELFTEST FAIL {name}: got={got} want={want} violations={violations}", file=sys.stderr)
    if failed:
        return 1
    print(f"cfn_datavolume_changeset_guard selftest: PASS ({len(cases)} cases)")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--selftest", action="store_true", help="run built-in fixtures")
    parser.add_argument(
        "--allowed-properties",
        default="Size,Iops,Throughput",
        help="comma-separated DataVolume properties allowed in this change set",
    )
    args = parser.parse_args()

    if args.selftest:
        return run_selftest()

    global ALLOWED_PROPERTY_NAMES
    ALLOWED_PROPERTY_NAMES = {p.strip() for p in args.allowed_properties.split(",") if p.strip()}

    try:
        doc = json.load(sys.stdin)
    except json.JSONDecodeError as exc:
        print(f"invalid change set JSON: {exc}", file=sys.stderr)
        return 2

    ok, violations, summaries = validate_change_set(doc)
    out = {"ok": ok, "changes": summaries, "violations": violations}
    print(json.dumps(out, indent=2, sort_keys=True))
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
