import json
import subprocess
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
RESOLVER = REPO_ROOT / "deploy/aws/lightsail/resolve-edge-lightsail-target.py"
MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"


def run_resolver(edge_id, confirm_instance="", allow_planned=False):
    cmd = [sys.executable, str(RESOLVER), "--edge-id", edge_id]
    if confirm_instance:
        cmd.extend(["--confirm-instance", confirm_instance])
    if allow_planned:
        cmd.append("--allow-planned")
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr.strip() or proc.stdout)
    out = {}
    for line in proc.stdout.splitlines():
        if "=" in line:
            k, v = line.split("=", 1)
            out[k] = v
    return out


class ResolveEdgeLightsailTargetTests(unittest.TestCase):
    """All four Lightsail targets are deployable=false by default — EC2 owns
    uk1/us1 today (DNS conflict guard); fra1/sg1 are planned PoC targets.
    `--allow-planned` is the explicit operator opt-in."""

    def test_uk1_planned_resolves_with_allow_planned(self):
        data = json.loads(MATRIX.read_text(encoding="utf-8"))
        expected = data["targets"]["uk1"]["instance_name"]
        resolved = run_resolver("uk1", confirm_instance=expected, allow_planned=True)
        self.assertEqual(resolved["edge_id"], "uk1")
        self.assertEqual(resolved["instance_name"], expected)
        self.assertEqual(resolved["lightsail_region"], "eu-west-2")
        self.assertEqual(resolved["deployable"], "false")

    def test_uk1_planned_fails_without_allow_planned(self):
        proc = subprocess.run(
            [sys.executable, str(RESOLVER), "--edge-id", "uk1"],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not deployable", proc.stderr)

    def test_confirm_instance_mismatch_fails(self):
        # --allow-planned to reach the confirm_instance check (deployable
        # check happens first; that's intentional fail-fast ordering).
        proc = subprocess.run(
            [sys.executable, str(RESOLVER), "--edge-id", "uk1",
             "--confirm-instance", "wrong-name", "--allow-planned"],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("confirm_instance mismatch", proc.stderr)

    def test_planned_fra1_fails_without_allow_planned(self):
        proc = subprocess.run(
            [sys.executable, str(RESOLVER), "--edge-id", "fra1"],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not deployable", proc.stderr)

    def test_planned_fra1_with_allow_planned(self):
        resolved = run_resolver("fra1", allow_planned=True)
        self.assertEqual(resolved["edge_id"], "fra1")
        self.assertEqual(resolved["lightsail_region"], "eu-central-1")
        self.assertEqual(resolved["deployable"], "false")

    def test_unknown_edge_id_fails(self):
        proc = subprocess.run(
            [sys.executable, str(RESOLVER), "--edge-id", "zz9"],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("unknown edge_id", proc.stderr)


if __name__ == "__main__":
    unittest.main()
