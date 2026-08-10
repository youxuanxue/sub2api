#!/usr/bin/env python3
"""Fail-closed plan/apply gate for retiring the migrated Lightsail fleet."""

from __future__ import annotations

import argparse
import copy
import datetime as dt
import importlib.util
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
AWS_READ_CALLS = {
    "caller_identity": ("sts", "get-caller-identity", "sts:GetCallerIdentity"),
    "lightsail_instance": ("lightsail", "get-instance", "lightsail:GetInstance"),
    "lightsail_static_ip": ("lightsail", "get-static-ip", "lightsail:GetStaticIp"),
    "ssm_parameter": ("ssm", "get-parameter", "ssm:GetParameter"),
    "ssm_inventory": (
        "ssm",
        "describe-instance-information",
        "ssm:DescribeInstanceInformation",
    ),
    "ssm_probe": ("ssm", "send-command", "ssm:SendCommand"),
    "ssm_probe_result": (
        "ssm",
        "get-command-invocation",
        "ssm:GetCommandInvocation",
    ),
    "stack": (
        "cloudformation",
        "describe-stacks",
        "cloudformation:DescribeStacks",
    ),
    "data_snapshot": ("ec2", "describe-snapshots", "ec2:DescribeSnapshots"),
    "alarms": ("cloudwatch", "describe-alarms", "cloudwatch:DescribeAlarms"),
    "alarm_history": (
        "cloudwatch",
        "describe-alarm-history",
        "cloudwatch:DescribeAlarmHistory",
    ),
}


class Runner(Protocol):
    def run(self, argv: list[str]) -> None: ...


class LiveCollector(Protocol):
    def collect(
        self,
        snapshot: dict[str, Any],
        now: dt.datetime,
    ) -> dict[str, Any]: ...


class AwsRunner:
    def run(self, argv: list[str]) -> None:
        subprocess.run(
            argv,
            check=True,
            env={**os.environ, "AWS_PAGER": ""},
        )


