#!/usr/bin/env python3
"""Unit tests for live vs Lightsail-only deployable edge id selection."""
from __future__ import annotations

import pathlib
import sys
import unittest

_HERE = pathlib.Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))

from edge_routing_matrix import (  # noqa: E402
    deployable_edge_ids,
    live_deployable_edge_ids,
)


class LiveDeployableEdgeIdsTest(unittest.TestCase):
    def test_prefers_hetzner_when_both_deployable(self) -> None:
        ls = {
            "uk1": {
                "deployable": True,
                "lightsail_region": "eu-west-2",
                "ssm_prefix": "/tokenkey/lightsail/uk1",
            },
            "us3": {
                "deployable": True,
                "lightsail_region": "us-east-2",
                "ssm_prefix": "/tokenkey/lightsail/us3",
            },
        }
        hz = {
            "uk1": {
                "deployable": True,
                "location": "fsn1",
                "server_type": "cax21",
                "ssm_prefix": "/tokenkey/hetzner/uk1",
                "instance_name": "tokenkey-edge-uk1-hz-cax21",
            },
            "us3": {
                "deployable": False,
                "location": "fsn1",
                "server_type": "cax21",
                "ssm_prefix": "/tokenkey/hetzner/us3",
                "instance_name": "tokenkey-edge-us3-hz-cax21",
            },
        }
        self.assertEqual(
            live_deployable_edge_ids(".", lightsail_targets=ls, hetzner_targets=hz),
            ["uk1", "us3"],
        )
        self.assertEqual(deployable_edge_ids(ls), ["uk1", "us3"])

    def test_omits_retired_lightsail_without_hetzner(self) -> None:
        ls = {
            "us3": {
                "deployable": False,
                "lightsail_region": "us-east-2",
                "ssm_prefix": "/tokenkey/lightsail/us3",
            },
            "us4": {
                "deployable": True,
                "lightsail_region": "us-west-2",
                "ssm_prefix": "/tokenkey/lightsail/us4",
            },
        }
        hz = {
            "us4": {
                "deployable": True,
                "location": "fsn1",
                "server_type": "cax21",
                "ssm_prefix": "/tokenkey/hetzner/us4",
                "instance_name": "tokenkey-edge-us4-hz-cax21",
            },
        }
        self.assertEqual(
            live_deployable_edge_ids(".", lightsail_targets=ls, hetzner_targets=hz),
            ["us4"],
        )


if __name__ == "__main__":
    unittest.main()
