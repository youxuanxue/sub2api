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
    def test_add_source_requires_committed_receipt_and_is_idempotent(self):
        for status in ('pending', 'committed'):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as directory:
                work = Path(directory)
                original_stat, original_lstat = Path.stat, Path.lstat
                def owned(info):
                    return SimpleNamespace(st_uid=0, st_mode=info.st_mode)
                def root_stat(path, *args, **kwargs):
                    info = original_stat(path, *args, **kwargs)
                    return owned(info) if path == work else info
                def root_lstat(path, *args, **kwargs):
                    info = original_lstat(path, *args, **kwargs)
                    return owned(info) if path == work / 'additional-app-sources.json' else info
                with patch.object(acl, 'WORK', work), patch.object(acl, 'validate'), \
                     patch.object(acl, 'enforce') as enforce, patch.object(acl, 'remove') as remove, \
                     patch.object(Path, 'stat', root_stat), patch.object(Path, 'lstat', root_lstat), \
                     patch('sys.argv', ['acl', '--site', 'prod', '--phase', 'add-source', '--source', '10.4.0.9']):
                    acl.save({'site': 'prod', 'status': status, 'token': 'test-receipt'})
                    if status == 'pending':
                        with self.assertRaisesRegex(ValueError, 'committed ACL'):
                            acl.main()
                        enforce.assert_not_called()
                        self.assertFalse((work / 'additional-app-sources.json').exists())
                    else:
                        acl.main()
                        acl.main()
                        self.assertEqual(acl.app_sources('prod'), ['10.4.0.16', '10.4.0.9'])
                        self.assertEqual(enforce.call_args_list, [unittest.mock.call('prod')] * 2)
                        state = json.loads((work / 'receipt.json').read_text())
                        self.assertEqual(state['status'], 'committed')
                        self.assertEqual(state['additional_app_sources'], ['10.4.0.16', '10.4.0.9'])
                    remove.assert_not_called()

    def test_additive_sources_preserve_old_host_and_validate_private_config(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            config = work / 'additional-app-sources.json'
            config.write_text(json.dumps({'site': 'prod', 'sources': ['10.4.0.19', '10.4.0.19']}))
            config.chmod(0o600)
            original = Path.lstat
            def root_lstat(path, *args, **kwargs):
                info = original(path, *args, **kwargs)
                if path == config:
                    return SimpleNamespace(st_uid=0, st_mode=info.st_mode)
                return info
            with patch.object(acl, 'WORK', work), patch.object(Path, 'lstat', root_lstat):
                self.assertEqual(acl.app_sources('prod'), ['10.4.0.16', '10.4.0.19'])
                for forward in (True, False):
                    entries = acl.rules('prod', forward)
                    for port in ('5432', '6379'):
                        selected = [r for r in entries if port in r]
                        for source in ('10.4.0.16/32', '10.4.0.19/32'):
                            self.assertTrue(any(source in r and r[-1] == 'RETURN' for r in selected))
                        self.assertEqual(selected[-1][-1], 'DROP')
                with self.assertRaises(ValueError):
                    acl.app_sources('cn-prod')
                config.chmod(0o644)
                with self.assertRaises(ValueError):
                    acl.app_sources('prod')
        for source in ('0.0.0.0/0', '127.0.0.1', '10.2.4.11', '10.4.0.255', '8.8.8.8'):
            with self.subTest(source=source), self.assertRaises(ValueError):
                acl.validate_source('prod', source)

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
