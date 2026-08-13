#!/usr/bin/env python3
"""Behavior tests for the fixed production partition-maintenance controller."""

from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import tempfile
import unittest

import data_layer_partition_maintenance as controller


INSTANCE_ID = "i-0123456789abcdef0"
COMMAND_ID = "cmd-0123456789abcdef0"


def remote_receipt() -> dict[str, object]:
    return {
        "receipt_version": 1,
        "mode": "partition_maintenance",
        "ok": True,
        "job_name": "ops_partition_maintenance",
        "completed_at": "2026-08-04T12:00:00Z",
        "tables": [
            {"table": "ops_system_logs", "range_count": 8},
            {"table": "ops_error_logs", "range_count": 8},
            {"table": "usage_logs", "range_count": 8},
        ],
        "deletion_authorized": False,
    }


class FakeAWS:
    def __init__(self, *, receipt: dict[str, object] | None = None, send_instance: str = INSTANCE_ID) -> None:
        self.calls: list[list[str]] = []
        self.receipt = receipt if receipt is not None else remote_receipt()
        self.send_instance = send_instance

    def __call__(self, args: list[str]) -> subprocess.CompletedProcess[str]:
        self.calls.append(list(args))
        operation = args[1:3]
        if operation == ["cloudformation", "describe-stacks"]:
            payload = {
                "Stacks": [{
                    "StackName": controller.PROD_STACK,
                    "Outputs": [{"OutputKey": "InstanceId", "OutputValue": INSTANCE_ID}],
                }]
            }
        elif operation == ["ssm", "send-command"]:
            payload = {
                "Command": {
                    "CommandId": COMMAND_ID,
                    "DocumentName": "AWS-RunShellScript",
                    "InstanceIds": [self.send_instance],
                }
            }
        elif operation == ["ssm", "get-command-invocation"]:
            payload = {
                "CommandId": COMMAND_ID,
                "InstanceId": INSTANCE_ID,
                "Status": "Success",
                "ResponseCode": 0,
                "StandardOutputContent": json.dumps(self.receipt, separators=(",", ":")),
                "StandardErrorContent": "",
            }
        else:
            raise AssertionError(f"unexpected AWS call: {args!r}")
        return subprocess.CompletedProcess(args, 0, stdout=json.dumps(payload), stderr="")


class DataLayerPartitionMaintenanceTest(unittest.TestCase):
    def test_remote_script_is_bash_syntax_clean(self) -> None:
        proc = subprocess.run(
            ["bash", "-n"],
            input=controller._REMOTE_SCRIPT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_wrong_confirmation_makes_zero_aws_calls(self) -> None:
        fake = FakeAWS()
        with tempfile.TemporaryDirectory() as td:
            with self.assertRaisesRegex(controller.PartitionMaintenanceError, "confirmation"):
                controller.run(
                    pathlib.Path(td) / "receipt.json",
                    "wrong",
                    run_aws=fake,
                    sleep=lambda _: None,
                )
        self.assertEqual(fake.calls, [])

    def test_parser_exposes_no_target_command_or_script_options(self) -> None:
        parser = controller.build_parser()
        subparsers = next(
            action for action in parser._actions if isinstance(action, argparse._SubParsersAction)
        )
        run_parser = subparsers.choices["run"]
        option_dests = {action.dest for action in run_parser._actions}
        self.assertEqual(option_dests, {"help", "receipt", "confirm"})

    def test_success_uses_fixed_instance_command_and_full_json(self) -> None:
        fake = FakeAWS()
        with tempfile.TemporaryDirectory() as td:
            receipt_path = pathlib.Path(td) / "receipt.json"
            result = controller.run(
                receipt_path,
                controller.CONFIRMATION,
                run_aws=fake,
                sleep=lambda _: None,
            )
            persisted = json.loads(receipt_path.read_text(encoding="utf-8"))

        self.assertEqual(result, persisted)
        self.assertEqual(result["region"], controller.PROD_REGION)
        self.assertEqual(result["stack"], controller.PROD_STACK)
        self.assertEqual(result["instance_id"], INSTANCE_ID)
        self.assertEqual(result["command_id"], COMMAND_ID)
        self.assertEqual(result["remote_receipt"], remote_receipt())
        self.assertEqual(len(fake.calls), 3)

        describe, send, invocation = fake.calls
        for call in fake.calls:
            self.assertIn("--output", call)
            self.assertIn("json", call)
            self.assertNotIn("--query", call)
        self.assertEqual(describe[describe.index("--region") + 1], controller.PROD_REGION)
        self.assertEqual(describe[describe.index("--stack-name") + 1], controller.PROD_STACK)
        self.assertEqual(send[send.index("--instance-ids") + 1], INSTANCE_ID)
        parameters = json.loads(send[send.index("--parameters") + 1])
        self.assertEqual(parameters, {"commands": [controller.REMOTE_COMMAND]})
        self.assertTrue(controller.REMOTE_COMMAND.startswith("sudo bash -c "))
        self.assertNotIn("run-probe.sh", controller.REMOTE_COMMAND)
        self.assertIn(
            'sudo docker exec --user 1000:1000 "$APP_CONTAINER" /app/sub2api --partition-maintenance-once --confirm tokenkey-prod-partition-maintenance-v1',
            controller.REMOTE_COMMAND,
        )
        self.assertEqual(invocation[invocation.index("--instance-id") + 1], INSTANCE_ID)

    def test_send_command_instance_drift_fails_without_receipt(self) -> None:
        fake = FakeAWS(send_instance="i-11111111111111111")
        with tempfile.TemporaryDirectory() as td:
            receipt_path = pathlib.Path(td) / "receipt.json"
            with self.assertRaisesRegex(controller.PartitionMaintenanceError, "instance"):
                controller.run(
                    receipt_path,
                    controller.CONFIRMATION,
                    run_aws=fake,
                    sleep=lambda _: None,
                )
            self.assertFalse(receipt_path.exists())

    def test_incomplete_remote_receipt_fails_closed(self) -> None:
        fake = FakeAWS(receipt={"ok": True, "deletion_authorized": False})
        with tempfile.TemporaryDirectory() as td:
            receipt_path = pathlib.Path(td) / "receipt.json"
            with self.assertRaisesRegex(controller.PartitionMaintenanceError, "receipt"):
                controller.run(
                    receipt_path,
                    controller.CONFIRMATION,
                    run_aws=fake,
                    sleep=lambda _: None,
                )
            self.assertFalse(receipt_path.exists())

    def test_existing_receipt_is_not_overwritten_or_sent(self) -> None:
        fake = FakeAWS()
        with tempfile.TemporaryDirectory() as td:
            receipt_path = pathlib.Path(td) / "receipt.json"
            receipt_path.write_text('{"existing":true}\n', encoding="utf-8")
            with self.assertRaisesRegex(controller.PartitionMaintenanceError, "already exists"):
                controller.run(
                    receipt_path,
                    controller.CONFIRMATION,
                    run_aws=fake,
                    sleep=lambda _: None,
                )
            self.assertEqual(receipt_path.read_text(encoding="utf-8"), '{"existing":true}\n')
        self.assertEqual(fake.calls, [])


if __name__ == "__main__":
    unittest.main()
