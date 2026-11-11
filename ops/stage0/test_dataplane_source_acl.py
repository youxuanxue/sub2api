"""Source rule scope, directional matching and service lifecycle contract."""
import unittest
import fcntl
import json
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
from unittest.mock import patch
from ops.stage0 import dataplane_source_acl as acl
from ops.stage0.dataplane_source_acl import SITES, rules, service


class DataSourceACLTest(unittest.TestCase):
    def test_only_original_direction_published_data_ports_are_filtered(self):
        for site, row in SITES.items():
            with self.subTest(site=site):
                forward = rules(site, True)
                self.assertEqual(len(forward), 5)
                for index, port in ((0, '5432'), (2, '6379')):
                    allowed = forward[index]
                    self.assertEqual(allowed[allowed.index('--ctorigdst') + 1], row['data'])
                    self.assertEqual(allowed[allowed.index('--ctorigdstport') + 1], port)
                    self.assertEqual(allowed[allowed.index('--ctdir') + 1], 'ORIGINAL')
                    self.assertEqual(allowed[-4:], ['-s', row['app'] + '/32', '-j', 'RETURN'])
                    self.assertEqual(forward[index + 1], allowed[:-4] + ['-j', 'DROP'])
                self.assertEqual(forward[-1], ['-j', 'RETURN'])

    def test_input_fallback_retains_local_access_and_service_reinstalls_on_docker_restart(self):
        for site, row in SITES.items():
            entries = rules(site, False)
            for offset, port in ((0, '5432'), (4, '6379')):
                for index, source in enumerate((row['app'], row['data'], '127.0.0.1')):
                    self.assertEqual(entries[offset + index], ['-p', 'tcp', '-d', row['data'], '--dport', port,
                                                              '-s', source + '/32', '-j', 'RETURN'])
            unit = service(site)
            self.assertIn('After=docker.service\nPartOf=docker.service\n', unit)
            self.assertIn('--site ' + site + ' --phase enforce\n', unit)
            self.assertIn('WantedBy=docker.service\n', unit)

    def test_commit_releases_lock_before_service_start_and_rolls_back_failure(self):
        for fail in (False, True):
            with self.subTest(fail=fail), tempfile.TemporaryDirectory() as directory:
                work = Path(directory)
                original_stat = Path.stat
                def root_stat(path, *args, **kwargs):
                    info = original_stat(path, *args, **kwargs)
                    if path == work:
                        return SimpleNamespace(st_uid=0, st_mode=info.st_mode)
                    return info
                def command(args, **kwargs):
                    if args[:2] == ['systemctl', 'start']:
                        # Another open description reproduces ExecStart's lock acquisition.
                        with (work / 'lock').open('r') as child_lock:
                            fcntl.flock(child_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    if fail and args[:2] == ['systemctl', 'is-active']:
                        raise subprocess.CalledProcessError(3, args)
                    return SimpleNamespace(returncode=0)
                with patch.object(acl, 'WORK', work), patch.object(acl, 'validate'), \
                     patch.object(acl, 'enforce'), patch.object(acl, 'remove') as remove, \
                     patch.object(acl, 'run', side_effect=command), patch.object(Path, 'stat', root_stat), \
                     patch('sys.argv', ['acl', '--site', 'prod', '--phase', 'commit', '--token', 'test-receipt']):
                    acl.save({'site': 'prod', 'status': 'pending', 'token': 'test-receipt'})
                    if fail:
                        with self.assertRaises(subprocess.CalledProcessError):
                            acl.main()
                        remove.assert_called_once()
                    else:
                        acl.main()
                        remove.assert_not_called()
                    state = json.loads((work / 'receipt.json').read_text())
                    self.assertEqual(state['status'], 'rolled-back' if fail else 'committed')


if __name__ == '__main__':
    unittest.main()
