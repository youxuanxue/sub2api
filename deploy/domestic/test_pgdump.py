import importlib.util
import base64
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name+'.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class BackupTests(unittest.TestCase):
    def test_remote_object_must_match_size_and_checksum(self):
        module = load('pgdump')
        artifact = {'bytes': 42, 'sha256': 'a'*64}
        checksum = base64.b64encode(bytes.fromhex(artifact['sha256'])).decode()
        module.validate_head(artifact, {'ContentLength':42, 'ChecksumSHA256':checksum})
        for head in ({'ContentLength':41,'ChecksumSHA256':checksum},
                     {'ContentLength':42,'ChecksumSHA256':'wrong'},
                     {'ContentLength':42}):
            with self.subTest(head=head), self.assertRaises(ValueError):
                module.validate_head(artifact, head)

    def test_artifact_rejects_traversal_symlink_and_changed_content(self):
        module = load('pgdump_remote')
        with tempfile.TemporaryDirectory() as temp, patch.object(module, 'ROOT', Path(temp)):
            name = 'tokenkey-20261019T120000Z.sql.gz'
            path = Path(temp)/name
            path.write_bytes(b'before')
            before = module.artifact(name)
            path.write_bytes(b'after')
            self.assertNotEqual(before['sha256'], module.artifact(name)['sha256'])
            with self.assertRaises(ValueError):
                module.artifact('../secret')
            path.unlink()
            path.symlink_to(Path(temp)/'other')
            with self.assertRaises(ValueError):
                module.artifact(name)

    def test_failed_command_does_not_expose_signed_url(self):
        module = load('pgdump_remote')
        with self.assertRaisesRegex(RuntimeError, '^backup operation failed: sh$'):
            module.run(['sh','-c','echo signed-secret >&2; exit 2'])

    def test_apply_guard_prevents_remote_execution(self):
        module = load('pgdump_remote')
        with patch.dict(module.os.environ, {'APPLY':''}), self.assertRaises(ValueError):
            module.main()

    def test_dump_guard_accepts_root_filesystem_and_rejects_other_mounts(self):
        module = load('pgdump_remote')
        with tempfile.TemporaryDirectory() as temp:
            data = Path(temp) / 'data'
            data.mkdir()
            cases = (
                ('/\n', 'stop-after-guard'),
                (str(data) + '\n', 'stop-after-guard'),
                ('/mnt/lost\n', 'data volume is not mounted'),
            )
            for findmnt_out, expected in cases:
                seen = []

                def fake_run(args, **kwargs):
                    seen.append(args[0])
                    if args[0] == 'findmnt':
                        return findmnt_out
                    raise RuntimeError('stop-after-guard')

                with patch.object(module, 'ROOT', data / 'pgdump'), \
                     patch.object(module, 'STATE', Path(temp) / 'state'), \
                     patch.object(module, 'run', fake_run), \
                     patch.object(module.os, 'geteuid', return_value=0), \
                     patch.dict(module.os.environ, {'APPLY': 'yes-cn-pgdump'}), \
                     patch.object(module.sys, 'argv', ['pgdump_remote.py', 'dump']), \
                     patch.object(Path, 'is_file', lambda self: self == Path('/opt/tokenkey/compose.yml')), \
                     patch.object(Path, 'exists', lambda self: False):
                    with self.assertRaises((RuntimeError, ValueError)) as raised:
                        module.main()
                self.assertIn(expected, str(raised.exception))
                self.assertEqual(seen[0], 'findmnt')
                if expected == 'data volume is not mounted':
                    self.assertNotIn('bash', seen)


if __name__ == '__main__':
    unittest.main()
