#!/usr/bin/env python3
"""Exercise the existing-prod installer without SSM, root, or systemd writes."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops/observability/remediate-hcloud-prod-host-alerts.sh"
SOURCE = ROOT / "deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh"
MATRIX = ROOT / "deploy/hetzner/prod-target-hetzner.json"


class HostAlertsInstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        # Exercise a Hcloud installation independently of the live migration phase.
        self.edge_matrix = self.root / 'edge-targets-hetzner.json'
        matrix = json.loads((ROOT / 'deploy/hetzner/edge-targets-hetzner.json').read_text())
        matrix['targets']['uk1']['deployable'] = True
        self.edge_matrix.write_text(json.dumps(matrix))
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.installed = self.root / "tokenkey-disk-metrics.sh"
        self.installed.write_text("#!/bin/bash\necho old-script\n")
        self.dropin = self.root / "drop-ins"
        self.dropin.mkdir()
        (self.dropin / "hetzner-volume.conf").write_text("old binding\n")
        (self.dropin / "unrelated.conf").write_text("preserve me\n")
        self.device = self.root / "volume-device"
        self.device.touch()
        self.trace = self.root / "systemctl.trace"
        self.env = {
            **os.environ,
            "PATH": f"{self.bin}:{os.environ['PATH']}",
            "TEST_HOST": json.loads(MATRIX.read_text())["target"]["instance_name"],
            "TEST_DEVICE": str(self.device),
            "TEST_INSTALLED": str(self.installed),
            "TEST_DROPIN": str(self.dropin),
            "TEST_TRACE": str(self.trace),
        }
        self.env.pop("APPLY", None)
        for name, body in {
            "hostname": 'echo "$TEST_HOST"',
            "mountpoint": 'exit "${TEST_MOUNT_FAILURE:-0}"',
            "findmnt": 'echo "$TEST_DEVICE"',
            "id": 'echo 0',
            "systemctl": '''printf '%s\\n' "$*" >> "$TEST_TRACE"
case "$1" in
 cat) echo '[Service]' ;;
 show)
  if [ "$4" = ExecStart ]; then
   printf '{ path=%s ; argv[]=%s ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\\n' "$TEST_INSTALLED" "$TEST_INSTALLED"
  else
   sed -n 's/^Environment=//p' "$TEST_DROPIN/hetzner-volume.conf"
  fi ;;
 is-enabled|is-active) exit 0 ;;
 daemon-reload) exit "${TEST_RELOAD_FAILURE:-0}" ;;
 *) exit 9 ;;
esac''',
        }.items():
            path = self.bin / name
            path.write_text("#!/bin/bash\nset -euo pipefail\n" + body + "\n")
            path.chmod(0o755)

    def run_installer(self, apply=False, **env):
        # Internal paths are rebound after sourcing; the executable accepts no
        # environment escape hatch for a different installation root or host.
        variables = {
            "ALERT_SOURCE": SOURCE,
            "PROD_MATRIX": MATRIX,
            "EDGE_MATRIX": self.edge_matrix,
            "ALERT_INSTALLED": self.installed,
            "DROPIN_DIR": self.dropin,
            "BACKUP_ROOT": self.root / "backups",
        }
        harness = f"source {shlex.quote(str(SCRIPT))}\n"
        harness += "\n".join(f"{key}={shlex.quote(str(value))}" for key, value in variables.items())
        harness += f"\nVOLUME_DEVICES=({shlex.quote(str(self.device))})\nmain\n"
        if apply:
            env["APPLY"] = "yes-hcloud-prod-host-alerts"
        return subprocess.run(["bash", "-c", harness], env={**self.env, **env}, capture_output=True, text=True)

    def snapshot(self):
        return {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file() and p != self.trace}

    def test_plan_does_not_write_or_execute_service(self):
        before = self.snapshot()
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("mode=plan", result.stdout)
        self.assertEqual(before, self.snapshot())
        self.assertNotIn("daemon-reload", self.trace.read_text())
        self.assertNotIn("start", self.trace.read_text().lower().replace("execstart", ""))

    def test_identity_and_mount_fail_before_mutation(self):
        for env in [{"TEST_HOST": "an-edge"}, {"TEST_MOUNT_FAILURE": "1"}, {"TEST_DEVICE": "/dev/root"}, {"APPLY": "1"}]:
            with self.subTest(env=env):
                before = self.snapshot()
                result = self.run_installer(**env)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(before, self.snapshot())

    def test_apply_uses_current_source_preserves_backup_and_is_repeatable(self):
        before = self.installed.read_bytes()
        for _ in range(2):
            result = self.run_installer(apply=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("mode=apply", result.stdout)
            self.assertEqual(self.installed.read_bytes(), SOURCE.read_bytes())
            self.assertIn("Environment=TOKENKEY_DATA_VOLUME_PATH=/var/lib/tokenkey", (self.dropin / "hetzner-volume.conf").read_text())
            self.assertEqual((self.dropin / "unrelated.conf").read_text(), "preserve me\n")
        backups = sorted((self.root / "backups").iterdir(), key=lambda p: p.stat().st_mtime_ns)
        self.assertEqual(len(backups), 2)
        self.assertEqual((backups[0] / "tokenkey-disk-metrics.sh").read_bytes(), before)
        self.assertEqual((backups[0] / "drop-ins/hetzner-volume.conf").read_text(), "old binding\n")
        self.assertEqual(self.trace.read_text().count("daemon-reload"), 2)
        self.assertNotIn("restart", self.trace.read_text())

    def test_reload_failure_exits_nonzero_and_prints_backup(self):
        result = self.run_installer(apply=True, TEST_RELOAD_FAILURE="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("backup=", result.stdout)
        self.assertNotIn("mode=apply", result.stdout)
        self.assertTrue(list((self.root / "backups").glob("*/tokenkey-disk-metrics.sh")))

    def test_edge_installs_only_alert_script_without_volume_or_timer_restart(self):
        import sys
        sys.path.insert(0, str(ROOT / "ops/stage0"))
        from edge_routing_matrix import materialize_hetzner_target
        matrix = json.loads(self.edge_matrix.read_text())
        host = materialize_hetzner_target("uk1", matrix, matrix["targets"]["uk1"])["instance_name"]
        result = self.run_installer(ALERT_TARGET="edge:uk1", TEST_HOST=host,
                                    APPLY="yes-hcloud-host-alerts", TEST_MOUNT_FAILURE="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.installed.read_bytes(), SOURCE.read_bytes())
        self.assertEqual((self.dropin / "hetzner-volume.conf").read_text(), "old binding\n")
        self.assertNotIn("restart", self.trace.read_text())

    def test_non_deployable_edge_fails_before_mutation(self):
        before = self.snapshot()
        result = self.run_installer(ALERT_TARGET="edge:us3", APPLY="yes-hcloud-host-alerts")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(before, self.snapshot())


if __name__ == "__main__":
    unittest.main()
