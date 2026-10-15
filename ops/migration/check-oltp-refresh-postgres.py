#!/usr/bin/env python3
"""Real PostgreSQL acceptance in two disposable databases on a held candidate."""
import copy
import fcntl
import json
from pathlib import Path
import socket
import tempfile
from unittest.mock import patch

import fresh_host_delta as m

expected = socket.gethostname()
m.candidate_guard(expected)
admin = m.DB()
source = m.DB('migration_oltp_test_source', 'oltp')
target = m.DB('migration_oltp_test_target', 'oltp')
names = [source.database, target.database]
for name in names:
    if admin.run("SELECT count(*) FROM pg_database WHERE datname='" + name + "'") != '0':
        raise RuntimeError('scratch database already exists; inspect before retry')
created = []
try:
    for name in names:
        admin.run('CREATE DATABASE ' + m.ident(name))
        created.append(name)
    schema = '''CREATE TABLE balances(id bigserial PRIMARY KEY,balance numeric);
    CREATE TABLE children(id bigint PRIMARY KEY,parent_id bigint REFERENCES balances(id));
    CREATE TABLE usage_logs(id bigint PRIMARY KEY,parent_id bigint REFERENCES balances(id));'''
    for db in (source, target):
        db.run(schema + 'INSERT INTO balances(balance) VALUES(10),(20); INSERT INTO children VALUES(1,1);')
    target.run('INSERT INTO usage_logs VALUES(99,2);')
    with tempfile.TemporaryDirectory(prefix='oltp-test-', dir='/var/lib/tokenkey/migration') as tmp:
        root = Path(tmp)
        with m.Snapshot(target) as snap:
            base = m.manifest(target, snap)
        source.run('UPDATE balances SET balance=9.5 WHERE id=1; DELETE FROM balances WHERE id=2;')
        with patch.object(m.socket, 'gethostname', return_value=m.CANDIDATES[expected]):
            m.export_bundle(source, base, root / 'orphan')
        try:
            m.apply_bundle(target, root / 'orphan', expected)
            raise AssertionError('retained log orphan accepted')
        except RuntimeError as exc:
            assert 'apply failed' in str(exc), str(exc)
        assert target.run('SELECT sum(balance) FROM balances') == '30'
        assert target.run('SELECT last_value FROM balances_id_seq') == '2'
        source.run('INSERT INTO balances VALUES(2,20); SELECT setval(\'balances_id_seq\',500);')
        with patch.object(m.socket, 'gethostname', return_value=m.CANDIDATES[expected]):
            m.export_bundle(source, base, root / 'good')
        file = root / 'good' / 'bundle.json'
        bundle = json.loads(file.read_text())
        corrupted = copy.deepcopy(bundle)
        corrupted['changes'][0]['sha256'] = 'invalid'
        file.write_text(json.dumps(corrupted))
        try:
            m.apply_bundle(target, root / 'good', expected)
            raise AssertionError('corrupt payload accepted')
        except ValueError as exc:
            assert 'checksum' in str(exc)
        file.write_text(json.dumps(bundle))
        with Path('/var/lib/tokenkey/migration/apply.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            try:
                m.apply_bundle(target, root / 'good', expected)
                raise AssertionError('concurrent refresh accepted')
            except RuntimeError as exc:
                assert 'another candidate refresh is running' in str(exc)
        assert target.run('SELECT sum(balance) FROM balances') == '30'
        result = m.apply_bundle(target, root / 'good', expected)
        assert result['content_verified_before_commit']
        assert target.run('SELECT balance FROM balances WHERE id=1') == '9.5'
        assert target.run('SELECT parent_id FROM usage_logs WHERE id=99') == '2'
        assert target.run('SELECT parent_id FROM children WHERE id=1') == '1'
        assert target.run("SELECT nextval('balances_id_seq')") == '501'
        try:
            m.apply_bundle(target, root / 'good', expected)
            raise AssertionError('stale baseline accepted')
        except RuntimeError as exc:
            assert 'candidate changed' in str(exc)
        # Log partitions have inherited FKs and a sequence owned by their parent.
        partition_schema = """CREATE TABLE ops_system_logs(id bigserial, day date NOT NULL,
          parent_id bigint REFERENCES balances(id), PRIMARY KEY(id,day)) PARTITION BY RANGE(day);
          CREATE TABLE ops_system_logs_202610 PARTITION OF ops_system_logs
          FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');"""
        for db in (source, target):
            db.run(partition_schema)
        source.run("INSERT INTO balances VALUES(3,30); INSERT INTO ops_system_logs(day,parent_id) VALUES('2026-10-15',3); INSERT INTO usage_logs VALUES(100,1);")
        source.scope = target.scope = 'logs'
        with m.Snapshot(target) as snap:
            log_base = m.manifest(target, snap)
        with patch.object(m.socket, 'gethostname', return_value=m.CANDIDATES[expected]):
            m.export_bundle(source, log_base, root / 'log-orphan')
        try:
            m.apply_bundle(target, root / 'log-orphan', expected)
            raise AssertionError('inherited partition FK violation accepted')
        except RuntimeError as exc:
            assert 'apply failed' in str(exc)
        assert target.run('SELECT id FROM usage_logs') == '99'
        source.run('UPDATE ops_system_logs SET parent_id=1;')
        with patch.object(m.socket, 'gethostname', return_value=m.CANDIDATES[expected]):
            m.export_bundle(source, log_base, root / 'logs')
        m.apply_bundle(target, root / 'logs', expected)
        assert target.run('SELECT id FROM usage_logs') == '100'
        assert target.run('SELECT parent_id FROM ops_system_logs') == '1'
        assert target.run("SELECT nextval('ops_system_logs_id_seq')") == '2'
        assert target.run('SELECT balance FROM balances WHERE id=1') == '9.5'
    print('PASS OLTP: retained-log FK rejection rolls back balances; checksum rejection; content verification; children retained; sequence; stale baseline rejection')
    print('PASS log partitions: inherited FK rejection rolls back all log changes; parent sequence restored; unrelated balances preserved')
finally:
    for name in reversed(created):
        admin.run('DROP DATABASE ' + m.ident(name))
