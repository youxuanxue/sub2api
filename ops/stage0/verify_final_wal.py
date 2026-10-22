#!/usr/bin/env python3
"""Offline consistency check of final-WAL receipts; never authorizes promotion.

Receipts must be collected during an approved window. This checks their contents,
not their authenticity or whether all writers were really fenced. No cloud/SQL
calls, subprocesses, promotion, DNS changes, or production writes are performed.
"""
import argparse
import datetime as dt
import importlib.util
import json
from pathlib import Path
import re

_spec = importlib.util.spec_from_file_location(
    'migration_snapshots', Path(__file__).parents[1] / 'observability/compare-pg-migration-snapshots.py')
_snapshots = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_snapshots)


def verify(bundle, now=None):
    now = now or dt.datetime.now(dt.timezone.utc)
    if not isinstance(bundle, dict):
        raise ValueError('bundle must be an object')
    receipts = {key: bundle[key] for key in ('fence', 'source', 'archive', 'target')}
    stamps = {}
    for name, row in receipts.items():
        if not isinstance(row, dict) or row.get('kind') != 'final_wal_' + name:
            raise ValueError('invalid receipt kind')
        stamp = dt.datetime.fromisoformat(row['at'])
        if stamp.utcoffset() is None or not -5 <= (now - stamp).total_seconds() <= 120:
            raise ValueError('stale, future or timezone-free receipt')
        stamps[name] = stamp
        if not isinstance(row.get('migration_id'), str) or not row['migration_id']:
            raise ValueError('missing migration identity')
        if not isinstance(row.get('system_identifier'), str) or not re.fullmatch(r'[1-9][0-9]*', row['system_identifier']):
            raise ValueError('invalid system identifier')
        if type(row.get('timeline')) is not int or not 0 < row['timeline'] <= 0xFFFFFFFF:
            raise ValueError('invalid timeline')
    for key in ('migration_id', 'system_identifier', 'timeline'):
        if len({row[key] for row in receipts.values()}) != 1:
            raise ValueError('receipt identity mismatch')
    if not stamps['fence'] <= stamps['source'] <= stamps['archive'] <= stamps['target']:
        raise ValueError('receipt order invalid')
    fence, source, archive, target = (receipts[k] for k in ('fence', 'source', 'archive', 'target'))
    for key in ('ingress_closed', 'writers_stopped', 'direct_writes_blocked', 'release_locked'):
        if fence.get(key) is not True:
            raise ValueError('source fencing evidence incomplete')
    for key in ('in_flight', 'active_transactions', 'writable_sessions'):
        if type(fence.get(key)) is not int or fence[key] != 0:
            raise ValueError('source still has requests or writers')
    if source.get('in_recovery') is not False:
        raise ValueError('source is not primary')
    if target.get('in_recovery') is not True or target.get('read_only') != 'on' or target.get('archive_mode') != 'off':
        raise ValueError('target is not isolated standby')
    final = _snapshots.lsn(source.get('final_lsn'))
    replay = _snapshots.lsn(target.get('replay_lsn'))
    if final is None or final <= 0 or replay is None or replay < final:
        raise ValueError('final LSN not replayed')
    if source.get('restore_point') != target.get('restore_point') or not source.get('restore_point'):
        raise ValueError('restore point evidence mismatch')
    if target.get('restore_point_replayed') is not True:
        raise ValueError('restore point replay not verified')
    size = source.get('wal_segment_size_bytes')
    if type(size) is not int or not (1 << 20) <= size <= (1 << 30) or size & (size - 1):
        raise ValueError('invalid WAL segment size')
    # A record end exactly at a segment boundary belongs to the previous segment.
    segno = (final - 1) // size
    per_log = (1 << 32) // size
    expected = f"{source['timeline']:08X}{segno // per_log:08X}{segno % per_log:08X}"
    if source.get('final_wal') != expected or archive.get('wal') != expected:
        raise ValueError('wrong final WAL segment')
    if archive.get('retrieved_and_verified') is not True:
        raise ValueError('final WAL retrieval not verified')
    if archive.get('repository_role') != 'source-readonly':
        raise ValueError('wrong repository role')
    return {'verdict': 'final_wal_receipts_consistent', 'final_lsn': source['final_lsn'],
            'final_wal': expected, 'target_replay_lsn': target['replay_lsn'],
            'cutover_authorized': False, 'promotion_performed': False,
            'live_fencing_verified_by_this_tool': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('bundle', type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(verify(json.loads(args.bundle.read_text()))))
    except (OSError, ValueError, KeyError, TypeError, AttributeError, OverflowError):
        # Input could accidentally contain credentials or production data.
        print(json.dumps({'verdict': 'final_wal_receipts_rejected', 'cutover_authorized': False}))
        return 2
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
