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
    def test_preparation_target_rejects_online_operations_before_ssh(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(ssh_module, 'ssh') as remote:
                for operation in ('bootstrap', 'deploy', 'smoke-only'):
                    with self.subTest(operation=operation), patch.object(
                            sys, 'argv', ['dispatch', '--target', 'zhupeng-prod',
                                          '--operation', operation, '--tag', '1.8.302']):
                        with self.assertRaisesRegex(RuntimeError, 'preparation-only'):
                            ssh_module.main()
                remote.assert_not_called()

    def test_preparation_plan_does_not_probe_unverified_node1(self):
        with patch.object(ssh_module, 'probe') as remote, patch.object(
                sys, 'argv', ['dispatch', '--target', 'zhupeng-prod',
                              '--operation', 'plan', '--tag', '1.8.302']):
            ssh_module.main()
            remote.assert_not_called()

    def test_production_inventory_is_render_only_and_cannot_bootstrap(self):
        before = target_module.INVENTORY.read_bytes()
        data = target_module.production_inventory('zhupeng-prod', 'api.galaxywaveai.com')
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'pending.json'
            path.write_text(json.dumps(data))
            target = target_module.load_target('zhupeng-prod', path)
            self.assertEqual(target['frontend_url'], 'https://api.galaxywaveai.com')
            self.assertEqual(target['ingress'], 'ucloud-wireguard')
            with patch.object(ssh_module, 'probe') as probe, patch.object(ssh_module, 'ssh') as ssh:
                with self.assertRaisesRegex(ValueError, 'fresh bootstrap is forbidden'):
                    ssh_module.bootstrap(target, '1.8.302', Path(directory))
                probe.assert_not_called()
                ssh.assert_not_called()
        self.assertEqual(target_module.INVENTORY.read_bytes(), before)

    def test_public_host_checks_require_the_approval_and_candidate_mount_guard(self):
        target = target_module.production_inventory('zhupeng-prod', 'api.galaxywaveai.com')['targets']['zhupeng-prod']
        disk = json.dumps({'filesystems': [{'uuid': target['data_uuid'], 'fstype': 'ext4'}]})
        with patch.object(Path, 'read_text', return_value=target['machine_id']), \
                patch.object(Path, 'is_symlink', return_value=False), \
                patch.object(Path, 'exists', return_value=False), \
                patch.object(subprocess, 'check_output', return_value=disk), \
                patch.object(subprocess, 'run', side_effect=subprocess.CalledProcessError(1, 'guard')) as guard:
            with self.assertRaises(subprocess.CalledProcessError):
                host_module.verify(target)
            self.assertEqual(guard.call_args.args[0],
                             ['python3', '/usr/local/lib/tokenkey/node1_runtime_guard.py'])

    def test_probe_host_guard_fails_before_delivery(self):
        target = target_module.load_target('zhupeng-prod')
        target.update(preparation_only=False, inventory_status='verified')
        with patch.object(probe_module, 'load_target', return_value=target), \
                patch.object(sys, 'argv', ['probe', '--target', 'zhupeng-prod', '--timeout-seconds', '60']), \
                patch.object(sys, 'stdin', io.StringIO('{"commands":["true"]}')), \
                patch.object(probe_module.transport, 'probe', side_effect=ValueError('wrong host')), \
                patch.object(probe_module.transport, 'ssh') as deliver:
            with self.assertRaisesRegex(ValueError, 'wrong host'):
                probe_module.main()
            deliver.assert_not_called()

    def test_probe_rejects_unverified_preparation_target_before_ssh(self):
        with patch.object(sys, 'argv', ['probe', '--target', 'zhupeng-prod', '--timeout-seconds', '60']), \
                patch.object(sys, 'stdin', io.StringIO('{"commands":["true"]}')), \
                patch.object(probe_module.transport, 'probe') as remote_probe, \
                patch.object(probe_module.transport, 'ssh') as remote_ssh:
            with self.assertRaisesRegex(RuntimeError, 'inventory is verified'):
                probe_module.main()
            remote_probe.assert_not_called()
            remote_ssh.assert_not_called()

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

    def test_unverified_candidate_identity_cannot_be_marked_ready(self):
        original = json.loads(target_module.INVENTORY.read_text())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'targets.json'
            data = json.loads(json.dumps(original))
            data['targets']['zhupeng-prod']['candidate_identity_status'] = 'verified'
            path.write_text(json.dumps(data))
            with self.assertRaisesRegex(ValueError, 'identity must remain pending'):
                target_module.load_target('zhupeng-prod', path)

    def test_external_node1_capacity_is_pending_until_ssh_inventory(self):
        target = target_module.load_target('zhupeng-prod')
        self.assertEqual((target['source_cpu'], target['source_memory_gib'],
                          target['source_data_disk_gib']), (8, 8, 20))
        self.assertEqual((target['candidate_cpu'], target['candidate_memory_gib'],
                          target['candidate_data_disk_gib']), (None, None, None))
        self.assertEqual(target['candidate_provider'], 'home-server')
        self.assertEqual(target['candidate_ssh_port'], 2201)
        self.assertEqual(target['candidate_capacity_status'], 'pending-readonly-verification')

    def test_verified_candidate_capacity_cannot_be_lower_than_source(self):
        original = json.loads(target_module.INVENTORY.read_text())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'targets.json'
            data = json.loads(json.dumps(original))
            candidate = data['targets']['zhupeng-prod']
            candidate.update(candidate_capacity_status='verified', candidate_cpu=2,
                             candidate_memory_gib=2, candidate_data_disk_gib=0)
            path.write_text(json.dumps(data))
            with self.assertRaisesRegex(ValueError, 'below the UCloud source capacity'):
                target_module.load_target('zhupeng-prod', path)

    def test_verified_candidate_capacity_accepts_equal_or_larger_resources(self):
        original = json.loads(target_module.INVENTORY.read_text())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'targets.json'
            data = json.loads(json.dumps(original))
            candidate = data['targets']['zhupeng-prod']
            candidate.update(candidate_capacity_status='verified', candidate_cpu=8,
                             candidate_memory_gib=8, candidate_data_disk_gib=20)
            path.write_text(json.dumps(data))
            loaded = target_module.load_target('zhupeng-prod', path)
            self.assertEqual(loaded['candidate_capacity_status'], 'verified')

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
