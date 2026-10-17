#!/usr/bin/env python3
"""Real PG acceptance for old usage updates/backfills/deletes, on Node1 only.

Only two newly created synthetic databases are written. No baseline data or
production source is touched. The candidate hold and network isolation remain.
"""
import json
import gzip
import hashlib
from pathlib import Path
import socket
import tempfile
from unittest.mock import patch

import fresh_host_delta as engine
from node1_candidate import Policy, CONTAINER

SOURCE_DB = 'node1_usage_delta_test_source'
TARGET_DB = 'node1_usage_delta_test_target'


class FixturePolicy:
    work = Policy.work
    source = 'node1-usage-delta-fixture-source'

    def guard(self, db):
        Policy().guard()
        if db.database != TARGET_DB or db.prefix != ['docker', 'exec', '-i', CONTAINER] or db.scope != 'usage':
            raise RuntimeError('not the dedicated usage delta fixture')


def main():
    Policy().guard()
    source, target = [engine.DB(name, 'usage') for name in (SOURCE_DB, TARGET_DB)]
    admin = engine.DB()
    for db in (source, target, admin):
        db.prefix = ['docker', 'exec', '-i', CONTAINER]
    created = []
    try:
        for db in (source, target):
            admin.run('CREATE DATABASE ' + engine.ident(db.database))
            created.append(db.database)
            db.run("CREATE TABLE users(id bigint PRIMARY KEY); INSERT INTO users VALUES(1),(2);"
                   "CREATE TABLE ops_system_logs(id bigint PRIMARY KEY);"
                   "CREATE TABLE usage_logs(id bigserial,user_id bigint REFERENCES users(id),"
                   "actual_cost numeric,created_at timestamptz) PARTITION BY RANGE(created_at);"
                   "CREATE TABLE usage_logs_20260901 PARTITION OF usage_logs FOR VALUES FROM ('2026-09-01') TO ('2026-09-02');"
                   "CREATE TABLE usage_logs_20260902 PARTITION OF usage_logs FOR VALUES FROM ('2026-09-02') TO ('2026-09-03');"
                   "INSERT INTO usage_logs VALUES(10,1,1,'2026-09-01T01:00Z'),(400000,2,2,'2026-09-02T01:00Z');")
        with engine.Snapshot(target) as snap:
            base = engine.manifest(target, snap)
        source.run("UPDATE usage_logs SET actual_cost=1.12345678 WHERE id=10;"
                   "INSERT INTO ops_system_logs VALUES(1);"
                   "INSERT INTO usage_logs VALUES(5,2,0.5,'2026-09-01T00:01Z');"
                   "DELETE FROM usage_logs WHERE id=400000;"
                   "SELECT setval('usage_logs_id_seq',500000);")
        with tempfile.TemporaryDirectory(prefix='usage-delta-fixture-', dir=Policy.work) as directory:
            root = Path(directory) / 'bundle'
            with patch.object(engine.socket, 'gethostname', return_value=FixturePolicy.source):
                exported = engine.export_bundle(source, base, root)
            assert exported['changed_units'] == 2, exported
            manifest_file = root / 'bundle.json'
            original = manifest_file.read_text()
            bad = json.loads(original)
            bad['changes'][0]['sha256'] = 'bad'
            manifest_file.write_text(json.dumps(bad))
            try:
                engine.apply_bundle(target, root, socket.gethostname(), policy=FixturePolicy())
                raise AssertionError('corrupt bundle accepted')
            except ValueError as exc:
                assert 'checksum' in str(exc)
            assert target.run('SELECT actual_cost FROM usage_logs WHERE id=10') == '1'
            manifest_file.write_text(original)
            # A valid transport checksum cannot substitute for content checks.
            # Force failure after DELETE/COPY and prove the transaction rolls back.
            altered = json.loads(original)
            entry = next(c for c in altered['changes'] if c['table'] == 'usage_logs_20260901')
            copy_path = root / entry['file']
            original_copy = copy_path.read_bytes()
            raw = gzip.decompress(original_copy)
            assert b'1.12345678' in raw
            copy_path.write_bytes(gzip.compress(raw.replace(b'1.12345678', b'1.99999999')))
            entry['sha256'] = hashlib.sha256(copy_path.read_bytes()).hexdigest()
            manifest_file.write_text(json.dumps(altered))
            try:
                engine.apply_bundle(target, root, socket.gethostname(), policy=FixturePolicy())
                raise AssertionError('incorrect row content accepted')
            except RuntimeError as exc:
                assert 'delta apply failed' in str(exc)
            assert target.run('SELECT actual_cost FROM usage_logs WHERE id=10') == '1'
            assert target.run("SELECT string_agg(id::text,',' ORDER BY id) FROM usage_logs") == '10,400000'
            assert target.run('SELECT last_value FROM usage_logs_id_seq') == '1'
            copy_path.write_bytes(original_copy)
            manifest_file.write_text(original)
            # Inject a real PostgreSQL timeout after COPY, validation and sequence
            # restart, immediately before COMMIT. The shared apply path must
            # report failure and roll back rows AND transactional sequence state.
            original_run = engine.subprocess.run

            def timeout_before_commit(args, **kwargs):
                if args == target.psql() and 'stdin' in kwargs:
                    script = kwargs['stdin'].read()
                    assert script.endswith(b'COMMIT;\n')
                    with tempfile.TemporaryFile() as injected:
                        injected.write(script[:-len(b'COMMIT;\n')] +
                                       b"SET LOCAL statement_timeout='1ms'; SELECT pg_sleep(0.05); COMMIT;\n")
                        injected.seek(0)
                        return original_run(args, **dict(kwargs, stdin=injected))
                return original_run(args, **kwargs)

            with patch.object(engine.subprocess, 'run', side_effect=timeout_before_commit):
                try:
                    engine.apply_bundle(target, root, socket.gethostname(), policy=FixturePolicy())
                    raise AssertionError('timed-out transaction accepted')
                except RuntimeError as exc:
                    assert 'delta apply failed' in str(exc)
            assert target.run('SELECT actual_cost FROM usage_logs WHERE id=10') == '1'
            assert target.run("SELECT string_agg(id::text,',' ORDER BY id) FROM usage_logs") == '10,400000'
            assert target.run('SELECT last_value FROM usage_logs_id_seq') == '1'
            applied = engine.apply_bundle(target, root, socket.gethostname(), policy=FixturePolicy())
            assert applied['content_verified_before_commit']
            assert target.run('SELECT actual_cost FROM usage_logs WHERE id=10') == '1.12345678'
            assert target.run("SELECT string_agg(id::text,',' ORDER BY id) FROM usage_logs") == '5,10'
            assert target.run("SELECT nextval('usage_logs_id_seq')") == '500001'
            assert target.run('SELECT count(*) FROM ops_system_logs') == '0'
            try:
                engine.apply_bundle(target, root, socket.gethostname(), policy=FixturePolicy())
                raise AssertionError('stale baseline accepted')
            except RuntimeError as exc:
                assert 'candidate changed' in str(exc)
            source.run("CREATE TABLE usage_logs_20260903 PARTITION OF usage_logs "
                       "FOR VALUES FROM ('2026-09-03') TO ('2026-09-04')")
            try:
                engine.export_bundle(source, base, Path(directory) / 'partition-drift')
                raise AssertionError('partition drift accepted')
            except ValueError as exc:
                assert 'schema changed' in str(exc)
        receipt = dict(old_id_update=True, historical_backfill=True, deletion=True,
                       partitioned_history=True, sequence=True, corrupt_bundle_rejected=True,
                       stale_baseline_rejected=True, baseline_database_untouched=True,
                       transaction_rollback_verified=True, partition_drift_rejected=True,
                       database_timeout_rollback_verified=True,
                       full_size_timing_verified=False, final_freeze_verified=False)
        (Policy.work / 'usage-delta-fixture-receipt.json').write_text(json.dumps(receipt))
        print(json.dumps(receipt))
    finally:
        for name in reversed(created):
            Policy().guard()
            admin.run('DROP DATABASE ' + engine.ident(name))


if __name__ == '__main__':
    main()