def _load_cutover_module():
    path = REPO_ROOT / "ops/migration/edge_platform_cutover_check.py"
    spec = importlib.util.spec_from_file_location("retirement_cutover_reads", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load live read owner: {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def _read_argv(name: str, *args: str) -> list[str]:
    service, command, _ = AWS_READ_CALLS[name]
    return [service, command, *args]


class DefaultLiveAdapter:
    def __init__(self) -> None:
        self.cutover = _load_cutover_module()

    def current_commit(self) -> str:
        return subprocess.check_output(
            ["git", "rev-parse", "HEAD"],
            cwd=REPO_ROOT,
            text=True,
        ).strip()

    def aws_json(self, args: list[str]) -> dict[str, Any]:
        return self.cutover._aws_json(args)

    def aws_optional_json(self, args: list[str]) -> dict[str, Any] | None:
        completed = subprocess.run(
            ["aws", *args, "--output", "json"],
            text=True,
            capture_output=True,
            check=False,
        )
        if completed.returncode != 0:
            error = completed.stderr.strip()
            if any(
                code in error
                for code in ("NotFoundException", "ParameterNotFound", "InvalidInstanceId")
            ):
                return None
            raise RuntimeError(f"aws {' '.join(args)} failed: {error}")
        value = json.loads(completed.stdout)
        if not isinstance(value, dict):
            raise RuntimeError(f"aws {' '.join(args)} returned non-object JSON")
        return value

    def ssm_status(self, region: str, instance_id: str) -> str:
        payload = self.aws_json(_read_argv(
            "ssm_inventory",
            "--region", region,
            "--filters", f"Key=InstanceIds,Values={instance_id}",
        ))
        rows = payload.get("InstanceInformationList")
        if not isinstance(rows, list) or len(rows) != 1 or not isinstance(rows[0], dict):
            return ""
        return str(rows[0].get("PingStatus") or "")

    def remote_probe(self, region: str, instance_id: str, since: str) -> dict[str, Any]:
        return self.cutover._run_remote_probe(region, instance_id, since)

    def stack_output(self, region: str, stack: str, key: str) -> str:
        return self.cutover._stack_output(region, stack, key)

    def authoritative_ipv4(self, domain: str) -> list[str]:
        return self.cutover._authoritative_ipv4(domain)

    def public_ipv4(self, domain: str) -> list[str]:
        return self.cutover._dig_ipv4(domain, "1.1.1.1")

    def public_health(self, domain: str, ipv4: str) -> bool:
        return self.cutover._public_health(domain, ipv4)


def _iso_utc(value: dt.datetime) -> str:
    return value.astimezone(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def _history_entered_alarm(item: object) -> bool:
    if not isinstance(item, dict):
        return True
    data = item.get("HistoryData")
    if isinstance(data, str) and data:
        try:
            parsed = json.loads(data)
        except json.JSONDecodeError:
            return True
        new_state = parsed.get("newState") if isinstance(parsed, dict) else None
        if isinstance(new_state, dict):
            return new_state.get("stateValue") == "ALARM"
    summary = str(item.get("HistorySummary") or "").lower()
    if not summary:
        return True
    return "to alarm" in summary or "进入 alarm" in summary


class AwsLiveCollector:
    def __init__(self, adapter: object | None = None):
        self.adapter = adapter or DefaultLiveAdapter()

    def collect(
        self,
        snapshot: dict[str, Any],
        now: dt.datetime,
    ) -> dict[str, Any]:
        if now.tzinfo is None:
            raise ValueError("now must be timezone-aware")
        now = now.astimezone(dt.timezone.utc)
        anchor_fields = (
            "schema_version",
            "execution_commit",
            "expected_aws_account_id",
            "final_cutover_receipt",
            "fleet_observation_started_at",
        )
        live = {
            field: copy.deepcopy(snapshot[field])
            for field in anchor_fields
            if field in snapshot
        }
        adapter = self.adapter

        identity = adapter.aws_json(_read_argv("caller_identity"))
        live["aws_account_id"] = str(identity.get("Account") or "")
        live["runtime_commit"] = adapter.current_commit()

        lightsail_targets = _load_targets(LIGHTSAIL_MATRIX)
        ec2_targets = _load_targets(EC2_MATRIX)
        observation_started = _parse_time(live.get("fleet_observation_started_at"))
        edges: dict[str, Any] = {}
        for edge_id in EDGE_ORDER:
            source = lightsail_targets[edge_id]
            target = ec2_targets[edge_id]
            source_region = str(source["lightsail_region"])
            target_region = str(target["region"])
            stack = str(target["stack"])
            instance_name = str(source["instance_name"])
            static_ip_name = str(source["static_ip_name"])

            instance_payload = adapter.aws_optional_json(_read_argv(
                "lightsail_instance",
                "--region", source_region,
                "--instance-name", instance_name,
            ))
            static_payload = adapter.aws_optional_json(_read_argv(
                "lightsail_static_ip",
                "--region", source_region,
                "--static-ip-name", static_ip_name,
            ))
            parameter = adapter.aws_optional_json(_read_argv(
                "ssm_parameter",
                "--region", str(source["ec2_equivalent_region"]),
                "--name", f"{source['ssm_prefix']}/ssm_managed_instance_id",
            ))
            managed_id = str(((parameter or {}).get("Parameter") or {}).get("Value") or "")
            managed_status = (
                adapter.ssm_status(str(source["ec2_equivalent_region"]), managed_id)
                if managed_id
                else ""
            )
            managed_exists = bool(managed_status)
            managed_online = managed_status == "Online"
            source_probe: dict[str, Any] = {}
            if managed_online:
                source_probe = adapter.remote_probe(
                    str(source["ec2_equivalent_region"]),
                    managed_id,
                    "15m",
                )

            target_instance = adapter.stack_output(target_region, stack, "InstanceId")
            target_ip = adapter.stack_output(target_region, stack, "PublicIP")
            data_volume = adapter.stack_output(target_region, stack, "DataVolumeId")
            target_online = adapter.ssm_status(target_region, target_instance) == "Online"
            target_probe = (
                adapter.remote_probe(target_region, target_instance, "15m")
                if target_online
                else {}
            )

            alarm_names = [
                adapter.stack_output(target_region, stack, key)
                for key in (
                    "InstanceCpuAlarmName",
                    "RootVolumeDiskAlarmName",
                    "DataVolumeDiskAlarmName",
                )
            ]
            alarm_payload = adapter.aws_json(_read_argv(
                "alarms", "--region", target_region, "--alarm-names", *alarm_names,
            ))
            alarm_rows = alarm_payload.get("MetricAlarms") or []
            current_alarm_state = {
                str(row.get("AlarmName")): str(row.get("StateValue"))
                for row in alarm_rows
                if isinstance(row, dict)
            }
            alarms_healthy = (
                set(current_alarm_state) == set(alarm_names)
                and all(current_alarm_state[name] == "OK" for name in alarm_names)
            )
            for alarm_name in alarm_names:
                history = adapter.aws_json(_read_argv(
                    "alarm_history",
                    "--region", target_region,
                    "--alarm-name", alarm_name,
                    "--history-item-type", "StateUpdate",
                    "--start-date", _iso_utc(observation_started),
                ))
                if any(
                    _history_entered_alarm(item)
                    for item in (history.get("AlarmHistoryItems") or [])
                ):
                    alarms_healthy = False
                if history.get("NextToken"):
                    alarms_healthy = False

            snapshot_payload = adapter.aws_json(_read_argv(
                "data_snapshot",
                "--region", target_region,
                "--owner-ids", "self",
                "--filters", f"Name=volume-id,Values={data_volume}",
                "Name=status,Values=completed",
            ))
            completed_snapshots = [
                row for row in (snapshot_payload.get("Snapshots") or [])
                if isinstance(row, dict)
                and row.get("State") == "completed"
                and row.get("SnapshotId")
                and row.get("StartTime")
            ]
            latest_snapshot = max(
                completed_snapshots,
                key=lambda row: str(row["StartTime"]),
                default={},
            )

            authoritative = adapter.authoritative_ipv4(str(target["domain"]))
            public = adapter.public_ipv4(str(target["domain"]))
            target_public_health = adapter.public_health(str(target["domain"]), target_ip)
            ec2_healthy = all((
                target_online,
                target_probe.get("docker_healthy") is True,
                target_probe.get("health_ok") is True,
                target_public_health,
                alarms_healthy,
            ))
            static_row = (static_payload or {}).get("staticIp") or {}
            reports_attached = bool(static_row.get("isAttached"))
            attached_to = str(static_row.get("attachedTo") or "")
            static_attached = reports_attached and attached_to == instance_name
            static_attachment_safe = (
                (not reports_attached and not attached_to) or static_attached
            )

            edges[edge_id] = {
                "owner": (
                    "ec2"
                    if target.get("deployable") is True
                    and source.get("deployable") is False
                    else "invalid"
                ),
                "lightsail_deployable": source.get("deployable") is True,
                "ec2_deployable": target.get("deployable") is True,
                "ec2_healthy": ec2_healthy,
                "ec2_health_window_ok": alarms_healthy,
                "ec2_eip": target_ip,
                "dns_ipv4": public,
                "dns_authoritative_ipv4": authoritative,
                "dns_public_ipv4": public,
                "source_schedulable_accounts": (
                    source_probe.get("schedulable_accounts")
                    if managed_exists
                    else (0 if instance_payload is None else None)
                ),
                "logical_backup": target_probe.get("logical_backup") or {
                    "verified": False,
                },
                "data_snapshot": {
                    "snapshot_id": latest_snapshot.get("SnapshotId"),
                    "state": latest_snapshot.get("State"),
                    "start_time": latest_snapshot.get("StartTime"),
                },
                "lightsail": {
                    "region": source_region,
                    "instance_name": instance_name,
                    "instance_exists": instance_payload is not None,
                    "static_ip_name": static_ip_name,
                    "static_ip_exists": static_payload is not None,
                    "static_ip_attached": static_attached,
                    "static_ip_attachment_safe": static_attachment_safe,
                    "managed_instance_id": managed_id or None,
                    "managed_instance_exists": managed_exists,
                    "ssm_prefix": source["ssm_prefix"],
                    "ec2_region": target_region,
                    "ec2_stack": stack,
                },
            }
        live["edges"] = edges
        live["generated_at"] = _iso_utc(now)
        return live


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
    runtime_commit = required("runtime_commit")
    receipt_commit = required("final_cutover_receipt.commit")
    if execution_commit is not _MISSING and receipt_commit is not _MISSING:
        check(
            "cutover_receipt_commit_mismatch",
            execution_commit == receipt_commit,
            {"execution": execution_commit, "receipt": receipt_commit},
        )
    if execution_commit is not _MISSING and runtime_commit is not _MISSING:
        check(
            "runtime_commit_mismatch",
            execution_commit == runtime_commit,
            {"execution": execution_commit, "runtime": runtime_commit},
        )

    expected_account = required("expected_aws_account_id")
    actual_account = required("aws_account_id")
    if expected_account is not _MISSING and actual_account is not _MISSING:
        valid_accounts = all(
            isinstance(value, str) and len(value) == 12 and value.isdigit()
            for value in (expected_account, actual_account)
        )
        if not valid_accounts:
            block("invalid:aws_account_id")
        else:
            check(
                "aws_account_mismatch",
                expected_account == actual_account,
                {"expected": expected_account, "actual": actual_account},
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

    cutover_completed_at: dt.datetime | None = None
    cutover_completed = required("final_cutover_receipt.completed_at")
    if cutover_completed is not _MISSING:
        try:
            cutover_completed_at = _parse_time(cutover_completed)
            elapsed = now - cutover_completed_at
            check(
                "final_cutover_under_1d",
                elapsed >= MIN_FLEET_OBSERVATION,
                int(elapsed.total_seconds()),
            )
        except ValueError:
            block("invalid:final_cutover_receipt.completed_at")

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
            ("ec2_deployable", f"ec2_not_deployable:{edge_id}", True),
            (
                "ec2_health_window_ok",
                f"ec2_health_window_failed:{edge_id}",
                True,
            ),
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
        dns_values = {
            "combined": row_value("dns_ipv4"),
            "authoritative": row_value("dns_authoritative_ipv4"),
            "public": row_value("dns_public_ipv4"),
        }
        if eip is not _MISSING and all(value is not _MISSING for value in dns_values.values()):
            check(
                f"dns_not_ec2:{edge_id}",
                all(value == [eip] for value in dns_values.values()),
                dns_values,
            )

        schedulable = row_value("source_schedulable_accounts")
        if isinstance(schedulable, int) and not isinstance(schedulable, bool):
            check(f"source_accounts_schedulable:{edge_id}", schedulable == 0, schedulable)
        elif schedulable is not _MISSING:
            block(f"invalid:edges.{edge_id}.source_schedulable_accounts")

        backup_verified = row_value("logical_backup.verified")
        backup_path = row_value("logical_backup.path")
        backup_size = row_value("logical_backup.size_bytes")
        backup_checksum = row_value("logical_backup.checksum")
        backup_ok = (
            backup_verified is True
            and isinstance(backup_path, str)
            and backup_path.startswith("/var/lib/tokenkey/pgdump/tokenkey")
            and isinstance(backup_size, int)
            and not isinstance(backup_size, bool)
            and backup_size >= 2048
            and isinstance(backup_checksum, str)
            and backup_checksum.startswith("sha256:")
            and len(backup_checksum) == len("sha256:") + 64
            and all(char in "0123456789abcdef" for char in backup_checksum[7:])
        )
        check(f"logical_backup_unverified:{edge_id}", backup_ok)

        snapshot_id = row_value("data_snapshot.snapshot_id")
        snapshot_state = row_value("data_snapshot.state")
        snapshot_started = row_value("data_snapshot.start_time")
        data_snapshot_ok = (
            isinstance(snapshot_id, str)
            and snapshot_id.startswith("snap-")
            and snapshot_state == "completed"
        )
        check(f"data_snapshot_incomplete:{edge_id}", data_snapshot_ok, snapshot_state)
        if snapshot_started is not _MISSING:
            try:
                started_at = _parse_time(snapshot_started)
                check(
                    f"data_snapshot_before_cutover:{edge_id}",
                    cutover_completed_at is not None and started_at >= cutover_completed_at,
                    snapshot_started,
                )
            except ValueError:
                block(f"invalid:edges.{edge_id}.data_snapshot.start_time")

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

        managed_id = _path_get(row, "lightsail.managed_instance_id")

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

        managed_id_missing = managed_id is _MISSING or managed_id is None or managed_id == ""
        if existence.get("managed_instance_exists") and managed_id_missing:
            block(f"missing:edges.{edge_id}.lightsail.managed_instance_id")
        elif not managed_id_missing:
            check(
                f"invalid_managed_instance_id:{edge_id}",
                isinstance(managed_id, str) and managed_id.startswith("mi-"),
                managed_id,
            )

        attachment_safe = row_value("lightsail.static_ip_attachment_safe")
        if isinstance(attachment_safe, bool):
            check(
                f"static_ip_attached_elsewhere:{edge_id}",
                attachment_safe,
                attachment_safe,
            )
        elif attachment_safe is not _MISSING:
            block(f"invalid:edges.{edge_id}.lightsail.static_ip_attachment_safe")

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
        "observed_at": _iso_utc(now),
        "execution_commit": execution_commit if execution_commit is not _MISSING else None,
        "runtime_commit": runtime_commit if runtime_commit is not _MISSING else None,
        "aws_account_id": actual_account if actual_account is not _MISSING else None,
        "fleet_observation_started_at": (
            observation_started if observation_started is not _MISSING else None
        ),
    }


def run_retirement(
    snapshot: dict[str, Any],
    *,
    apply: bool,
    confirm: str,
    runner: Runner,
    collector: LiveCollector | None = None,
    now: dt.datetime | None = None,
) -> dict[str, Any]:
    """Build the plan and optionally execute it in fail-fast order."""
    if apply and confirm != CONFIRMATION:
        raise ValueError(f"--apply requires exact confirmation: {CONFIRMATION}")
    input_snapshot = snapshot
    collection_started_at = now or dt.datetime.now(dt.timezone.utc)
    if collector is not None:
        snapshot = collector.collect(input_snapshot, collection_started_at)
    elif apply:
        raise ValueError("--apply requires live revalidation")
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

    verification_started_at = now or dt.datetime.now(dt.timezone.utc)
    verified_snapshot = collector.collect(input_snapshot, verification_started_at)
    verification = build_retirement_plan(
        verified_snapshot,
        now or dt.datetime.now(dt.timezone.utc),
    )
    remaining_actions = [
        {"edge_id": action["edge_id"], "name": action["name"]}
        for action in verification["actions"]
    ]
    result["post_apply"] = {
        "observed_at": verification["observed_at"],
        "blockers": verification["blockers"],
        "remaining_actions": remaining_actions,
    }
    if verification["blockers"]:
        result["blockers"].append("post_apply_revalidation_failed")
    if remaining_actions:
        result["blockers"].append("retirement_incomplete")
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
            collector=AwsLiveCollector(),
        )
    except (
        OSError,
        RuntimeError,
        subprocess.SubprocessError,
        json.JSONDecodeError,
        ValueError,
    ) as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 2

    rendered = json.dumps(result, indent=2, sort_keys=True)
    print(rendered)
    if args.output:
        _write_json(args.output, result)
    return 1 if result["blockers"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
