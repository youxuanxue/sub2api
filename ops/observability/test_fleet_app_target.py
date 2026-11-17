#!/usr/bin/env python3
"""Tests for fleet_app_target.py multi-app resolution."""
from __future__ import annotations

import json
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops/observability/fleet_app_target.py"


def run(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["python3", str(SCRIPT), *args],
        capture_output=True,
        text=True,
        check=False,
        cwd=ROOT,
    )


class FleetAppTargetTest(unittest.TestCase):
    def test_prod_lists_both_apps_with_ssm(self) -> None:
        proc = run("--site", "prod", "--list")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        apps = json.loads(proc.stdout)
        names = {a["name"] for a in apps}
        self.assertEqual(names, {"app-old", "app-cand"})
        for app in apps:
            self.assertTrue(app["ssm_instance_id"].startswith("mi-"))

    def test_prod_node_kv(self) -> None:
        proc = run("--site", "prod", "--node", "app-cand", "--format", "kv")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("name=app-cand", proc.stdout)
        self.assertIn("ssm_instance_id=mi-000c0168439c243a8", proc.stdout)

    def test_unknown_node_refused(self) -> None:
        proc = run("--site", "prod", "--node", "nope")
        self.assertEqual(proc.returncode, 1)
        self.assertIn("unknown --node", proc.stderr)

    def test_cn_prod_all_apps_fails_without_app_b_ssm(self) -> None:
        """Fail closed: app-b has no Hybrid id, so fleet fan-out cannot pretend to cover it."""
        proc = run("--site", "cn-prod", "--list")
        self.assertEqual(proc.returncode, 2, proc.stderr)
        self.assertIn("app-b", proc.stderr)
        self.assertIn("ssm_instance_id", proc.stderr)

    def test_cn_prod_app_a_ok(self) -> None:
        proc = run("--site", "cn-prod", "--node", "app-a", "--format", "kv")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("name=app-a", proc.stdout)
        self.assertIn("ssm_instance_id=mi-06568963b4b3c3239", proc.stdout)


if __name__ == "__main__":
    unittest.main()
