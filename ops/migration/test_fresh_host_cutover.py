"""Regression for uk2 downtime causes, without touching real production hosts."""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ops.migration import fresh_host_runtime as runtime
from ops.migration import fresh_host_sync as sync


class PreparedSyncTests(unittest.TestCase):
    def test_tokyo_sync_refuses_aws_only_path_and_cannot_select_old_standby(self):
        inventory = json.loads((sync.ROOT / 'deploy/aws/fresh-migration-20261015.json').read_text())
        inventory['state'] = 'prepared'
        inventory['prod']['standby_us_east_20261015']['retired'] = False
        with patch.object(Path, 'read_text', return_value=json.dumps(inventory)), patch.object(sync, 'aws') as aws:
            with self.assertRaisesRegex(ValueError, 'Node1 SSH migration path'):
                sync.resolve('prod')
            aws.assert_not_called()
        inventory['state'] = 'tokyo-serving-edges-retired-20261017'
        with patch.object(Path, 'read_text', return_value=json.dumps(inventory)), patch.object(sync, 'aws') as aws:
            with self.assertRaisesRegex(ValueError, 'already active'):
                sync.resolve('prod')
            aws.assert_not_called()

    def test_retired_sources_and_edges_refuse_before_aws(self):
        inventory = json.loads((sync.ROOT / 'deploy/aws/fresh-migration-20261015.json').read_text())
        inventory['state'] = 'prepared'
        with patch.object(Path, 'read_text', return_value=json.dumps(inventory)), patch.object(sync, 'aws') as aws:
            for node in ('prod', 'uk1'):
                with self.subTest(node=node), self.assertRaisesRegex(ValueError, 'retired'):
                    sync.resolve(node)
            aws.assert_not_called()

    def test_retired_edge_pins_refuse_before_host_or_docker_access(self):
        spec = importlib.util.spec_from_file_location('edge_pins', Path(__file__).with_name('prepare-prod-edge-pins.py'))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        inventory = json.loads((sync.ROOT / 'deploy/aws/fresh-migration-20261015.json').read_text())
        with patch.object(module.subprocess, 'check_output') as remote:
            with self.assertRaisesRegex(SystemExit, 'legacy IP pins'):
                module.prepare(inventory, Path('/nonexistent'), Path('/nonexistent'))
            remote.assert_not_called()

    def receipt(self):
        return {'node': 'uk1', 'state': 'prepared', 'prepared_at': 100,
                'candidate': {'instance_name': runtime.expected_host('uk1'), 'instance_id': 'new', 'region': 'r'},
                'source': {'instance_name': 'tokenkey-edge-uk1-hz-cax21', 'instance_id': 'old', 'region': 'r'},
                'code': {n: sync.digest(sync.CODE[n]) for n in sync.FILES},
                'remote': '/tmp/migration-test', 'uri': 's3://test/migration', 'commands': []}

    def test_stale_replayed_identity_and_code_changes_rejected(self):
        receipt = self.receipt()
        sync.validate_prepared(receipt, receipt['candidate'], receipt['source'], 101)
        for mutation in ({'state': 'syncing'}, {'prepared_at': -1801}, {'prepared_at': 200}, {'code': {}}):
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                sync.validate_prepared({**receipt, **mutation}, receipt['candidate'], receipt['source'], 101)
        with self.assertRaisesRegex(ValueError, 'identity'):
            sync.validate_prepared(receipt, {}, receipt['source'], 101)

    def test_unique_remote_scripts_execute_literal_payload_and_cleanup(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'out'
            command = sync.remote_command(f"printf '%s' 'literal $(false) `false`' > {path}\n")
            subprocess.run(['bash', '-c', command], check=True)
            self.assertEqual(path.read_text(), 'literal $(false) `false`')

    def test_preparation_bundles_parse_and_fit_ssm_transport(self):
        receipt = self.receipt()
        scripts = []
        def execute(data, path, phase, host, script):
            scripts.append(script)
            subprocess.run(['bash', '-n'], input=script, text=True, check=True)
            self.assertLess(len(sync.remote_command(script)), 48 * 1024)
            data['commands'].append({'phase': phase, 'output': 'IMAGES=["gateway","worker"]'})
        with tempfile.TemporaryDirectory() as tmp, patch.object(sync, 'execute', side_effect=execute):
            sync.prepare(receipt, Path(tmp) / 'receipt.json')
        self.assertEqual(receipt['state'], 'prepared')
        self.assertEqual(receipt['images'], ['gateway','worker'])

    def test_final_sync_stops_at_first_failed_phase_no_activation(self):
        receipt = self.receipt()
        calls = []
        def execute(data, path, phase, host, script):
            calls.append(phase)
            subprocess.run(['bash', '-n'], input=script, text=True, check=True)
            if phase == 'apply':
                raise RuntimeError('apply failed')
        with tempfile.TemporaryDirectory() as tmp, patch.object(sync, 'resolve', return_value=(receipt['candidate'], receipt['source'])), patch.object(sync.time, 'time', return_value=101), patch.object(sync, 'execute', side_effect=execute):
            with self.assertRaisesRegex(RuntimeError, 'apply failed'):
                sync.sync(receipt, Path(tmp) / 'receipt.json', True)
        self.assertEqual(calls, ['export', 'apply'])
        self.assertEqual(receipt['state'], 'syncing')
        self.assertFalse(receipt.get('final_freeze_verified', False))

    def test_final_sync_rechecks_frozen_source_after_apply(self):
        receipt = self.receipt()
        calls = []
        def execute(data, path, phase, host, script):
            calls.append(phase)
            subprocess.run(['bash', '-n'], input=script, text=True, check=True)
        with tempfile.TemporaryDirectory() as tmp, patch.object(sync, 'resolve', return_value=(receipt['candidate'], receipt['source'])), patch.object(sync.time, 'time', return_value=101), patch.object(sync, 'execute', side_effect=execute):
            sync.sync(receipt, Path(tmp) / 'receipt.json', True)
        self.assertEqual(calls, ['export', 'apply', 'verify-source-still-frozen'])
        self.assertTrue(receipt['final_freeze_verified'])
        self.assertEqual(receipt['state'], 'synced')


class ComposeNetworkLabelTests(unittest.TestCase):
    def test_require_compose_network_labels_rejects_bare_create(self):
        with self.assertRaisesRegex(ValueError, 'compose network label'):
            runtime.require_compose_network_labels({'Labels': {}})
        runtime.require_compose_network_labels({
            'Labels': dict(runtime.COMPOSE_NET_LABELS),
        })

    def test_replace_network_merges_compose_labels_when_opening_public(self):
        calls = []

        def run(*args):
            calls.append(args)
            if args[:3] == ('docker', 'network', 'inspect'):
                return json.dumps([{
                    'Internal': False,
                    'Labels': dict(runtime.COMPOSE_NET_LABELS),
                }])
            if args[:2] == ('docker', 'inspect'):
                return json.dumps([{
                    'State': {'Health': {'Status': 'healthy'}},
                    'Id': 'pg1',
                    'Mounts': [],
                }])
            return ''

        network = {
            'Driver': 'bridge',
            'Internal': True,
            'Labels': {},  # bare create — must be repaired
            'Options': {},
            'IPAM': {'Options': {}, 'Config': [{'Subnet': '172.28.0.0/16'}]},
            'EnableIPv6': False,
        }
        containers = [{
            'Id': 'pg1',
            'NetworkSettings': {'Networks': {runtime.NET: {'Aliases': ['postgres']}}},
        }]
        with patch.object(runtime, 'run', side_effect=run), patch.object(runtime, 'wait_ready'):
            runtime.replace_network(network, containers, internal=False)
        create = next(c for c in calls if c[:3] == ('docker', 'network', 'create'))
        self.assertNotIn('--internal', create)
        joined = ' '.join(create)
        self.assertIn('com.docker.compose.network=tokenkey-network', joined)
        self.assertIn('com.docker.compose.project=tokenkey', joined)


class TrafficPreparationTests(unittest.TestCase):
    def test_single_edge_uses_current_prod_and_preserves_other_nodes(self):
        spec = importlib.util.spec_from_file_location('transition', Path(__file__).with_name('prepare-aws-traffic-transition.py'))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        inventory = json.loads((sync.ROOT / 'deploy/aws/fresh-migration-20261015.json').read_text())
        with self.assertRaisesRegex(ValueError, 'already completed'):
            module.render(inventory, Path('/nonexistent'), Path('/nonexistent'), nodes=['prod'], prod_ip=inventory['prod']['ip'])
        inventory['state'] = 'prepared'
        inventory['edges']['uk1']['retired'] = False
        config = """# fixture
{
}
(proxy) {
    reverse_proxy tokenkey-blue:8080 {
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-For {remote_host}
    }
}
api-uk1.tokenkey.dev {
    @allowed {
        remote_ip 192.0.2.1/32
    }
    import proxy
}
"""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for kind in ('source','candidate'):
                (root / f'uk1-{kind}.Caddyfile').write_text(config)
            receipt = module.render(inventory, root, root / 'out', nodes=['uk1'], prod_ip=inventory['prod']['source_ip'])
            self.assertEqual(list(receipt['nodes']), ['uk1'])
            forward = (root / 'out/uk1-source-forward.Caddyfile').read_text()
            self.assertIn('remote_ip ' + inventory['prod']['source_ip'] + '/32', forward)
            self.assertNotIn('remote_ip ' + inventory['prod']['ip'] + '/32', forward)
            self.assertIn('https://' + inventory['edges']['uk1']['ip'], forward)
            with self.assertRaisesRegex(ValueError, 'prod IP'):
                module.render(inventory, root, root / 'bad', nodes=['uk1'], prod_ip='0.0.0.0/0')


class RuntimeTests(unittest.TestCase):
    def test_gateway_and_control_alias_ready_before_worker_then_systemd(self):
        events = []
        def run(*args):
            events.append(args)
            if args[:3] == ('docker', 'start', 'tokenkey-gemini-web'):
                self.assertTrue(any(e[:3] == ('docker', 'exec', 'tokenkey-blue') for e in events))
                self.assertTrue(any(e[0] == 'bash' and e[1].endswith('sync-control-alias-on-host.sh') for e in events))
            if args[:3] == ('docker', 'exec', 'tokenkey-gemini-web'):
                return json.dumps({'status': 'ready'})
            if args[:2] == ('systemctl', 'is-active'):
                return 'active'
            return 'ok'
        def compose(file, *args):
            events.append(('compose', file, *args))
        with patch.object(runtime, 'run', side_effect=run), patch.object(runtime, 'inspect', return_value={'State': {'Running': True}}), patch.object(runtime, 'compose', side_effect=compose), patch.object(runtime, 'resolve', return_value=('tokenkey-blue', [])):
            runtime.start_services('blue')
        worker = next(i for i, e in enumerate(events) if e[:3] == ('docker', 'exec', 'tokenkey-gemini-web'))
        service = events.index(('systemctl', 'start', 'tokenkey.service'))
        self.assertLess(worker, service)
        self.assertEqual([e[-1] for e in events if e[0] == 'compose'], ['caddy', 'tokenkey-blue'])
        self.assertFalse(any('postgres' in e or 'tokenkey-postgres' in e for e in events))

    def test_exited_worker_fails_immediately_without_starting_systemd(self):
        events = []
        def run(*args):
            events.append(args)
            return '{"status":"ok"}'
        with patch.object(runtime, 'run', side_effect=run), patch.object(runtime, 'compose'), patch.object(runtime, 'inspect', return_value={'State': {'Running': False}}), patch.object(runtime.time, 'sleep') as sleep:
            with self.assertRaisesRegex(ValueError, 'worker exited'):
                runtime.start_services('blue')
            sleep.assert_not_called()
        self.assertNotIn(('systemctl', 'start', 'tokenkey.service'), events)

    def test_activation_failure_restores_hold_and_stops_all_writers(self):
        events = []
        def run(*args):
            events.append(args)
            if args[:3] == ('docker', 'inspect', 'pg'):
                return '[]'
            if args[:3] == ('docker', 'ps', '-aq'):
                return 'pg'
            if args[:3] == ('docker', 'ps', '-a'):
                return 'tokenkey-blue\ntokenkey-green\ntokenkey-gemini-web\ntokenkey-gemini-web-alias'
            return ''
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'migration').mkdir()
            (root / 'migration/DATA_SYNC_HOLD').touch()
            (root / '.env').write_text('synthetic_only=yes\n')
            caddy = root / 'prepared.Caddyfile'
            caddy.write_text('reverse_proxy tokenkey-blue:8080 {\n}\n')
            with patch.object(runtime, 'R', root), patch.object(runtime, 'validate_final'), patch.object(runtime, 'check', return_value=('blue', {}, {})), patch.object(runtime, 'inspect', return_value={'Image': 'fixture'}), patch.object(runtime, 'run', side_effect=run), patch.object(runtime, 'replace_network', side_effect=RuntimeError('network failure')):
                with self.assertRaisesRegex(RuntimeError, 'network failure'):
                    runtime.activate('prod', {}, caddy, sync.digest(caddy))
            self.assertEqual((root / 'migration/DATA_SYNC_HOLD').read_text(), '')
            stopped = [e[-1] for e in events if e[:2] == ('docker','stop')]
            self.assertEqual(stopped, ['tokenkey-blue','tokenkey-green','tokenkey-gemini-web','tokenkey-gemini-web-alias'])
            self.assertNotIn('tokenkey-postgres', stopped)

    def test_activation_rejects_sequence_drift_before_opening_network(self):
        with patch.object(runtime, 'validate_final'), patch.object(runtime, 'check', return_value=('blue', {}, {})), patch.object(runtime, 'run', side_effect=RuntimeError('sequence behind restored data')) as run, patch.object(runtime, 'replace_network') as replace_network:
            with self.assertRaisesRegex(RuntimeError, 'sequence behind restored data'):
                runtime.activate('prod', {}, Path('/unused-prepared-caddy'), 'unused')
            self.assertEqual(run.call_args.args[-1], runtime.sequence_guard_sql())
            replace_network.assert_not_called()

    def test_activation_rejects_standby_before_accessing_candidate_state(self):
        for hostname in ('tokenkey-prod-aws-20261015', 'unrelated-host'):
            with self.subTest(hostname=hostname), patch.object(runtime, 'validate_final'), patch.object(runtime.socket, 'gethostname', return_value=hostname), patch.object(Path, 'is_file', side_effect=AssertionError('must reject host before reading hold')), patch.object(runtime, 'run') as run:
                with self.assertRaisesRegex(ValueError, 'wrong candidate hostname'):
                    runtime.activate('prod', {}, Path('/unused-caddy'), 'unused')
                run.assert_not_called()

    def test_activation_archives_systemd_dropins_with_shutil_move(self):
        """EC2 /etc vs /var/lib can be EXDEV; drop-ins must use shutil.move, not Path.rename."""
        moved = []

        def run(*args):
            if args[:3] == ('docker', 'ps', '-aq'):
                return 'pg'
            if args[:2] == ('docker', 'inspect'):
                return json.dumps([{'Id': 'pg1', 'Mounts': [], 'Image': 'caddy:fixture'}])
            return ''

        def fake_move(src, dst):
            moved.append((src, dst))
            Path(dst).write_text('archived')

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'migration').mkdir()
            (root / 'migration/DATA_SYNC_HOLD').touch()
            (root / 'caddy').mkdir()
            (root / 'caddy/Caddyfile').write_text('old\n')
            (root / '.env').write_text('synthetic_only=yes\n')
            caddy = root / 'prepared.Caddyfile'
            caddy.write_text('reverse_proxy tokenkey-blue:8080 {\n}\n')
            receipt = PreparedSyncTests().receipt()
            receipt.update(
                state='synced', final_freeze_verified=True, completed_at=100, node='prod',
                candidate={'instance_name': runtime.expected_host('prod'), 'instance_id': 'new', 'region': 'r'},
                commands=[{'phase': p, 'status': 'Success', 'target': t}
                          for p, t in [('export', 'old'), ('apply', 'new'), ('verify-source-still-frozen', 'old')]],
            )
            orig_exists = Path.exists

            def exists(self):
                if str(self).endswith('migration-isolation.conf') or str(self).endswith('migration-hold-stop.conf'):
                    return True
                return orig_exists(self)

            with (
                patch.object(runtime, 'R', root),
                patch.object(runtime, 'validate_final'),
                patch.object(runtime, 'check', return_value=('blue', {'Id': 'net'}, {'Id': 'pg1', 'Mounts': []})),
                patch.object(runtime, 'inspect', return_value={'Id': 'pg1', 'Mounts': [], 'Image': 'caddy:fixture'}),
                patch.object(runtime, 'run', side_effect=run),
                patch.object(runtime, 'replace_network'),
                patch.object(runtime, 'start_services'),
                patch.object(runtime, 'mounts', return_value=[]),
                patch('ops.migration.fresh_host_runtime.shutil.move', side_effect=fake_move),
                patch.object(Path, 'exists', exists),
            ):
                runtime.activate('prod', receipt, caddy, sync.digest(caddy))
            self.assertTrue(any(src.endswith('migration-isolation.conf') for src, _ in moved))
            self.assertTrue(any(src.endswith('migration-hold-stop.conf') for src, _ in moved))
            self.assertTrue(list((root / 'migration').glob('activation-*/DATA_SYNC_HOLD.completed')))

    def test_prod_start_does_not_require_an_edge_worker(self):
        calls = []
        def run(*args):
            calls.append(args)
            return 'active'
        with patch.object(runtime, 'run', side_effect=run), patch.object(runtime, 'compose'), patch.object(runtime, 'resolve', return_value=('tokenkey-green', [])):
            runtime.start_services('green', worker=False)
        self.assertNotIn(('docker','start','tokenkey-gemini-web'), calls)
        self.assertIn(('systemctl','start','tokenkey.service'), calls)

    def test_presync_or_wrong_identity_cannot_activate(self):
        receipt = PreparedSyncTests().receipt()
        receipt.update(state='synced', final_freeze_verified=True, completed_at=100,
                       commands=[{'phase': phase, 'status': 'Success', 'target': target} for phase, target in
                                 [('export', 'old'), ('apply', 'new'), ('verify-source-still-frozen', 'old')]])
        runtime.validate_final(receipt, 'uk1', 101)
        for mutation in ({'final_freeze_verified': False}, {'commands': []}, {'node': 'uk2'}, {'completed_at': -601}):
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                runtime.validate_final({**receipt, **mutation}, 'uk1', 101)
        bad = copy.deepcopy(receipt)
        bad['candidate']['instance_name'] = 'tokenkey-edge-uk1-hz-cax21'
        with self.assertRaises(ValueError):
            runtime.validate_final(bad, 'uk1', 101)

    def test_tokyo_final_receipt_requires_us_source(self):
        receipt = PreparedSyncTests().receipt()
        receipt.update(state='synced', final_freeze_verified=True, completed_at=100, node='prod',
                       candidate={'instance_name': runtime.expected_host('prod'), 'instance_id': 'new'},
                       source={'instance_name': 'tokenkey-prod-aws-20261015', 'instance_id': 'old'},
                       commands=[{'phase': phase, 'status': 'Success', 'target': target} for phase, target in
                                 [('export', 'old'), ('apply', 'new'), ('verify-source-still-frozen', 'old')]])
        runtime.validate_final(receipt, 'prod', 101)
        receipt['source']['instance_name'] = 'tokenkey-prod-hz-cax21'
        with self.assertRaisesRegex(ValueError, 'matching final-freeze'):
            runtime.validate_final(receipt, 'prod', 101)


if __name__ == '__main__':
    unittest.main()
