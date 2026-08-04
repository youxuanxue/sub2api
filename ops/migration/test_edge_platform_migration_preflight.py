#!/usr/bin/env python3
"""Behavior tests for the all-edge EC2 migration preflight."""

from __future__ import annotations

import copy
import json
import pathlib
import subprocess
import tempfile
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "ops/migration/edge-platform-migration-preflight.sh"


def healthy_fixture() -> dict:
    fleet = []
    definitions = (
        ("us3", "us-east-2", "18.220.195.44"),
        ("us4", "us-west-2", "35.81.204.18"),
        ("us5", "us-west-2", "32.185.163.163"),
        ("us6", "us-east-2", "3.148.79.145"),
    )
    for edge_id, region, ip in definitions:
        fleet.append(
            {
                "edge_id": edge_id,
                "region": region,
                "domain": f"api-{edge_id}.tokenkey.dev",
                "expected_ipv4": ip,
                "instance_name": f"tokenkey-{edge_id}-lightsail",
                "ssm_prefix": f"/tokenkey/lightsail/{edge_id}",
            },
        )
    return {
        "fleet": fleet,
        "quotas": {
            "us-east-2": {
                "eip_limit": 5,
                "eip_used": 1,
                "vpc_limit": 5,
                "vpc_used": 1,
            },
            "us-west-2": {
                "eip_limit": 5,
                "eip_used": 0,
                "vpc_limit": 5,
                "vpc_used": 1,
            },
        },
        "network_out_30d": {edge_id: 10.0 for edge_id, _, _ in definitions},
        "cpu_24h": {
            edge_id: {"average_pct": 8.0, "p95_pct": 29.0}
            for edge_id, _, _ in definitions
        },
        "dns": {
            edge_id: {"expected_ipv4": ip, "resolved_ipv4": [ip]}
            for edge_id, _, ip in definitions
        },
        "amis": {
            "us-east-2": {"image_id": "ami-east", "architecture": "arm64"},
            "us-west-2": {"image_id": "ami-west", "architecture": "arm64"},
        },
        "instance_type_offerings": {
            "us-east-2": ["t4g.small"],
            "us-west-2": ["t4g.small"],
        },
        "ssm": {
            edge_id: {"instance_id": f"mi-{edge_id}", "ping_status": "Online"}
            for edge_id, _, _ in definitions
        },
        "secret_value": "must-not-leak",
    }


class EdgePlatformMigrationPreflightTests(unittest.TestCase):
    maxDiff = None

    def run_fixture(self, fixture: dict, *, output: bool = False) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as tmp:
            fixture_path = pathlib.Path(tmp) / "fixture.json"
            fixture_path.write_text(json.dumps(fixture), encoding="utf-8")
            command = [
                "bash",
                str(SCRIPT),
                "--fixture",
                str(fixture_path),
                "--format",
                "json",
            ]
            output_path = pathlib.Path(tmp) / "report.json"
            if output:
                command.extend(("--output", str(output_path)))
            completed = subprocess.run(
                command,
                cwd=REPO_ROOT,
                text=True,
                capture_output=True,
                check=False,
            )
            if output and output_path.exists():
                completed.report_text = output_path.read_text(encoding="utf-8")  # type: ignore[attr-defined]
            return completed

    def assert_blocked(self, fixture: dict, blocker_prefix: str) -> dict:
        completed = self.run_fixture(fixture)
        self.assertEqual(1, completed.returncode, completed.stderr)
        report = json.loads(completed.stdout)
        self.assertTrue(
            any(item.startswith(blocker_prefix) for item in report["blockers"]),
            report["blockers"],
        )
        return report

    def test_healthy_fixture_reports_four_edges_and_both_cost_scopes(self) -> None:
        completed = self.run_fixture(healthy_fixture(), output=True)
        self.assertEqual(0, completed.returncode, completed.stderr)
        report = json.loads(completed.report_text)  # type: ignore[attr-defined]
        self.assertEqual(["us3", "us4", "us5", "us6"], [row["edge_id"] for row in report["fleet"]])
        self.assertEqual(
            {"us3": 19.12, "us4": 19.12, "us5": 19.12, "us6": 19.12},
            report["fixed_monthly_usd"]["per_edge"],
        )
        self.assertEqual(76.46, report["fixed_monthly_usd"]["fleet"])
        self.assertEqual(31, report["forecast_monthly_usd"]["per_edge"]["us3"])
        self.assertEqual(124, report["forecast_monthly_usd"]["fleet"])
        self.assertEqual([], report["blockers"])
        self.assertNotIn("must-not-leak", completed.report_text)  # type: ignore[attr-defined]

    def test_eip_quota_requires_two_spare_addresses_per_region(self) -> None:
        fixture = healthy_fixture()
        fixture["quotas"]["us-east-2"]["eip_used"] = 4
        self.assert_blocked(fixture, "quota:eip:us-east-2")

    def test_vpc_quota_requires_two_spare_vpcs_per_region(self) -> None:
        fixture = healthy_fixture()
        fixture["quotas"]["us-west-2"]["vpc_used"] = 4
        self.assert_blocked(fixture, "quota:vpc:us-west-2")

    def test_missing_network_out_blocks_cost_approval(self) -> None:
        fixture = healthy_fixture()
        del fixture["network_out_30d"]["us4"]
        self.assert_blocked(fixture, "network_out_30d:us4")

    def test_dns_drift_blocks_migration(self) -> None:
        fixture = healthy_fixture()
        fixture["dns"]["us5"]["resolved_ipv4"] = ["203.0.113.5"]
        self.assert_blocked(fixture, "dns:us5")

    def test_offline_ssm_source_blocks_migration(self) -> None:
        fixture = healthy_fixture()
        fixture["ssm"]["us4"]["ping_status"] = "ConnectionLost"
        self.assert_blocked(fixture, "ssm:us4")

    def test_non_arm_ami_blocks_region(self) -> None:
        fixture = healthy_fixture()
        fixture["amis"]["us-west-2"]["architecture"] = "x86_64"
        self.assert_blocked(fixture, "ami:us-west-2")

    def test_missing_t4g_small_offering_blocks_region(self) -> None:
        fixture = healthy_fixture()
        fixture["instance_type_offerings"]["us-east-2"] = []
        self.assert_blocked(fixture, "offering:us-east-2")


if __name__ == "__main__":
    unittest.main()
