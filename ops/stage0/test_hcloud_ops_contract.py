#!/usr/bin/env python3
"""Migration regressions: live inventory, workflow routing and Ubuntu transport."""
from __future__ import annotations

import base64
import importlib.util
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'ops/stage0'))
from edge_routing_matrix import live_deployable_edge_ids, load_lightsail_targets, load_live_targets, resolve_route_tab


def load(relative):
    path = ROOT / relative
    sys.path.insert(0, str(path.parent))
    spec = importlib.util.spec_from_file_location(path.stem.replace('-', '_') + '_hcloud_test', path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class HcloudOpsContractTest(unittest.TestCase):
    def test_active_inventory_is_unambiguous(self):
        targets = load_live_targets(ROOT)
        self.assertEqual(sorted(targets), ['uk1', 'uk2', 'us4', 'us5'])
        for edge, row in targets.items():
            expected = 'hetzner' if row.get('location') else 'lightsail'
            self.assertEqual(resolve_route_tab(ROOT, edge)[0], expected)
        for edge in ['us3', 'us6']:
            with self.assertRaises(SystemExit):
                resolve_route_tab(ROOT, edge)
        with mock.patch('edge_routing_matrix.load_lightsail_targets', return_value={
            'x': {'deployable': True, 'lightsail_region': 'us-west-2', 'ssm_prefix': '/x'}
        }), mock.patch('edge_routing_matrix.load_hetzner_targets', return_value={
            'x': {'deployable': True, 'location': 'fsn1', 'server_type': 'cax21', 'instance_name': 'x', 'ssm_prefix': '/x'}}):
            with self.assertRaisesRegex(SystemExit, 'ambiguous'):
                live_deployable_edge_ids(ROOT)
            with self.assertRaisesRegex(SystemExit, 'ambiguous'):
                resolve_route_tab(ROOT, 'x')

    def test_account_import_and_model_probe_enumerate_same_live_fleet(self):
        relay = load('ops/accounts/edge_relay.py')
        probe = load('ops/pricing/refresh-servable-allowlist.py')
        expected = live_deployable_edge_ids(ROOT)
        self.assertEqual([row['edge_id'] for row in relay.list_deployable_edges()], expected)
        self.assertEqual(probe.deployable_edges(), expected)
        with mock.patch.object(probe.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, 'resolver')):
            with self.assertRaises(subprocess.CalledProcessError):
                probe.deployable_edges()

    def test_worker_workflow_resolves_active_platform_before_dispatch(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/deploy-gemini-web-worker.yml').read_text())
        steps = workflow['jobs']['worker']['steps']
        resolve = next(step for step in steps if step.get('id') == 'edge')
        for edge in live_deployable_edge_ids(ROOT):
            with tempfile.TemporaryDirectory() as tmp:
                output = pathlib.Path(tmp) / 'output'
                result = subprocess.run(['bash', '-c', resolve['run']], cwd=ROOT, capture_output=True, text=True,
                    env={**os.environ, 'OIDC_ROLE_ARN': 'test-role', 'INPUT_EDGE_ID': edge,
                         'INPUT_CONFIRM_INSTANCE': load_live_targets(ROOT)[edge]['instance_name'], 'GITHUB_OUTPUT': str(output)})
                self.assertEqual(result.returncode, 0, result.stderr)
                values = dict(line.split('=', 1) for line in output.read_text().splitlines())
                self.assertEqual(values['platform'], resolve_route_tab(ROOT, edge)[0])
                self.assertEqual(values['ssm_region'], load_live_targets(ROOT)[edge].get('lightsail_region', 'eu-west-2'))
                for step in steps:
                    if 'AWS_REGION' in step.get('env', {}):
                        self.assertEqual(step['env']['AWS_REGION'], '${{ steps.edge.outputs.ssm_region }}')

    def test_disk_cleanup_failure_never_reaches_lightsail_standby(self):
        import shlex
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            py = root / "python3"
            py.write_text("#!/bin/sh\ncase \"$1\" in *edge_ssm_execution.py) echo " + shlex.quote(json.dumps({
                "routing": "hetzner", "region": "eu-west-2", "instance_id": "mi-0123456789abcdef0",
                "domain": "api-uk1.tokenkey.dev"})) + ";; *) exec " + shlex.quote(sys.executable) + " \"$@\";; esac\n")
            aws = root / "aws"
            aws.write_text("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$AWS_CALLS\"\ncase \"$*\" in *Status*) echo Failed;; esac\nexit 1\n")
            py.chmod(0o755)
            aws.chmod(0o755)
            log = root / "calls"
            result = subprocess.run(["bash", str(ROOT / "ops/stage0/remediate-edge-disk-via-ssm.sh"), "--edge-id", "uk1"],
                cwd=ROOT, capture_output=True, text=True,
                env={**os.environ, "PATH": f"{tmp}:{os.environ['PATH']}", "AWS_CALLS": str(log), "STAGE0_SSM_TIMEOUT_SECONDS": "1"})
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("refusing fallback", result.stderr)
            calls = log.read_text()
            self.assertIn("ssm send-command", calls)
            self.assertNotIn("lightsail", calls)

    def test_qa_host_tools_keep_ssm_region_separate_from_aws_storage(self):
        for relative in ('ops/qa/prod_qa_maintenance.py', 'ops/qa/prod_qa_archive_closeout.py'):
            module = load(relative)
            with self.subTest(relative=relative), mock.patch.object(module._SSM, 'resolve_prod_identity',
                    return_value=('mi-0123456789abcdef0', 'eu-west-2')):
                instance = module._resolve_instance()
                calls = []
                def aws(args):
                    calls.append(args)
                    return {'Status': 'Success', 'ResponseCode': 0, 'StandardOutputContent': 'qa-result'}
                with mock.patch.object(module, '_aws_json', side_effect=aws):
                    self.assertEqual(module._poll('query-command', instance), 'qa-result')
                self.assertEqual(calls[0][calls[0].index('--region') + 1], 'eu-west-2')
                self.assertEqual(calls[0][calls[0].index('--instance-id') + 1], instance)
                if relative.endswith('prod_qa_archive_closeout.py'):
                    self.assertEqual(module.PROD_REGION, 'us-east-1')

    def test_oauth_guard_query_runs_under_ubuntu_shell(self):
        module = load('ops/anthropic/check-edge-oauth-stability.py')
        with tempfile.TemporaryDirectory() as tmp:
            sudo = pathlib.Path(tmp) / 'sudo'
            sudo.write_text('#!/bin/sh\ncat\n')
            sudo.chmod(0o755)
            result = None
            def fake_remote(args):
                nonlocal result
                if 'send-command' in args:
                    params = json.loads(args[args.index('--parameters') + 1])
                    result = subprocess.run(['dash', '-c', '\n'.join(params['commands'])],
                        env={**os.environ, 'PATH': f'{tmp}:{os.environ["PATH"]}'},
                        capture_output=True, text=True)
                    return 'query-command'
                if 'wait' in args:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    return ''
                return result.stdout
            with mock.patch.object(module, 'run_cmd', side_effect=fake_remote):
                command, output = module.run_remote_query({'region': 'eu-west-2'},
                    'mi-0123456789abcdef0', "SELECT 'quote and $literal';", comment='test')
            self.assertEqual(command, 'query-command')
            self.assertEqual(output, "SELECT 'quote and $literal';")

    def test_post_release_transports_run_under_dash_and_propagate_failure(self):
        execute = subprocess.run
        for relative, function in [('ops/anthropic/manage-anthropic-config.py', 'ssm_run_shell'),
                                   ('ops/pricing/manage-account-model-mapping-runtime.py', '_ssm_run_shell_b64_region')]:
            module = load(relative)
            for body, expected_code in [('cat >/dev/null; printf survived', 0), ('exit 7', 7)]:
                result = None
                def fake_aws(args, **kwargs):
                    nonlocal result
                    if 'send-command' in args:
                        params = json.loads(args[args.index('--parameters') + 1])
                        result = execute(['dash', '-c', '\n'.join(params['commands'])], input='', text=True, capture_output=True)
                        return 'test-command'
                    return json.dumps({'Status': 'Success' if result.returncode == 0 else 'Failed',
                        'ResponseCode': result.returncode, 'StandardOutputContent': result.stdout,
                        'StandardErrorContent': result.stderr})
                with self.subTest(relative=relative, body=body), mock.patch.object(module.subprocess, 'check_output', side_effect=fake_aws), mock.patch.object(module.subprocess, 'run'):
                    if function == 'ssm_run_shell':
                        out, _, ok, _ = module.ssm_run_shell('eu-west-2', 'mi-0123456789abcdef0', body, 'test')
                        self.assertEqual(ok, expected_code == 0)
                    else:
                        encoded = base64.b64encode(body.encode()).decode()
                        if expected_code:
                            with self.assertRaises(RuntimeError):
                                getattr(module, function)('eu-west-2', 'mi-0123456789abcdef0', encoded, 'test')
                            continue
                        out = getattr(module, function)('eu-west-2', 'mi-0123456789abcdef0', encoded, 'test')
                    self.assertEqual(result.returncode, expected_code)
                    if not expected_code:
                        self.assertEqual(out, 'survived')


if __name__ == '__main__':
    unittest.main()
