import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('standby', Path(__file__).with_name('archive_standby_rehearsal.py'))
standby = importlib.util.module_from_spec(spec)
spec.loader.exec_module(standby)


class StandbyTest(unittest.TestCase):
    def test_restore_stays_standby_on_explicit_timeline(self):
        self.assertEqual(standby.restore_options('20261019-234616F_20261021-083006I', 1), [
            '--stanza=tokenkey', '--set=20261019-234616F_20261021-083006I',
            '--type=standby', '--target-timeline=1', '--archive-mode=off',
            '--process-max=4', 'restore'])

    def test_bad_restore_inputs_refused(self):
        for label, timeline in [('latest', 1), ('20261019-234616F', 0), ('20261019-234616F', True)]:
            with self.subTest(label=label, timeline=timeline), self.assertRaises(ValueError):
                standby.restore_options(label, timeline)

    def test_host_mismatch_runs_nothing(self):
        with patch.object(standby.socket, 'gethostname', return_value='production'), patch.object(standby, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'host mismatch'):
                standby.check_host('isolated')
            run.assert_not_called()

    def test_locked_workspace_runs_nothing(self):
        with patch.object(standby.socket, 'gethostname', return_value='isolated'), patch.object(standby.os.path, 'ismount', return_value=False), patch.object(standby, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'not mounted'):
                standby.check_host('isolated')
            run.assert_not_called()

    def test_status_rejects_promoted_database(self):
        row = {'in_recovery': False, 'read_only': 'on', 'archive_mode': 'off'}
        with patch.object(standby, 'inspect_owned'), patch.object(standby, 'run', return_value=json.dumps(row)), patch.object(Path, 'read_text', return_value='{}'):
            with self.assertRaisesRegex(ValueError, 'safety invariant'):
                standby.status()

    def test_status_rejects_different_source(self):
        row = {'in_recovery': True, 'read_only': 'on', 'archive_mode': 'off', 'system_identifier': '2', 'checkpoint_timeline': 1}
        with patch.object(standby, 'inspect_owned'), patch.object(standby, 'run', return_value=json.dumps(row)), patch.object(Path, 'read_text', return_value='{"system_identifier":"1","timeline":1}'):
            with self.assertRaisesRegex(ValueError, 'identity mismatch'):
                standby.status()


if __name__ == '__main__':
    unittest.main()
