"""SSH selection/rendering must not invoke AWS or select an existing production host."""
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
spec = importlib.util.spec_from_file_location("domestic_target", HERE / "target.py")
target_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(target_module)
spec = importlib.util.spec_from_file_location("domestic_host", HERE / "verify-host.py")
host_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host_module)
spec = importlib.util.spec_from_file_location("domestic_ssh", HERE / "ssh-deploy.py")
ssh_module = importlib.util.module_from_spec(spec)
with patch.dict(sys.modules, {"target": target_module}):
    spec.loader.exec_module(ssh_module)
spec = importlib.util.spec_from_file_location("domestic_probe", HERE / "ssh-probe.py")
probe_module = importlib.util.module_from_spec(spec)
with patch.dict(sys.modules, {"target": target_module}):
    spec.loader.exec_module(probe_module)


class SSHDeployTests(unittest.TestCase):
    def test_probe_host_guard_fails_before_delivery(self):
        with patch.object(sys, 'argv', ['probe', '--target', 'zhupeng-prod', '--timeout-seconds', '60']), \
                patch.object(sys, 'stdin', io.StringIO('{"commands":["true"]}')), \
                patch.object(probe_module.transport, 'probe', side_effect=ValueError('wrong host')), \
                patch.object(probe_module.transport, 'ssh') as deliver:
            with self.assertRaisesRegex(ValueError, 'wrong host'):
                probe_module.main()
            deliver.assert_not_called()

    def test_shared_probe_dispatches_ssh_without_aws_or_instance_override(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            marker = root / 'aws-called'
            cli = root / 'aws'
            cli.write_text('#!/bin/sh\ntouch '+str(marker)+'\nexit 99\n')
            cli.chmod(0o700)
            python = root / 'python3'
            python.write_text('#!' + sys.executable + '\nimport sys,os,json\n'
                'if sys.argv[1].endswith("/ssh-probe.py"):\n'
                ' p=json.load(sys.stdin); assert p["commands"]\n'
                ' print(json.dumps({"ssh":True,"args":sys.argv[2:]}))\n'
                'else: os.execv(sys.executable,[sys.executable]+sys.argv[1:])\n')
            python.chmod(0o700)
            script = root / 'fixture-probe.sh'
            script.write_text('echo fixture\n')
            command = ['bash', str(ROOT / 'ops/observability/run-probe.sh'),
                       '--target', 'zhupeng-prod', '--script', str(script), '--timeout-seconds', '60']
            env = dict(os.environ, PATH=directory + ':' + os.environ['PATH'])
            result = subprocess.run(command, env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue(json.loads(result.stdout)['ssh'])
            self.assertFalse(marker.exists())
            result = subprocess.run(command + ['--expected-instance-id', 'i-022f2553503211775'],
                                    env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists())

    def test_failed_https_smoke_cannot_be_masked_by_container_listing(self):
        target = target_module.load_target("zhupeng-prod")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            marker = root / "docker-was-called"
            for name, body in {"curl": "exit 22", "docker": f"touch '{marker}'\nexit 0"}.items():
                path = root / name
                path.write_text("#!/bin/sh\n" + body + "\n")
                path.chmod(0o700)
            def execute(_target, command):
                return subprocess.run(["bash", "-c", command], check=True, capture_output=True,
                                      env=dict(os.environ, PATH=directory+":/usr/bin:/bin"))
            with patch.object(ssh_module, "probe"), patch.object(ssh_module, "ssh", side_effect=execute):
                with self.assertRaises(subprocess.CalledProcessError) as error:
                    ssh_module.smoke(target)
            self.assertEqual(error.exception.returncode, 22)
            self.assertFalse(marker.exists())

    def test_unknown_disabled_and_injected_target_rejected(self):
        with self.assertRaises(ValueError):
            target_module.load_target("prod")
        original = json.loads(target_module.INVENTORY.read_text())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "targets.json"
            for field, value in [("enabled", False), ("ssh_host", "node1; touch /tmp/wrong"),
                                 ("data_mount", "/"), ("tls", "acme")]:
                data = json.loads(json.dumps(original))
                data["targets"]["zhupeng-prod"][field] = value
                path.write_text(json.dumps(data))
                with self.subTest(field=field), self.assertRaises(ValueError):
                    target_module.load_target("zhupeng-prod", path)

    def test_wrong_machine_rejected_before_disk_commands(self):
        target = target_module.load_target("zhupeng-prod")
        with patch.object(Path, "read_text", return_value="different-machine"), patch.object(subprocess, "check_output") as command:
            with self.assertRaisesRegex(ValueError, "identity mismatch"):
                host_module.verify(target)
            command.assert_not_called()

    def test_wrong_volume_rejected(self):
        target = target_module.load_target("zhupeng-prod")
        with patch.object(Path, "read_text", return_value=target["machine_id"]), patch.object(subprocess, "check_output", return_value=json.dumps({"filesystems": [{"uuid": "wrong", "fstype": "ext4"}]})):
            with self.assertRaisesRegex(ValueError, "data disk"):
                host_module.verify(target)

    def test_ssh_render_has_prod_payload_without_hetzner_volume_or_aws_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.dict(os.environ, {"QA_CAPTURE_ENABLED": "true", "QA_ARCHIVE_STORAGE_BUCKET": "production-bucket",
                                         "TELEMETRY_ARCHIVE_ENABLED": "true", "STAGE0_BLUEGREEN_STAGE": "promote"}):
                env = ssh_module.render_environment(directory)
            self.assertNotIn("QA_ARCHIVE_STORAGE_BUCKET", env)
            self.assertNotIn("STAGE0_BLUEGREEN_STAGE", env)
            self.assertEqual(env["QA_CAPTURE_ENABLED"], "false")
            self.assertEqual(env["TELEMETRY_ARCHIVE_ENABLED"], "false")
            result = subprocess.run(["bash", str(ROOT / "ops/stage0/deploy_via_ssm_bluegreen.sh"), "1.8.292", "ssh-zhupeng-prod"], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            commands = json.loads((Path(directory) / "ssm-params.json").read_text())["commands"]
            launch = commands[-1]
            self.assertIn("HYBRID_PROD='false'", launch)
            self.assertIn("DEPLOY_PROFILE='prod'", launch)
            self.assertIn("QA_ARCHIVE_ENABLED='false'", launch)
            env.pop("STAGE0_RENDER_ONLY")
            result = subprocess.run(["bash", str(ROOT / "ops/stage0/deploy_via_ssm_bluegreen.sh"), "1.8.292", "ssh-zhupeng-prod"], env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("require render-only", result.stderr)


if __name__ == "__main__":
    unittest.main()
