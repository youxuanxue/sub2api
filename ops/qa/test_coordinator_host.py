import copy
import datetime as dt
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import coordinator_host as host
import data_machine_coordinator as policy
import install_coordinator as installer
from test_source_worker import source_config


HOUR = dt.datetime(2026, 11, 10, 10, tzinfo=dt.timezone.utc)


def config(root):
    return {'schema_version': 1, 'hostname': 'fixture-data', 'environment': 'prod',
            'binary': '/fixture-binary', 'binary_sha256': 'a' * 64, 'max_hours_per_run': 4,
            'state_root': str(root / 'state'), 'host_lock': str(root / 'host.lock'),
            'ssh_key': '/fixture-key', 'known_hosts': '/fixture-known-hosts',
            'sources': [{'id': source, 'ssh_target': f'root@{source}', 'worker': '/opt/qa/source_worker.py',
                         'worker_config': '/etc/qa/source.json', 'active_from': HOUR.isoformat(),
                         'active_until': (HOUR + dt.timedelta(hours=1)).isoformat()} for source in ('a', 'b')]}


class RunnerTest(unittest.TestCase):
    def test_a_unreachable_does_not_block_b_cleanup_or_authorize_shared_drop(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cfg = config(root)
            cleanups = []

            def source_command(config, source, command, hour, **kwargs):
                if source['id'] == 'a':
                    raise OSError('a unavailable')
                if command == 'cleanup':
                    cleanups.append((source['id'], kwargs['plan']['shared_partition_action']))
                    return {'ok': True, 'source_id': 'b', 'action': 'delete', 'window_start': hour.isoformat()}
                return {'schema_version': policy.SOURCE_SCHEMA, 'environment': 'prod', 'source_id': 'b',
                        'window_start': hour.isoformat(), 'window_end': (hour + dt.timedelta(hours=1)).isoformat(),
                        'ok': True, 'file_count': 1, 'total_bytes': 7,
                        'manifest_sha256': 'a' * 64, 'seal_digest': 'b' * 64}

            def database(config, phase, *args, **kwargs):
                if phase == 'inspect':
                    return {'db_anchor_utc': (HOUR + dt.timedelta(hours=26)).isoformat(), 'hours': [HOUR.isoformat()]}
                if phase == 'provision':
                    raise OSError('backup lock busy')
                self.fail('shared partition must not be dropped while a is missing')

            with mock.patch.object(host.socket, 'gethostname', return_value='fixture-data'), \
                 mock.patch.object(host, 'source_command', side_effect=source_command), \
                 mock.patch.object(host, 'database', side_effect=database):
                result = host.run(cfg, apply=True)
            self.assertEqual(cleanups, [('b', 'hold')])
            self.assertFalse(result['ok'])
            self.assertIn('provision', [error['operation'] for error in result['errors']])
            self.assertEqual(result['blocked_hours'], 1)
            self.assertTrue((root / 'state' / 'cleaned' / 'b' / (HOUR.strftime('%Y/%m/%d/%H') + '.json')).is_file())

    def test_retired_source_stays_in_its_historical_hour(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = config(Path(directory))
            self.assertEqual([source['id'] for source in host.participants(cfg, HOUR)], ['a', 'b'])
            self.assertEqual(host.participants(cfg, HOUR + dt.timedelta(hours=1)), [])
            changed = copy.deepcopy(cfg)
            changed['sources'][0]['active_from'] = HOUR.replace(minute=1).isoformat()
            with self.assertRaises(policy.CoordinatorError):
                host.validate_config(changed)

    def test_no_automatic_gap_policy_can_be_silently_enabled(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = config(Path(directory))
            cfg['allow_gap'] = True
            with self.assertRaisesRegex(policy.CoordinatorError, 'approval'):
                host.validate_config(cfg)

    def test_source_transport_is_pinned_and_does_not_shell_interpolate_json(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = config(Path(directory))
            response = mock.Mock(stdout=json.dumps({'ok': True, 'source_id': 'a'}))
            with mock.patch.object(host.subprocess, 'run', return_value=response) as run:
                host.source_command(cfg, cfg['sources'][0], 'archive', HOUR)
            argv = run.call_args.args[0]
            self.assertIn('StrictHostKeyChecking=yes', argv)
            self.assertIn('IdentitiesOnly=yes', argv)
            self.assertTrue(argv[-1].startswith('/usr/bin/nice -n 19 /usr/bin/ionice -c 3 /usr/bin/python3 '))
            self.assertEqual(json.loads(run.call_args.kwargs['input'])['source_id'], 'a')
            self.assertNotIn('shell', run.call_args.kwargs)


class InstallerTest(unittest.TestCase):
    def test_plan_does_not_write_units_or_enable_timers(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / 'binary'
            binary.write_bytes(b'fixture binary')
            cfg = source_config(root)
            cfg['binary_sha256'] = hashlib.sha256(binary.read_bytes()).hexdigest()
            with mock.patch.object(installer.socket, 'gethostname', return_value='fixture-data'), \
                 mock.patch.object(installer.subprocess, 'run') as run:
                result = installer.install(cfg, binary, role='source', base=root / 'bundles',
                                           environment_file=None, apply=False)
            run.assert_not_called()
            self.assertFalse(result['timer_enabled'])
            self.assertFalse((root / 'bundles').exists())

    def test_runtime_is_resource_limited_without_gateway_actions(self):
        unit = installer.units(Path('/opt/qa/abc'), Path('/etc/qa/pg.env'))['tokenkey-qa-coordinator.service']
        self.assertIn('CPUQuota=20%', unit)
        self.assertIn('MemoryMax=512M', unit)
        self.assertNotIn('restart', unit)
        self.assertIn('--apply', unit)


class BackupGuardTest(unittest.TestCase):
    def test_database_write_defers_before_lock_or_native_execution_near_backup(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / 'binary'
            binary.write_bytes(b'native')
            cfg = config(root)
            cfg.update(binary=str(binary), binary_sha256=hashlib.sha256(b'native').hexdigest())
            replies = [mock.Mock(stdout='soon'), mock.Mock(stdout=str(int(dt.datetime.now(dt.timezone.utc).timestamp()) + 60))]
            with mock.patch.object(host.subprocess, 'run', side_effect=replies) as run:
                with self.assertRaisesRegex(policy.CoordinatorError, 'deferred'):
                    host.database(cfg, 'provision')
            self.assertEqual(run.call_count, 2)


if __name__ == '__main__':
    unittest.main()
