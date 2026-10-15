#!/usr/bin/env python3
"""Exercise admin target resolution without reaching AWS or reading credentials."""
from __future__ import annotations

import os
import json
import pathlib
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'ops/stage0/edge_admin_resolve_target.py'


class EdgeAdminResolveTargetTest(unittest.TestCase):
    def resolve(self, *args, edge_platform='hetzner'):
        with tempfile.TemporaryDirectory() as tmp:
            fixture = pathlib.Path(tmp) / 'repo'
            for platform, name in [('hetzner', 'deploy/hetzner/edge-targets-hetzner.json'),
                                   ('lightsail', 'deploy/aws/lightsail/edge-targets-lightsail.json')]:
                matrix = json.loads((ROOT / name).read_text())
                matrix['targets']['uk1']['deployable'] = platform == edge_platform
                target = fixture / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(json.dumps(matrix))
            stub = pathlib.Path(tmp) / 'aws'
            stub.write_text('''#!/usr/bin/env python3
import json, sys, os
args = sys.argv
if 'describe-instance-information' in args:
    print(json.dumps({'InstanceInformationList': [{'ComputerName': 'tokenkey-prod-hz-cax21', 'InstanceId': 'mi-0123456789abcdef0', 'PingStatus': 'Online'}]}))
elif 'get-parameter' in args:
    assert args[args.index('--name')+1] == '/tokenkey/' + os.environ['TEST_EDGE_PLATFORM'] + '/uk1/ssm_managed_instance_id'
    assert args[args.index('--region')+1] == 'eu-west-2'
    print('mi-11111111111111111')
else:
    raise AssertionError('unexpected AWS standby lookup: ' + repr(args))
''')
            stub.chmod(0o755)
            env = {**os.environ, 'PATH': f'{tmp}:{os.environ["PATH"]}', 'PROD_SSM_TARGET': 'auto',
                   'TEST_EDGE_PLATFORM': edge_platform}
            return subprocess.run(['python3', str(SCRIPT), str(fixture), *args], env=env,
                                  capture_output=True, text=True)

    def test_prod_defaults_to_hybrid_instance_and_ssm_region(self):
        proc = self.resolve('prod')
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout, 'hetzner\teu-west-2\tmi-0123456789abcdef0\n')

    def test_edge_uses_hybrid_region_not_hcloud_location(self):
        proc = self.resolve('uk1')
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout, 'hetzner\teu-west-2\tmi-11111111111111111\n')

    def test_invalid_prod_platform_fails_before_aws(self):
        proc = self.resolve('prod', 'lightsail')
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn('prod has no Lightsail target', proc.stderr)

    def test_edge_cutover_uses_active_lightsail_parameter(self):
        proc = self.resolve('uk1', edge_platform='lightsail')
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout, 'lightsail\teu-west-2\tmi-11111111111111111\n')

    def test_retired_edge_does_not_query_standby(self):
        proc = self.resolve('us3')
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn('not deployable', proc.stderr)


if __name__ == '__main__':
    unittest.main()
