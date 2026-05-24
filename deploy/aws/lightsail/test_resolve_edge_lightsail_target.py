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
    """Test resolver against the live lightsail matrix. We pick a deployable=true
    edge and a deployable=false edge DYNAMICALLY from the matrix so the tests
    don't break each time an edge gets flipped during a migration."""

    @classmethod
    def setUpClass(cls):
        cls.data = json.loads(MATRIX.read_text(encoding="utf-8"))
        targets = cls.data.get("targets", {})
        deployable = sorted(
            edge_id for edge_id, t in targets.items() if t.get("deployable") is True
        )
        planned = sorted(
            edge_id for edge_id, t in targets.items() if t.get("deployable") is False
        )
        cls.deployable_id = deployable[0] if deployable else None
        cls.planned_id = planned[0] if planned else None

    def test_deployable_resolves_without_allow_planned(self):
        if not self.deployable_id:
            self.skipTest("no deployable=true Lightsail edges in matrix")
        edge_id = self.deployable_id
        expected = self.data["targets"][edge_id]["instance_name"]
        resolved = run_resolver(edge_id, confirm_instance=expected)
        self.assertEqual(resolved["edge_id"], edge_id)
        self.assertEqual(resolved["instance_name"], expected)
        self.assertEqual(resolved["deployable"], "true")

    def test_planned_fails_without_allow_planned(self):
        if not self.planned_id:
            self.skipTest("no deployable=false Lightsail edges in matrix")
        proc = subprocess.run(
            [sys.executable, str(RESOLVER), "--edge-id", self.planned_id],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not deployable", proc.stderr)

    def test_planned_resolves_with_allow_planned(self):
        if not self.planned_id:
            self.skipTest("no deployable=false Lightsail edges in matrix")
        resolved = run_resolver(self.planned_id, allow_planned=True)
        self.assertEqual(resolved["edge_id"], self.planned_id)
        self.assertEqual(resolved["deployable"], "false")

    def test_confirm_instance_mismatch_fails(self):
        # Pick any edge (deployable or planned); deployable preferred so we
        # don't need --allow-planned.
        edge_id = self.deployable_id or self.planned_id
        if not edge_id:
            self.skipTest("matrix empty")
        args = [sys.executable, str(RESOLVER), "--edge-id", edge_id,
                "--confirm-instance", "wrong-name"]
        if edge_id == self.planned_id:
            args.append("--allow-planned")
        proc = subprocess.run(args, capture_output=True, text=True)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("confirm_instance mismatch", proc.stderr)

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
