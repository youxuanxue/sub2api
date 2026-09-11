import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/stage0/replay-prod-release.py"


def run(tmp_path, obs=None):
    manifest = tmp_path / "manifest.json"
    manifest.write_text(json.dumps({"users": ["u1"], "models": ["m1"], "protocols": ["chat"], "samples": ["s1"]}))
    observations = tmp_path / "obs.json"
    observations.write_text(json.dumps(obs or {"active_color": "blue", "caddy_hash": "abc", "target_color": "green", "target_tag": "1.2.3", "listener": "127.0.0.1", "isolated_db": True, "isolated_redis": True}))
    out = tmp_path / "out"
    return subprocess.run([sys.executable, str(SCRIPT), "--tag", "1.2.3", "--prepared-receipt", "a" * 64, "--capture-manifest", str(manifest), "--observations", str(observations), "--out", str(out)], capture_output=True, text=True)


def test_replay_writes_pending_receipt(tmp_path):
    result = run(tmp_path)
    assert result.returncode == 0
    receipt = json.loads((tmp_path / "out/replay-receipt.json").read_text())
    assert receipt["verdict"] == "green"
    assert receipt["cutover"] is False
    assert receipt["approval_pending"] is True


def test_replay_fails_on_cutover(tmp_path):
    result = run(tmp_path, {"active_color": "blue", "caddy_hash": "abc", "target_color": "green", "target_tag": "1.2.3", "listener": "127.0.0.1", "isolated_db": True, "isolated_redis": True, "caddy_changed": True})
    assert result.returncode != 0
