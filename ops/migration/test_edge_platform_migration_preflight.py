#!/usr/bin/env python3
"""Behavior tests for the all-edge EC2 migration preflight."""

from __future__ import annotations

import copy
import json
import os
import pathlib
import subprocess
import tempfile
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "ops/migration/edge-platform-migration-preflight.sh"
FAKE_AWS = r'''#!/usr/bin/env python3
import datetime as dt
import json
import os
import sys

args = sys.argv[1:]
with open(os.environ["FAKE_AWS_LOG"], "a", encoding="utf-8") as handle:
    handle.write(json.dumps(args) + "\n")


def option(name):
    return args[args.index(name) + 1]


if args[:2] == ["service-quotas", "get-service-quota"]:
    payload = {"Quota": {"Value": 5}}
elif args[:2] == ["ec2", "describe-addresses"]:
    payload = {"Addresses": []}
elif args[:2] == ["ec2", "describe-vpcs"]:
    payload = {"Vpcs": []}
elif args[:2] == ["ec2", "describe-instance-type-offerings"]:
    payload = {"InstanceTypeOfferings": [{"InstanceType": "t4g.small"}]}
elif args[:2] == ["ssm", "get-parameter"]:
    name = option("--name")
    value = "ami-test-arm64" if name.startswith("/aws/service/ami-") else "mi-test-source"
    payload = {"Parameter": {"Value": value}}
elif args[:2] == ["ec2", "describe-images"]:
    payload = {"Images": [{"Architecture": "arm64"}]}
elif args[:2] == ["ssm", "describe-instance-information"]:
    payload = {"InstanceInformationList": [{"PingStatus": "Online"}]}
elif args[:2] == ["lightsail", "get-instance-metric-data"]:
    start = dt.datetime.fromisoformat(option("--start-time"))
    metric_name = option("--metric-name")
    if metric_name != "CPUUtilization":
        print(f"unexpected metric: {metric_name}", file=sys.stderr)
        raise SystemExit(3)
    payload = {
        "metricData": [
            {
                "timestamp": (start + dt.timedelta(hours=index)).isoformat(),
                "average": 8.0,
            }
            for index in range(24)
        ],
    }
else:
    print(f"unexpected fake aws command: {args}", file=sys.stderr)
    raise SystemExit(2)

print(json.dumps(payload))
'''


FAKE_DIG = r'''#!/usr/bin/env python3
import sys

addresses = {
    "api-us3.tokenkey.dev": "18.220.195.44",
    "api-us4.tokenkey.dev": "35.81.204.18",
    "api-us5.tokenkey.dev": "32.185.163.163",
    "api-us6.tokenkey.dev": "3.148.79.145",
}
print(addresses[sys.argv[3]])
'''


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

    def run_live(self) -> tuple[subprocess.CompletedProcess[str], list[list[str]]]:
        fixture = healthy_fixture()
        matrix = {
            "targets": {
                edge["edge_id"]: {
                    "deployable": True,
                    "lightsail_region": edge["region"],
                    "domain": edge["domain"],
                    "porkbun_a_ipv4": edge["expected_ipv4"],
                    "instance_name": edge["instance_name"],
                    "ssm_prefix": edge["ssm_prefix"],
                }
                for edge in fixture["fleet"]
            },
        }
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            bin_path = tmp_path / "bin"
            bin_path.mkdir()
            aws_path = bin_path / "aws"
            aws_path.write_text(FAKE_AWS, encoding="utf-8")
            aws_path.chmod(0o755)
            dig_path = bin_path / "dig"
            dig_path.write_text(FAKE_DIG, encoding="utf-8")
            dig_path.chmod(0o755)
            matrix_path = tmp_path / "matrix.json"
            matrix_path.write_text(json.dumps(matrix), encoding="utf-8")
            aws_log = tmp_path / "aws.jsonl"
            environment = os.environ.copy()
            environment.update(
                {
                    "FAKE_AWS_LOG": str(aws_log),
                    "PATH": f"{bin_path}{os.pathsep}{environment['PATH']}",
                },
            )
            completed = subprocess.run(
                ["bash", str(SCRIPT), "--matrix", str(matrix_path), "--format", "json"],
                cwd=REPO_ROOT,
                env=environment,
                text=True,
                capture_output=True,
                check=False,
            )
            calls = [json.loads(line) for line in aws_log.read_text(encoding="utf-8").splitlines()]
            return completed, calls

    def assert_blocked(self, fixture: dict, blocker_prefix: str) -> dict:
        completed = self.run_fixture(fixture)
        self.assertEqual(1, completed.returncode, completed.stderr)
        report = json.loads(completed.stdout)
        self.assertTrue(
            any(item.startswith(blocker_prefix) for item in report["blockers"]),
            report["blockers"],
        )
        return report

    def test_healthy_fixture_reports_readiness_without_cost_fields(self) -> None:
        completed = self.run_fixture(healthy_fixture(), output=True)
        self.assertEqual(0, completed.returncode, completed.stderr)
        report = json.loads(completed.report_text)  # type: ignore[attr-defined]
        self.assertEqual(["us3", "us4", "us5", "us6"], [row["edge_id"] for row in report["fleet"]])
        for forbidden in (
            "network_" + "out_30d",
            "fixed_" + "monthly_usd",
            "forecast_" + "monthly_usd",
            "approved_fleet_ceiling_usd",
        ):
            self.assertNotIn(forbidden, report)
        self.assertEqual([], report["blockers"])
        self.assertNotIn("must-not-leak", completed.report_text)  # type: ignore[attr-defined]

    def test_live_collection_does_not_request_network_egress_metrics(self) -> None:
        completed, calls = self.run_live()
        self.assertEqual(0, completed.returncode, completed.stderr)
        report = json.loads(completed.stdout)
        self.assertEqual([], report["blockers"])
        metric_names = [
            call[call.index("--metric-name") + 1]
            for call in calls
            if call[:2] == ["lightsail", "get-instance-metric-data"]
        ]
        self.assertEqual(["CPUUtilization"] * 4, metric_names)
        self.assertFalse([
            call
            for call in calls
            if call[:2] == ["lightsail", "get-instance-metric-data"]
            and call[call.index("--metric-name") + 1] != "CPUUtilization"
        ])

    def test_eip_quota_requires_two_spare_addresses_per_region(self) -> None:
        fixture = healthy_fixture()
        fixture["quotas"]["us-east-2"]["eip_used"] = 4
        self.assert_blocked(fixture, "quota:eip:us-east-2")

    def test_vpc_quota_requires_two_spare_vpcs_per_region(self) -> None:
        fixture = healthy_fixture()
        fixture["quotas"]["us-west-2"]["vpc_used"] = 4
        self.assert_blocked(fixture, "quota:vpc:us-west-2")

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
