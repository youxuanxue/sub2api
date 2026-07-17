#!/usr/bin/env python3
"""CFN alarm contract checks for stage0-single-ec2.yaml (stdlib-only)."""
from __future__ import annotations

import pathlib
import re
import unittest

CFN = pathlib.Path(__file__).resolve().parents[1] / "cloudformation/stage0-single-ec2.yaml"


class Stage0CfnAlarmsTest(unittest.TestCase):
    def test_root_volume_alarm_sustained_85_for_10m(self) -> None:
        text = CFN.read_text(encoding="utf-8")
        block = re.search(
            r"RootVolumeDiskAlarm:\s*\n\s*Type: AWS::CloudWatch::Alarm\s*\n\s*Properties:(.*?)(?:\n  [A-Z][A-Za-z0-9]+:|\nOutputs:)",
            text,
            re.S,
        )
        self.assertIsNotNone(block, "RootVolumeDiskAlarm resource missing")
        body = block.group(1)
        for needle in (
            "MetricName: RootVolumeUsedPercent",
            "Statistic: Average",
            "Period: 300",
            "EvaluationPeriods: 2",
            "DatapointsToAlarm: 2",
            "Threshold: 85",
            "ComparisonOperator: GreaterThanThreshold",
            "TreatMissingData: notBreaching",
        ):
            self.assertIn(needle, body)

    def test_instance_cpu_alarm_sustained_80_for_15m(self) -> None:
        text = CFN.read_text(encoding="utf-8")
        block = re.search(
            r"InstanceCpuAlarm:\s*\n\s*Type: AWS::CloudWatch::Alarm\s*\n\s*Properties:(.*?)(?:\n  [A-Z][A-Za-z0-9]+:|\nOutputs:)",
            text,
            re.S,
        )
        self.assertIsNotNone(block, "InstanceCpuAlarm resource missing")
        body = block.group(1)
        self.assertIn("MetricName: CPUUtilization", body)
        self.assertIn("Statistic: Average", body)
        self.assertIn("Period: 300", body)
        self.assertIn("EvaluationPeriods: 3", body)
        self.assertIn("DatapointsToAlarm: 3", body)
        self.assertIn("Threshold: 80", body)
        self.assertIn("ComparisonOperator: GreaterThanThreshold", body)
        self.assertIn("TreatMissingData: notBreaching", body)

    def test_sync_script_alarm_contract_matches_cfn(self) -> None:
        script_path = pathlib.Path(__file__).resolve().parents[3] / "ops/stage0/sync-instance-cpu-alarm.sh"
        script = script_path.read_text(encoding="utf-8")
        for needle in (
            "--period 300",
            "--evaluation-periods 3",
            "--datapoints-to-alarm 3",
            "--threshold 80",
            "GreaterThanThreshold",
            "notBreaching",
            "CPUUtilization",
        ):
            self.assertIn(needle, script, f"sync script missing {needle!r}")

    def test_root_disk_sync_script_alarm_contract_matches_cfn(self) -> None:
        script_path = pathlib.Path(__file__).resolve().parents[3] / "ops/stage0/sync-instance-root-disk-alarm.sh"
        script = script_path.read_text(encoding="utf-8")
        for needle in (
            "--period 300",
            "--evaluation-periods 2",
            "--datapoints-to-alarm 2",
            "--threshold 85",
            "GreaterThanThreshold",
            "notBreaching",
            "RootVolumeUsedPercent",
        ):
            self.assertIn(needle, script, f"root disk sync script missing {needle!r}")


if __name__ == "__main__":
    unittest.main()
