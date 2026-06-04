#!/usr/bin/env python3
"""Tests for Gate C (scripts/checks/workflow-edge-coverage.py).

Drives the real module against temp fixtures by patching its path constants, so
the YAML parsing and matrix logic are exercised end-to-end. stdlib + pyyaml.
"""
from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest import mock

import yaml

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "workflow-edge-coverage.py"
_spec = importlib.util.spec_from_file_location("workflow_edge_coverage", _MOD_PATH)
wec = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(wec)


def _matrix(deployable_ids: list[str]) -> str:
    return json.dumps({"targets": {eid: {"deployable": True} for eid in deployable_ids}})


def _workflow(input_name: str, options: list[str]) -> str:
    # safe_dump writes the key "on" literally; on reload YAML 1.1 turns it back
    # into boolean True — exactly the real-workflow shape the check handles.
    return yaml.safe_dump(
        {
            "name": "T",
            "on": {"workflow_dispatch": {"inputs": {input_name: {"type": "choice", "options": options}}}},
            "jobs": {"x": {"runs-on": "ubuntu-latest", "steps": [{"run": "true"}]}},
        },
        sort_keys=False,
    )


class WorkflowEdgeCoverageTest(unittest.TestCase):
    def _run(self, *, ec2: list[str], lightsail: list[str], wf_options: list[str],
             input_name: str = "edge_id", option_prefix: str = "",
             required_set: str = "ec2-deployable", opt_out: list[dict] | None = None) -> int:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            (root / "deploy/aws/stage0").mkdir(parents=True)
            (root / "deploy/aws/lightsail").mkdir(parents=True)
            (root / ".github/workflows").mkdir(parents=True)
            ec2_path = root / "deploy/aws/stage0/edge-targets.json"
            ls_path = root / "deploy/aws/lightsail/edge-targets-lightsail.json"
            ec2_path.write_text(_matrix(ec2))
            ls_path.write_text(_matrix(lightsail))
            wf_rel = ".github/workflows/wf.yml"
            (root / wf_rel).write_text(_workflow(input_name, wf_options))
            reg = root / "registry.json"
            reg.write_text(json.dumps({"workflows": {wf_rel: {
                "input": input_name, "option_prefix": option_prefix,
                "required_set": required_set, "opt_out_edges": opt_out or [],
            }}}))
            with mock.patch.object(wec, "REPO_ROOT", root), \
                 mock.patch.object(wec, "REGISTRY", reg), \
                 mock.patch.object(wec, "EC2_MATRIX", ec2_path), \
                 mock.patch.object(wec, "LIGHTSAIL_MATRIX", ls_path):
                return wec.main()

    def test_covered_passes(self) -> None:
        self.assertEqual(self._run(ec2=["us1"], lightsail=[], wf_options=["fra1", "us1"]), 0)

    def test_new_deployable_edge_missing_from_options_fails(self) -> None:
        # us9 added to the matrix but not to the workflow options -> drift.
        self.assertEqual(self._run(ec2=["us1", "us9"], lightsail=[], wf_options=["us1"]), 1)

    def test_opt_out_covers_missing_edge(self) -> None:
        self.assertEqual(
            self._run(ec2=["us1", "us9"], lightsail=[], wf_options=["us1"],
                      opt_out=[{"id": "us9", "reason": "pilot only"}]),
            0,
        )

    def test_stale_opt_out_fails(self) -> None:
        # us9 opted out but no longer deployable -> registry must be cleaned.
        self.assertEqual(
            self._run(ec2=["us1"], lightsail=[], wf_options=["us1"],
                      opt_out=[{"id": "us9", "reason": "pilot only"}]),
            1,
        )

    def test_opt_out_without_reason_fails(self) -> None:
        self.assertEqual(
            self._run(ec2=["us1", "us9"], lightsail=[], wf_options=["us1"],
                      opt_out=[{"id": "us9", "reason": "  "}]),
            1,
        )

    def test_prefixed_option_pg_dump_shape(self) -> None:
        # edge id us1 must appear as option 'edge-us1' when option_prefix='edge-'.
        self.assertEqual(
            self._run(ec2=["us1"], lightsail=[], wf_options=["prod", "edge-us1", "all"],
                      input_name="target", option_prefix="edge-", required_set="all-deployable"),
            0,
        )


if __name__ == "__main__":
    unittest.main()
