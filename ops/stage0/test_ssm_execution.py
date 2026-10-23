#!/usr/bin/env python3
"""Behavior tests for cutover-aware prod SSM resolution."""
from __future__ import annotations

import importlib.util
import base64
import json
import pathlib
import subprocess
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[2]
MOD_PATH = ROOT / "ops/stage0/ssm_execution.py"


def load_mod():
    spec = importlib.util.spec_from_file_location("ssm_execution_under_test", MOD_PATH)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class SsmExecutionResolveTest(unittest.TestCase):
    def setUp(self) -> None:
        self.mod = load_mod()

    def test_shell_transport_runs_under_dash_without_consuming_following_commands(self) -> None:
        run = subprocess.run
        for script, expected_rc in (("cat >/dev/null\nprintf after-stdin", 0), ("exit 7", 7)):
            with self.subTest(script=script):
                result = None

                def aws(args, **kwargs):
                    nonlocal result
                    if "send-command" in args:
                        params = json.loads(args[args.index("--parameters") + 1])
                        result = run(["dash", "-c", "\n".join(params["commands"])],
                                     input="", capture_output=True, text=True)
                        return "test-command"
                    return json.dumps({"Status": "Success" if result.returncode == 0 else "Failed",
                                       "ResponseCode": result.returncode,
                                       "StandardOutputContent": result.stdout,
                                       "StandardErrorContent": result.stderr})

                with mock.patch.object(self.mod.subprocess, "check_output", side_effect=aws), \
                     mock.patch.object(self.mod.subprocess, "run"):
                    encoded = base64.b64encode(script.encode()).decode()
                    if expected_rc:
                        with self.assertRaises(SystemExit):
                            self.mod.run_shell_b64("mi-033c9569c7fb8b884", encoded, "test")
                    else:
                        self.assertEqual(self.mod.run_shell_b64("mi-033c9569c7fb8b884", encoded, "test"),
                                         "after-stdin")
                self.assertEqual(result.returncode, expected_rc)

    def test_resolve_prod_instance_rejects_retired_hetzner(self) -> None:
        fake = mock.Mock()
        fake.resolve_mode.return_value = "hetzner"
        fake.DEFAULT_MATRIX = pathlib.Path("/tmp/matrix.json")
        fake.resolve_hetzner.side_effect = SystemExit("retired")
        with mock.patch.object(self.mod, "_resolve_mod", return_value=fake):
            with self.assertRaises(SystemExit):
                self.mod.resolve_prod_instance()
        fake.resolve_hetzner.assert_called_once_with(fake.DEFAULT_MATRIX)

    def test_resolve_prod_instance_follows_tencent_live_prod(self) -> None:
        fake = mock.Mock()
        fake.resolve_mode.return_value = "tencent-usw-candidate"
        fake.TENCENT_USW_MATRIX = pathlib.Path("/tmp/tencent.json")
        fake.resolve_tencent_usw_candidate.return_value = {
            "instance_id": "mi-080ed76dc6d97d8b5",
            "ssm_region": "eu-west-2",
        }
        with mock.patch.object(self.mod, "_resolve_mod", return_value=fake):
            iid = self.mod.resolve_prod_instance()
        self.assertEqual(iid, "mi-080ed76dc6d97d8b5")
        self.assertEqual(self.mod.PROD_REGION, "eu-west-2")
        fake.resolve_tencent_usw_candidate.assert_called_once_with(fake.TENCENT_USW_MATRIX)
        fake.resolve_hetzner.assert_not_called()
        fake.resolve_fresh_aws.assert_not_called()

    def test_resolve_prod_instance_aws_path_keeps_us_east_1(self) -> None:
        fake = mock.Mock()
        fake.resolve_mode.return_value = "aws"
        fake.resolve_aws.return_value = {
            "instance_id": "i-0e43099f831b03160",
            "ssm_region": "us-east-1",
        }
        with mock.patch.object(self.mod, "_resolve_mod", return_value=fake):
            iid = self.mod.resolve_prod_instance()
        self.assertEqual(iid, "i-0e43099f831b03160")
        self.assertEqual(self.mod.PROD_REGION, "us-east-1")
        fake.resolve_aws.assert_called_once_with(self.mod.PROD_STACK)

    def test_resolve_prod_instance_fail_closed_on_empty_region(self) -> None:
        fake = mock.Mock()
        fake.resolve_mode.return_value = "tencent-usw-candidate"
        fake.TENCENT_USW_MATRIX = pathlib.Path("/tmp/tencent.json")
        fake.resolve_tencent_usw_candidate.return_value = {
            "instance_id": "mi-080ed76dc6d97d8b5",
            "ssm_region": "",
        }
        with mock.patch.object(self.mod, "_resolve_mod", return_value=fake):
            with self.assertRaises(SystemExit):
                self.mod.resolve_prod_instance()

    def test_region_for_mi_pin_reads_tencent_matrix(self) -> None:
        fake = mock.Mock()
        fake.TENCENT_USW_MATRIX = pathlib.Path("/tmp/tencent.json")
        fake.load_tencent_usw_matrix.return_value = {"ssm_region": "eu-west-2"}
        with mock.patch.object(self.mod, "_resolve_mod", return_value=fake):
            self.assertEqual(
                self.mod.region_for_instance_id("mi-080ed76dc6d97d8b5"),
                "eu-west-2",
            )
        fake.load_hetzner_matrix.assert_not_called()

    def test_region_for_ec2_pin_is_retired(self) -> None:
        fake = mock.Mock(spec=["TENCENT_USW_MATRIX", "load_tencent_usw_matrix"])
        with mock.patch.object(self.mod, "_resolve_mod", return_value=fake):
            with self.assertRaises(SystemExit):
                self.mod.region_for_instance_id("i-045f062ce33e1a5af")
        fake.load_tencent_usw_matrix.assert_not_called()

