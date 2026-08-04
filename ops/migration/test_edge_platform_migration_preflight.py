#!/usr/bin/env python3
"""Behavior tests for the all-edge EC2 migration preflight."""

from __future__ import annotations

import copy
import datetime as dt
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
from decimal import Decimal


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "ops/migration/edge-platform-migration-preflight.sh"
GIB_BYTES = 1024**3
WINDOW_START = "2026-07-06T00:00:00+00:00"
WINDOW_END = "2026-08-05T00:00:00+00:00"


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
    end = dt.datetime.fromisoformat(option("--end-time"))
    metric_name = option("--metric-name")
    if metric_name == "NetworkOut":
        total_bytes = 46290298522  # 43.1111999992... GiB; approved formula must ceil to $34.
        daily_bytes, remainder = divmod(total_bytes, 30)
        points = [
            {
                "timestamp": (start + dt.timedelta(days=index)).isoformat(),
                "sum": daily_bytes + (1 if index < remainder else 0),
            }
            for index in range(30)
        ]
        scenario = os.environ.get("FAKE_NETWORK_SCENARIO", "healthy")
        if scenario == "29_buckets":
            points.pop()
        elif scenario == "partial_bucket":
            points[-1]["timestamp"] = (start + dt.timedelta(days=29, hours=12)).isoformat()
        elif scenario == "duplicate_bucket":
            points[-1]["timestamp"] = points[-2]["timestamp"]
        elif scenario == "out_of_range_bucket":
            points[-1]["timestamp"] = end.isoformat()
        payload = {"metricData": points}
    else:
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


def network_entry(gib: str) -> dict:
    return {
        "window_start": WINDOW_START,
        "window_end": WINDOW_END,
        "bucket_count": 30,
        "total_bytes": str(int(Decimal(gib) * GIB_BYTES)),
    }


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
        "network_out_30d": {edge_id: network_entry("10") for edge_id, _, _ in definitions},
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

    def run_live(
        self,
        scenario: str = "healthy",
    ) -> tuple[subprocess.CompletedProcess[str], list[list[str]]]:
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
                    "FAKE_NETWORK_SCENARIO": scenario,
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

    def assert_live_network_buckets_blocked(self, scenario: str) -> dict:
        completed, _ = self.run_live(scenario)
        self.assertEqual(1, completed.returncode, completed.stderr)
        report = json.loads(completed.stdout)
        self.assertTrue(
            any(
                item.startswith("collection:network_out_30d:us3:")
                and "expected_30_complete_utc_daily_buckets" in item
                for item in report["blockers"]
            ),
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
        self.assertEqual(
            {
                "window_start": WINDOW_START,
                "window_end": WINDOW_END,
                "bucket_count": 30,
                "gib": 10.0,
            },
            report["network_out_30d"]["us3"],
        )
        self.assertEqual([], report["blockers"])
        self.assertNotIn("must-not-leak", completed.report_text)  # type: ignore[attr-defined]
        self.assertNotIn("total_bytes", completed.report_text)  # type: ignore[attr-defined]

    def test_forecast_uses_unrounded_bytes_before_ceiling(self) -> None:
        fixture = healthy_fixture()
        fixture["network_out_30d"]["us3"] = network_entry("43.1112")
        completed = self.run_fixture(fixture)
        self.assertEqual(0, completed.returncode, completed.stderr)
        report = json.loads(completed.stdout)
        self.assertEqual(43.111, report["network_out_30d"]["us3"]["gib"])
        self.assertEqual(34, report["forecast_monthly_usd"]["per_edge"]["us3"])

    def test_live_collection_uses_exact_complete_utc_day_window(self) -> None:
        completed, calls = self.run_live()
        self.assertEqual(0, completed.returncode, completed.stderr)
        report = json.loads(completed.stdout)
        receipt = report["network_out_30d"]["us3"]
        window_start = dt.datetime.fromisoformat(receipt["window_start"])
        window_end = dt.datetime.fromisoformat(receipt["window_end"])
        self.assertEqual(dt.time(0, 0), window_start.timetz().replace(tzinfo=None))
        self.assertEqual(dt.time(0, 0), window_end.timetz().replace(tzinfo=None))
        self.assertEqual(dt.timedelta(days=30), window_end - window_start)
        self.assertEqual(30, receipt["bucket_count"])
        self.assertEqual(43.111, receipt["gib"])
        network_calls = [
            call
            for call in calls
            if call[:2] == ["lightsail", "get-instance-metric-data"]
            and call[call.index("--metric-name") + 1] == "NetworkOut"
        ]
        self.assertEqual(4, len(network_calls))
        for call in network_calls:
            self.assertEqual(receipt["window_start"], call[call.index("--start-time") + 1])
            self.assertEqual(receipt["window_end"], call[call.index("--end-time") + 1])

    def test_live_collection_blocks_29_daily_network_buckets(self) -> None:
        self.assert_live_network_buckets_blocked("29_buckets")

    def test_live_collection_blocks_partial_daily_network_bucket(self) -> None:
        self.assert_live_network_buckets_blocked("partial_bucket")

    def test_live_collection_blocks_duplicate_and_out_of_range_network_buckets(self) -> None:
        for scenario in ("duplicate_bucket", "out_of_range_bucket"):
            with self.subTest(scenario=scenario):
                self.assert_live_network_buckets_blocked(scenario)

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
