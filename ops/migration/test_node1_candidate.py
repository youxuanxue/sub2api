import copy
import configparser
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from ops.migration import node1_candidate as candidate
from ops.migration import usage_history_audit as audit
from ops.migration import node1_history_rehearsal as history
from ops.migration import node1_full_backup as backup
from ops.migration import node1_ops_prepare as host_ops
from ops.migration import node1_runtime_guard as runtime_guard
from ops.migration import node1_delta_rehearsal as delta_rehearsal


def held_container():
    return {'Id': 'candidate', 'Name': '/' + candidate.CONTAINER,
            'Image': candidate.IMAGE, 'State': {'Running': True},
            'Config': {'Labels': dict(candidate.LABEL)},
            'HostConfig': {'NetworkMode': 'none', 'PortBindings': {},
                           'RestartPolicy': {'Name': 'no'}, 'Privileged': False},
            'NetworkSettings': {'Networks': {'none': {}}},
            'Mounts': [{'Source': str(candidate.ROOT / 'postgres'),
                        'Destination': '/var/lib/postgresql', 'RW': True}]}


class CandidateSafetyTest(unittest.TestCase):
    def test_candidate_is_bound_to_ucloud_source(self):
        self.assertEqual(candidate.SOURCE, 'ucloud-cn-prod-106.75.29.139')
        self.assertEqual(candidate.ROOT, Path('/data/tokenkey-prod-candidate-ucloud-cn-current'))
        self.assertEqual(candidate.CONTAINER, 'tokenkey-prod-candidate-ucloud-cn-current-postgres')

    def test_missing_inventory_fails_before_identity_or_disk_commands(self):
        with patch.object(candidate, 'MIGRATION_INVENTORY', Path('/definitely/missing/node1-inventory.json')), \
                patch.object(candidate, 'output') as command:
            with self.assertRaisesRegex(RuntimeError, 'inventory is not verified'):
                candidate.host_guard()
            command.assert_not_called()

    def test_wrong_host_fails_before_any_disk_command(self):
        with patch.object(candidate.os, 'geteuid', return_value=0), \
                patch.object(candidate, 'verified_inventory', return_value={
                    'machine_id': 'expected', 'data_uuid': 'expected-volume',
                    'postgres_image': candidate.IMAGE, 'domain': 'api-cn.tokenkey.dev'}), \
                patch.object(Path, 'read_text', return_value='foreign-host'), \
                patch.object(candidate, 'output') as command:
            with self.assertRaisesRegex(RuntimeError, 'identity'):
                candidate.host_guard()
            command.assert_not_called()

    def test_only_held_owned_database_is_accepted(self):
        info = held_container()
        candidate.validate_container(info, [info])
        cases = [('NetworkMode', 'bridge'), ('PortBindings', {'5432/tcp': [{}]}),
                 ('RestartPolicy', {'Name': 'always'}), ('Privileged', True)]
        for field, value in cases:
            changed = copy.deepcopy(info)
            changed['HostConfig'][field] = value
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                candidate.validate_container(changed, [changed])
        for field, value in [('Name', '/tokenkey-postgres'), ('Image', 'other-image')]:
            changed = copy.deepcopy(info)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                candidate.validate_container(changed, [changed])

    def test_other_container_cannot_share_candidate_data(self):
        info = held_container()
        other = {'Id': 'app', 'Mounts': [{'Source': str(candidate.ROOT)}]}
        with self.assertRaisesRegex(RuntimeError, 'another container'):
            candidate.validate_container(info, [info, other])


class UsageHistoryAuditTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.source = Path(self.directory.name) / 'source.jsonl'
        self.target = Path(self.directory.name) / 'target.jsonl'
        self.row = dict.fromkeys(audit.FIELDS, 10)
        self.row.update(user_id=5, day='2026-09-01', first='2026-09-01T00:00:00+00',
                        last='2026-09-01T01:00:00+00')
        self.write(self.source, [self.row])

    def write(self, path, rows):
        path.write_text('BEGIN\n' + '\n'.join(json.dumps(r) for r in rows) + '\nROLLBACK\n')

    def test_same_tenant_history_matches(self):
        self.write(self.target, [self.row])
        self.assertTrue(audit.compare(self.source, self.target)['matched'])

    def test_equal_global_totals_cannot_hide_wrong_tenant_or_changed_content(self):
        for mutation in ({'user_id': 6}, {'row_fingerprint': 11}, {'actual_cost': 11},
                         {'cache_read_tokens': 11}, {'rows': 9}, {'day': '2026-09-02'}):
            self.write(self.target, [self.row | mutation])
            with self.subTest(mutation=mutation):
                self.assertFalse(audit.compare(self.source, self.target)['matched'])

    def test_empty_duplicate_or_incomplete_manifest_rejected(self):
        for rows in ([], [self.row, self.row], [{'user_id': 5}]):
            self.write(self.target, rows)
            with self.assertRaises(ValueError):
                audit.compare(self.source, self.target)

    def test_decimal_precision_is_not_rounded_away(self):
        text = json.dumps(self.row).replace('"actual_cost": 10', '"actual_cost": 0.12345678901234567890')
        self.source.write_text(text + '\n')
        self.target.write_text(text.replace('0.12345678901234567890', '0.12345678901234567891') + '\n')
        self.assertFalse(audit.compare(self.source, self.target)['matched'])


class HistoryIsolationTest(unittest.TestCase):
    def test_only_owned_namespace_and_mounts_are_accepted(self):
        info = {'Name': '/' + history.APP,
                'Image': history.APP_IMAGE,
                'Config': {'Labels': {'tokenkey.rehearsal': 'node1-ucloud-cn-history'}},
                'HostConfig': {'NetworkMode': 'container:pg', 'PortBindings': {},
                               'RestartPolicy': {'Name': 'no'}},
                'Mounts': [{'Type': 'bind', 'Source': str(history.WORK / 'app'),
                            'Destination': '/app/data'}]}
        history.validate(info, 'pg')
        for field, value in [('NetworkMode', 'bridge'), ('Privileged', True),
                             ('CapAdd', ['NET_ADMIN']), ('PortBindings', {'8080': []})]:
            changed = copy.deepcopy(info)
            changed['HostConfig'][field] = value
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                history.validate(changed, 'pg')
        changed = copy.deepcopy(info)
        changed['Mounts'][0]['Source'] = str(candidate.ROOT / 'postgres')
        with self.assertRaises(RuntimeError):
            history.validate(changed, 'pg')


