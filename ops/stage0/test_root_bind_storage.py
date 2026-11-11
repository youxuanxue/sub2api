"""Fail-closed storage identity and delivery regressions; no production writes."""
import base64
import json
import os
import subprocess
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch

from ops.stage0.bluegreen_host_runtime import check_volume, volume_contract
from ops.stage0.test_deploy_via_ssm_bluegreen import _render, _extract_shell_function

UUID = '11111111-2222-3333-4444-555555555555'
ROOT = Path('/var/lib/tokenkey')
COMPAT = Path('/srv/tokenkey-rehearsal')


class RootBindStorageTest(unittest.TestCase):
    def check(self, override=None, same=True, empty=False):
        rows = {
            '/': {'target': '/', 'source': '/dev/vda2', 'fstype': 'ext4', 'uuid': UUID, 'fsroot': '/'},
            str(ROOT): {'target': '/', 'source': '/dev/vda2', 'fstype': 'ext4', 'uuid': UUID, 'fsroot': '/'},
            str(COMPAT): {'target': str(COMPAT), 'source': '/dev/vda2[/var/lib/tokenkey]',
                          'fstype': 'ext4', 'uuid': UUID, 'fsroot': str(ROOT)},
        }
        if override:
            key, fields = override
            rows[key].update(fields)
        def run(args, **kwargs):
            return json.dumps({'filesystems': [rows[args[3]]]})
        with patch.object(Path, 'is_symlink', return_value=False), \
             patch.object(Path, 'is_dir', return_value=True), \
             patch.object(Path, 'iterdir', side_effect=lambda: iter([] if empty else ['retained'])), \
             patch('ops.stage0.bluegreen_host_runtime.os.path.samefile', return_value=same), \
             patch('ops.stage0.bluegreen_host_runtime.private_file'):
            return check_volume(ROOT, run, platform='tencent-root-bind', root_uuid=UUID)

    def test_canonical_root_and_same_directory_bind_pass(self):
        self.assertIsNone(self.check())

    def test_wrong_uuid_device_mount_or_empty_state_rejected(self):
        for key, fields in [
            ('/', {'uuid': 'different'}),
            (str(ROOT), {'source': '/dev/mapper/tokenkey_recovery'}),
            (str(ROOT), {'source': '/dev/vdb2'}),
            (str(ROOT), {'target': str(ROOT)}),
            (str(COMPAT), {'target': '/'}),
            (str(COMPAT), {'fsroot': '/other-copy'}),
            (str(COMPAT), {'fstype': 'tmpfs'}),
        ]:
            with self.subTest(key=key, fields=fields), self.assertRaises(ValueError):
                self.check((key, fields))
        for kwargs in ({'same': False}, {'empty': True}):
            with self.subTest(kwargs=kwargs), self.assertRaises(ValueError):
                self.check(**kwargs)

    def test_root_mode_requires_pinned_identity_and_luks_default_is_unchanged(self):
        self.assertEqual(volume_contract({})['platform'], 'tencent-luks')
        with self.assertRaises(ValueError):
            volume_contract({'volume_platform': 'tencent-root-bind'})
        with self.assertRaises(ValueError):
            volume_contract({'volume_mapper': '../bad'})
        contract = volume_contract({'volume_platform': 'tencent-root-bind', 'volume_root_uuid': UUID})
        self.assertEqual(contract['root_uuid'], UUID)

    def test_hybrid_delivery_matches_live_matrix_and_guard_preserves_contract(self):
        matrix = json.loads((Path(__file__).resolve().parents[2] /
                             'deploy/tencent/candidate-usw-20261022.json').read_text())['target']
        proc, params, remote = _render(instance_id=matrix['expected_ssm_instance_id'])
        self.assertEqual(proc.returncode, 0, proc.stderr)
        command = params['commands'][-1]
        import shlex
        fields = dict(item.split('=', 1) for item in shlex.split(command)[:-1])
        contract = json.loads(base64.b64decode(fields['VOLUME_CONTRACT_B64']))
        self.assertEqual(contract, volume_contract(matrix))
        function = _extract_shell_function(remote, 'install_bluegreen_systemd_unit')
        for identity in (contract, volume_contract({'volume_platform': 'tencent-root-bind', 'volume_root_uuid': UUID})):
            encoded = base64.b64encode(json.dumps(identity).encode()).decode()
            with tempfile.TemporaryDirectory() as directory:
                sandbox = Path(directory)
                (sandbox / 'usr/local/bin').mkdir(parents=True)
                (sandbox / 'etc/systemd/system').mkdir(parents=True)
                script = function + r"""
sudo() {
  local -a args=()
  local arg
  for arg in "$@"; do
    if [[ "$arg" = /* ]]; then args+=("$SANDBOX$arg"); else args+=("$arg"); fi
  done
  if [[ "${args[0]}" = systemctl ]]; then return 0; fi
  command "${args[@]}"
}
log() { :; }
install_bluegreen_systemd_unit
"""
                result = subprocess.run(['bash'], input=script, text=True, capture_output=True,
                    env={**os.environ, 'SANDBOX': directory, 'HYBRID_PROD': 'true',
                         'HOST_RUNTIME_B64': fields['HOST_RUNTIME_B64'], 'VOLUME_CONTRACT_B64': encoded})
                self.assertEqual(result.returncode, 0, result.stderr)
                for unit in ('docker', 'tokenkey'):
                    guard = (sandbox / f'etc/systemd/system/{unit}.service.d/tokenkey-volume.conf').read_text()
                    self.assertEqual(guard, '[Unit]\nRequiresMountsFor=/var/lib/tokenkey /srv/tokenkey-rehearsal\n'
                        '[Service]\nExecStartPre=/usr/bin/python3 /usr/local/lib/tokenkey/bluegreen_host_runtime.py '
                        '--check-volume --contract-b64 ' + encoded + '\n')

    def test_unknown_hybrid_host_cannot_receive_a_storage_contract(self):
        proc, params, _ = _render(instance_id='mi-00000000000000000')
        self.assertNotEqual(proc.returncode, 0)
        self.assertIsNone(params)
        self.assertIn('storage contract instance mismatch', proc.stderr)


if __name__ == '__main__':
    unittest.main()
