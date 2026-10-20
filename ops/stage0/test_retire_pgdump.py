"""Exercise retirement side effects against an isolated host filesystem."""
import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch

import retire_pgdump as retire
import preserve_legacy_pgdump as preserve


class RetirementTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / 'postgres-backup'
        self.state.mkdir()
        (self.root / 'qa-lifecycle').mkdir()
        (self.root / 'pgdump').mkdir()
        (self.root / 'pgdump/old.sql.gz').write_bytes(b'retained data')
        (self.root / '.env').write_text('TOKENKEY_PGDUMP_S3_URI=s3://bucket/prod/pgdump\n')
        self.config = self.root / 'config.json'
        self.config.write_text(json.dumps({'node': 'prod', 'jobs': ['tokenkey-pgdump'],
                                         'paused_jobs': {'tokenkey-qa-boundary': 'keep'}}))
        now = time.time()
        self.receipt = self.root / 'retention.json'
        self.receipt.write_text(json.dumps({'status': 'ok', 's3_uri': 's3://bucket/prod/pgdump',
                                           'expiration_disabled': True, 'retained_objects': 1,
                                           'observed_at': now, 'retain_until': now + 31*86400}))
        accepted = {'status': 'ok', 'completed_at': now, 'paths': ['latest-archived', 'tk_point']}
        self.health = {'status': 'ok', 'reasons': [], 'latest_backup_at': now,
                       'archive_check': {'status': 'ok', 'started_at': now},
                       'restore_acceptance': accepted, 'repository_verification': accepted}
        self.timer = {'ActiveState': 'active', 'UnitFileState': 'enabled'}
        self.service = {'ActiveState': 'inactive'}
        self.commands = []
        self.marker = self.state / 'pgdump-retired.json'
        self.dropin = self.root / 'systemd/retired.conf'
        self.exporter = self.root / 'tokenkey-pgdump.sh'
        self.exporter.write_text('#!/bin/bash\necho unexpected-dump\n')
        for name, value in [('ROOT', self.root), ('STATE', self.state),
                            ('MARKER', self.marker), ('DROPIN', self.dropin), ('EXPORTER', self.exporter)]:
            self.enterContext(patch.object(retire, name, value))
        self.enterContext(patch.object(retire.socket, 'gethostname', return_value='expected'))
        self.enterContext(patch.object(retire.os, 'geteuid', return_value=0))
        self.enterContext(patch.object(retire, 'operator', return_value=Path('/operator.py')))
        self.enterContext(patch.object(retire, 'properties', side_effect=lambda unit:
                                      self.timer.copy() if unit.endswith('.timer') else self.service.copy()))
        self.enterContext(patch.object(retire, 'run', side_effect=self.run_command))

    def run_command(self, args):
        self.commands.append(args)
        if args == ['python3', '/operator.py', 'status']:
            return json.dumps(self.health)
        if args == ['systemctl', 'disable', '--now', 'tokenkey-pgdump.timer']:
            self.timer = {'ActiveState': 'inactive', 'UnitFileState': 'disabled'}
            return ''
        if args == ['systemctl', 'daemon-reload']:
            return ''
        raise AssertionError(f'unexpected command {args}')

    def invoke(self, apply=False):
        argv = ['retire', '--expected-host', 'expected', '--retention-receipt', str(self.receipt),
                '--watch-config', str(self.config)] + (['--apply'] if apply else [])
        with patch('sys.argv', argv), contextlib.redirect_stdout(io.StringIO()) as out:
            retire.main()
        return json.loads(out.getvalue())

    def test_plan_is_read_only_then_apply_and_recheck(self):
        self.assertEqual(self.invoke()['status'], 'planned')
        self.assertFalse(self.marker.exists())
        result = self.invoke(True)
        self.assertEqual(result['status'], 'retired')
        self.assertEqual((self.root / 'pgdump/old.sql.gz').read_bytes(), b'retained data')
        config = json.loads(self.config.read_text())
        self.assertEqual(config['paused_jobs']['tokenkey-qa-boundary'], 'keep')
        self.assertIn('tokenkey-pgdump', config['paused_jobs'])
        blocked = subprocess.run(['bash', str(self.exporter)], capture_output=True, text=True)
        self.assertEqual(blocked.returncode, 1)
        self.assertEqual(blocked.stdout, '')
        self.assertIn('pgdump-retired', blocked.stderr)
        self.assertEqual(self.invoke(True)['status'], 'already-retired')
        self.timer['ActiveState'] = 'active'
        with self.assertRaisesRegex(retire.BackupError, 'incomplete or drifted'):
            self.invoke(True)

    def test_running_dump_and_partial_attempt_refuse(self):
        self.service['ActiveState'] = 'active'
        with self.assertRaisesRegex(retire.BackupError, 'still running'):
            self.invoke(True)
        self.assertFalse(self.marker.exists())
        self.assertFalse(any(cmd[0] == 'systemctl' for cmd in self.commands))
        self.marker.write_text('{"status":"retiring"}')
        with self.assertRaisesRegex(retire.BackupError, 'incomplete or drifted'):
            self.invoke(True)

    def test_lock_busy_cannot_change_timer(self):
        with patch.object(retire.fcntl, 'flock', side_effect=BlockingIOError):
            with self.assertRaises(BlockingIOError):
                self.invoke(True)
        self.assertEqual(self.timer['UnitFileState'], 'enabled')
        self.assertFalse(self.marker.exists())

    def test_dump_starting_before_timer_stop_cannot_complete_retirement(self):
        original = self.exporter.read_text()

        def timer_race(args):
            result = self.run_command(args)
            if args == ['systemctl', 'disable', '--now', 'tokenkey-pgdump.timer']:
                # A scheduled service entered while retirement held host.lock.
                # Stopping the timer leaves that service running.
                self.service['ActiveState'] = 'active'
            return result

        with patch.object(retire, 'run', side_effect=timer_race):
            with self.assertRaisesRegex(retire.BackupError, 'pgdump started'):
                self.invoke(True)
        self.assertEqual(self.service['ActiveState'], 'active')
        self.assertEqual(self.timer['UnitFileState'], 'disabled')
        self.assertFalse(self.marker.exists())
        self.assertEqual(self.exporter.read_text(), original)
        self.assertEqual((self.root / 'pgdump/old.sql.gz').read_bytes(), b'retained data')

    def test_retention_receipt_cannot_overwrite_saved_baseline(self):
        with patch('sys.argv', ['preserve', '--bucket', 'unused', '--receipt', str(self.receipt), '--apply']), \
                patch.object(preserve, 'aws') as aws:
            with self.assertRaisesRegex(retire.BackupError, 'already exists'):
                preserve.main()
            aws.assert_not_called()


if __name__ == '__main__':
    unittest.main()
