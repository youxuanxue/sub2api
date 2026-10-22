import datetime as dt
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('final_wal', Path(__file__).with_name('verify_final_wal.py'))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
NOW = dt.datetime(2026, 10, 22, tzinfo=dt.timezone.utc)


def evidence():
    b = {k: dict(kind='final_wal_' + k, at=NOW.isoformat(), migration_id='synthetic',
                 system_identifier='123', timeline=1) for k in ('fence', 'source', 'archive', 'target')}
    b['fence'].update(ingress_closed=True, writers_stopped=True, direct_writes_blocked=True,
                      release_locked=True, in_flight=0, active_transactions=0, writable_sessions=0)
    b['source'].update(in_recovery=False, final_lsn='1/2000000', restore_point='synthetic-final',
                       wal_segment_size_bytes=16 * 1024 * 1024, final_wal='000000010000000100000001')
    b['archive'].update(wal='000000010000000100000001', retrieved_and_verified=True,
                        repository_role='source-readonly')
    b['target'].update(in_recovery=True, read_only='on', archive_mode='off', replay_lsn='1/2000000',
                       restore_point='synthetic-final', restore_point_replayed=True)
    return b


class FinalWalTest(unittest.TestCase):
    def test_boundary_uses_previous_segment_without_authorizing_cutover(self):
        result = gate.verify(evidence(), NOW)
        self.assertEqual(result['final_wal'], '000000010000000100000001')
        self.assertFalse(result['cutover_authorized'])
        self.assertFalse(result['live_fencing_verified_by_this_tool'])

    def test_next_record_requires_next_segment(self):
        b = evidence()
        b['source']['final_lsn'] = b['target']['replay_lsn'] = '1/2000001'
        with self.assertRaises(ValueError): gate.verify(b, NOW)
        b['source']['final_wal'] = b['archive']['wal'] = '000000010000000100000002'
        self.assertEqual(gate.verify(b, NOW)['verdict'], 'final_wal_receipts_consistent')

    def test_rejects_incomplete_or_conflicting_evidence(self):
        changes = [('fence', 'writers_stopped', False), ('fence', 'in_flight', 1),
                   ('fence', 'writable_sessions', False), ('fence', 'direct_writes_blocked', 'true'),
                   ('fence', 'release_locked', False), ('source', 'in_recovery', True),
                   ('archive', 'retrieved_and_verified', False), ('archive', 'wal', 'missing'),
                   ('archive', 'repository_role', 'new-primary'), ('target', 'timeline', 2),
                   ('target', 'timeline', True), ('target', 'system_identifier', '456'),
                   ('target', 'migration_id', 'different'), ('target', 'in_recovery', False),
                   ('target', 'replay_lsn', '1/1FFFFFF'), ('target', 'archive_mode', 'on'),
                   ('target', 'restore_point_replayed', False), ('target', 'restore_point', 'other'),
                   ('source', 'wal_segment_size_bytes', 3), ('source', 'final_lsn', None)]
        for row, key, value in changes:
            with self.subTest(row=row, key=key, value=value):
                b = evidence(); b[row][key] = value
                with self.assertRaises(ValueError): gate.verify(b, NOW)

    def test_rejects_missing_receipts_and_fields(self):
        for row in evidence():
            b = evidence(); del b[row]
            with self.assertRaises((ValueError, KeyError)): gate.verify(b, NOW)
            for key in evidence()[row]:
                b = evidence(); del b[row][key]
                with self.subTest(row=row, key=key), self.assertRaises((ValueError, KeyError)):
                    gate.verify(b, NOW)

    def test_stale_future_unzoned_and_wrong_order(self):
        for stamp in [NOW - dt.timedelta(seconds=121), NOW + dt.timedelta(seconds=6), NOW.replace(tzinfo=None)]:
            b = evidence(); b['target']['at'] = stamp.isoformat()
            with self.assertRaises(ValueError): gate.verify(b, NOW)
        b = evidence(); b['fence']['at'] = (NOW + dt.timedelta(seconds=1)).isoformat()
        with self.assertRaises(ValueError): gate.verify(b, NOW)


if __name__ == '__main__':
    unittest.main()
