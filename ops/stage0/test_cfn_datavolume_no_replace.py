#!/usr/bin/env python3
"""Tests for the DataVolume no-replace CloudFormation guard."""

from __future__ import annotations

import json
import pathlib
import subprocess
import unittest

_SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
_GUARD = _SCRIPT_DIR / "cfn_datavolume_changeset_guard.py"
_SHELL = _SCRIPT_DIR / "reconcile-cfn-datavolume-no-replace.sh"


class CfnDataVolumeNoReplaceTest(unittest.TestCase):
    def test_guard_selftest(self) -> None:
        proc = subprocess.run(
            ["python3", str(_GUARD), "--selftest"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("PASS", proc.stdout)

    def test_guard_accepts_only_datavolume_property_changes(self) -> None:
        doc = {
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
                            }
                        ],
                    }
                }
            ]
        }
        proc = subprocess.run(
            ["python3", str(_GUARD)],
            input=json.dumps(doc),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        out = json.loads(proc.stdout)
        self.assertTrue(out["ok"])

    def test_guard_rejects_instance_replacement(self) -> None:
        doc = {
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
        proc = subprocess.run(
            ["python3", str(_GUARD)],
            input=json.dumps(doc),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(proc.returncode, 0)
        out = json.loads(proc.stdout)
        self.assertFalse(out["ok"])
        self.assertIn("blocked resource", "\n".join(out["violations"]))

    def test_guard_rejects_iops_in_size_only_mode(self) -> None:
        doc = {
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
                                    "Name": "Iops",
                                    "RequiresRecreation": "Never",
                                }
                            }
                        ],
                    }
                }
            ]
        }
        proc = subprocess.run(
            ["python3", str(_GUARD), "--allowed-properties", "Size"],
            input=json.dumps(doc),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(proc.returncode, 0)
        out = json.loads(proc.stdout)
        self.assertFalse(out["ok"])
        self.assertIn("unexpected property changes ['Iops']", "\n".join(out["violations"]))

    def test_shell_script_parses(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SHELL)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_shell_script_has_no_execute_path(self) -> None:
        body = _SHELL.read_text(encoding="utf-8")
        self.assertNotIn("aws cloudformation execute-change-set", body)
        self.assertNotIn("--execute-approved", body)
        swallow_pattern = "||" + " true"
        self.assertNotIn(swallow_pattern, body)

    def test_shell_script_uses_previous_template_with_stable_ami(self) -> None:
        body = _SHELL.read_text(encoding="utf-8")
        self.assertIn("--use-previous-template", body)
        self.assertIn("STABLE_AMI_PARAM", body)
        self.assertIn("DataVolumeSizeGiB", body)
        self.assertIn('"AmazonLinux2023Arm64Ami": stable_ami_param', body)
        self.assertIn("--allowed-properties Size", body)
        self.assertNotIn("--template)", body)
        self.assertNotIn("--iops", body)
        self.assertNotIn("--throughput", body)
        self.assertNotIn("Iops: !Ref DataVolumeIops", body)
        self.assertNotIn("Throughput: !Ref DataVolumeThroughput", body)

    def test_keep_change_set_only_retains_after_guard_validation(self) -> None:
        body = _SHELL.read_text(encoding="utf-8")
        self.assertIn("CHANGE_SET_VALIDATED=0", body)
        self.assertIn("CHANGE_SET_VALIDATED=1", body)
        self.assertIn(
            '! ( "${KEEP_CHANGE_SET}" = 1 && "${CHANGE_SET_VALIDATED}" = 1 )',
            body,
        )


if __name__ == "__main__":
    unittest.main()
