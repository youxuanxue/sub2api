import copy
import datetime as dt
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import source_worker as worker
import data_machine_coordinator as policy


def source_config(root):
    return {'schema_version': 1, 'hostname': 'fixture-data', 'environment': 'prod', 'source_id': 'a',
            'source_root': str(root), 'host_lock': str(root / 'worker.lock'),
            'binary': '/fixture-binary', 'binary_sha256': 'a' * 64,
            'receipt_root': str(root / 'receipts'),
            'remote': {'key': '/fixture-key', 'jump': 'root@jump', 'target': 'root@node1',
                       'remote_root': '/archive', 'rate_kbps': 1024, 'timeout': 1200}}


class SourceWorkerTest(unittest.TestCase):
    def test_unsafe_remote_endpoint_and_paths_rejected_before_any_command(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = source_config(Path(directory))
            for field, value in [('jump', 'root@jump;touch /tmp/no'), ('target', '-oProxyCommand=bad'),
                                 ('remote_root', '/archive\nother')]:
                changed = copy.deepcopy(cfg)
                changed['remote'][field] = value
                with self.subTest(field=field), self.assertRaises(policy.CoordinatorError):
                    worker.validate_config(changed)

    def test_archive_seals_before_and_after_and_transfers_directly(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = source_config(Path(directory))
            hour = dt.datetime(2026, 11, 10, tzinfo=dt.timezone.utc)
            request = {'command': 'archive', 'source_id': 'a', 'environment': 'prod', 'hour': hour.isoformat()}
            def systemctl(argv, **kwargs):
                return mock.Mock(returncode=0, stdout='ActiveState=inactive\nUnitFileState=disabled\n' if '--value' not in argv else 'inactive\n')
            with mock.patch.object(worker.socket, 'gethostname', return_value=cfg['hostname']), \
                 mock.patch.object(worker.subprocess, 'run', side_effect=systemctl), \
                 mock.patch.object(worker, 'seal', return_value={'state_digest': 'b' * 64}) as seal, \
                 mock.patch.object(worker.archive, 'push_hour', return_value={'ok': True}) as push:
                result = worker.run(cfg, request)
            self.assertEqual(seal.call_count, 2)
            self.assertEqual(result['seal_digest'], 'b' * 64)
            self.assertEqual(push.call_args.args[4]['target'], 'root@node1')

    def test_legacy_owner_prevents_archive_authorization(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = source_config(Path(directory))
            request = {'command': 'archive', 'source_id': 'a', 'environment': 'prod', 'hour': '2026-11-10T00:00:00+00:00'}
            with mock.patch.object(worker.socket, 'gethostname', return_value=cfg['hostname']), \
                 mock.patch.object(worker.subprocess, 'run', return_value=mock.Mock(returncode=0, stdout='ActiveState=active\nUnitFileState=enabled')) , \
                 mock.patch.object(worker.archive, 'push_hour') as push:
                with self.assertRaisesRegex(policy.CoordinatorError, 'legacy'):
                    worker.run(cfg, request)
                push.assert_not_called()

    def test_changed_capture_seal_cannot_authorize_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = source_config(Path(directory))
            request = {'command': 'archive', 'source_id': 'a', 'environment': 'prod', 'hour': '2026-11-10T00:00:00+00:00'}
            with mock.patch.object(worker.socket, 'gethostname', return_value=cfg['hostname']), \
                 mock.patch.object(worker.subprocess, 'run', return_value=mock.Mock(returncode=0, stdout='LoadState=not-found\nActiveState=inactive')), \
                 mock.patch.object(worker, 'seal', side_effect=[{'state_digest': 'a' * 64}, {'state_digest': 'b' * 64}]), \
                 mock.patch.object(worker.archive, 'push_hour', return_value={'ok': True}):
                with self.assertRaisesRegex(policy.CoordinatorError, 'capture changed'):
                    worker.run(cfg, request)


if __name__ == '__main__':
    unittest.main()
