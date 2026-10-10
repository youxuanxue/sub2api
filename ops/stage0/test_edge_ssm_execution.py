#!/usr/bin/env python3
"""Behavior tests for edge_ssm_execution Hetzner vs Lightsail resolution."""
from __future__ import annotations

import pathlib
import sys
import unittest
from unittest import mock

STAGE0 = pathlib.Path(__file__).resolve().parent
REPO_ROOT = STAGE0.parents[1]
if str(STAGE0) not in sys.path:
    sys.path.insert(0, str(STAGE0))

import edge_ssm_execution as ese  # noqa: E402


class EdgeSsmExecutionTests(unittest.TestCase):
    def test_auto_prefers_deployable_hetzner(self) -> None:
        with mock.patch.object(
            ese,
            "ssm_parameter_managed_instance_id",
            return_value="mi-hetzner-us4",
        ) as get_param:
            ident = ese.resolve_edge_execution_identity(
                REPO_ROOT, "us4", platform="auto"
            )
        self.assertEqual(ident.routing, "hetzner")
        self.assertEqual(ident.region, "eu-west-2")
        self.assertEqual(ident.instance_id, "mi-hetzner-us4")
        self.assertEqual(ident.ssm_prefix, "/tokenkey/hetzner/us4")
        self.assertEqual(ident.domain, "api-us4.tokenkey.dev")
        get_param.assert_called_once_with("eu-west-2", "/tokenkey/hetzner/us4")

    def test_explicit_lightsail_still_resolves_standby(self) -> None:
        with mock.patch.object(
            ese,
            "ssm_parameter_managed_instance_id",
            return_value="mi-lightsail-us4",
        ) as get_param:
            ident = ese.resolve_edge_execution_identity(
                REPO_ROOT, "us4", platform="lightsail"
            )
        self.assertEqual(ident.routing, "lightsail")
        self.assertEqual(ident.instance_id, "mi-lightsail-us4")
        self.assertTrue(ident.ssm_prefix.startswith("/tokenkey/lightsail/"))
        get_param.assert_called_once()
        self.assertEqual(get_param.call_args.args[1], ident.ssm_prefix)


if __name__ == "__main__":
    unittest.main()
