#!/usr/bin/env python3
"""Behavior tests for cutover-aware prod SSM resolution."""
from __future__ import annotations

import importlib.util
import pathlib
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

    def test_resolve_prod_instance_follows_hetzner_cutover_and_binds_region(self) -> None:
        fake = mock.Mock()
        fake.resolve_mode.return_value = "hetzner"
        fake.DEFAULT_MATRIX = pathlib.Path("/tmp/matrix.json")
        fake.resolve_hetzner.return_value = {
            "instance_id": "mi-033c9569c7fb8b884",
            "ssm_region": "eu-west-2",
        }
        with mock.patch.object(self.mod, "_resolve_mod", return_value=fake):
            iid = self.mod.resolve_prod_instance()
        self.assertEqual(iid, "mi-033c9569c7fb8b884")
        self.assertEqual(self.mod.PROD_REGION, "eu-west-2")
        fake.resolve_hetzner.assert_called_once_with(fake.DEFAULT_MATRIX)
        fake.resolve_aws.assert_not_called()

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
        fake.resolve_mode.return_value = "hetzner"
        fake.DEFAULT_MATRIX = pathlib.Path("/tmp/matrix.json")
        fake.resolve_hetzner.return_value = {
            "instance_id": "mi-033c9569c7fb8b884",
            "ssm_region": "",
        }
        with mock.patch.object(self.mod, "_resolve_mod", return_value=fake):
            with self.assertRaises(SystemExit):
                self.mod.resolve_prod_instance()

    def test_region_for_mi_pin_reads_matrix(self) -> None:
        fake = mock.Mock()
        fake.DEFAULT_MATRIX = pathlib.Path("/tmp/matrix.json")
        fake.load_hetzner_matrix.return_value = {"ssm_region": "eu-west-2"}
        with mock.patch.object(self.mod, "_resolve_mod", return_value=fake):
            self.assertEqual(
                self.mod.region_for_instance_id("mi-033c9569c7fb8b884"),
                "eu-west-2",
            )

    def test_region_for_ec2_pin_is_us_east_1(self) -> None:
        self.assertEqual(
            self.mod.region_for_instance_id("i-0e43099f831b03160"),
            "us-east-1",
        )


if __name__ == "__main__":
    unittest.main()
