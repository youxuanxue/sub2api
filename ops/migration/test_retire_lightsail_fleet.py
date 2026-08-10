#!/usr/bin/env python3
"""Behavior tests for the gated Lightsail fleet retirement tool."""

from __future__ import annotations

import copy
import datetime as dt
import importlib.util
import json
import pathlib
import sys
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
MODULE_PATH = REPO_ROOT / "ops/migration/retire_lightsail_fleet.py"
SPEC = importlib.util.spec_from_file_location("retire_lightsail_fleet", MODULE_PATH)
assert SPEC and SPEC.loader
RETIRE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = RETIRE
SPEC.loader.exec_module(RETIRE)

NOW = dt.datetime(2026, 8, 10, 12, 0, tzinfo=dt.timezone.utc)


def healthy_snapshot() -> dict:
    lightsail = json.loads(
        (REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json").read_text(
            encoding="utf-8",
        ),
    )["targets"]
    ec2 = json.loads(
        (REPO_ROOT / "deploy/aws/stage0/edge-targets.json").read_text(encoding="utf-8"),
    )["targets"]
    edges = {}
    for index, edge_id in enumerate(RETIRE.EDGE_ORDER, start=1):
        source = lightsail[edge_id]
        target = ec2[edge_id]
        target_ip = f"203.0.113.{index + 10}"
        edges[edge_id] = {
            "owner": "ec2",
            "lightsail_deployable": False,
            "ec2_deployable": True,
            "ec2_healthy": True,
            "ec2_health_window_ok": True,
            "ec2_eip": target_ip,
            "dns_ipv4": [target_ip],
            "dns_authoritative_ipv4": [target_ip],
            "dns_public_ipv4": [target_ip],
            "source_schedulable_accounts": 0,
            "logical_backup": {
                "verified": True,
                "path": f"/var/lib/tokenkey/pgdump/tokenkey-{edge_id}.sql.gz",
                "size_bytes": 4096,
                "checksum": "sha256:" + f"{index:x}" * 64,
            },
            "data_snapshot": {
                "snapshot_id": f"snap-{index:017x}",
                "state": "completed",
                "start_time": "2026-08-10T11:00:00Z",
            },
            "lightsail": {
                "region": source["lightsail_region"],
                "instance_name": source["instance_name"],
                "instance_exists": True,
                "static_ip_name": source["static_ip_name"],
                "static_ip_exists": True,
                "static_ip_attached": True,
                "static_ip_attachment_safe": True,
                "managed_instance_id": f"mi-{index:017x}",
                "managed_instance_exists": True,
                "ssm_prefix": source["ssm_prefix"],
                "ec2_region": target["region"],
                "ec2_stack": target["stack"],
            },
        }
    return {
        "schema_version": 1,
        "generated_at": "2026-08-10T11:55:00Z",
        "execution_commit": "0123456789abcdef",
        "runtime_commit": "0123456789abcdef",
        "expected_aws_account_id": "123456789012",
        "aws_account_id": "123456789012",
        "final_cutover_receipt": {
            "commit": "0123456789abcdef",
            "completed_at": "2026-08-09T10:00:00Z",
        },
        "fleet_observation_started_at": "2026-08-09T10:00:00Z",
        "edges": edges,
    }


class FakeRunner:
    def __init__(self, fail_at: int | None = None):
        self.calls: list[list[str]] = []
        self.fail_at = fail_at

    def run(self, argv: list[str]) -> None:
        self.calls.append(list(argv))
        if self.fail_at is not None and len(self.calls) == self.fail_at:
            raise RuntimeError("injected AWS failure")


