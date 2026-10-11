"""Behavioral gates for hcloud data and identity mounts, without live mutations."""
import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import yaml

from ops.stage0.bluegreen_host_runtime import bindings, check_volume, patch_compose
from ops.stage0.test_deploy_via_ssm_bluegreen import _render, _extract_shell_function


class HostRuntimeTest(unittest.TestCase):
    def test_both_colors_receive_readonly_identity_without_secret_contents(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'aws').mkdir()
            (root / 'qa-secrets').mkdir()
            for path in (root / 'aws/credentials', root / 'qa-secrets/queue.dsn', root / 'qa-secrets/bundle.json'):
                path.write_text('secret-must-not-be-rendered')
                path.chmod(0o600)
            (root / 'qa-secrets/queue-ca.pem').write_text('synthetic-ca')
            env = {'QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE': '/run/secrets/tokenkey-qa/queue.dsn',
                   'QA_BUNDLE_STORAGE_CREDENTIALS_FILE': '/run/secrets/tokenkey-qa/bundle.json'}
            mounts, environment = bindings(root, env, True, uid=os.getuid())
            proc, _, remote = _render()
            self.assertEqual(proc.returncode, 0, proc.stderr)
            source = remote.split("<<'YAML'\n", 1)[1].split('\nYAML', 1)[0]
            result = yaml.safe_load(patch_compose(source, mounts, environment))
            self.assertNotIn('secret-must-not-be-rendered', json.dumps(result))
            for color in ('blue', 'green'):
                service = result['services']['tokenkey-' + color]
                actual = service['volumes'][1:]
                self.assertEqual({m['target'] for m in actual}, {'/aws', *env.values(), '/run/secrets/tokenkey-qa/queue-ca.pem'})
                self.assertTrue(all(m['read_only'] and not m['bind']['create_host_path'] for m in actual))
                self.assertIn('AWS_SHARED_CREDENTIALS_FILE=/aws/credentials', service['environment'])

    def test_missing_public_or_wrong_identity_paths_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertEqual(bindings(root, {}, False), ([], []))
            with self.assertRaises(ValueError):
                bindings(root, {}, True)
            (root / 'qa-secrets').mkdir()
            path = root / 'qa-secrets/bundle.json'
            path.write_text('synthetic')
            path.chmod(0o644)
            env = {'QA_BUNDLE_STORAGE_CREDENTIALS_FILE': '/run/secrets/tokenkey-qa/bundle.json'}
            with self.assertRaises(ValueError):
                bindings(root, env, False, uid=os.getuid())
            path.chmod(0o600)
            with self.assertRaises(ValueError):
                bindings(root, {**env, 'QA_BUNDLE_STORAGE_CREDENTIALS_FILE': '/etc/passwd'}, False)
            path.unlink()
            path.symlink_to('/etc/passwd')
            with self.assertRaises(ValueError):
                bindings(root, env, False, uid=os.getuid())

    def test_root_disk_and_unmounted_data_are_rejected(self):
        root = Path('/var/lib/tokenkey')
        def response(source='/dev/sdb', target=str(root), fstype='ext4'):
            return lambda *a, **kw: json.dumps({'filesystems': [{'source': source, 'target': target, 'fstype': fstype}]})
        with patch.object(Path, 'glob', return_value=[Path('/dev/sdb')]):
            check_volume(root, response())
            for run in (response(target='/'), response(source='/dev/root'), response(fstype='tmpfs')):
                with self.assertRaises(ValueError):
                    check_volume(root, run)
        def missing(*args, **kwargs):
            raise subprocess.CalledProcessError(1, args[0])
        with self.assertRaises(subprocess.CalledProcessError):
            check_volume(root, missing)

    def test_unmounted_prod_stops_before_host_writes(self):
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, proc.stderr)
        prefix = remote.split('# Serialize host mutations', 1)[0]
        helper = Path(__file__).with_name('bluegreen_host_runtime.py').read_bytes()
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            findmnt = root / 'findmnt'
            findmnt.write_text('#!/bin/sh\nexit 1\n')
            findmnt.chmod(0o755)
            marker_path = root / 'must-not-write'
            result = subprocess.run(['bash'], input=prefix + '\ntouch "$TEST_MARKER"\n', capture_output=True, text=True,
                                    env={**os.environ, 'PATH': str(root) + os.pathsep + os.environ['PATH'],
                                         'TAG': '1.8.99', 'DEPLOY_PROFILE': 'prod', 'HYBRID_PROD': 'true',
                                         'HOST_RUNTIME_B64': base64.b64encode(helper).decode(), 'TEST_MARKER': str(marker_path)})
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker_path.exists())
            self.assertIn('runtime prerequisite failed', result.stderr)

    def test_gateway_upgrade_cannot_switch_queue_backend_or_redirect_migrated_objects(self):
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, proc.stderr)
        function = _extract_shell_function(remote, 'assert_qa_provider_unchanged')
        contract = Path(__file__).with_name('qa-runtime-environment.sh').read_text()
        for current, desired, bucket, expected in (
            ('sqs', 'postgres', 'existing', 1), ('postgres', 'sqs', 'existing', 1),
            ('postgres', 'postgres', 'different', 1), ('postgres', 'postgres', 'existing', 0),
            ('', 'sqs', 'existing', 0),
        ):
            script = contract + '\n' + function + """
set -eu
die() { echo "$*" >&2; exit 1; }
env_get() {
  case "$1" in
    QA_BUNDLE_QUEUE_DRIVER) printf '%s' "$TEST_CURRENT" ;;
    QA_BUNDLE_STORAGE_BUCKET) printf existing ;;
  esac
}
assert_qa_provider_unchanged
printf allowed
"""
            result = subprocess.run(['bash'], input=script, capture_output=True, text=True,
                                    env={**os.environ, 'DEPLOY_PROFILE': 'prod', 'TEST_CURRENT': current,
                                         'QA_BUNDLE_QUEUE_DRIVER': desired, 'QA_BUNDLE_STORAGE_BUCKET': bucket,
                                         'QA_BUNDLE_STORAGE_BUCKET_SET': 'true'})
            self.assertEqual(result.returncode, expected, (current, desired, result.stderr))
            self.assertEqual(result.stdout, 'allowed' if expected == 0 else '')

    def test_postgres_renderer_accepts_file_and_rejects_ambiguous_queue(self):
        env = {'QA_BUNDLE_ENABLED': 'true', 'QA_BUNDLE_QUEUE_DRIVER': 'postgres',
               'QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE': '/run/secrets/tokenkey-qa/queue.dsn',
               'QA_BUNDLE_QUEUE_URL': '', 'QA_BUNDLE_STORAGE_DRIVER': 's3',
               'QA_BUNDLE_STORAGE_REGION': 'fsn1', 'QA_BUNDLE_STORAGE_BUCKET': 'qa-test',
               'QA_BUNDLE_STORAGE_PREFIX': 'user-qa'}
        proc, params, _ = _render(env_extra=env)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        # Execute the delivered assignment prefix to test quoting and actual
        # env propagation rather than matching the script's spelling.
        prefix = params['commands'][-1].rsplit(' /tmp/tokenkey-bluegreen-deploy.sh', 1)[0] + ' '
        observed = subprocess.run(['bash', '-c', prefix + "python3 -c 'import os; print(os.environ[\"QA_BUNDLE_QUEUE_DRIVER\"]); print(os.environ[\"QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE\"])'"],
                                  capture_output=True, text=True, env=os.environ)
        self.assertEqual(observed.returncode, 0, observed.stderr)
        self.assertEqual(observed.stdout.splitlines(), ['postgres', env['QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE']])
        for override in ({'QA_BUNDLE_QUEUE_URL': 'https://sqs.example/queue'},
                         {'QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE': ''}, {'QA_BUNDLE_QUEUE_DRIVER': 'unknown'},
                         {'QA_BUNDLE_STORAGE_ENDPOINT': 'bad\nINJECT=secret'}):
            failed, params, _ = _render(env_extra=env | override)
            self.assertNotEqual(failed.returncode, 0)
            self.assertIsNone(params)


if __name__ == '__main__':
    unittest.main()
