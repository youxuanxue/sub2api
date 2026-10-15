#!/usr/bin/env python3
"""Run synthetic real-Postgres delta acceptance on an isolated candidate.

Uses only two newly created migration_delta_test_* databases. Production tables
are never written. Fails if these scratch databases already exist.
"""
import importlib.util,json,pathlib,tempfile,subprocess,socket
from unittest.mock import patch
path=pathlib.Path(__file__).with_name('fresh_host_delta.py')
spec=importlib.util.spec_from_file_location('delta',path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
expected=socket.gethostname();assert expected in m.CANDIDATES
source=m.DB('migration_delta_test_source','precious');target=m.DB('migration_delta_test_target','precious');admin=m.DB()
for name in [source.database,target.database]:
 admin.run('CREATE DATABASE '+m.ident(name))
try:
 schema='CREATE TABLE migration_test_balances(id bigserial PRIMARY KEY,balance numeric, secret text); CREATE TABLE children(id bigint PRIMARY KEY,user_id bigint REFERENCES migration_test_balances(id)); CREATE TABLE usage_billing_dedup(id bigserial PRIMARY KEY,request_id text UNIQUE); CREATE TABLE usage_billing_dedup_archive(id bigint PRIMARY KEY,request_id text UNIQUE);'
 initial="INSERT INTO migration_test_balances(balance,secret) VALUES(10,'synthetic-only'); INSERT INTO children VALUES(1,1); INSERT INTO usage_billing_dedup(id,request_id) VALUES(10,'ten'),(400000,'old-tail');"
 for db in [source,target]:db.run(schema+initial)
 with m.Snapshot(target) as snap:base=m.manifest(target,snap)
 source.run("UPDATE migration_test_balances SET balance=9.5 WHERE id=1; INSERT INTO usage_billing_dedup(id,request_id) VALUES(5,'late-lower-id'); DELETE FROM usage_billing_dedup WHERE id=400000; INSERT INTO usage_billing_dedup_archive VALUES(400000,'old-tail'); SELECT setval('usage_billing_dedup_id_seq',500000);")
 with tempfile.TemporaryDirectory(prefix='delta-test-',dir='/var/lib/tokenkey/migration') as tmp:
  root=pathlib.Path(tmp)/'bundle'
  with patch.object(m.socket,'gethostname',return_value=m.CANDIDATES[expected]):result=m.export_bundle(source,base,root)
  assert result['changed_units']==4,result
  manifest_file=root/'bundle.json';original=manifest_file.read_text();bundle=json.loads(original)
  bundle['changes'][0]['sha256']='bad';manifest_file.write_text(json.dumps(bundle))
  try:m.apply_bundle(target,root,expected);raise AssertionError('accepted corrupt archive')
  except ValueError as e:assert 'checksum' in str(e)
  assert target.run('SELECT balance FROM migration_test_balances WHERE id=1')=='10'
  manifest_file.write_text(original)
  result=m.apply_bundle(target,root,expected)
  assert result['content_verified_before_commit']
  assert target.run('SELECT balance FROM migration_test_balances WHERE id=1')=='9.5'
  assert target.run('SELECT count(*) FROM children')=='1'
  assert target.run('SELECT string_agg(id::text,\',\' ORDER BY id) FROM usage_billing_dedup')=='5,10'
  assert target.run('SELECT request_id FROM usage_billing_dedup_archive WHERE id=400000')=='old-tail'
  assert target.run("SELECT nextval('usage_billing_dedup_id_seq')")=='500001'
  try:m.apply_bundle(target,root,expected);raise AssertionError('accepted stale baseline')
  except RuntimeError as e:assert 'candidate changed' in str(e)
 print('PASS real PostgreSQL: balance update, late lower ID, hot-to-archive movement, retained FK child, sequence, checksum rejection, stale baseline rejection')
finally:
 for name in [source.database,target.database]:admin.run('DROP DATABASE '+m.ident(name))
