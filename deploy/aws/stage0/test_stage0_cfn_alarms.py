#!/usr/bin/env python3
"""CFN alarm contract checks for stage0-single-ec2.yaml (stdlib-only)."""
from __future__ import annotations

import pathlib
import re
import unittest

CFN = pathlib.Path(__file__).resolve().parents[1] / "cloudformation/stage0-single-ec2.yaml"


class Stage0CfnAlarmsTest(unittest.TestCase):
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


if __name__ == "__main__":
    unittest.main()