class FakeCollector:
    def __init__(
        self,
        replacement: dict | None = None,
        *,
        retired_on_second_collect: bool = False,
    ):
        self.replacement = replacement
        self.retired_on_second_collect = retired_on_second_collect
        self.calls: list[tuple[dict, dt.datetime]] = []

    def collect(self, snapshot: dict, now: dt.datetime) -> dict:
        self.calls.append((copy.deepcopy(snapshot), now))
        collected = copy.deepcopy(self.replacement or snapshot)
        if self.retired_on_second_collect and len(self.calls) >= 2:
            for row in collected["edges"].values():
                row["source_schedulable_accounts"] = 0
                row["lightsail"].update(
                    instance_exists=False,
                    static_ip_exists=False,
                    static_ip_attached=False,
                    managed_instance_id=None,
                    managed_instance_exists=False,
                )
        return collected


class FakeLiveAdapter:
    def __init__(self, alarm_history_items: list[object] | None = None):
        self.aws_calls: list[list[str]] = []
        self.alarm_history_items = alarm_history_items or []

    def current_commit(self) -> str:
        return "0123456789abcdef"

    def aws_json(self, args: list[str]) -> dict:
        self.aws_calls.append(list(args))
        service, command = args[:2]
        if (service, command) == ("sts", "get-caller-identity"):
            return {"Account": "123456789012"}
        if (service, command) == ("ec2", "describe-snapshots"):
            return {
                "Snapshots": [{
                    "SnapshotId": "snap-00000000000000001",
                    "State": "completed",
                    "StartTime": "2026-08-10T11:00:00Z",
                }],
            }
        if (service, command) == ("cloudwatch", "describe-alarms"):
            return {
                "MetricAlarms": [
                    {"AlarmName": name, "StateValue": "OK"}
                    for name in ("cpu", "root-disk", "data-disk")
                ],
            }
        if (service, command) == ("cloudwatch", "describe-alarm-history"):
            return {"AlarmHistoryItems": self.alarm_history_items}
        raise AssertionError(args)

    def aws_optional_json(self, args: list[str]) -> dict | None:
        self.aws_calls.append(list(args))
        service, command = args[:2]
        edge_id = next(
            value.rsplit("-", 1)[-1]
            for value in args
            if value.startswith("tokenkey-edge-")
        ) if any(value.startswith("tokenkey-edge-") for value in args) else "us5"
        if (service, command) == ("lightsail", "get-instance"):
            return {"instance": {"name": args[-1]}}
        if (service, command) == ("lightsail", "get-static-ip"):
            targets = json.loads(
                (REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json").read_text(
                    encoding="utf-8",
                ),
            )["targets"]
            target = next(
                row for row in targets.values()
                if row["static_ip_name"] == args[-1]
            )
            return {
                "staticIp": {
                    "name": args[-1],
                    "isAttached": True,
                    "attachedTo": target["instance_name"],
                },
            }
        if (service, command) == ("ssm", "get-parameter"):
            return {"Parameter": {"Value": f"mi-{edge_id}"}}
        raise AssertionError(args)

    def ssm_status(self, region: str, instance_id: str) -> str:
        return "Online"

    def remote_probe(self, region: str, instance_id: str, since: str) -> dict:
        if instance_id.startswith("mi-"):
            return {"schedulable_accounts": 0}
        return {
            "docker_healthy": True,
            "health_ok": True,
            "logical_backup": {
                "verified": True,
                "path": "/var/lib/tokenkey/pgdump/tokenkey.sql.gz",
                "size_bytes": 4096,
                "checksum": "sha256:" + "a" * 64,
            },
        }

    def stack_output(self, region: str, stack: str, key: str) -> str:
        outputs = {
            "InstanceId": f"i-{stack[-3:]}",
            "PublicIP": "203.0.113.11",
            "DataVolumeId": f"vol-{stack[-3:]}",
            "InstanceCpuAlarmName": "cpu",
            "RootVolumeDiskAlarmName": "root-disk",
            "DataVolumeDiskAlarmName": "data-disk",
        }
        return outputs[key]

    def authoritative_ipv4(self, domain: str) -> list[str]:
        return ["203.0.113.11"]

    def public_ipv4(self, domain: str) -> list[str]:
        return ["203.0.113.11"]

    def public_health(self, domain: str, ipv4: str) -> bool:
        return True


