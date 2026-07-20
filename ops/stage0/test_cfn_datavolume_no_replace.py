#!/usr/bin/env python3
"""Tests for the DataVolume no-replace CloudFormation guard."""

from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
_GUARD = _SCRIPT_DIR / "cfn_datavolume_changeset_guard.py"
_PARAM_PLAN = _SCRIPT_DIR / "cfn_datavolume_parameter_plan.py"
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
                        "Scope": ["Properties"],
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
                        "Scope": ["Properties"],
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

    def test_guard_rejects_empty_or_non_property_scope(self) -> None:
        empty = subprocess.run(
            ["python3", str(_GUARD)],
            input=json.dumps({"Changes": []}),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(empty.returncode, 0)
        self.assertIn("no resource changes", empty.stdout)

        wrong_scope = {
            "Changes": [
                {
                    "ResourceChange": {
                        "Action": "Modify",
                        "LogicalResourceId": "DataVolume",
                        "ResourceType": "AWS::EC2::Volume",
                        "Replacement": "False",
                        "Scope": ["Tags"],
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
            ["python3", str(_GUARD), "--allowed-properties", "Size"],
            input=json.dumps(wrong_scope),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("expected Scope", proc.stdout)

    def test_guard_rejects_multiple_resource_changes(self) -> None:
        resource_change = {
            "Action": "Modify",
            "LogicalResourceId": "DataVolume",
            "ResourceType": "AWS::EC2::Volume",
            "Replacement": "False",
            "Scope": ["Properties"],
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
        proc = subprocess.run(
            ["python3", str(_GUARD), "--allowed-properties", "Size"],
            input=json.dumps(
                {
                    "Changes": [
                        {"ResourceChange": resource_change},
                        {"ResourceChange": resource_change},
                    ]
                }
            ),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("expected exactly one resource change", proc.stdout)

    def test_shell_script_parses(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SHELL)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_parameter_plan_grows_without_rewriting_unrelated_values(self) -> None:
        stack = {
            "Stacks": [
                {
                    "Parameters": [
                        {"ParameterKey": "DataVolumeSizeGiB", "ParameterValue": "50"},
                        {
                            "ParameterKey": "AmazonLinux2023Arm64Ami",
                            "ParameterValue": "/aws/service/ami-amazon-linux-latest/example",
                        },
                        {"ParameterKey": "ImageTag", "ParameterValue": "stable"},
                    ]
                }
            ]
        }
        with tempfile.TemporaryDirectory() as tmp:
            stack_path = pathlib.Path(tmp) / "stack.json"
            out_path = pathlib.Path(tmp) / "params.json"
            stack_path.write_text(json.dumps(stack), encoding="utf-8")
            proc = subprocess.run(
                [
                    "python3",
                    str(_PARAM_PLAN),
                    "--stack-json",
                    str(stack_path),
                    "--out",
                    str(out_path),
                    "--size",
                    "100",
                    "--stable-ami-param",
                    "/tokenkey/test/ami",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr)
            params = json.loads(out_path.read_text(encoding="utf-8"))
        by_key = {item["ParameterKey"]: item for item in params}
        self.assertEqual(by_key["DataVolumeSizeGiB"]["ParameterValue"], "100")
        self.assertEqual(
            by_key["AmazonLinux2023Arm64Ami"]["ParameterValue"],
            "/tokenkey/test/ami",
        )
        self.assertEqual(by_key["ImageTag"], {"ParameterKey": "ImageTag", "UsePreviousValue": True})

    def test_parameter_plan_rejects_shrink(self) -> None:
        stack = {
            "Stacks": [
                {"Parameters": [{"ParameterKey": "DataVolumeSizeGiB", "ParameterValue": "100"}]}
            ]
        }
        with tempfile.TemporaryDirectory() as tmp:
            stack_path = pathlib.Path(tmp) / "stack.json"
            out_path = pathlib.Path(tmp) / "params.json"
            stack_path.write_text(json.dumps(stack), encoding="utf-8")
            proc = subprocess.run(
                [
                    "python3",
                    str(_PARAM_PLAN),
                    "--stack-json",
                    str(stack_path),
                    "--out",
                    str(out_path),
                    "--size",
                    "50",
                    "--stable-ami-param",
                    "/tokenkey/test/ami",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("refusing DataVolume shrink", proc.stderr)

    def test_parameter_plan_rejects_duplicate_parameter_keys(self) -> None:
        stack = {
            "Stacks": [
                {
                    "Parameters": [
                        {"ParameterKey": "DataVolumeSizeGiB", "ParameterValue": "50"},
                        {"ParameterKey": "DataVolumeSizeGiB", "ParameterValue": "50"},
                    ]
                }
            ]
        }
        with tempfile.TemporaryDirectory() as tmp:
            stack_path = pathlib.Path(tmp) / "stack.json"
            out_path = pathlib.Path(tmp) / "params.json"
            stack_path.write_text(json.dumps(stack), encoding="utf-8")
            proc = subprocess.run(
                [
                    "python3",
                    str(_PARAM_PLAN),
                    "--stack-json",
                    str(stack_path),
                    "--out",
                    str(out_path),
                    "--size",
                    "100",
                    "--stable-ami-param",
                    "/tokenkey/test/ami",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("duplicate stack parameter", proc.stderr)

    def test_prod_plan_requires_exact_confirmation_before_aws(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SHELL), "--size", "100"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 2)
        self.assertIn("prod plan refused", proc.stderr)

        mismatch = subprocess.run(
            [
                "bash",
                str(_SHELL),
                "--size",
                "100",
                "--confirm-prod-plan",
                "wrong-stack",
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(mismatch.returncode, 2)
        self.assertIn("prod plan refused", mismatch.stderr)

    def test_size_is_required_before_aws(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SHELL)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 2)
        self.assertIn("--size GIB", proc.stderr)

    def test_plan_success_uses_fake_aws_and_cleans_preview_artifacts(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            bin_path = tmp_path / "bin"
            bin_path.mkdir()
            log_path = tmp_path / "aws.jsonl"
            aws_stub = bin_path / "aws"
            aws_stub.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env python3
                    import json
                    import os
                    import sys

                    args = sys.argv[1:]
                    with open(os.environ["AWS_STUB_LOG"], "a", encoding="utf-8") as out:
                        out.write(json.dumps(args) + "\\n")

                    service, operation = args[0:2]
                    if service == "cloudformation" and operation == "describe-stacks":
                        if "--query" in args:
                            print("ami-0123456789abcdef0")
                        else:
                            print(json.dumps({"Stacks": [{"Parameters": [
                                {"ParameterKey": "DataVolumeSizeGiB", "ParameterValue": "50"},
                                {"ParameterKey": "AmazonLinux2023Arm64Ami", "ParameterValue": "/aws/service/ami", "ResolvedValue": "ami-0123456789abcdef0"},
                                {"ParameterKey": "ImageTag", "ParameterValue": "stable"}
                            ]}]}))
                    elif service == "ssm" and operation == "get-parameter":
                        raise SystemExit(1)
                    elif service == "cloudformation" and operation == "describe-change-set":
                        if "--query" in args:
                            query = args[args.index("--query") + 1]
                            if query == "Status":
                                print("CREATE_COMPLETE")
                            elif query == "StatusReason":
                                print("None")
                            else:
                                print("DataVolume Modify False Properties")
                        else:
                            print(json.dumps({"Changes": [{"ResourceChange": {
                                "Action": "Modify",
                                "LogicalResourceId": "DataVolume",
                                "ResourceType": "AWS::EC2::Volume",
                                "Replacement": "False",
                                "Scope": ["Properties"],
                                "Details": [{"Target": {
                                    "Attribute": "Properties",
                                    "Name": "Size",
                                    "RequiresRecreation": "Never"
                                }}]
                            }}]}))
                    """
                ),
                encoding="utf-8",
            )
            aws_stub.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{bin_path}:{env['PATH']}"
            env["AWS_STUB_LOG"] = str(log_path)
            proc = subprocess.run(
                [
                    "bash",
                    str(_SHELL),
                    "--stack",
                    "tokenkey-rehearsal-stage0",
                    "--region",
                    "us-east-1",
                    "--size",
                    "100",
                    "--change-set-name",
                    "test-capacity-plan",
                ],
                capture_output=True,
                text=True,
                check=False,
                env=env,
            )
            calls = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]

        rendered = [" ".join(call) for call in calls]
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("validated preview only", proc.stdout)
        self.assertTrue(any(call.startswith("ssm put-parameter ") for call in rendered))
        self.assertTrue(
            any(call.startswith("cloudformation create-change-set ") for call in rendered)
        )
        self.assertTrue(
            any(call.startswith("cloudformation delete-change-set ") for call in rendered)
        )
        self.assertTrue(any(call.startswith("ssm delete-parameter ") for call in rendered))
        self.assertFalse(any("execute-change-set" in call for call in rendered))

    def test_shell_script_has_no_execute_path(self) -> None:
        body = _SHELL.read_text(encoding="utf-8")
        self.assertNotIn("aws cloudformation execute-change-set", body)
        self.assertNotIn("--execute-approved", body)
        swallow_pattern = "||" + " true"
        self.assertNotIn(swallow_pattern, body)

    def test_shell_script_uses_previous_template_with_stable_ami(self) -> None:
        body = _SHELL.read_text(encoding="utf-8")
        planner_body = _PARAM_PLAN.read_text(encoding="utf-8")
        self.assertIn("--use-previous-template", body)
        self.assertIn("STABLE_AMI_PARAM", body)
        self.assertIn("STABLE_AMI_PARAM_CREATED", body)
        self.assertIn("DataVolumeSizeGiB", body)
        self.assertIn('"AmazonLinux2023Arm64Ami": stable_ami_param', planner_body)
        self.assertIn("--allowed-properties Size", body)
        self.assertNotIn("--template)", body)
        self.assertNotIn("--iops", body)
        self.assertNotIn("--throughput", body)
        self.assertNotIn("Iops: !Ref DataVolumeIops", body)
        self.assertNotIn("Throughput: !Ref DataVolumeThroughput", body)
        self.assertNotIn("--overwrite", body)

    def test_keep_change_set_only_retains_after_guard_validation(self) -> None:
        body = _SHELL.read_text(encoding="utf-8")
        self.assertIn("CHANGE_SET_VALIDATED=0", body)
        self.assertIn("CHANGE_SET_VALIDATED=1", body)
        self.assertIn(
            '! ( "${KEEP_CHANGE_SET}" = 1 && "${CHANGE_SET_VALIDATED}" = 1 )',
            body,
        )
        self.assertIn("obtain separate production execution approval", body)


if __name__ == "__main__":
    unittest.main()
