#!/usr/bin/env python3
"""Unit tests for resolve_prod_ssm_target (mocked AWS)."""
from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[2]
MOD_PATH = ROOT / "ops/stage0/resolve_prod_ssm_target.py"


def load_mod():
    spec = importlib.util.spec_from_file_location("resolve_prod_ssm_target", MOD_PATH)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class ResolveProdSsmTargetTest(unittest.TestCase):
    def setUp(self) -> None:
        self.mod = load_mod()

    def test_auto_defaults_fresh_aws_without_cloud_lookup(self) -> None:
        with mock.patch.dict("os.environ", {}, clear=True), mock.patch.object(
            self.mod.subprocess, "check_output", side_effect=AssertionError("unexpected AWS lookup")
        ), mock.patch.object(self.mod.subprocess, "run", side_effect=AssertionError("unexpected cutover lookup")):
            self.assertEqual(self.mod.resolve_mode("auto"), "aws-fresh")

    def test_auto_env_still_defaults_fresh_aws(self) -> None:
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "auto"}, clear=True):
            self.assertEqual(self.mod.resolve_mode("auto"), "aws-fresh")

    def test_explicit_standby_env(self) -> None:
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "aws"}, clear=True):
            self.assertEqual(self.mod.resolve_mode("auto"), "aws")

    def test_invalid_env_fails_closed(self) -> None:
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "typo"}, clear=True):
            with self.assertRaises(SystemExit):
                self.mod.resolve_mode("auto")

    def test_env_hetzner_wins(self) -> None:
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "hetzner"}):
            self.assertEqual(self.mod.resolve_mode("auto"), "hetzner")

    def test_explicit_aws_ignores_env(self) -> None:
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "hetzner"}):
            self.assertEqual(self.mod.resolve_mode("aws"), "aws")

    def test_retired_prod_modes_never_query_cloud(self) -> None:
        with mock.patch.object(self.mod, "aws_json", side_effect=AssertionError("must reject before AWS")):
            with self.assertRaises(SystemExit):
                self.mod.resolve_aws("tokenkey-prod-stage0")
            with self.assertRaisesRegex(SystemExit, "retired"):
                self.mod.resolve_hetzner(self.mod.DEFAULT_MATRIX)

    def test_explicit_fresh_candidate_never_uses_old_stack(self) -> None:
        inventory = json.loads(self.mod.FRESH_MATRIX.read_text())
        row = inventory["prod"]
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "aws-fresh"}, clear=True):
            self.assertEqual(self.mod.resolve_mode("auto"), "aws-fresh")
        instance = {"InstanceId": row["instance_id"], "PingStatus": "Online", "ComputerName": row["instance_name"]}
        with mock.patch.object(self.mod, "aws_json", side_effect=AssertionError("unexpected stack lookup")), \
             mock.patch.object(self.mod, "list_instance_information", return_value=[instance]):
            out = self.mod.resolve_fresh_aws()
            self.assertEqual(out["instance_id"], row["instance_id"])
            instance["InstanceId"] = "i-0e43099f831b03160"
            with self.assertRaises(SystemExit):
                self.mod.resolve_fresh_aws()

    def test_resolve_hetzner_by_computer_name(self) -> None:
        matrix = {
            "target": {
                "instance_name": "tokenkey-prod-hz-cax21",
                "ssm_region": "eu-west-2",
                "domain": "api.tokenkey.dev",
            }
        }
        info = {
            "InstanceInformationList": [
                {
                    "InstanceId": "mi-033c9569c7fb8b884",
                    "ComputerName": "tokenkey-prod-hz-cax21",
                    "PingStatus": "Online",
                }
            ]
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "m.json"
            path.write_text(json.dumps({"target": matrix["target"]}), encoding="utf-8")
            with mock.patch.object(self.mod, "aws_json", return_value=info):
                out = self.mod.resolve_hetzner(path)
        self.assertEqual(out["instance_id"], "mi-033c9569c7fb8b884")
        self.assertEqual(out["id"], "mi-033c9569c7fb8b884")
        self.assertEqual(out["ssm_region"], "eu-west-2")
        self.assertEqual(out["deploy_profile"], "prod")
        self.assertEqual(out["api_url"], "https://api.tokenkey.dev")
        self.assertEqual(out["browser_origin"], "https://tokenkey.dev")

    def test_resolve_hetzner_paginates_instance_information(self) -> None:
        matrix = {
            "target": {
                "instance_name": "tokenkey-prod-hz-cax21",
                "ssm_region": "eu-west-2",
                "domain": "api.tokenkey.dev",
            }
        }
        pages = [
            {
                "InstanceInformationList": [
                    {"InstanceId": "mi-aaaaaaaaaaaaaaaaa", "ComputerName": "other", "PingStatus": "Online"}
                ],
                "NextToken": "page-2",
            },
            {
                "InstanceInformationList": [
                    {
                        "InstanceId": "mi-033c9569c7fb8b884",
                        "ComputerName": "tokenkey-prod-hz-cax21",
                        "PingStatus": "Online",
                    }
                ]
            },
        ]

        def fake_aws(args: list[str]) -> dict:
            if "--next-token" in args:
                return pages[1]
            return pages[0]

        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "m.json"
            path.write_text(json.dumps({"target": matrix["target"]}), encoding="utf-8")
            with mock.patch.object(self.mod, "aws_json", side_effect=fake_aws):
                out = self.mod.resolve_hetzner(path)
        self.assertEqual(out["instance_id"], "mi-033c9569c7fb8b884")

    def test_hybrid_missing_offline_or_ambiguous_never_falls_back(self) -> None:
        target = self.mod.load_hetzner_matrix(self.mod.DEFAULT_MATRIX)
        row = {"ComputerName": target["instance_name"], "InstanceId": "mi-0123456789abcdef0", "PingStatus": "Online"}
        for rows in ([], [{**row, "PingStatus": "ConnectionLost"}], [row, row]):
            with self.subTest(rows=rows), mock.patch.object(self.mod, "load_hetzner_matrix", return_value={**target, "retired": False}), mock.patch.object(self.mod, "list_instance_information", return_value=rows):
                with self.assertRaises(SystemExit):
                    self.mod.resolve_hetzner(self.mod.DEFAULT_MATRIX)

    def test_resolve_aws_rejects_mi(self) -> None:
        rows = [
            {"OutputKey": "InstanceId", "OutputValue": "mi-033c9569c7fb8b884"},
            {"OutputKey": "ApiUrl", "OutputValue": "https://api.tokenkey.dev"},
        ]
        with mock.patch.object(self.mod, "aws_json", return_value=rows):
            with self.assertRaises(SystemExit):
                self.mod.resolve_aws("tokenkey-prod-stage0")



if __name__ == "__main__":
    unittest.main()