class RetireLightsailFleetTests(unittest.TestCase):
    maxDiff = None

    def setUp(self) -> None:
        self.runner = FakeRunner()
        self.collector = FakeCollector(retired_on_second_collect=True)

    def test_plan_mode_never_calls_delete(self) -> None:
        result = RETIRE.run_retirement(
            healthy_snapshot(),
            apply=False,
            confirm="",
            runner=self.runner,
            now=NOW,
        )
        self.assertEqual([], result["blockers"])
        self.assertEqual([], self.runner.calls)
        self.assertTrue(result["actions"])

    def test_plan_can_recollect_live_state_without_deleting(self) -> None:
        result = RETIRE.run_retirement(
            healthy_snapshot(),
            apply=False,
            confirm="",
            runner=self.runner,
            collector=self.collector,
            now=NOW,
        )
        self.assertEqual([], result["blockers"])
        self.assertEqual(1, len(self.collector.calls))
        self.assertEqual([], self.runner.calls)

    def test_apply_requires_exact_confirmation(self) -> None:
        with self.assertRaisesRegex(ValueError, "exact confirmation"):
            RETIRE.run_retirement(
                healthy_snapshot(),
                apply=True,
                confirm="wrong",
                runner=self.runner,
                collector=self.collector,
                now=NOW,
            )
        self.assertEqual([], self.runner.calls)
        self.assertEqual([], self.collector.calls)

    def test_apply_requires_live_revalidation(self) -> None:
        with self.assertRaisesRegex(ValueError, "live revalidation"):
            RETIRE.run_retirement(
                healthy_snapshot(),
                apply=True,
                confirm=RETIRE.CONFIRMATION,
                runner=self.runner,
                collector=None,
                now=NOW,
            )
        self.assertEqual([], self.runner.calls)

    def test_apply_uses_recollected_state_instead_of_the_input_snapshot(self) -> None:
        live = healthy_snapshot()
        live["edges"]["us5"]["ec2_healthy"] = False
        collector = FakeCollector(live)
        result = RETIRE.run_retirement(
            healthy_snapshot(),
            apply=True,
            confirm=RETIRE.CONFIRMATION,
            runner=self.runner,
            collector=collector,
            now=NOW,
        )
        self.assertEqual(1, len(collector.calls))
        self.assertIn("ec2_unhealthy:us5", result["blockers"])
        self.assertEqual([], self.runner.calls)

    def assert_blocked(self, snapshot: dict, code: str) -> dict:
        result = RETIRE.run_retirement(
            snapshot,
            apply=False,
            confirm="",
            runner=self.runner,
            now=NOW,
        )
        self.assertIn(code, result["blockers"], result)
        self.assertEqual([], self.runner.calls)
        return result

    def test_observation_must_cover_one_full_day(self) -> None:
        snapshot = healthy_snapshot()
        snapshot["fleet_observation_started_at"] = "2026-08-09T12:00:01Z"
        self.assert_blocked(snapshot, "fleet_observation_under_1d")

    def test_snapshot_must_be_fresh_and_commit_bound(self) -> None:
        snapshot = healthy_snapshot()
        snapshot["generated_at"] = "2026-08-10T11:44:59Z"
        snapshot["final_cutover_receipt"]["commit"] = "different"
        result = self.assert_blocked(snapshot, "snapshot_older_than_15m")
        self.assertIn("cutover_receipt_commit_mismatch", result["blockers"])

    def test_runtime_commit_and_aws_account_must_match_the_receipt(self) -> None:
        snapshot = healthy_snapshot()
        snapshot.update(
            expected_aws_account_id="123456789012",
            aws_account_id="210987654321",
            runtime_commit="different",
        )
        result = self.assert_blocked(snapshot, "aws_account_mismatch")
        self.assertIn("runtime_commit_mismatch", result["blockers"])

    def test_aws_collector_rebuilds_all_delete_gates_from_live_reads(self) -> None:
        adapter = FakeLiveAdapter()
        snapshot = healthy_snapshot()
        snapshot["unexpected_resources"] = ["stale-input-must-not-survive"]
        collected = RETIRE.AwsLiveCollector(adapter).collect(snapshot, NOW)
        self.assertEqual("123456789012", collected["aws_account_id"])
        self.assertEqual("0123456789abcdef", collected["runtime_commit"])
        self.assertEqual("2026-08-10T12:00:00Z", collected["generated_at"])
        self.assertNotIn("unexpected_resources", collected)
        for edge_id in RETIRE.EDGE_ORDER:
            edge = collected["edges"][edge_id]
            self.assertTrue(edge["ec2_healthy"], edge_id)
            self.assertEqual(0, edge["source_schedulable_accounts"], edge_id)
            self.assertTrue(edge["logical_backup"]["verified"], edge_id)
            self.assertEqual("completed", edge["data_snapshot"]["state"], edge_id)
            self.assertTrue(edge["lightsail"]["static_ip_attachment_safe"], edge_id)

        commands = {(call[0], call[1]) for call in adapter.aws_calls}
        self.assertTrue({
            ("sts", "get-caller-identity"),
            ("lightsail", "get-instance"),
            ("lightsail", "get-static-ip"),
            ("ssm", "get-parameter"),
            ("ec2", "describe-snapshots"),
            ("cloudwatch", "describe-alarms"),
            ("cloudwatch", "describe-alarm-history"),
        } <= commands)

    def test_aws_collector_fails_closed_on_malformed_alarm_history(self) -> None:
        collected = RETIRE.AwsLiveCollector(FakeLiveAdapter([{}])).collect(
            healthy_snapshot(),
            NOW,
        )
        for edge_id in RETIRE.EDGE_ORDER:
            self.assertFalse(collected["edges"][edge_id]["ec2_health_window_ok"])

    def test_each_live_safety_gate_blocks_retirement(self) -> None:
        mutations = (
            ("ec2_unhealthy:us5", lambda row: row.update(ec2_healthy=False)),
            (
                "lightsail_still_deployable:us5",
                lambda row: row.update(lightsail_deployable=True),
            ),
            ("ec2_not_deployable:us5", lambda row: row.update(ec2_deployable=False)),
            (
                "ec2_health_window_failed:us5",
                lambda row: row.update(ec2_health_window_ok=False),
            ),
            ("dns_not_ec2:us5", lambda row: row.update(dns_ipv4=["198.51.100.8"])),
            (
                "source_accounts_schedulable:us5",
                lambda row: row.update(source_schedulable_accounts=1),
            ),
            (
                "logical_backup_unverified:us5",
                lambda row: row["logical_backup"].update(verified=False),
            ),
            (
                "data_snapshot_incomplete:us5",
                lambda row: row["data_snapshot"].update(state="pending"),
            ),
            (
                "data_snapshot_before_cutover:us5",
                lambda row: row["data_snapshot"].update(
                    start_time="2026-08-09T09:59:59Z",
                ),
            ),
        )
        for code, mutate in mutations:
            with self.subTest(code=code):
                snapshot = healthy_snapshot()
                mutate(snapshot["edges"]["us5"])
                self.assert_blocked(snapshot, code)

    def test_static_ip_attached_elsewhere_blocks_retirement(self) -> None:
        snapshot = healthy_snapshot()
        snapshot["edges"]["us5"]["lightsail"]["static_ip_attachment_safe"] = False
        self.assert_blocked(snapshot, "static_ip_attached_elsewhere:us5")

    def test_unexpected_target_blocks(self) -> None:
        snapshot = healthy_snapshot()
        snapshot["edges"]["us7"] = copy.deepcopy(snapshot["edges"]["us5"])
        self.assert_blocked(snapshot, "unexpected_target:us7")

    def test_resource_identity_must_match_the_lightsail_matrix(self) -> None:
        snapshot = healthy_snapshot()
        snapshot["edges"]["us5"]["lightsail"]["instance_name"] = "other-instance"
        result = self.assert_blocked(snapshot, "resource_mismatch:us5:instance_name")
        self.assertEqual([], result["actions"])

    def test_apply_with_a_blocker_never_calls_aws(self) -> None:
        snapshot = healthy_snapshot()
        snapshot["edges"]["us5"]["ec2_healthy"] = False
        result = RETIRE.run_retirement(
            snapshot,
            apply=True,
            confirm=RETIRE.CONFIRMATION,
            runner=self.runner,
            collector=self.collector,
            now=NOW,
        )
        self.assertIn("ec2_unhealthy:us5", result["blockers"])
        self.assertEqual([], self.runner.calls)

    def test_apply_reports_incomplete_until_resources_disappear(self) -> None:
        collector = FakeCollector()
        result = RETIRE.run_retirement(
            healthy_snapshot(),
            apply=True,
            confirm=RETIRE.CONFIRMATION,
            runner=self.runner,
            collector=collector,
            now=NOW,
        )
        self.assertEqual(2, len(collector.calls))
        self.assertIn("retirement_incomplete", result["blockers"])
        self.assertTrue(result["post_apply"]["remaining_actions"])

    def test_partial_deletion_retry_skips_absent_resources(self) -> None:
        snapshot = healthy_snapshot()
        source = snapshot["edges"]["us5"]["lightsail"]
        source.update(
            instance_exists=False,
            static_ip_exists=False,
            static_ip_attached=False,
            managed_instance_id=None,
            managed_instance_exists=False,
        )
        result = RETIRE.run_retirement(
            snapshot,
            apply=True,
            confirm=RETIRE.CONFIRMATION,
            runner=self.runner,
            collector=self.collector,
            now=NOW,
        )
        self.assertEqual([], result["blockers"])
        flattened = "\n".join(" ".join(call) for call in self.runner.calls)
        self.assertNotIn(source["instance_name"], flattened)
        self.assertNotIn(source["static_ip_name"], flattened)
        self.assertFalse(
            [action for action in result["actions"] if action["edge_id"] == "us5"],
        )

    def test_apply_order_is_fixed_and_failure_stops_fleet(self) -> None:
        runner = FakeRunner(fail_at=3)
        result = RETIRE.run_retirement(
            healthy_snapshot(),
            apply=True,
            confirm=RETIRE.CONFIRMATION,
            runner=runner,
            collector=self.collector,
            now=NOW,
        )
        self.assertEqual(3, len(runner.calls))
        self.assertEqual("detach-static-ip", runner.calls[0][2])
        self.assertEqual("delete-instance", runner.calls[1][2])
        self.assertEqual("release-static-ip", runner.calls[2][2])
        self.assertIn("apply_failed:us5:release_static_ip", result["blockers"])

    def test_apply_uses_fixed_edge_order_and_exact_resource_names(self) -> None:
        snapshot = healthy_snapshot()
        result = RETIRE.run_retirement(
            snapshot,
            apply=True,
            confirm=RETIRE.CONFIRMATION,
            runner=self.runner,
            collector=self.collector,
            now=NOW,
        )
        self.assertEqual([], result["blockers"])
        self.assertEqual(16, len(self.runner.calls))
        for index, edge_id in enumerate(RETIRE.EDGE_ORDER):
            calls = self.runner.calls[index * 4 : (index + 1) * 4]
            source = snapshot["edges"][edge_id]["lightsail"]
            self.assertEqual(
                [
                    "detach-static-ip",
                    "delete-instance",
                    "release-static-ip",
                    "deregister-managed-instance",
                ],
                [call[2] for call in calls],
            )
            self.assertIn(source["static_ip_name"], calls[0])
            self.assertIn(source["instance_name"], calls[1])
            self.assertIn(source["static_ip_name"], calls[2])
            self.assertIn(source["managed_instance_id"], calls[3])


if __name__ == "__main__":
    unittest.main()
