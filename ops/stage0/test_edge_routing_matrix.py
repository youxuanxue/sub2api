#!/usr/bin/env python3
"""Unit tests for live vs Lightsail-only deployable edge id selection."""
from __future__ import annotations

import json
import pathlib
import sys
import tempfile
import unittest

_HERE = pathlib.Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))

from edge_routing_matrix import (  # noqa: E402
    EXPECTED_LIVE_EDGE_IDS,
    deployable_edge_ids,
    live_deployable_edge_ids,
    validate_live_fleet,
)


class LiveDeployableEdgeIdsTest(unittest.TestCase):
    def test_shipped_matrices_are_prod_only_empty_edges(self):
        root = _HERE.parents[1]
        self.assertEqual(EXPECTED_LIVE_EDGE_IDS, frozenset())
        self.assertEqual(validate_live_fleet(root), [])
        self.assertEqual(live_deployable_edge_ids(root), [])

    def test_any_deployable_row_rejected_until_contract_expands(self):
        root = _HERE.parents[1]
        hz = json.loads((root / "deploy/hetzner/edge-targets-hetzner.json").read_text())
        ls = json.loads((root / "deploy/aws/lightsail/edge-targets-lightsail.json").read_text())
        with tempfile.TemporaryDirectory() as tmp:
            fixture = pathlib.Path(tmp)

            def write():
                for name, data in (
                    ("hetzner/edge-targets-hetzner.json", hz),
                    ("aws/lightsail/edge-targets-lightsail.json", ls),
                ):
                    path = fixture / "deploy" / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text(json.dumps(data))

            write()
            self.assertEqual(validate_live_fleet(fixture), [])

            ls["targets"]["uk1"]["deployable"] = True
            write()
            with self.assertRaisesRegex(ValueError, "invalid active lightsail edge: uk1"):
                validate_live_fleet(fixture)

            ls["targets"]["uk1"]["deployable"] = False
            hz["targets"]["uk1"]["deployable"] = True
            write()
            with self.assertRaisesRegex(ValueError, "invalid active hetzner edge: uk1"):
                validate_live_fleet(fixture)

            hz["targets"]["uk1"]["deployable"] = True
            ls["targets"]["uk1"]["deployable"] = True
            write()
            # expected live set is empty → any deployable row fails before
            # the ambiguous-platform check inside live_deployable_edge_ids.
            with self.assertRaisesRegex(ValueError, "invalid active"):
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
        self.assertEqual(
            live_deployable_edge_ids(".", lightsail_targets=ls, hetzner_targets=hz),
            ["us4"],
        )


if __name__ == "__main__":
    unittest.main()