class Node1BackupTest(unittest.TestCase):
    def test_delta_rehearsal_refuses_baseline_before_querying_it(self):
        db = delta_rehearsal.engine.DB('tokenkey', 'usage')
        db.prefix = ['docker', 'exec', '-i', candidate.CONTAINER]
        with patch.object(candidate.Policy, 'guard'), patch.object(db, 'run') as query:
            with self.assertRaisesRegex(RuntimeError, 'independent backup'):
                delta_rehearsal.RestorePolicy().guard(db)
            query.assert_not_called()

    def test_restarted_sequence_preserves_the_next_allocated_id(self):
        before = {'schema': 'same', 'tables': {},
                  'sequences': {'usage_logs_id_seq': {'last_value': 123, 'is_called': True}}}
        after = copy.deepcopy(before)
        after['sequences']['usage_logs_id_seq'] = {'last_value': 124, 'is_called': False}
        delta_rehearsal.equivalent(before, after)
        after['sequences']['usage_logs_id_seq']['last_value'] = 123
        with self.assertRaisesRegex(RuntimeError, 'sequence mismatch'):
            delta_rehearsal.equivalent(before, after)

    def test_failed_offbox_copy_is_fatal_and_preserves_local_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            artifact = Path(directory) / 'full.dump'
            artifact.write_bytes(b'keep-local-copy')
            with patch.object(backup, 'run', side_effect=RuntimeError('upload failed')):
                with self.assertRaises(RuntimeError):
                    backup.upload_archive(artifact, backup.PREFIX+'test.dump', 'hash', {})
            self.assertEqual(artifact.read_bytes(), b'keep-local-copy')
            with patch.object(backup, 'run') as command:
                with self.assertRaises(ValueError):
                    backup.upload_archive(artifact, 'prod/test.dump', 'hash', {})
                command.assert_not_called()

    def test_production_prefix_and_ambient_aws_identity_cannot_be_used(self):
        config = dict(bucket=backup.BUCKET, prefix=backup.PREFIX, region='us-east-1',
                      credentials_file='/private/credentials')
        credentials = '[node1-backup]\naws_access_key_id=fixture\naws_secret_access_key=fixture-secret\n'
        with patch.object(backup, 'private', return_value=credentials), \
                patch.dict(backup.os.environ, {'AWS_ACCESS_KEY_ID': 'ambient',
                    'AWS_SECRET_ACCESS_KEY': 'ambient', 'AWS_ENDPOINT_URL': 'https://wrong.invalid'}):
            env = backup.backup_environment(config)
            self.assertNotIn('AWS_ACCESS_KEY_ID', env)
            self.assertNotIn('AWS_SECRET_ACCESS_KEY', env)
            self.assertNotIn('AWS_ENDPOINT_URL', env)
            self.assertEqual(env['AWS_PROFILE'], 'node1-backup')
            with self.assertRaises(ValueError):
                backup.backup_environment(config | {'prefix': 'prod/pgdump/'})


class DormantHostUnitsTest(unittest.TestCase):
    def test_staged_mount_follows_candidate_and_keeps_approval_condition(self):
        unit = configparser.ConfigParser()
        with patch.object(runtime_guard, 'ROOT', Path('/data/reviewed-new-candidate')):
            unit.read_string(runtime_guard.mount_unit())
        self.assertEqual(unit['Mount']['What'], '/data/reviewed-new-candidate/postgres/18/docker')
        self.assertEqual(unit['Mount']['Where'], '/var/lib/tokenkey/postgres')
        self.assertEqual(unit['Unit']['ConditionPathExists'], str(backup.ACTIVATED))

    def test_runtime_guard_rejects_unapproved_start_before_mount_commands(self):
        with patch.object(runtime_guard, 'host_guard'), \
                patch.object(runtime_guard, 'private', return_value='{"target":"zhupeng-prod","cutover_approved":false}'), \
                patch.object(runtime_guard.subprocess, 'run') as command:
            with self.assertRaisesRegex(RuntimeError, 'approval'):
                runtime_guard.verify()
            command.assert_not_called()

    def test_replacement_requires_matching_reviewed_hash_and_preserves_original(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'watch.service'
            path.write_bytes(b'original')
            for previous in (None, {str(path): 'stale'}):
                with self.assertRaises(RuntimeError):
                    host_ops.install_owned(path, b'new', previous=previous)
                self.assertEqual(path.read_bytes(), b'original')
            digest = hashlib.sha256(b'original').hexdigest()
            host_ops.install_owned(path, b'new', previous={str(path): digest})
            self.assertEqual(path.read_bytes(), b'new')
            self.assertEqual(path.with_name(path.name + '.before-' + digest[:16]).read_bytes(), b'original')

    def test_symlink_cannot_redirect_installation(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'operator-file'
            target.write_bytes(b'keep')
            path = Path(directory) / 'watch.service'
            path.symlink_to(target)
            with self.assertRaises(RuntimeError):
                host_ops.install_owned(path, b'new')
            self.assertEqual(target.read_bytes(), b'keep')


if __name__ == '__main__':
    unittest.main()
