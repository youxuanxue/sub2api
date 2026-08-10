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
            "ec2_healthy": True,
            "ec2_eip": target_ip,
            "dns_ipv4": [target_ip],
            "source_schedulable_accounts": 0,
            "logical_backup": {
                "verified": True,
                "s3_key": f"retirement/{edge_id}/backup.sql.gz",
                "checksum": f"sha256:{edge_id}",
            },
            "data_snapshot": {
                "snapshot_id": f"snap-{index:017x}",
                "state": "completed",
            },
            "lightsail": {
                "region": source["lightsail_region"],
                "instance_name": source["instance_name"],
                "instance_exists": True,
                "static_ip_name": source["static_ip_name"],
                "static_ip_exists": True,
                "static_ip_attached": True,
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
        "final_cutover_receipt": {
            "commit": "0123456789abcdef",
            "completed_at": "2026-08-09T10:00:00Z",
        },
        "fleet_observation_started_at": "2026-08-09T10:00:00Z",
        "unexpected_resources": [],
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


class RetireLightsailFleetTests(unittest.TestCase):
    maxDiff = None

    def setUp(self) -> None:
        self.runner = FakeRunner()

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

    def test_apply_requires_exact_confirmation(self) -> None:
        with self.assertRaisesRegex(ValueError, "exact confirmation"):
            RETIRE.run_retirement(
                healthy_snapshot(),
                apply=True,
                confirm="wrong",
                runner=self.runner,
                now=NOW,
            )
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

    def test_each_live_safety_gate_blocks_retirement(self) -> None:
        mutations = (
            ("ec2_unhealthy:us5", lambda row: row.update(ec2_healthy=False)),
            (
                "lightsail_still_deployable:us5",
                lambda row: row.update(lightsail_deployable=True),
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
        )
        for code, mutate in mutations:
            with self.subTest(code=code):
                snapshot = healthy_snapshot()
                mutate(snapshot["edges"]["us5"])
                self.assert_blocked(snapshot, code)

    def test_unexpected_target_or_resource_blocks(self) -> None:
        snapshot = healthy_snapshot()
        snapshot["edges"]["us7"] = copy.deepcopy(snapshot["edges"]["us5"])
        snapshot["unexpected_resources"] = ["lightsail-instance:unknown"]
        result = self.assert_blocked(snapshot, "unexpected_target:us7")
        self.assertIn("unexpected_lightsail_resources", result["blockers"])

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
            now=NOW,
        )
        self.assertIn("ec2_unhealthy:us5", result["blockers"])
        self.assertEqual([], self.runner.calls)

    def test_partial_deletion_retry_skips_absent_resources(self) -> None:
        snapshot = healthy_snapshot()
        source = snapshot["edges"]["us5"]["lightsail"]
        source.update(
            instance_exists=False,
            static_ip_exists=False,
            static_ip_attached=False,
            managed_instance_exists=False,
        )
        result = RETIRE.run_retirement(
            snapshot,
            apply=True,
            confirm=RETIRE.CONFIRMATION,
            runner=self.runner,
            now=NOW,
        )
        self.assertEqual([], result["blockers"])
        flattened = "\n".join(" ".join(call) for call in self.runner.calls)
        self.assertNotIn(source["instance_name"], flattened)
        self.assertNotIn(source["static_ip_name"], flattened)
        self.assertNotIn(source["managed_instance_id"], flattened)

    def test_apply_order_is_fixed_and_failure_stops_fleet(self) -> None:
        runner = FakeRunner(fail_at=3)
        result = RETIRE.run_retirement(
            healthy_snapshot(),
            apply=True,
            confirm=RETIRE.CONFIRMATION,
            runner=runner,
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
