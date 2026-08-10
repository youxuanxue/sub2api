#!/usr/bin/env python3
"""Fail-closed plan/apply gate for retiring the migrated Lightsail fleet."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import subprocess
import sys
import tempfile
from typing import Any, Protocol


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
LIGHTSAIL_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"
EC2_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
EDGE_ORDER = ("us5", "us4", "us6", "us3")
CONFIRMATION = "retire-lightsail-us3-us4-us5-us6-after-one-day"
MAX_SNAPSHOT_AGE = dt.timedelta(minutes=15)
MIN_FLEET_OBSERVATION = dt.timedelta(days=1)
_MISSING = object()
AWS_CALLS = {
    "detach_static_ip": {
        "service": "lightsail",
        "command": "detach-static-ip",
        "iam_action": "lightsail:DetachStaticIp",
    },
    "delete_instance": {
        "service": "lightsail",
        "command": "delete-instance",
        "iam_action": "lightsail:DeleteInstance",
    },
    "release_static_ip": {
        "service": "lightsail",
        "command": "release-static-ip",
        "iam_action": "lightsail:ReleaseStaticIp",
    },
    "deregister_managed_instance": {
        "service": "ssm",
        "command": "deregister-managed-instance",
        "iam_action": "ssm:DeregisterManagedInstance",
    },
}


class Runner(Protocol):
    def run(self, argv: list[str]) -> None: ...


class AwsRunner:
    def run(self, argv: list[str]) -> None:
        subprocess.run(argv, check=True)


def _parse_time(value: object) -> dt.datetime:
    if not isinstance(value, str) or not value:
        raise ValueError("timestamp is required")
    normalized = f"{value[:-1]}+00:00" if value.endswith("Z") else value
    parsed = dt.datetime.fromisoformat(normalized)
    if parsed.tzinfo is None:
        raise ValueError("timestamp timezone is required")
    return parsed.astimezone(dt.timezone.utc)


def _load_targets(path: pathlib.Path) -> dict[str, dict[str, Any]]:
    data = json.loads(path.read_text(encoding="utf-8"))
    targets = data.get("targets")
    if not isinstance(targets, dict):
        raise ValueError(f"invalid target matrix: {path}")
    return targets


def _path_get(data: object, path: str) -> object:
    value = data
    for part in path.split("."):
        if not isinstance(value, dict) or part not in value:
            return _MISSING
        value = value[part]
    return value


def _action(edge_id: str, name: str, args: list[str]) -> dict[str, Any]:
    call = AWS_CALLS[name]
    return {
        "edge_id": edge_id,
        "name": name,
        "argv": ["aws", call["service"], call["command"], *args],
        "status": "planned",
    }


def build_retirement_plan(
    snapshot: dict[str, Any],
    now: dt.datetime,
) -> dict[str, Any]:
    """Evaluate a freshly collected snapshot and build exact AWS calls."""
    if now.tzinfo is None:
        raise ValueError("now must be timezone-aware")
    now = now.astimezone(dt.timezone.utc)

    blockers: list[str] = []
    checks: list[dict[str, Any]] = []

    def block(code: str) -> None:
        if code not in blockers:
            blockers.append(code)

    def check(code: str, ok: bool, actual: object = None) -> None:
        checks.append({"name": code, "ok": bool(ok), "actual": actual})
        if not ok:
            block(code)

    def required(path: str) -> object:
        value = _path_get(snapshot, path)
        if value is _MISSING or value is None or value == "":
            block(f"missing:{path}")
            return _MISSING
        return value

    schema_version = required("schema_version")
    if schema_version is not _MISSING:
        check("unsupported_schema_version", schema_version == 1, schema_version)

    generated_at = required("generated_at")
    if generated_at is not _MISSING:
        try:
            generated = _parse_time(generated_at)
            age = now - generated
            check("snapshot_from_future", age >= dt.timedelta(), int(age.total_seconds()))
            check(
                "snapshot_older_than_15m",
                age <= MAX_SNAPSHOT_AGE,
                int(age.total_seconds()),
            )
        except ValueError:
            block("invalid:generated_at")

    execution_commit = required("execution_commit")
    receipt_commit = required("final_cutover_receipt.commit")
    if execution_commit is not _MISSING and receipt_commit is not _MISSING:
        check(
            "cutover_receipt_commit_mismatch",
            execution_commit == receipt_commit,
            {"execution": execution_commit, "receipt": receipt_commit},
        )

    observation_started = required("fleet_observation_started_at")
    if observation_started is not _MISSING:
        try:
            elapsed = now - _parse_time(observation_started)
            check(
                "fleet_observation_under_1d",
                elapsed >= MIN_FLEET_OBSERVATION,
                int(elapsed.total_seconds()),
            )
        except ValueError:
            block("invalid:fleet_observation_started_at")

    cutover_completed = required("final_cutover_receipt.completed_at")
    if cutover_completed is not _MISSING:
        try:
            elapsed = now - _parse_time(cutover_completed)
            check(
                "final_cutover_under_1d",
                elapsed >= MIN_FLEET_OBSERVATION,
                int(elapsed.total_seconds()),
            )
        except ValueError:
            block("invalid:final_cutover_receipt.completed_at")

    unexpected = required("unexpected_resources")
    if unexpected is not _MISSING:
        if not isinstance(unexpected, list):
            block("invalid:unexpected_resources")
        else:
            check("unexpected_lightsail_resources", not unexpected, unexpected)

    edges = required("edges")
    if not isinstance(edges, dict):
        if edges is not _MISSING:
            block("invalid:edges")
        edges = {}
    for edge_id in sorted(set(edges) - set(EDGE_ORDER)):
        block(f"unexpected_target:{edge_id}")
    for edge_id in EDGE_ORDER:
        if edge_id not in edges:
            block(f"missing:edges.{edge_id}")

    lightsail_targets = _load_targets(LIGHTSAIL_MATRIX)
    ec2_targets = _load_targets(EC2_MATRIX)
    actions: list[dict[str, Any]] = []
    for edge_id in EDGE_ORDER:
        row = edges.get(edge_id)
        if not isinstance(row, dict):
            if row is not None:
                block(f"invalid:edges.{edge_id}")
            continue

        def row_value(path: str) -> object:
            value = _path_get(row, path)
            if value is _MISSING or value is None or value == "":
                block(f"missing:edges.{edge_id}.{path}")
                return _MISSING
            return value

        owner = row_value("owner")
        if owner is not _MISSING:
            check(f"owner_not_ec2:{edge_id}", owner == "ec2", owner)

        for path, code, expected in (
            ("ec2_healthy", f"ec2_unhealthy:{edge_id}", True),
            (
                "lightsail_deployable",
                f"lightsail_still_deployable:{edge_id}",
                False,
            ),
        ):
            value = row_value(path)
            if not isinstance(value, bool):
                if value is not _MISSING:
                    block(f"invalid:edges.{edge_id}.{path}")
            else:
                check(code, value is expected, value)

        eip = row_value("ec2_eip")
        dns = row_value("dns_ipv4")
        if eip is not _MISSING and dns is not _MISSING:
            check(f"dns_not_ec2:{edge_id}", dns == [eip], dns)

        schedulable = row_value("source_schedulable_accounts")
        if isinstance(schedulable, int) and not isinstance(schedulable, bool):
            check(f"source_accounts_schedulable:{edge_id}", schedulable == 0, schedulable)
        elif schedulable is not _MISSING:
            block(f"invalid:edges.{edge_id}.source_schedulable_accounts")

        backup_verified = row_value("logical_backup.verified")
        backup_key = row_value("logical_backup.s3_key")
        backup_checksum = row_value("logical_backup.checksum")
        backup_ok = (
            backup_verified is True
            and isinstance(backup_key, str)
            and bool(backup_key)
            and isinstance(backup_checksum, str)
            and bool(backup_checksum)
        )
        check(f"logical_backup_unverified:{edge_id}", backup_ok)

        snapshot_id = row_value("data_snapshot.snapshot_id")
        snapshot_state = row_value("data_snapshot.state")
        data_snapshot_ok = (
            isinstance(snapshot_id, str)
            and snapshot_id.startswith("snap-")
            and snapshot_state == "completed"
        )
        check(f"data_snapshot_incomplete:{edge_id}", data_snapshot_ok, snapshot_state)

        source = row.get("lightsail")
        if not isinstance(source, dict):
            block(f"invalid:edges.{edge_id}.lightsail")
            continue
        matrix_source = lightsail_targets.get(edge_id)
        matrix_target = ec2_targets.get(edge_id)
        if not isinstance(matrix_source, dict) or not isinstance(matrix_target, dict):
            block(f"target_missing_from_matrix:{edge_id}")
            continue

        identity_contract = {
            "region": matrix_source.get("lightsail_region"),
            "instance_name": matrix_source.get("instance_name"),
            "static_ip_name": matrix_source.get("static_ip_name"),
            "ssm_prefix": matrix_source.get("ssm_prefix"),
            "ec2_region": matrix_target.get("region"),
            "ec2_stack": matrix_target.get("stack"),
        }
        for field, expected in identity_contract.items():
            actual = row_value(f"lightsail.{field}")
            if actual is not _MISSING:
                check(
                    f"resource_mismatch:{edge_id}:{field}",
                    actual == expected,
                    actual,
                )

        managed_id = row_value("lightsail.managed_instance_id")
        if managed_id is not _MISSING:
            check(
                f"invalid_managed_instance_id:{edge_id}",
                isinstance(managed_id, str) and managed_id.startswith("mi-"),
                managed_id,
            )

        existence: dict[str, bool] = {}
        for field in (
            "instance_exists",
            "static_ip_exists",
            "static_ip_attached",
            "managed_instance_exists",
        ):
            value = row_value(f"lightsail.{field}")
            if isinstance(value, bool):
                existence[field] = value
            elif value is not _MISSING:
                block(f"invalid:edges.{edge_id}.lightsail.{field}")

        if existence.get("static_ip_attached") and not existence.get("static_ip_exists"):
            block(f"invalid_resource_state:{edge_id}:static_ip_attached_without_ip")

        region = source.get("region")
        instance_name = source.get("instance_name")
        static_ip_name = source.get("static_ip_name")
        ec2_region = source.get("ec2_region")
        if existence.get("static_ip_exists") and existence.get("static_ip_attached"):
            actions.append(_action(edge_id, "detach_static_ip", [
                "--region", str(region),
                "--static-ip-name", str(static_ip_name),
            ]))
        if existence.get("instance_exists"):
            actions.append(_action(edge_id, "delete_instance", [
                "--region", str(region),
                "--instance-name", str(instance_name),
            ]))
        if existence.get("static_ip_exists"):
            actions.append(_action(edge_id, "release_static_ip", [
                "--region", str(region),
                "--static-ip-name", str(static_ip_name),
            ]))
        if existence.get("managed_instance_exists"):
            actions.append(_action(edge_id, "deregister_managed_instance", [
                "--region", str(ec2_region),
                "--instance-id", str(managed_id),
            ]))

    if blockers:
        actions = []
    return {
        "targets": list(EDGE_ORDER),
        "checks": checks,
        "blockers": blockers,
        "actions": actions,
        "mode": "plan",
    }


def run_retirement(
    snapshot: dict[str, Any],
    *,
    apply: bool,
    confirm: str,
    runner: Runner,
    now: dt.datetime | None = None,
) -> dict[str, Any]:
    """Build the plan and optionally execute it in fail-fast order."""
    if apply and confirm != CONFIRMATION:
        raise ValueError(f"--apply requires exact confirmation: {CONFIRMATION}")
    observed_at = now or dt.datetime.now(dt.timezone.utc)
    result = build_retirement_plan(snapshot, observed_at)
    result["mode"] = "apply" if apply else "plan"
    if not apply or result["blockers"]:
        return result

    for action in result["actions"]:
        try:
            runner.run(action["argv"])
        except Exception as exc:
            action["status"] = "failed"
            action["error"] = str(exc)
            result["blockers"].append(
                f"apply_failed:{action['edge_id']}:{action['name']}"
            )
            break
        action["status"] = "applied"
    return result


def _write_json(path: pathlib.Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(
        mode="w",
        encoding="utf-8",
        dir=path.parent,
        prefix=f".{path.name}.",
        delete=False,
    ) as handle:
        json.dump(payload, handle, indent=2, sort_keys=True)
        handle.write("\n")
        temporary = pathlib.Path(handle.name)
    os.replace(temporary, path)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot", required=True, type=pathlib.Path)
    parser.add_argument("--output", type=pathlib.Path)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--confirm", default="")
    args = parser.parse_args(argv)

    try:
        snapshot = json.loads(args.snapshot.read_text(encoding="utf-8"))
        if not isinstance(snapshot, dict):
            raise ValueError("snapshot must be a JSON object")
        result = run_retirement(
            snapshot,
            apply=args.apply,
            confirm=args.confirm,
            runner=AwsRunner(),
        )
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 2

    rendered = json.dumps(result, indent=2, sort_keys=True)
    print(rendered)
    if args.output:
        _write_json(args.output, result)
    return 1 if result["blockers"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
