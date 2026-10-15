#!/usr/bin/env python3
"""Unit tests for live vs Lightsail-only deployable edge id selection."""
from __future__ import annotations

import pathlib
import json
import tempfile
import itertools
import sys
import unittest

_HERE = pathlib.Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))

from edge_routing_matrix import (  # noqa: E402
    validate_live_fleet,
    deployable_edge_ids,
    live_deployable_edge_ids,
)


class LiveDeployableEdgeIdsTest(unittest.TestCase):
    def test_all_migration_orders_and_invalid_fleets(self):
        root = _HERE.parents[1]
        hz = json.loads((root / "deploy/hetzner/edge-targets-hetzner.json").read_text())
        ls = json.loads((root / "deploy/aws/lightsail/edge-targets-lightsail.json").read_text())
        edges = ("uk1", "uk2", "us4", "us5")
        with tempfile.TemporaryDirectory() as tmp:
            fixture = pathlib.Path(tmp)
            def write():
                for name, data in (("hetzner/edge-targets-hetzner.json", hz),
                                   ("aws/lightsail/edge-targets-lightsail.json", ls)):
                    path = fixture / "deploy" / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text(json.dumps(data))
            for states in itertools.product((False, True), repeat=len(edges)):
                for edge, aws in zip(edges, states):
                    hz["targets"][edge]["deployable"] = not aws
                    ls["targets"][edge]["deployable"] = aws
                write()
                self.assertEqual(validate_live_fleet(fixture), list(edges))
            ls["targets"]["uk1"]["deployable"] = False
            write()
            with self.assertRaisesRegex(ValueError, "missing=.*uk1"):
                validate_live_fleet(fixture)
            ls["targets"]["uk1"]["deployable"] = True
            hz["targets"]["uk1"]["deployable"] = True
            write()
            with self.assertRaisesRegex(SystemExit, "ambiguous"):
                validate_live_fleet(fixture)
            hz["targets"]["uk1"]["deployable"] = False
            ls["targets"]["us3"]["deployable"] = True
            write()
            with self.assertRaisesRegex(ValueError, "invalid active lightsail edge: us3"):
                validate_live_fleet(fixture)
            ls["targets"]["us3"]["deployable"] = False
            ls["targets"]["uk1"]["ssm_prefix"] = ""
            write()
            with self.assertRaisesRegex(ValueError, "invalid active lightsail edge: uk1"):
                validate_live_fleet(fixture)

    def test_rejects_ambiguous_active_platforms(self) -> None:
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
        with self.assertRaisesRegex(SystemExit, "ambiguous"):
            live_deployable_edge_ids(".", lightsail_targets=ls, hetzner_targets=hz)
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
        hz["us4"]["deployable"] = False
        self.assertEqual(live_deployable_edge_ids(".", lightsail_targets=ls, hetzner_targets=hz), ["us4"])


if __name__ == "__main__":
    unittest.main()
