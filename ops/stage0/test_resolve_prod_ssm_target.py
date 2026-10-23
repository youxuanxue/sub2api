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

    def test_auto_fails_closed_when_tencent_not_live(self) -> None:
        with mock.patch.object(self.mod, "tencent_matrix_is_live_prod", return_value=False), mock.patch.dict(
            "os.environ", {}, clear=True
        ), mock.patch.object(
            self.mod.subprocess, "check_output", side_effect=AssertionError("unexpected AWS lookup")
        ), mock.patch.object(self.mod.subprocess, "run", side_effect=AssertionError("unexpected cutover lookup")):
            with self.assertRaises(SystemExit):
                self.mod.resolve_mode("auto")

    def test_auto_env_auto_uses_matrix_live_gate(self) -> None:
        with mock.patch.object(self.mod, "tencent_matrix_is_live_prod", return_value=False), mock.patch.dict(
            "os.environ", {"PROD_SSM_TARGET": "auto"}, clear=True
        ):
            with self.assertRaises(SystemExit):
                self.mod.resolve_mode("auto")
        with mock.patch.object(self.mod, "tencent_matrix_is_live_prod", return_value=True), mock.patch.dict(
            "os.environ", {"PROD_SSM_TARGET": "auto"}, clear=True
        ):
            self.assertEqual(self.mod.resolve_mode("auto"), "tencent-usw-candidate")

    def test_explicit_standby_env(self) -> None:
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "aws"}, clear=True):
            self.assertEqual(self.mod.resolve_mode("auto"), "aws")

    def test_invalid_env_fails_closed(self) -> None:
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "typo"}, clear=True):
            with self.assertRaises(SystemExit):
                self.mod.resolve_mode("auto")

    def test_env_hetzner_selects_retired_mode(self) -> None:
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "hetzner"}):
            self.assertEqual(self.mod.resolve_mode("auto"), "hetzner")

    def test_explicit_aws_ignores_env(self) -> None:
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "hetzner"}):
            self.assertEqual(self.mod.resolve_mode("aws"), "aws")

    def test_retired_aws_mode_never_queries_cloud(self) -> None:
        with mock.patch.object(self.mod, "aws_json", side_effect=AssertionError("must reject before AWS")):
            with self.assertRaises(SystemExit):
                self.mod.resolve_aws("tokenkey-prod-stage0")

    def test_retired_hetzner_mode_never_queries_cloud(self) -> None:
        with mock.patch.object(self.mod, "list_instance_information", side_effect=AssertionError("must reject")):
            with self.assertRaises(SystemExit):
                self.mod.resolve_hetzner()

    def test_retired_aws_fresh_never_queries_cloud(self) -> None:
        # Tokyo was retired 2026-10-23: no override, no inventory, no SSM lookup.
        self.assertFalse(hasattr(self.mod, "FRESH_MATRIX"))
        for env in ({}, {"ALLOW_FROZEN_TOKYO_TARGET": "1"}):
            with self.subTest(env=env), \
                 mock.patch.dict("os.environ", env, clear=True), \
                 mock.patch.object(self.mod, "aws_json", side_effect=AssertionError("no AWS")), \
                 mock.patch.object(self.mod, "list_instance_information", side_effect=AssertionError("no AWS")):
                with self.assertRaises(SystemExit):
                    self.mod.resolve_fresh_aws()

    def test_list_instance_information_paginates(self) -> None:
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
                        "InstanceId": "mi-080ed76dc6d97d8b5",
                        "Name": "tokenkey-usw-rehearsal-20261021",
                        "PingStatus": "Online",
                    }
                ]
            },
        ]

        def fake_aws(args: list[str]) -> dict:
            if "--next-token" in args:
                return pages[1]
            return pages[0]

        with mock.patch.object(self.mod, "aws_json", side_effect=fake_aws):
            rows = self.mod.list_instance_information("eu-west-2")
        self.assertEqual(len(rows), 2)
        self.assertEqual(rows[1]["InstanceId"], "mi-080ed76dc6d97d8b5")

    def test_resolve_aws_rejects_mi(self) -> None:
        rows = [
            {"OutputKey": "InstanceId", "OutputValue": "mi-033c9569c7fb8b884"},
            {"OutputKey": "ApiUrl", "OutputValue": "https://api.tokenkey.dev"},
        ]
        with mock.patch.object(self.mod, "aws_json", return_value=rows):
            with self.assertRaises(SystemExit):
                self.mod.resolve_aws("tokenkey-prod-stage0")

    def test_auto_selects_tencent_when_matrix_deployable(self) -> None:
        with mock.patch.object(self.mod, "tencent_matrix_is_live_prod", return_value=False):
            with mock.patch.dict("os.environ", {}, clear=True):
                with self.assertRaises(SystemExit):
                    self.mod.resolve_mode("auto")
        with mock.patch.object(self.mod, "tencent_matrix_is_live_prod", return_value=True):
            with mock.patch.dict("os.environ", {}, clear=True):
                self.assertEqual(self.mod.resolve_mode("auto"), "tencent-usw-candidate")
        with mock.patch.dict("os.environ", {"PROD_SSM_TARGET": "aws-fresh"}, clear=True):
            self.assertEqual(self.mod.resolve_mode("auto"), "aws-fresh")

    def test_resolve_tencent_usw_candidate_pre_cutover(self) -> None:
        matrix = {
            "target": {
                "resolvable": True,
                "deployable": False,
                "cutover_authorized": False,
                "computer_name": "tokenkey-usw-rehearsal-20261021",
                "ssm_region": "eu-west-2",
                "expected_ssm_instance_id": "mi-080ed76dc6d97d8b5",
                "directed_domain": "tencent-usw-candidate.tokenkey.invalid",
                "volume_mount": "/srv/tokenkey-rehearsal",
                "volume_platform": "tencent-luks",
            }
        }
        info = [{
            "InstanceId": "mi-080ed76dc6d97d8b5",
            "ComputerName": "localhost.localdomain",
            "Name": "tokenkey-usw-rehearsal-20261021",
            "PingStatus": "Online",
        }]
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "m.json"
            path.write_text(json.dumps(matrix), encoding="utf-8")
            with mock.patch.object(self.mod, "list_instance_information", return_value=info):
                out = self.mod.resolve_tencent_usw_candidate(path)
        self.assertEqual(out["target"], "tencent-usw-candidate")
        self.assertEqual(out["deploy_profile"], "tencent-usw-candidate")
        self.assertEqual(out["instance_id"], "mi-080ed76dc6d97d8b5")
        self.assertEqual(out["api_url"], "https://tencent-usw-candidate.tokenkey.invalid")
        self.assertEqual(out["cutover_authorized"], "false")
        self.assertNotEqual(out["deploy_profile"], "prod")

    def test_resolve_tencent_usw_live_prod(self) -> None:
        matrix = {
            "target": {
                "resolvable": True,
                "deployable": True,
                "cutover_authorized": True,
                "cutover_completed": True,
                "computer_name": "tokenkey-usw-rehearsal-20261021",
                "ssm_region": "eu-west-2",
                "expected_ssm_instance_id": "mi-080ed76dc6d97d8b5",
                "api_domain": "api.tokenkey.dev",
                "volume_mount": "/srv/tokenkey-rehearsal",
                "volume_platform": "tencent-luks",
            }
        }
        info = [{
            "InstanceId": "mi-080ed76dc6d97d8b5",
            "ComputerName": "localhost.localdomain",
            "Name": "tokenkey-usw-rehearsal-20261021",
            "PingStatus": "Online",
        }]
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "m.json"
            path.write_text(json.dumps(matrix), encoding="utf-8")
            with mock.patch.object(self.mod, "list_instance_information", return_value=info):
                out = self.mod.resolve_tencent_usw_candidate(path)
        self.assertEqual(out["target"], "tencent-usw-prod")
        self.assertEqual(out["deploy_profile"], "prod")
        self.assertEqual(out["api_url"], "https://api.tokenkey.dev")
        self.assertEqual(out["cutover_authorized"], "true")
        self.assertEqual(out["instance_id"], "mi-080ed76dc6d97d8b5")

    def test_tencent_pre_cutover_rejects_live_domain_and_authorized(self) -> None:
        base = {
            "resolvable": True,
            "deployable": False,
            "cutover_authorized": False,
            "computer_name": "tokenkey-usw-rehearsal-20261021",
            "ssm_region": "eu-west-2",
            "expected_ssm_instance_id": "mi-080ed76dc6d97d8b5",
            "directed_domain": "tencent-usw-candidate.tokenkey.invalid",
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "m.json"
            for bad in (
                {**base, "directed_domain": "api.tokenkey.dev"},
                {**base, "cutover_authorized": True},
                {**base, "resolvable": False},
            ):
                path.write_text(json.dumps({"target": bad}), encoding="utf-8")
                with self.assertRaises(SystemExit):
                    self.mod.resolve_tencent_usw_candidate(path)


if __name__ == "__main__":
    unittest.main()
