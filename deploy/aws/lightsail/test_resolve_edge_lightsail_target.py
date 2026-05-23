import json
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
RESOLVER = REPO_ROOT / "deploy/aws/lightsail/resolve-edge-lightsail-target.py"
MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"


def run_resolver(edge_id: str, confirm_instance: str = "", allow_planned: bool = False) -> dict:
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


def test_uk1_deployable_resolves():
    data = json.loads(MATRIX.read_text(encoding="utf-8"))
    expected = data["targets"]["uk1"]["instance_name"]
    resolved = run_resolver("uk1", confirm_instance=expected)
    assert resolved["edge_id"] == "uk1"
    assert resolved["instance_name"] == expected
    assert resolved["lightsail_region"] == "eu-west-2"
    assert resolved["deployable"] == "true"


def test_confirm_instance_mismatch_fails():
    proc = subprocess.run(
        [sys.executable, str(RESOLVER), "--edge-id", "uk1", "--confirm-instance", "wrong-name"],
        capture_output=True,
        text=True,
    )
    assert proc.returncode != 0
    assert "confirm_instance mismatch" in proc.stderr


def test_planned_fra1_fails_without_allow_planned():
    proc = subprocess.run(
        [sys.executable, str(RESOLVER), "--edge-id", "fra1"],
        capture_output=True,
        text=True,
    )
    assert proc.returncode != 0
    assert "not deployable" in proc.stderr


def test_planned_fra1_with_allow_planned():
    resolved = run_resolver("fra1", allow_planned=True)
    assert resolved["edge_id"] == "fra1"
    assert resolved["lightsail_region"] == "eu-central-1"
    assert resolved["deployable"] == "false"


if __name__ == "__main__":
    test_uk1_deployable_resolves()
    test_confirm_instance_mismatch_fails()
    test_planned_fra1_fails_without_allow_planned()
    test_planned_fra1_with_allow_planned()
    print("ok: all resolve-edge-lightsail-target tests passed")
