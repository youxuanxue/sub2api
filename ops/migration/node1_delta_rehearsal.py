#!/usr/bin/env python3
"""Measure real-size deltas against the independently restored backup only.

The held Tokyo candidate is read-only throughout. The restored backup is first
perturbed, then repaired by the production content-delta engine. No application,
network, source-freeze, DNS or activation operation is available here.
"""
import json
import os
from pathlib import Path
import socket
import time

if __package__:
    from . import fresh_host_delta as engine
    from .node1_candidate import Policy, CONTAINER
    from .node1_backup_rehearsal import DATABASE, WORK as BACKUP_WORK
else:
    import fresh_host_delta as engine
    from node1_candidate import Policy, CONTAINER
    from node1_backup_rehearsal import DATABASE, WORK as BACKUP_WORK

WORK = Path('/data/tokenkey-node1-tokyo-delta-rehearsal')


class RestorePolicy:
    work = WORK
    source = socket.gethostname()

    def guard(self, db):
        Policy().guard()
        if (db.database != DATABASE or db.scope not in ('precious', 'usage')
                or db.prefix != ['docker', 'exec', '-i', CONTAINER]):
            raise RuntimeError('only the independent backup restore can be modified')
        if not json.loads((BACKUP_WORK / 'receipt.json').read_text()).get('restore_verified'):
            raise RuntimeError('complete off-box restore acceptance first')
        if db.run("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() "
                  "AND pid<>pg_backend_pid() AND backend_type='client backend'") != '0':
            raise RuntimeError('another client is using the disposable restore')


def manifest(db):
    with engine.Snapshot(db) as snap:
        return engine.manifest(db, snap)


def equivalent(left, right):
    for key in ('schema', 'tables'):
        if left[key] != right[key]:
            raise RuntimeError('rehearsal content mismatch: ' + key)
    def next_values(value):
        return {k: int(v['last_value']) + int(v['is_called']) for k, v in value['sequences'].items()}
    if next_values(left) != next_values(right):
        raise RuntimeError('rehearsal sequence mismatch')


def main():
    os.umask(0o077)
    Policy().guard()
    WORK.mkdir(mode=0o700)  # Preserve all earlier attempts; do not overwrite.
    results = []
    for scope in ('precious', 'usage'):
        source = engine.DB('tokenkey', scope)
        target = engine.DB(DATABASE, scope)
        for db in (source, target):
            db.prefix = ['docker', 'exec', '-i', CONTAINER]
        policy = RestorePolicy()
        policy.guard(target)
        started = time.monotonic()
        original = manifest(source)
        equivalent(original, manifest(target))
        # These mutations are exclusively in the independently restored DB.
        if scope == 'precious':
            target.run("UPDATE accounts SET priority=priority+1 WHERE id="
                       "(SELECT min(id) FROM accounts WHERE deleted_at IS NULL); "
                       "UPDATE api_keys SET name=name||'-restore-fixture' WHERE id="
                       "(SELECT min(id) FROM api_keys WHERE deleted_at IS NULL); "
                       "UPDATE proxies SET password='restore-fixture' WHERE id="
                       "(SELECT min(id) FROM proxies WHERE deleted_at IS NULL)")
        else:
            # Real historical edits and gaps, plus a spurious row. Reconciliation
            # must repair these even though the source max(id) has not advanced.
            target.run("UPDATE ONLY usage_logs_20260905 SET actual_cost=actual_cost+0.00000001 "
                       "WHERE id=(SELECT min(id) FROM ONLY usage_logs_20260905); "
                       "DELETE FROM ONLY usage_logs_20260905 WHERE id="
                       "(SELECT max(id) FROM ONLY usage_logs_20260905); "
                       "DELETE FROM usage_logs WHERE id=(SELECT max(id) FROM usage_logs); "
                       "INSERT INTO usage_logs SELECT (jsonb_populate_record(NULL::usage_logs,"
                       "to_jsonb(t)||jsonb_build_object('id',-991001,'request_id',"
                       "'node1-restore-fixture-not-a-user-request'))).* "
                       "FROM ONLY usage_logs_20260905 t ORDER BY id LIMIT 1")
        base = manifest(target)
        if not engine.changes(base, original):
            raise RuntimeError('fixture did not change any rows')
        export_start = time.monotonic()
        bundle = WORK / (scope + '-bundle')
        exported = engine.export_bundle(source, base, bundle)
        export_seconds = time.monotonic() - export_start
        apply_start = time.monotonic()
        applied = engine.apply_bundle(target, bundle, socket.gethostname(), policy=policy)
        apply_seconds = time.monotonic() - apply_start
        equivalent(original, manifest(target))
        Policy().guard()
        result = {"scope": scope, **exported, **applied,
                  "export_seconds": round(export_seconds, 2),
                  "apply_seconds": round(apply_seconds, 2),
                  "total_seconds": round(time.monotonic() - started, 2),
                  "original_content_restored": True, "source_candidate_modified": False,
                  "cross_cloud_transfer_measured": False, "final_freeze_verified": False}
        results.append(result)
        (WORK / 'progress.json').write_text(json.dumps(results, indent=2))
        print(json.dumps(result), flush=True)
    (WORK / 'receipt.json').write_text(json.dumps(results, indent=2))


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        raise SystemExit('Node1 delta rehearsal failed: ' + type(exc).__name__) from None
