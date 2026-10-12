import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from ops.qa import hcloud_host as host
from ops.qa.colocation_resources import load_policy
from ops.qa.hcloud_runtime import render_files


class HostBoundaryTest(unittest.TestCase):
    def setUp(self):
        self.policy = load_policy()

    def test_rendered_configuration_accepts_valid_and_rejects_boundary_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            config = root / 'config'
            config.mkdir()
            with patch.object(host, 'ROOT', root), patch('ops.qa.hcloud_runtime.HOST_ROOT', str(root)):
                files = render_files('ghcr.io/youxuanxue/sub2api@sha256:' + 'a'*64,
                                     'postgres@sha256:' + 'b'*64, '10.77.0.3', '10.77.0.2', ['88.198.120.64'])
                for name, value in files.items():
                    (config / name).write_text(value)
                original = json.loads(files['compose.json'])
                self.assertEqual(host.config(self.policy), original)
                for changes in ({'privileged': True}, {'network_mode': 'host'}, {'pid': 'host'},
                                {'mem_limit': 8*1024**3}, {'image': 'postgres:latest'},
                                {'volumes': [{'source': str(root)+'/../docker.sock', 'target': '/socket'}]}):
                    bad = copy.deepcopy(original)
                    bad['services']['qa-worker'].update(changes)
                    (config/'compose.json').write_text(json.dumps(bad))
                    with self.subTest(changes=changes), self.assertRaisesRegex(RuntimeError, 'renderer'):
                        host.config(self.policy)
                (config/'compose.json').write_text(files['compose.json'])
                (root/'scratch').symlink_to(root.parent, target_is_directory=True)
                with self.assertRaisesRegex(RuntimeError, 'symlinks'):
                    host.config(self.policy)
                (root/'scratch').unlink()
                (config/'firewall.nft').write_text('table inet tokenkey_qa {}')
                with self.assertRaisesRegex(RuntimeError, 'renderer'):
                    host.config(self.policy)

    def test_live_slice_rejects_inactive_unlimited_and_missing_physical_io_cap(self):
        values = {'memory.max': '2147483648', 'memory.swap.max': '0', 'pids.max': '256',
                  'cpu.max': '100000 100000', 'io.max': '8:0 rbps=10485760 wbps=10485760 riops=100 wiops=100'}
        def run(*args):
            if args[:2] == ('systemctl', 'is-active'):
                return 'active'
            if args[0] == 'systemctl':
                return '/tokenkey.slice/tokenkey-qa.slice'
            return '8:0'
        with patch.object(host, 'run', side_effect=run), patch.object(host, 'root_io_device', return_value='/dev/sda'), \
                patch.object(Path, 'read_text', autospec=True, side_effect=lambda path: values[path.name]):
            host.check_slice(self.policy)
            for name, invalid in [('memory.max', 'max'), ('memory.swap.max', 'max'), ('pids.max', 'max'),
                                  ('cpu.max', 'max 100000'), ('io.max', ''),
                                  ('io.max', '7:0 rbps=10485760 wbps=10485760 riops=100 wiops=100')]:
                with self.subTest(name=name), patch.dict(values, {name: invalid}), self.assertRaises(RuntimeError):
                    host.check_slice(self.policy)
        with patch.object(host, 'run', return_value='inactive'), self.assertRaisesRegex(RuntimeError, 'not active'):
            host.check_slice(self.policy)

    def test_failed_boundary_prevents_container_start(self):
        with patch.object(host, 'runtime_boundary', side_effect=RuntimeError('invalid boundary')), \
                patch.object(host, 'compose') as compose:
            with self.assertRaises(RuntimeError):
                host.start_component('queue-start', self.policy)
            compose.assert_not_called()

    def test_stopped_queue_recovers_paused_before_worker_can_start(self):
        events = []
        state = {'running': False, 'paused': False, 'active_leases': 0}
        def inspect(name):
            return {'State': {'Running': state['running'], 'Health': {'Status': 'healthy'}}}
        def compose(*args):
            events.append(('compose', args[-1]))
            state['running'] = True
        def sql(query):
            self.assertTrue(state['running'])
            events.append(('sql', query))
            state['paused'] = True
        def status():
            self.assertTrue(state['running'])
            return state.copy()
        with patch.object(host, 'runtime_boundary'), patch.object(host, 'owned_container', side_effect=inspect), \
                patch.object(host, 'run', side_effect=['tokenkey-qa-queue', '', 'tokenkey-qa-queue']), \
                patch.object(host, 'compose', side_effect=compose), patch.object(host, 'sql', side_effect=sql), \
                patch.object(host, 'status', side_effect=status):
            host.start_component('queue-start', self.policy)
            host.start_component('worker-start', self.policy)
        self.assertEqual(events, [('compose', 'qa-queue'), ('sql', 'UPDATE qa_queue.control SET paused=true'),
                                  ('compose', 'qa-worker')])

    def test_recovery_refuses_a_live_worker_and_replacement_refuses_live_leases(self):
        def inspect(name):
            return {'State': {'Running': name == 'tokenkey-qa-worker'}}
        with patch.object(host, 'runtime_boundary'), patch.object(host, 'owned_container', side_effect=inspect), \
                patch.object(host, 'run', side_effect=['tokenkey-qa-queue', 'tokenkey-qa-worker']), \
                patch.object(host, 'compose') as compose:
            with self.assertRaisesRegex(RuntimeError, 'stop the worker'):
                host.start_component('queue-start', self.policy)
            compose.assert_not_called()
        with patch.object(host, 'runtime_boundary'), patch.object(host, 'owned_container', return_value={'State': {'Running': True}}), \
                patch.object(host, 'run', return_value='tokenkey-qa-queue'), patch.object(host, 'compose') as compose, \
                patch.object(host, 'status', return_value={'paused': True, 'active_leases': 1}):
            with self.assertRaisesRegex(RuntimeError, 'pause and drain'):
                host.start_component('worker-start', self.policy)
            compose.assert_not_called()


if __name__ == '__main__':
    unittest.main()
