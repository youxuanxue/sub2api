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
            for path in (root / 'aws/credentials', root / 'qa-secrets/raw.json'):
                path.write_text('secret-must-not-be-rendered')
                path.chmod(0o600)
            env = {'QA_ARCHIVE_STORAGE_CREDENTIALS_FILE': '/run/secrets/tokenkey-qa/raw.json'}
            mounts, environment = bindings(root, env, True, uid=os.getuid())
            proc, _, remote = _render()
            self.assertEqual(proc.returncode, 0, proc.stderr)
            source = remote.split("<<'YAML'\n", 1)[1].split('\nYAML', 1)[0]
            result = yaml.safe_load(patch_compose(source, mounts, environment))
            self.assertNotIn('secret-must-not-be-rendered', json.dumps(result))
            for color in ('blue', 'green'):
                service = result['services']['tokenkey-' + color]
                actual = service['volumes'][1:]
                self.assertEqual({m['target'] for m in actual}, {'/aws'})
                self.assertTrue(all(m['read_only'] and not m['bind']['create_host_path'] for m in actual))
                self.assertIn('AWS_SHARED_CREDENTIALS_FILE=/aws/credentials', service['environment'])

    def test_missing_public_or_wrong_identity_paths_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertEqual(bindings(root, {}, False), ([], []))
            with self.assertRaises(ValueError):
                bindings(root, {}, True)
            (root / 'qa-secrets').mkdir()
            path = root / 'qa-secrets/raw.json'
            path.write_text('synthetic')
            path.chmod(0o644)
            env = {'QA_ARCHIVE_STORAGE_CREDENTIALS_FILE': '/run/secrets/tokenkey-qa/raw.json'}
            with self.assertRaises(ValueError):
                bindings(root, env, False, uid=os.getuid(), qa_maintenance=True)
            path.chmod(0o600)
            with self.assertRaises(ValueError):
                bindings(root, {**env, 'QA_ARCHIVE_STORAGE_CREDENTIALS_FILE': '/etc/passwd'}, False, qa_maintenance=True)
            path.unlink()
            path.symlink_to('/etc/passwd')
            with self.assertRaises(ValueError):
                bindings(root, env, False, uid=os.getuid(), qa_maintenance=True)

    def test_only_maintenance_receives_private_raw_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            env = {'QA_ARCHIVE_STORAGE_CREDENTIALS_FILE': '/run/secrets/tokenkey-qa/raw.json'}
            self.assertEqual(bindings(root, env, False), ([], []))
            (root / 'qa-secrets').mkdir()
            raw = root / 'qa-secrets/raw.json'
            raw.write_text('synthetic-raw-identity')
            raw.chmod(0o600)
            mounts, environment = bindings(root, env, False, uid=os.getuid(), qa_maintenance=True)
            self.assertEqual(mounts, [{'type': 'bind', 'source': str(raw),
                                     'target': env['QA_ARCHIVE_STORAGE_CREDENTIALS_FILE'],
                                     'read_only': True, 'bind': {'create_host_path': False}}])
            self.assertEqual(environment, [])

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



    def test_failed_candidate_restores_environment_before_profile_changes(self):
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, proc.stderr)
        functions = '\n'.join(_extract_shell_function(remote, name) for name in (
            'run_bluegreen_stage', 'ensure_legacy_cutover', 'deploy_target_color',
            'backup_env', 'restore_env_if_safe', 'cleanup_on_exit',
        ))
        for stage, layout in (('deploy', 'legacy'), ('deploy', 'blue'), ('prepare', 'blue')):
            with self.subTest(stage=stage, layout=layout), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                original = 'QA_ARCHIVE_STORAGE_CREDENTIALS_FILE=\nTOKENKEY_IMAGE=repo:1.8.98\n'
                (root / '.env').write_text(original)
                script = 'set -euo pipefail\n' + functions + '''
ENV_FILE="$ROOT/.env"
PREPARED_FILE="$ROOT/prepared.json"
ENV_BACKUP=""
CUTOVER_COMMITTED=0
ROUTE_SWITCHED=0
TARGET_CONTAINER=""
LEGACY_MIGRATED=0
TAG=1.8.99
sudo() { if [[ "$1" = docker ]]; then return 0; fi; command "$@"; }
log() { :; }
die() { echo "$*" >&2; exit 1; }
read_active_color() { [[ "$TEST_LAYOUT" = legacy ]] || printf blue; }
caddy_active_color() { read_active_color; }
assert_active_route_consistent() { :; }
container_image() { printf repo:1.8.98; }
image_repo() { printf repo; }
other_color() { printf green; }
color_container() { printf 'tokenkey-%s' "$1"; }
env_set() { :; }
ensure_profile_environment() {
  printf 'QA_ARCHIVE_STORAGE_CREDENTIALS_FILE=/run/secrets/tokenkey-qa/raw.json\n' > "$ENV_FILE"
}
# A candidate prerequisite failure precedes Docker and cutover.
write_bluegreen_compose() { return 73; }
trap cleanup_on_exit EXIT
run_bluegreen_stage
'''
                result = subprocess.run(['bash'], input=script, capture_output=True, text=True,
                                        env={**os.environ, 'ROOT': tmp, 'STAGE': stage, 'TEST_LAYOUT': layout})
                self.assertEqual(result.returncode, 73, result.stderr)
                self.assertEqual((root / '.env').read_text(), original)


if __name__ == '__main__':
    unittest.main()
