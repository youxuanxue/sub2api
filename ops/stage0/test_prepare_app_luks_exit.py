"""Preparation ownership, drift, failure receipts and live-copy limits."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

from ops.stage0 import prepare_app_luks_exit as migration


class PrepareLuksExitTest(unittest.TestCase):
    def test_live_copy_preserves_metadata_and_never_claims_consistency(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(migration, 'WORK', Path(directory)):
            completed = SimpleNamespace(returncode=24, stdout='Number of files: 10\nTotal transferred file size: 32 bytes\nsecret path must not be returned\n')
            with patch.object(migration.subprocess, 'run', return_value=completed) as run:
                receipt = migration.copy_data(Path(directory) / 'staging')
            self.assertFalse(receipt['consistent'])
            self.assertEqual(receipt['returncode'], 24)
            self.assertEqual(receipt['stats'], ['Number of files: 10', 'Total transferred file size: 32 bytes'])
            self.assertEqual(json.loads((Path(directory) / 'precopy.json').read_text()), receipt)
            self.assertEqual(run.call_args.args[0], migration.copy_command(migration.ROOT, Path(directory) / 'staging'))
            self.assertEqual((Path(directory) / 'precopy.json').stat().st_mode & 0o777, 0o600)
            self.assertIn('-aHAXSx', run.call_args.args[0])
            self.assertIn('--numeric-ids', run.call_args.args[0])

    def test_copy_failure_is_persisted_without_secret_output(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(migration, 'WORK', Path(directory)), \
             patch.object(migration.subprocess, 'run', return_value=SimpleNamespace(returncode=23, stdout='private filename')):
            with self.assertRaises(RuntimeError):
                migration.copy_data(Path(directory) / 'staging')
            receipt = json.loads((Path(directory) / 'precopy.json').read_text())
            self.assertEqual(receipt['returncode'], 23)
            self.assertEqual(receipt['stats'], [])

    def test_workspace_refuses_unowned_files_symlinks_and_configuration_drift(self):
        identity = {k: 'identity' for k in ('host', 'machine_id', 'root_uuid', 'old_uuid', 'config_sha256')}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch.object(migration, 'WORK', root / 'owned'), patch.object(migration.os, 'geteuid', return_value=0):
                # Tests run on macOS as a regular user; preserve real mode checks.
                original_stat = Path.stat
                def owned_stat(path, *args, **kwargs):
                    info = original_stat(path, *args, **kwargs)
                    if path == root / 'owned':
                        return SimpleNamespace(st_uid=0, st_mode=info.st_mode, st_dev=info.st_dev)
                    return info
                with patch.object(Path, 'stat', owned_stat):
                    dest = migration.owned_workspace(identity)
                    self.assertEqual(dest, root / 'owned/staging')
                    self.assertEqual(json.loads((root / 'owned/identity.json').read_text()), identity)
                    with self.assertRaises(ValueError):
                        migration.owned_workspace({**identity, 'config_sha256': 'drift'})
                (root / 'owned/staging').rmdir()
                (root / 'owned/staging').symlink_to(root)
                with self.assertRaises(ValueError):
                    migration.owned_workspace(identity)
            with patch.object(migration, 'WORK', root / 'unowned'):
                (root / 'unowned').mkdir(mode=0o700)
                (root / 'unowned/keep').write_text('do not overwrite')
                with self.assertRaises(ValueError):
                    migration.owned_workspace(identity)
                self.assertEqual((root / 'unowned/keep').read_text(), 'do not overwrite')


if __name__ == '__main__':
    unittest.main()
