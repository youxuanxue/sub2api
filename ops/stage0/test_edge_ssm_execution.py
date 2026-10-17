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
import edge_routing_matrix as routing_matrix  # noqa: E402
from edge_routing_matrix import load_live_targets, resolve_route_tab


class EdgeSsmExecutionTests(unittest.TestCase):
    def test_auto_prefers_deployable_hetzner(self) -> None:
        # Shipped matrices are prod-only; fixture a live Hetzner us4 for routing.
        hz_row = {
            "deployable": True,
            "location": "fsn1",
            "server_type": "cax21",
            "instance_name": "tokenkey-edge-us4-hz-cax21",
            "ssm_prefix": "/tokenkey/hetzner/us4",
            "domain": "api-us4.tokenkey.dev",
            "lightsail_region": "eu-west-2",
        }
        ls_row = {
            "deployable": False,
            "lightsail_region": "us-west-2",
            "ssm_prefix": "/tokenkey/lightsail/us4",
            "domain": "api-us4.tokenkey.dev",
        }
        with mock.patch.object(ese, "resolve_route_tab", return_value=("hetzner", "eu-west-2", None)), \
             mock.patch.object(ese, "load_hetzner_targets", return_value={"us4": hz_row}), \
             mock.patch.object(ese, "load_lightsail_targets", return_value={"us4": ls_row}), \
             mock.patch.object(
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
        self.assertEqual(ident.ssm_prefix, hz_row["ssm_prefix"])
        self.assertEqual(ident.domain, "api-us4.tokenkey.dev")
        get_param.assert_called_once_with(ident.region, hz_row["ssm_prefix"])

    def test_retired_identity_never_reads_ssm_parameters(self) -> None:
        for platform in ("lightsail", "hetzner"):
            with self.subTest(platform=platform), mock.patch.object(ese, "ssm_parameter_managed_instance_id") as get_param:
                with self.assertRaisesRegex(SystemExit, "retired"):
                    ese.resolve_edge_execution_identity(REPO_ROOT, "us4", platform=platform)
                get_param.assert_not_called()

    def test_explicit_lightsail_can_resolve_planned_target_only_when_opted_in(self) -> None:
        planned = {
            "deployable": False,
            "lightsail_region": "us-west-2",
            "ssm_prefix": "/tokenkey/lightsail/planned",
            "domain": "api-planned.tokenkey.dev",
        }
        with mock.patch.object(ese, "load_lightsail_targets", return_value={"planned": planned}), \
             mock.patch.object(routing_matrix, "load_lightsail_targets", return_value={"planned": planned}), \
             mock.patch.object(
                 ese,
                 "ssm_parameter_managed_instance_id",
                 return_value="mi-lightsail-planned",
             ) as get_param:
            ident = ese.resolve_edge_execution_identity(
                REPO_ROOT, "planned", platform="lightsail", allow_planned=True
            )
        self.assertEqual(ident.routing, "lightsail")
        self.assertEqual(ident.instance_id, "mi-lightsail-planned")
        self.assertTrue(ident.ssm_prefix.startswith("/tokenkey/lightsail/"))
        get_param.assert_called_once()
        self.assertEqual(get_param.call_args.args[1], ident.ssm_prefix)


if __name__ == "__main__":
    unittest.main()
