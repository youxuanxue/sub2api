#!/usr/bin/env python3
"""Drain approved dedup archive batches, then rebuild hot indexes concurrently.

Default: read-only plan. SQL and retention/batch constants are read from their Go
owners; this tool does not implement a second retention policy; expired-key deletion uses the existing purge owner.
Approved scope: docs/approved/design-data-plane-split.md, 2026-11-11 closeout.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import re
import shutil
import socket
import sys
import subprocess
import time
import uuid

INDEXES = ('idx_usage_billing_dedup_request_api_key', 'usage_billing_dedup_pkey')
LOCK = 'ops:cleanup:leader'

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "lib"))
import resolve_app_container

PG = None
REDIS = None


def run(argv, timeout=30):
    return subprocess.check_output(argv, text=True, timeout=timeout).strip()


def psql(statement, timeout=125):
    return run(['docker', 'exec', PG, 'psql', '-U', 'tokenkey',
                '-d', 'tokenkey', '-XAt', '-v', 'ON_ERROR_STOP=1', '-c', statement], timeout)


def query(statement):
    # Empty-range proofs can scan the bloated heap until concurrent rebuilds finish.
    output = psql('SET statement_timeout=120000; ' + statement)
    if not output.startswith('SET\n'):
        raise RuntimeError('unexpected psql query response')
    return json.loads(output[4:])


def redis(*args):
    # Password stays inside the container and never enters command arguments/output.
    return run(['docker', 'exec', REDIS, 'sh', '-c',
                'REDISCLI_AUTH="$(cat /run/requirepass)" exec redis-cli --no-auth-warning "$@"',
                'redis-cli', *args])


def owner_plan(repository, policy):
    method = repository.split('func (r *dashboardAggregationRepository) CleanupUsageBillingDedup(', 1)[1]
    sql = method.split('r.sql.ExecContext(ctx, `', 1)[1].split('`', 1)[0]
    if not all(part in sql for part in ('WITH victims AS', 'INSERT INTO usage_billing_dedup_archive',
                                       'ON CONFLICT (request_id, api_key_id) DO NOTHING',
                                       'DELETE FROM usage_billing_dedup')):
        raise ValueError('archive owner SQL changed; review required')
    def constant(text, name):
        return int(re.search(r'\b' + name + r'\s*=\s*([0-9_]+)', text).group(1).replace('_', ''))
    return sql, constant(repository, 'usageBillingDedupCleanupBatchSize'), constant(repository, 'usageBillingDedupCleanupMaxRowsPerRun'), constant(policy, 'billingDedupRetentionDays'), constant(policy, 'billingDedupArchiveRetentionDays')


def purge_plan(source):
    method = source.split('func (r *dashboardAggregationRepository) PurgeExpiredUsageBillingDedup(', 1)[1]
    statement = method.split('r.purgeUsageBillingDedupBatches(ctx, `', 1)[1].split('`', 1)[0]
    if 'DELETE FROM usage_billing_dedup' not in statement or 'INSERT' in statement or 'usage_billing_dedup_archive' in statement:
        raise ValueError('hot purge owner changed; review required')
    def constant(name):
        return int(re.search(r'\b' + name + r'\s*=\s*([0-9_]+)', source).group(1).replace('_', ''))
    return statement, constant('usageBillingDedupPurgeBatchSize'), constant('usageBillingDedupPurgeMaxRowsPerRun')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--expected-host', required=True)
    parser.add_argument('--timeline', required=True, type=int)
    parser.add_argument('--repository', type=Path, required=True)
    parser.add_argument('--policy', type=Path, required=True)
    parser.add_argument('--purge-owner', type=Path, help='required only for the existing expired hot-key purge')
    parser.add_argument('--cutoff', help='fixed UTC archive cutoff from the initial plan')
    parser.add_argument('--action', choices=('purge', 'archive', 'reindex'), default='archive')
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    if socket.gethostname() != args.expected_host:
        raise RuntimeError('host mismatch')
    global PG, REDIS
    PG = resolve_app_container.resolve_postgres_server()
    REDIS = resolve_app_container.resolve_redis_server()
    if not PG or not REDIS:
        raise RuntimeError('unique running data servers not resolved')
    sql, batch, cap, days, archive_days = owner_plan(args.repository.read_text(), args.policy.read_text())
    now = datetime.now(timezone.utc)
    cutoff = datetime.fromisoformat(args.cutoff.replace('Z', '+00:00')) if args.cutoff else now - timedelta(days=days)
    if cutoff.tzinfo is None or cutoff > now - timedelta(days=days):
        raise RuntimeError('cutoff must be UTC-aware and at least the approved hot retention old')
    expiry_cutoff = (cutoff + timedelta(days=days-archive_days)).astimezone(timezone.utc).isoformat()
    if args.action == 'purge':
        if args.purge_owner is None:
            raise RuntimeError('purge requires the existing Go owner source')
        sql, batch, cap = purge_plan(args.purge_owner.read_text())
        cutoff = now - timedelta(days=archive_days)
    cutoff = cutoff.astimezone(timezone.utc).isoformat()
    identity = query("SELECT json_build_object('timeline',timeline_id,'recovery',pg_is_in_recovery(),"
                     "'started',pg_postmaster_start_time()) FROM pg_control_checkpoint()")
    if identity['recovery'] or identity['timeline'] != args.timeline:
        raise RuntimeError('primary timeline mismatch')
    def remaining():
        return query("SELECT to_json(EXISTS(SELECT 1 FROM usage_billing_dedup WHERE created_at<'" + cutoff + "'::timestamptz))")
    def indexes():
        return query("SELECT json_agg(t) FROM (SELECT indexrelid::regclass::text AS name,indisvalid,indisready,"
                     "pg_relation_size(indexrelid) AS bytes FROM pg_index WHERE indrelid='usage_billing_dedup'::regclass) t")
    before = indexes()
    result = dict(host=args.expected_host, timeline=args.timeline, cutoff=cutoff, action=args.action,
                  batch_size=batch, per_run_cap=cap, remaining=remaining(), indexes_before=before,
                  sql_sha256=hashlib.sha256(sql.encode()).hexdigest(), status='planned')
    print(json.dumps(result), flush=True)
    if not args.apply:
        return
    if any(not i['indisvalid'] or not i['indisready'] for i in before):
        raise RuntimeError('invalid/unready hot-table index; manual inspection required')
    if query("SELECT to_json(EXISTS(SELECT 1 FROM pg_stat_activity WHERE xact_start<now()-interval '1 minute'))"):
        raise RuntimeError('long transaction present')
    if args.action != 'purge' and query("SELECT to_json(EXISTS(SELECT 1 FROM usage_billing_dedup WHERE created_at<'" + expiry_cutoff + "'::timestamptz))"):
        raise RuntimeError('expired hot keys remain; existing purge owner must run first')
    if args.action == 'reindex' and result['remaining']:
        raise RuntimeError('archive backlog has not converged to the frozen cutoff')
    for unit in ('tokenkey-postgres-backup.service', 'tokenkey-cn-postgres-backup.service'):
        if subprocess.run(['systemctl', 'is-active', '--quiet', unit]).returncode == 0:
            raise RuntimeError('backup currently running')
    token = 'dedup-closeout-' + uuid.uuid4().hex
    if redis('SET', LOCK, token, 'NX', 'EX', '1800') != 'OK':
        raise RuntimeError('cleanup leader lock occupied')
    started = time.monotonic()
    try:
        if args.action in ('purge', 'archive'):
            total = 0
            while total < cap:
                if time.monotonic() - started > 1200 or redis('GET', LOCK) != token:
                    raise RuntimeError('archive budget or leader lock lost; committed batches preserved')
                statement = sql.replace('$1', "'" + cutoff + "'::timestamptz").replace('$2', str(min(batch, cap-total)))
                output = psql('BEGIN; SET LOCAL lock_timeout=2000; SET LOCAL statement_timeout=60000; ' + statement + '; COMMIT;', 70)
                matches = re.findall(r'^DELETE (\d+)$', output, re.M)
                if len(matches) != 1:
                    raise RuntimeError('unrecognized archive result')
                moved = int(matches[0])
                total += moved
                print(json.dumps(dict(action=args.action, processed_this_batch=moved, processed_total=total)), flush=True)
                if moved < batch:
                    break
                time.sleep(0.1)
            result['purged' if args.action == 'purge' else 'archived'] = total
        else:
            required = sum(i['bytes'] for i in before if i['name'] in INDEXES) * 2 + 5 * 1024**3
            if shutil.disk_usage('/var/lib/tokenkey').free < required:
                raise RuntimeError('insufficient free disk for concurrent rebuild')
            if {i['name'] for i in before} & set(INDEXES) != set(INDEXES):
                raise RuntimeError('expected index owner names changed')
            for name in INDEXES:
                if time.monotonic()-started > 1200 or redis('GET', LOCK) != token:
                    raise RuntimeError('reindex budget or leader lock lost')
                # PGOPTIONS applies timeouts without an explicit transaction block.
                run(['docker', 'exec', '-e', 'PGOPTIONS=-c lock_timeout=3000 -c statement_timeout=600000',
                     PG, 'psql', '-U', 'tokenkey', '-d', 'tokenkey', '-XAt',
                     '-v', 'ON_ERROR_STOP=1', '-c', 'REINDEX INDEX CONCURRENTLY public.' + name], 620)
                print(json.dumps(dict(reindexed=name)), flush=True)
        after = indexes()
        if any(not i['indisvalid'] or not i['indisready'] for i in after):
            raise RuntimeError('invalid index after operation; inspect before retry')
        if query('SELECT to_json(pg_postmaster_start_time())') != identity['started']:
            raise RuntimeError('primary restart detected during operation')
        result.update(status='ok', remaining=remaining(), indexes_after=after,
                      duration_seconds=round(time.monotonic()-started, 3), completed_at=time.time())
        receipt = Path('/var/lib/tokenkey/postgres-backup') / ('dedup-' + args.action + '-closeout.json')
        receipt.write_text(json.dumps(result, indent=2) + '\n')
        receipt.chmod(0o640)
        print(json.dumps(result), flush=True)
    finally:
        redis('EVAL', 'if redis.call("get",KEYS[1]) == ARGV[1] then return redis.call("del",KEYS[1]) else return 0 end', '1', LOCK, token)


if __name__ == '__main__':
    main()
