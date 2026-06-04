#!/usr/bin/env python3
"""Tests for ops/stage0/edge_admin_resolve_target.py — admin-target routing.

Covers the prod short-circuit (a fixed EC2/CFN target outside the edge matrices)
and confirms the existing edge resolution still works. Pure: reads the repo's
matrices for edges and hardcodes prod; no AWS calls. stdlib-only.
"""
from __future__ import annotations

import pathlib
import subprocess
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "edge_admin_resolve_target.py"
_REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]


def _resolve(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["python3", str(_SCRIPT), str(_REPO_ROOT), *args],
        capture_output=True, text=True, check=False,
    )


class EdgeAdminResolveTargetTest(unittest.TestCase):
    def test_prod_resolves_to_prod_stage0_ec2_stack(self) -> None:
        proc = _resolve("prod")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(proc.stdout, "ec2\tus-east-1\ttokenkey-prod-stage0\n")

    def test_prod_ignores_platform_preference(self) -> None:
        # prod is always EC2/CFN; an explicit --platform value must not change it.
        for pref in ("auto", "ec2", "lightsail"):
            with self.subTest(platform=pref):
                proc = _resolve("prod", pref)
                self.assertEqual(proc.returncode, 0, msg=proc.stderr)
                self.assertEqual(proc.stdout, "ec2\tus-east-1\ttokenkey-prod-stage0\n")

    def test_edge_resolution_still_works(self) -> None:
        # Sanity: a real deployable Lightsail edge still routes via the matrices,
        # proving the prod short-circuit did not shadow the edge path.
        proc = _resolve("uk1")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        mode, region, _stack = proc.stdout.rstrip("\n").split("\t")
        self.assertEqual(mode, "lightsail")
        self.assertTrue(region, "edge region should be non-empty")


if __name__ == "__main__":
    unittest.main()
