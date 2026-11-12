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

from ops.stage0.bluegreen_host_runtime import bindings, check_volume, patch_compose, volume_contract
from ops.stage0.test_deploy_via_ssm_bluegreen import _render, _extract_shell_function


class HostRuntimeTest(unittest.TestCase):
    def test_both_colors_allow_smaller_memory_without_changing_legacy_defaults(self):
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, proc.stderr)
        source = remote.split("<<'YAML'\n", 1)[1].split('\nYAML', 1)[0]
        services = yaml.safe_load(source)['services']
        for color in ('blue', 'green'):
            service = services['tokenkey-' + color]
            self.assertEqual(service['mem_limit'], '${TOKENKEY_APP_MEMORY_LIMIT:-8g}')
            self.assertEqual(service['memswap_limit'], service['mem_limit'])
            self.assertIn('GOMEMLIMIT=${TOKENKEY_APP_GOMEMLIMIT:-6GiB}', service['environment'])

    def test_ordinary_root_requires_pinned_identity_and_private_environment(self):
        root = Path('/var/lib/tokenkey')
        uuid = '12345678-1234-1234-1234-123456789abc'
        row = dict(source='/dev/vda1', target='/', fstype='ext4', uuid=uuid, fsroot='/')
        def response(**changes):
            return lambda *a, **kw: json.dumps({'filesystems': [{**row, **changes}]})
        with patch.object(Path, 'resolve', lambda p: p), patch.object(Path, 'is_dir', return_value=True), \
             patch('ops.stage0.bluegreen_host_runtime.private_file') as private:
            check_volume(root, response(), platform='tencent-root', root_uuid=uuid)
            private.assert_called_once_with(root / '.env', 0)
            for change in ({'uuid': 'wrong'}, {'target': str(root)}, {'fsroot': str(root)},
                           {'source': '/dev/mapper/tokenkey_recovery'}, {'fstype': 'tmpfs'}):
                with self.subTest(change=change), self.assertRaises(ValueError):
                    check_volume(root, response(**change), platform='tencent-root', root_uuid=uuid)
            with self.assertRaises(ValueError):
                check_volume(root, response(), platform='tencent-root')
        with patch.object(Path, 'resolve', return_value=Path('/tmp/elsewhere')):
            with self.assertRaises(ValueError):
                check_volume(root, response(), platform='tencent-root', root_uuid=uuid)
        with self.assertRaises(ValueError):
            volume_contract({'volume_platform': 'tencent-root', 'volume_root_uuid': 'unbound'})
        with self.assertRaises(ValueError):
            volume_contract({'volume_mapper': 'x;touch /tmp/unsafe'})

    def test_candidate_manifest_is_pinned_and_persists_startup_guard(self):
        iid = 'mi-123456789abcdef01'
        with tempfile.TemporaryDirectory() as tmp:
            matrix = Path(tmp) / 'candidate.json'
            matrix.write_text(json.dumps({'target': {
                'expected_ssm_instance_id': iid, 'volume_platform': 'tencent-root',
                'volume_root_uuid': '12345678-1234-1234-1234-123456789abc'}}))
            proc, params, remote = _render(iid, env_extra={'STAGE0_TARGET_MATRIX': str(matrix)})
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn('VOLUME_CONTRACT_B64=', json.dumps(params))
            self.assertIn('CONTROL_ALIAS_B64=', json.dumps(params))
            self.assertIn('--check-volume --contract-b64 ${VOLUME_CONTRACT_B64}', remote)
            self.assertIn('for unit in docker tokenkey', remote)
            proc, _, _ = _render('mi-abcdef01234567890', env_extra={'STAGE0_TARGET_MATRIX': str(matrix)})
            self.assertNotEqual(proc.returncode, 0)
            self.assertIn('contract instance mismatch', proc.stderr)

    def test_root_boot_alias_waits_for_http_and_fails_closed(self):
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, proc.stderr)
        script = remote.split("<<'BOOT_ALIAS'\n", 1)[1].split('\nBOOT_ALIAS', 1)[0]
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'active-color').write_text('blue\n')
            alias = root / 'alias.sh'
            alias.write_text('echo alias >> "$TEST_CALLS"\n')
            docker = root / 'docker'
            docker.write_text('''#!/bin/bash
echo "$*" >> "$TEST_CALLS"
n=$(wc -l < "$TEST_CALLS")
[[ "${FAIL_ALWAYS:-0}" != 1 && "$n" -gt 2 ]]
''')
            docker.chmod(0o755)
            runnable = script.replace('ROOT=/var/lib/tokenkey', 'ROOT=' + str(root)).replace(
                '/usr/local/lib/tokenkey/sync-control-alias-on-host.sh', str(alias))
            env = {**os.environ, 'PATH': str(root) + ':' + os.environ['PATH'], 'TEST_CALLS': str(root / 'calls')}
            result = subprocess.run(['bash'], input='sleep() { :; }\n' + runnable,
                                    env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = (root / 'calls').read_text().splitlines()
            self.assertEqual(len(calls), 5)
            self.assertEqual(calls[3], 'alias')
            self.assertIn('http://tokenkey:8080/health', calls[4])
            (root / 'calls').unlink()
            result = subprocess.run(['bash'], input='sleep() { :; }\n' + runnable,
                                    env={**env, 'FAIL_ALWAYS': '1'}, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(len((root / 'calls').read_text().splitlines()), 60)
            self.assertIn('did not become ready', result.stderr)

    def test_redis_startup_passes_persistence_flags_and_literal_password(self):
        source = Path(__file__).resolve().parents[2] / 'deploy/aws/stage0/docker-compose.yml'
        command = yaml.safe_load(source.read_text())['services']['redis']['command']
        # Compose unescapes $$ before invoking the shell; never interpolate secrets into its source.
        command = [part.replace('$$', '$') for part in command]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = root / 'redis-server'
            fake.write_text('#!/usr/bin/env python3\nimport json,sys\nprint(json.dumps(sys.argv[1:]))\n')
            fake.chmod(0o755)
            for password in ('', 'literal $value " quote; $(false)'):
                result = subprocess.run(command, capture_output=True, text=True,
                    env={**os.environ, 'PATH': str(root) + ':' + os.environ['PATH'], 'REDISCLI_AUTH': password})
                self.assertEqual(result.returncode, 0, result.stderr)
                expected = ['--save', '60', '1', '--appendonly', 'yes', '--appendfsync', 'everysec']
                if password: expected += ['--requirepass', password]
                self.assertEqual(json.loads(result.stdout), expected)

    def test_both_colors_receive_readonly_identity_without_secret_contents(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'aws').mkdir()
            path = root / 'aws/credentials'
            path.write_text('secret-must-not-be-rendered')
            path.chmod(0o600)
            mounts, environment = bindings(root, True, uid=os.getuid())
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
            self.assertEqual(bindings(root, False), ([], []))
            # Hybrid without /aws: empty mounts (matches live legacy tokenkey).
            self.assertEqual(bindings(root, True), ([], []))
            aws = root / 'aws'
            aws.symlink_to('/tmp')
            with self.assertRaises(ValueError):
                bindings(root, True)
            aws.unlink()
            aws.mkdir()
            with self.assertRaises(ValueError):
                bindings(root, True, uid=os.getuid())

    def test_hybrid_without_aws_dir_skips_identity_mount(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            mounts, environment = bindings(root, True, uid=os.getuid())
            self.assertEqual(mounts, [])
            self.assertEqual(environment, [])
            proc, _, remote = _render()
            self.assertEqual(proc.returncode, 0, proc.stderr)
            source = remote.split("<<'YAML'\n", 1)[1].split('\nYAML', 1)[0]
            result = yaml.safe_load(patch_compose(source, mounts, environment))
            for color in ('blue', 'green'):
                service = result['services']['tokenkey-' + color]
                self.assertNotIn('/aws', {m.get('target') for m in service.get('volumes', []) if isinstance(m, dict)})
                self.assertNotIn('AWS_SHARED_CREDENTIALS_FILE=/aws/credentials', service.get('environment', []))

    def test_root_disk_and_unmounted_data_are_rejected(self):
        root = Path('/var/lib/tokenkey')
        def response(source='/dev/sdb', target=str(root), fstype='ext4'):
            return lambda *a, **kw: json.dumps({'filesystems': [{'source': source, 'target': target, 'fstype': fstype}]})
        with patch.object(Path, 'glob', return_value=[Path('/dev/sdb')]):
            check_volume(root, response(), platform='hetzner')
            for run in (response(target='/'), response(source='/dev/root'), response(fstype='tmpfs')):
                with self.assertRaises(ValueError):
                    check_volume(root, run, platform='hetzner')
        def missing(*args, **kwargs):
            raise subprocess.CalledProcessError(1, args[0])
        with self.assertRaises(subprocess.CalledProcessError):
            check_volume(root, missing, platform='hetzner')

    def test_tencent_luks_mapper_accepted_and_hetzner_identity_refused(self):
        root = Path('/srv/tokenkey-rehearsal')
        mapper = '/dev/mapper/tokenkey_recovery'

        def response(source=mapper, target=str(root), fstype='ext4'):
            return lambda *a, **kw: json.dumps({
                'filesystems': [{'source': source, 'target': target, 'fstype': fstype}]
            })

        def identity_realpath(path, *args, **kwargs):
            return str(path)

        with patch.object(Path, 'glob', return_value=[]), \
             patch('ops.stage0.bluegreen_host_runtime.os.path.realpath', side_effect=identity_realpath), \
             patch.object(Path, 'resolve', lambda self, strict=False: Path(str(self))):
            check_volume(root, response(), platform='tencent-luks')
            with self.assertRaises(ValueError):
                check_volume(root, response(source='/dev/mapper/other'), platform='tencent-luks')
            with self.assertRaises(ValueError):
                check_volume(root, response(), platform='hetzner')

        def same_dm(path, *args, **kwargs):
            return '/dev/dm-1'

        # A source that also resolves as a Hetzner volume id must be rejected on tencent.
        with patch.object(Path, 'glob', return_value=[Path('/dev/disk/by-id/scsi-0HC_Volume_1')]), \
             patch('ops.stage0.bluegreen_host_runtime.os.path.realpath', side_effect=same_dm), \
             patch.object(Path, 'resolve', lambda self, strict=False: Path('/dev/dm-1')):
            with self.assertRaises(ValueError):
                check_volume(root, response(source='/dev/dm-1'), platform='tencent-luks')

    def test_unsupported_platform_fails_closed(self):
        root = Path('/var/lib/tokenkey')

        def response(*a, **kw):
            return json.dumps({'filesystems': [
                {'source': '/dev/sdb', 'target': str(root), 'fstype': 'ext4'}
            ]})

        with patch.object(Path, 'glob', return_value=[Path('/dev/sdb')]):
            with self.assertRaises(ValueError):
                check_volume(root, response, platform='aws-ebs')

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
            'backup_env', 'restore_env_if_safe', 'cleanup_on_exit', 'assert_post_cutover_environment',
        ))
        for stage, layout in (('deploy', 'legacy'), ('deploy', 'blue'), ('prepare', 'blue')):
            with self.subTest(stage=stage, layout=layout), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                original = 'API_DOMAIN=api.example.test\nTOKENKEY_IMAGE=repo:1.8.98\n'
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
# Restore-on-failure is under test here; the edge contract has its own tests.
assert_edge_profile_environment() { :; }
sudo() { if [[ "$1" = docker ]]; then return 0; fi; command "$@"; }
log() { :; }
die() { echo "$*" >&2; exit 1; }
prepare_live_paths() { :; }
read_active_color() { [[ "$TEST_LAYOUT" = legacy ]] || printf blue; }
caddy_active_color() { read_active_color; }
assert_active_route_consistent() { :; }
container_image() { printf repo:1.8.98; }
image_repo() { printf repo; }
other_color() { printf green; }
color_container() { printf 'tokenkey-%s' "$1"; }
env_set() { :; }
env_get() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -1; }
ensure_profile_environment() {
  printf 'API_DOMAIN=api.example.test\nSERVER_FRONTEND_URL=https://api.example.test\n' > "$ENV_FILE"
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
