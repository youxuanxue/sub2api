#!/usr/bin/env python3
"""Exercise the full-history backup on Node1's held baseline, never live prod.

Creates a fresh archive, uploads under a rehearsal-only Node1 object prefix,
downloads it, restores a separate database and reconciles every user/day.
No approval marker, production service or timer is changed.
"""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import subprocess
import time

if __package__:
    from . import node1_full_backup as backup
    from .node1_candidate import Policy, CONTAINER
    from .usage_history_audit import compare
else:
    import node1_full_backup as backup
    from node1_candidate import Policy, CONTAINER
    from usage_history_audit import compare

DATABASE = 'node1_tokyo_full_backup_restore'
WORK = Path('/data/tokenkey-node1-tokyo-backup-rehearsal')


def main():
    os.umask(0o077)
    policy = Policy()
    policy.guard()
    if backup.ACTIVATED.exists():
        raise RuntimeError('rehearsal forbidden after activation')
    if not json.loads((policy.work / 'usage-reconciliation.json').read_text()).get('matched'):
        raise RuntimeError('baseline reconciliation required')
    env = backup.backup_environment(json.loads(backup.private(backup.CONFIG)))
    WORK.mkdir(mode=0o700)  # One attempt; never overwrite a previous rehearsal.
    begin = time.monotonic()
    with (policy.work / 'apply.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        policy.guard()
        archive = WORK / 'full.dump'
        digest = backup.dump_archive(CONTAINER, 'tokenkey', archive)
        key = backup.PREFIX + 'rehearsal/' + digest + '.dump'
        state = {'sha256': digest, 'bytes': archive.stat().st_size, 'key': key,
                 'dump_seconds': round(time.monotonic() - begin, 2)}
        (WORK / 'progress.json').write_text(json.dumps(state))
        print(json.dumps(state), flush=True)
        backup.upload_archive(archive, key, digest, env)
        state['uploaded'] = True
        (WORK / 'progress.json').write_text(json.dumps(state))
        print(json.dumps({'uploaded': True}), flush=True)
        downloaded = WORK / 'downloaded.dump'
        backup.run(['aws', 's3', 'cp', '--only-show-errors',
                    's3://' + backup.BUCKET + '/' + key, str(downloaded)], env=env, timeout=7200)
        with downloaded.open('rb') as source:
            if hashlib.file_digest(source, 'sha256').hexdigest() != digest:
                raise RuntimeError('downloaded archive checksum mismatch')
        state['download_sha256_matched'] = True
        (WORK / 'progress.json').write_text(json.dumps(state))
        print(json.dumps({'download_sha256_matched': True}), flush=True)
        policy.guard()
        backup.run(['docker', 'exec', CONTAINER, 'createdb', '-U', 'tokenkey', DATABASE])
        with downloaded.open('rb') as source, (WORK / 'restore.private.log').open('wb') as log:
            result = subprocess.run(['docker', 'exec', '-i', CONTAINER, 'pg_restore',
                '-U', 'tokenkey', '-d', DATABASE, '--exit-on-error', '--no-owner'],
                stdin=source, stdout=log, stderr=subprocess.STDOUT, timeout=7200)
        if result.returncode:
            raise RuntimeError('off-box backup restore failed; private diagnostics retained')
        target = WORK / 'usage-by-user-day.jsonl'
        with target.open('wb') as out:
            backup.run(['docker', 'exec', '-i', CONTAINER, 'psql', '-XqAt',
                        '-v', 'ON_ERROR_STOP=1', '-U', 'tokenkey', '-d', DATABASE],
                       input=Path(__file__).with_name('usage_history_manifest.sql').read_bytes(),
                       stdout=out, timeout=900)
        reconciliation = compare(policy.work / 'usage-by-user-day.source.jsonl', target)
        if not reconciliation['matched']:
            raise RuntimeError('off-box restored usage history mismatch')
        source_counts = json.loads((policy.work / 'core-counts.source.json').read_text())
        counts = {}
        for table in ('users', 'accounts', 'api_keys', 'groups', 'settings', 'usage_billing_dedup', 'usage_logs'):
            result = backup.run(['docker', 'exec', CONTAINER, 'psql', '-XqAt',
                '-v', 'ON_ERROR_STOP=1', '-U', 'tokenkey', '-d', DATABASE, '-c',
                'SELECT count(*) FROM ' + table], capture_output=True, timeout=300)
            counts[table] = int(result.stdout)
        if counts != {k: source_counts[k] for k in counts}:
            raise RuntimeError('off-box restored core counts mismatch')
        policy.guard()
        state.update(restore_verified=True, usage=reconciliation, counts=counts,
                     elapsed_seconds=round(time.monotonic()-begin, 2),
                     final_freeze_verified=False, timers_enabled=False)
        (WORK / 'receipt.json').write_text(json.dumps(state, indent=2))
        print(json.dumps(state), flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        raise SystemExit('Node1 backup rehearsal failed: ' + type(exc).__name__) from None
