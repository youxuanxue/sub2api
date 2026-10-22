"""Credential handoff must fail closed without exposing or replacing secrets."""
import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import tencent_backup_identity as identity


class BackupIdentityTest(unittest.TestCase):
    def test_policy_limits_every_statement_to_target_and_source_to_reads(self):
        bucket = 'arn:aws:s3:::' + identity.BUCKET
        statements = identity.policy()['Statement']
        object_allows = {}
        for row in statements:
            self.assertEqual(row['Condition']['StringEquals'], {'aws:userid': identity.USER_ID})
            if row['Effect'] == 'Allow' and row['Resource'] != bucket:
                object_allows[row['Resource']] = set(row['Action'])
        self.assertEqual(object_allows[bucket + '/' + identity.SOURCE_PREFIX + '/*'],
                         {'s3:GetObject', 's3:GetObjectVersion'})
        self.assertEqual(object_allows[bucket + '/' + identity.CANDIDATE_PREFIX + '/*'],
                         {'s3:GetObject', 's3:PutObject', 's3:DeleteObject',
                          's3:AbortMultipartUpload', 's3:ListMultipartUploadParts'})
        denies = [row for row in statements if row['Effect'] == 'Deny']
        self.assertEqual([(r['Resource'], r['NotAction']) for r in denies],
                         [(bucket + '/' + identity.SOURCE_PREFIX + '/*',
                           ['s3:GetObject', 's3:GetObjectVersion'])])

    def test_only_expected_managed_instance_is_accepted(self):
        expected = {'Account': identity.ACCOUNT, 'UserId': identity.USER_ID,
                    'Arn': 'arn:aws:sts::' + identity.ACCOUNT + ':assumed-role/'
                           + identity.ROLE + '/' + identity.INSTANCE}
        identity.validate_identity(expected)
        for key in expected:
            with self.subTest(key=key), self.assertRaises(ValueError):
                identity.validate_identity({**expected, key: 'another-host'})

    def test_private_file_and_temporary_credentials_are_required(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'credentials'
            path.write_text('[default]\naws_access_key_id=synthetic-id\n'
                            'aws_secret_access_key=synthetic-secret\naws_session_token=synthetic-token\n')
            path.chmod(0o600)
            values = identity.read_credentials(path, os.getuid())
            self.assertEqual(values['aws_session_token'], 'synthetic-token')
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                identity.read_credentials(path, os.getuid())
            path.chmod(0o600)
            with self.assertRaises(ValueError):
                identity.read_credentials(path, os.getuid() + 1)
            link = path.with_name('link')
            link.symlink_to(path)
            with self.assertRaises(OSError):
                identity.read_credentials(link, os.getuid())
            path.write_text('[default]\naws_access_key_id=id\naws_secret_access_key=secret\n')
            with self.assertRaises(KeyError):
                identity.read_credentials(path, os.getuid())

    def test_repository_separation_and_atomic_rotation(self):
        values = {'aws_access_key_id': 'synthetic-id',
                  'aws_secret_access_key': 'synthetic-secret', 'aws_session_token': 'synthetic-token'}
        old = identity.render_config(values)
        new = identity.render_config(values, candidate=True)
        self.assertIn('repo1-path=/prod/postgres\n', old)
        self.assertNotIn('repo1-path=/prod/postgres\n', new)
        self.assertIn('repo1-path=/' + identity.CANDIDATE_PREFIX + '/postgres\n', new)
        self.assertIn('repo1-s3-key-type=shared\n', old)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'pgbackrest.conf'
            path.write_text('old-session')
            with patch.object(identity.os, 'fchown'):
                identity.atomic_private(path, old)
                self.assertEqual(path.read_text(), old)
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                with patch.object(identity.os, 'replace', side_effect=OSError('synthetic failure')):
                    with self.assertRaises(OSError):
                        identity.atomic_private(path, new)
                self.assertEqual(path.read_text(), old)
                self.assertEqual(list(Path(directory).glob('.identity-*')), [])

    def test_authentication_failure_keeps_current_configuration(self):
        with patch.object(identity.os, 'geteuid', return_value=0), \
             patch.object(identity.socket, 'gethostname', return_value=identity.HOST), \
             patch.object(identity.Path, 'is_symlink', return_value=False), \
             patch.object(identity.subprocess, 'check_output', return_value='tmpfs\n'), \
             patch.object(identity, 'read_credentials', return_value={}), \
             patch.object(identity, 'caller_identity', return_value={'UserId': 'wrong'}), \
             patch.object(identity, 'atomic_private') as write:
            with self.assertRaises(ValueError):
                identity.refresh()
            write.assert_not_called()

    def test_error_output_never_includes_credentials(self):
        output = io.StringIO()
        with patch('sys.argv', ['identity', 'refresh']), \
             patch.object(identity, 'refresh', side_effect=RuntimeError('synthetic-secret')), \
             contextlib.redirect_stdout(output):
            self.assertEqual(identity.main(), 1)
        self.assertNotIn('synthetic-secret', output.getvalue())
        self.assertEqual(json.loads(output.getvalue())['cutover_authorized'], False)

    def test_identity_comes_from_tencent_matrix_not_constants(self):
        matrix = json.loads((Path(__file__).resolve().parents[2] / 'deploy' / 'tencent'
                             / 'candidate-usw-20261022.json').read_text())['target']
        self.assertEqual(identity.INSTANCE, matrix['expected_ssm_instance_id'])
        self.assertEqual(identity.HOST, matrix['hostname'])
        self.assertEqual(identity.ROLE, matrix['ssm_hybrid_role_name'])
        self.assertEqual(identity.SSM_REGION, matrix['ssm_region'])
        self.assertEqual(identity.USER_ID, identity.ROLE_ID + ':' + matrix['expected_ssm_instance_id'])
        source = Path(identity.__file__).read_text()
        self.assertNotIn(matrix['expected_ssm_instance_id'], source)
        self.assertNotIn("'" + matrix['ssm_region'] + "'", source)

    def test_matrix_lookup_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(ValueError):
                identity.load_target([root / 'missing.json'])
            partial = root / 'partial.json'
            partial.write_text(json.dumps({'target': {'expected_ssm_instance_id': 'mi-0abc',
                                                      'hostname': 'h'}}))
            with self.assertRaises(ValueError):
                identity.load_target([partial])
            ec2 = root / 'ec2.json'
            ec2.write_text(json.dumps({'target': {'expected_ssm_instance_id': 'i-0abc', 'hostname': 'h',
                                                  'ssm_hybrid_role_name': 'r', 'ssm_region': 'x'}}))
            with self.assertRaises(ValueError):
                identity.load_target([ec2])

    def test_explicit_matrix_env_is_authoritative(self):
        with tempfile.TemporaryDirectory() as directory, \
             patch.dict(os.environ, {'TK_TENCENT_PROD_MATRIX': str(Path(directory) / 'none.json')}):
            self.assertEqual(identity.matrix_candidates(), [Path(directory) / 'none.json'])
            with self.assertRaises(ValueError):
                identity.load_target()

    def test_missing_matrix_refuses_every_action(self):
        with patch.object(identity, '_TARGET', None):
            for action in (identity.policy, identity.refresh,
                           lambda: identity.validate_identity({})):
                with self.subTest(action=action), self.assertRaises(ValueError):
                    action()


if __name__ == '__main__':
    unittest.main()
