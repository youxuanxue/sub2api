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

    def test_cos_credentials_require_root_only_exact_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'cos-backup.env'
            path.write_text('tencent_SecretId=synthetic-id\n'
                            'tencent_SecretKey=synthetic-secret\n')
            path.chmod(0o600)
            values = identity.read_cos_credentials(path, os.getuid())
            self.assertEqual(values, {'key': 'synthetic-id', 'secret': 'synthetic-secret'})
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                identity.read_cos_credentials(path, os.getuid())
            path.chmod(0o600)
            path.write_text('tencent_SecretId=synthetic-id\n')
            with self.assertRaises(ValueError):
                identity.read_cos_credentials(path, os.getuid())
            path.write_text('tencent_SecretId=synthetic-id\n'
                            'tencent_SecretKey=synthetic secret\n')
            with self.assertRaises(ValueError):
                identity.read_cos_credentials(path, os.getuid())

    def test_rendered_config_is_cos_only_with_no_aws(self):
        # 2026-11-08: AWS repo1 retired. The rendered config must carry no repo1
        # option, no AWS endpoint and no STS token, and COS stays at repo2.
        cos = {'key': 'cos-id', 'secret': 'cos-secret'}
        config = identity.render_config(cos)
        self.assertNotIn('repo1-', config)
        self.assertNotIn('amazonaws.com', config)
        self.assertNotIn('s3-token', config)
        self.assertNotIn(identity.BUCKET, config)
        self.assertNotIn(identity.CANDIDATE_PREFIX, config)
        self.assertIn('repo2-type=s3\n', config)
        self.assertIn('repo2-path=' + identity.COS_PATH + '\n', config)
        self.assertIn('repo2-s3-bucket=' + identity.COS_BUCKET + '\n', config)
        self.assertIn('repo2-s3-endpoint=' + identity.COS_ENDPOINT + '\n', config)
        self.assertIn('repo2-s3-uri-style=host\n', config)
        self.assertIn('repo2-s3-key=cos-id\n', config)
        self.assertIn('repo2-s3-key-secret=cos-secret\n', config)

    def test_module_no_longer_reads_aws_session_or_calls_sts(self):
        body = Path(identity.__file__).read_text()
        for needle in ('/root/.aws', 'GetCallerIdentity', 'aws_session_token', 'repo1-s3-token'):
            self.assertNotIn(needle, body)

    def test_atomic_rotation_keeps_old_file_on_failure(self):
        old = identity.render_config({'key': 'a', 'secret': 'b'})
        new = identity.render_config({'key': 'c', 'secret': 'd'})
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'pgbackrest.conf'
            path.write_text('previous')
            with patch.object(identity.os, 'fchown'):
                identity.atomic_private(path, old)
                self.assertEqual(path.read_text(), old)
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                with patch.object(identity.os, 'replace', side_effect=OSError('synthetic failure')):
                    with self.assertRaises(OSError):
                        identity.atomic_private(path, new)
                self.assertEqual(path.read_text(), old)
                self.assertEqual(list(Path(directory).glob('.identity-*')), [])

    def _refresh_arena(self, stack, directory):
        source = Path(directory) / 'recovery' / 'config' / 'pgbackrest.conf'
        candidate = Path(directory) / 'candidate' / 'config' / 'pgbackrest.conf'
        stack.enter_context(patch.object(identity, 'SOURCE_CONFIG', source))
        stack.enter_context(patch.object(identity, 'CANDIDATE_CONFIG', candidate))
        stack.enter_context(patch.object(identity.os, 'geteuid', return_value=0))
        stack.enter_context(patch.object(identity.socket, 'gethostname', return_value=identity.HOST))
        stack.enter_context(patch.object(identity.subprocess, 'check_output', return_value='tmpfs\n'))
        stack.enter_context(patch.object(identity.os, 'fchown'))
        # macOS temp dirs live under the /var -> /private/var symlink; the
        # symlink guard itself is exercised by atomic_private's own check.
        stack.enter_context(patch.object(identity.Path, 'is_symlink', return_value=False))
        return source, candidate

    def test_refresh_writes_cos_only_config_to_both_runtime_paths(self):
        with tempfile.TemporaryDirectory() as directory, contextlib.ExitStack() as stack:
            source, candidate = self._refresh_arena(stack, directory)
            stack.enter_context(patch.object(
                identity, 'read_cos_credentials', return_value={'key': 'cos-id', 'secret': 'cos-secret'}))
            result = identity.refresh()
            for path in (source, candidate):
                body = path.read_text()
                self.assertNotIn('repo1-', body)
                self.assertIn('repo2-s3-key=cos-id\n', body)
        self.assertEqual(result['aws_repo1'], 'retired')
        self.assertNotIn('cos-secret', json.dumps(result))

    def test_missing_cos_credentials_fail_without_aws_fallback(self):
        with tempfile.TemporaryDirectory() as directory, contextlib.ExitStack() as stack:
            source, candidate = self._refresh_arena(stack, directory)
            candidate.parent.mkdir(parents=True)
            candidate.write_text('previous')
            stack.enter_context(patch.object(
                identity, 'read_cos_credentials', side_effect=FileNotFoundError('absent')))
            with self.assertRaises(FileNotFoundError):
                identity.refresh()
            self.assertEqual(candidate.read_text(), 'previous')
            self.assertFalse(source.exists())

    def test_wrong_host_keeps_current_configuration(self):
        with patch.object(identity.os, 'geteuid', return_value=0), \
             patch.object(identity.socket, 'gethostname', return_value='another-host'), \
             patch.object(identity, 'atomic_private') as write:
            with self.assertRaisesRegex(ValueError, 'data-machine pgBackRest config is static'):
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
            for action in (identity.policy, identity.refresh):
                with self.subTest(action=action), self.assertRaises(ValueError):
                    action()


if __name__ == '__main__':
    unittest.main()
