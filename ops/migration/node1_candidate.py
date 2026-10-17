#!/usr/bin/env python3
"""Isolated Node1 prod rehearsal. No activation or cutover operation exists.

Authorization: docs/approved/node1-prod-precutover.md.
"""
from __future__ import annotations

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import time

ROOT = Path('/data/tokenkey-prod-candidate-tokyo-current')
CONTAINER = 'tokenkey-prod-candidate-tokyo-current-postgres'
MACHINE = 'c3312a1eec634d6cac597f41be19ed5a'
VOLUME = 'ea97a943-8b3f-427d-9d6c-b81baddaff79'
IMAGE = 'sha256:a088df28e5e0007511e03027fb95c8e5d86ca2c269dd1663eeca038054b72460'
SOURCE = 'tokenkey-prod-tokyo-20261017'
LABEL = {'tokenkey.candidate': 'node1-prod-tokyo-precutover'}


def output(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.DEVNULL).strip()


def host_guard():
    if os.geteuid() != 0 or Path('/etc/machine-id').read_text().strip() != MACHINE:
        raise RuntimeError('wrong Node1 identity')
    if output('findmnt', '-n', '-o', 'UUID', '--mountpoint', '/data') != VOLUME:
        raise RuntimeError('wrong Node1 data volume')
    if ROOT.resolve() != ROOT:
        raise RuntimeError('candidate path must not be redirected')


def validate_container(info, containers):
    host = info['HostConfig']
    labels = info['Config'].get('Labels') or {}
    if (info['Name'] != '/' + CONTAINER or info['Image'] != IMAGE
            or not info['State']['Running'] or any(labels.get(k) != v for k, v in LABEL.items())
            or host['NetworkMode'] != 'none' or host.get('PortBindings')
            or set(info['NetworkSettings']['Networks']) != {'none'}
            or host['RestartPolicy']['Name'] != 'no' or host.get('Privileged')):
        raise RuntimeError('candidate container is not held and isolated')
    binds = [(m['Source'], m['Destination'], m['RW']) for m in info['Mounts']]
    if binds != [(str(ROOT / 'postgres'), '/var/lib/postgresql', True)]:
        raise RuntimeError('candidate database mount mismatch')
    for other in containers:
        if other['Id'] == info['Id']:
            continue
        for mount in other['Mounts']:
            source = Path(mount['Source'])
            if source == ROOT or ROOT in source.parents or source in ROOT.parents:
                raise RuntimeError('another container can access candidate data')


class Policy:
    source = SOURCE
    work = ROOT / 'migration'

    def guard(self, db=None):
        host_guard()
        if not (self.work / 'DATA_SYNC_HOLD').is_file():
            raise RuntimeError('persistent candidate hold missing')
        if db is not None and (db.prefix != ['docker', 'exec', '-i', CONTAINER]
                               or db.database != 'tokenkey'
                               or db.scope not in ('precious', 'usage')):
            raise RuntimeError('candidate database command mismatch')
        ids = output('docker', 'ps', '-aq').splitlines()
        all_info = json.loads(output('docker', 'inspect', *ids)) if ids else []
        matches = [c for c in all_info if c['Name'] == '/' + CONTAINER]
        if len(matches) != 1:
            raise RuntimeError('candidate PostgreSQL missing')
        validate_container(matches[0], all_info)


def prepare():
    host_guard()
    if ROOT.exists() and set(p.name for p in ROOT.iterdir()) - {'migration'}:
        raise RuntimeError('candidate already exists; inspect instead of overwriting')
    if shutil.disk_usage('/data').free < 40 * 1024**3:
        raise RuntimeError('insufficient candidate disk headroom')
    if output('docker', 'image', 'inspect', IMAGE, '--format', '{{.Architecture}}') != 'amd64':
        raise RuntimeError('wrong PostgreSQL image architecture')
    ROOT.mkdir(mode=0o700, exist_ok=True)
    work = ROOT / 'migration'
    work.mkdir(mode=0o700, exist_ok=True)
    (work / 'DATA_SYNC_HOLD').write_text('Independent cutover approval required.\n')
    # PG18 drops to its postgres UID after creating/chowning PGDATA underneath
    # this parent. The mount root must be traversable; ROOT remains root-only.
    (ROOT / 'postgres').mkdir(mode=0o755)
    (ROOT / 'postgres').chmod(0o755)
    env = ROOT / 'postgres.env'
    env.write_text('POSTGRES_USER=tokenkey\nPOSTGRES_DB=tokenkey\nPOSTGRES_PASSWORD=' + secrets.token_urlsafe(48) + '\n')
    env.chmod(0o600)
    subprocess.run(['docker', 'run', '-d', '--name', CONTAINER, '--network', 'none',
        '--restart', 'no', '--label', 'tokenkey.candidate=node1-prod-tokyo-precutover',
        '--cpus', '2', '--memory', '4g', '--memory-swap', '4g',
        '--device-read-bps', '/dev/vdb:41943040', '--device-write-bps', '/dev/vdb:41943040',
        '--env-file', str(env), '--mount', f'type=bind,src={ROOT}/postgres,dst=/var/lib/postgresql',
        IMAGE, 'postgres', '-c', "listen_addresses=127.0.0.1", '-c', 'max_connections=20'],
        check=True, stdout=subprocess.DEVNULL)
    for _ in range(90):
        result = subprocess.run(['docker', 'exec', CONTAINER, 'psql', '-XAt', '-U', 'tokenkey',
                                 '-d', 'tokenkey', '-c', 'SELECT 1'], capture_output=True)
        if result.returncode == 0:
            Policy().guard()
            return {'prepared': True, 'container': CONTAINER, 'final_freeze_verified': False}
        time.sleep(1)
    raise RuntimeError('candidate database not ready; inspect held candidate')


def adopt():
    """Normalize an already restored Tokyo candidate without touching its data.

    The first restore was created with a temporary read-only dump bind and no
    label.  Recreate only that isolated PostgreSQL container, preserving the
    existing PGDATA bind and credentials, so the normal candidate guard can
    verify the exact hold contract.  This operation has no activation path.
    """
    host_guard()
    hold = ROOT / 'migration/DATA_SYNC_HOLD'
    if not hold.is_file():
        # The first Tokyo restore placed the hold at the candidate root.  Move
        # only the marker into the guarded work directory; never move data.
        legacy_hold = ROOT / 'DATA_SYNC_HOLD'
        if legacy_hold.is_symlink() or not legacy_hold.is_file():
            raise RuntimeError('persistent candidate hold missing')
        (ROOT / 'migration').mkdir(mode=0o700, exist_ok=True)
        hold.write_text(legacy_hold.read_text())
        hold.chmod(0o600)
    env = ROOT / 'postgres.env'
    if env.is_symlink() or not env.is_file() or env.stat().st_mode & 0o077:
        raise RuntimeError('candidate credentials must remain root-private')
    info = json.loads(output('docker', 'inspect', CONTAINER))[0]
    if info['Image'] != IMAGE or not info['State']['Running']:
        raise RuntimeError('unexpected candidate container state')
    mounts = {(m['Source'], m['Destination'], m['RW']) for m in info['Mounts']}
    if (str(ROOT / 'postgres'), '/var/lib/postgresql', True) not in mounts:
        raise RuntimeError('candidate PostgreSQL data mount mismatch')
    if info['HostConfig']['NetworkMode'] != 'none' or info['HostConfig'].get('PortBindings'):
        raise RuntimeError('candidate is not isolated')
    subprocess.run(['docker', 'stop', '--time', '60', CONTAINER], check=True,
                   stdout=subprocess.DEVNULL)
    subprocess.run(['docker', 'rm', CONTAINER], check=True, stdout=subprocess.DEVNULL)
    subprocess.run(['docker', 'run', '-d', '--name', CONTAINER, '--network', 'none',
        '--restart', 'no', '--label', 'tokenkey.candidate=node1-prod-tokyo-precutover',
        '--cpus', '2', '--memory', '4g', '--memory-swap', '4g',
        '--device-read-bps', '/dev/vdb:41943040', '--device-write-bps', '/dev/vdb:41943040',
        '--env-file', str(env), '--mount', f'type=bind,src={ROOT}/postgres,dst=/var/lib/postgresql',
        IMAGE, 'postgres', '-c', 'listen_addresses=127.0.0.1', '-c', 'max_connections=20'],
        check=True, stdout=subprocess.DEVNULL)
    for _ in range(90):
        result = subprocess.run(['docker', 'exec', CONTAINER, 'psql', '-XAt', '-U', 'tokenkey',
                                 '-d', 'tokenkey', '-c', 'SELECT 1'], capture_output=True)
        if result.returncode == 0:
            Policy().guard()
            return {'adopted': True, 'container': CONTAINER,
                    'source': SOURCE, 'final_freeze_verified': False}
        time.sleep(1)
    raise RuntimeError('adopted candidate database not ready; inspect held candidate')


def restore(dump, expected_sha256):
    policy = Policy()
    policy.guard()
    if dump.is_symlink() or not dump.is_file() or dump.parent != ROOT / 'migration':
        raise RuntimeError('dump must be a regular file in candidate migration directory')
    with dump.open('rb') as source:
        digest = hashlib.file_digest(source, 'sha256').hexdigest()
    if digest != expected_sha256:
        raise RuntimeError('dump SHA256 mismatch')
    with dump.open('rb') as source:
        if source.read(5) != b'PGDMP':
            raise RuntimeError('full custom-format snapshot dump required')
    with (policy.work / 'apply.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        policy.guard()
        if (policy.work / 'RESTORE_STARTED').exists():
            raise RuntimeError('restore already attempted; explicit recovery required')
        psql = ['docker', 'exec', '-i', CONTAINER, 'psql', '-XAt',
                '-v', 'ON_ERROR_STOP=1', '-U', 'tokenkey', '-d', 'tokenkey']
        if output(*psql, '-c', "SELECT count(*) FROM pg_tables WHERE schemaname='public'") != '0':
            raise RuntimeError('restore requires an empty candidate database')
        (policy.work / 'RESTORE_STARTED').write_text(digest + '\n')
        log = policy.work / 'restore.private.log'
        with dump.open('rb') as source, log.open('wb') as diagnostic:
            result = subprocess.run(['docker', 'exec', '-i', CONTAINER, 'pg_restore',
                '-U', 'tokenkey', '-d', 'tokenkey', '--exit-on-error', '--no-owner'],
                stdin=source, stdout=diagnostic, stderr=subprocess.STDOUT)
        if result.returncode:
            raise RuntimeError('candidate restore failed; private diagnostics retained')
        policy.guard()
        usage_rows = int(output(*psql, '-c', "SELECT count(*) FROM usage_logs WHERE created_at >= '2026-08-31T16:00:00Z'"))
        if usage_rows == 0:
            raise RuntimeError('historical usage data missing; cannot accept precious-only backup')
        receipt = {'restored': True, 'dump': dump.name, 'sha256': digest,
                   'final_freeze_verified': False, 'source': SOURCE,
                   'usage_rows_since_september': usage_rows,
                   'usage_per_user_reconciliation_pending': True}
        (policy.work / 'restore-receipt.json').write_text(json.dumps(receipt))
        return receipt


def delta(operation, scope, path):
    """Use the shared content delta engine with the held Node1 database policy."""
    if __package__:
        from . import fresh_host_delta as engine
    else:
        import fresh_host_delta as engine
    policy = Policy()
    db = engine.DB(scope=scope)
    db.prefix = ['docker', 'exec', '-i', CONTAINER]
    policy.guard(db)
    if path.is_symlink() or policy.work not in path.resolve().parents:
        raise RuntimeError('delta artifacts must remain in candidate migration directory')
    if operation == 'delta-manifest':
        if path.exists():
            raise RuntimeError('refusing to overwrite a delta baseline')
        with engine.Snapshot(db) as snap:
            manifest = engine.manifest(db, snap)
        path.write_text(json.dumps(manifest, sort_keys=True))
        return {'scope': scope, 'tables': len(manifest['tables']),
                'final_freeze_verified': False}
    return engine.apply_bundle(db, path, socket.gethostname(), policy=policy)


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('prepare', 'adopt', 'inspect', 'restore', 'delta-manifest', 'delta-apply'))
    parser.add_argument('--dump', type=Path)
    parser.add_argument('--sha256')
    parser.add_argument('--scope', choices=('precious', 'usage'), default='precious')
    parser.add_argument('--artifact', type=Path)
    args = parser.parse_args()
    if args.operation == 'prepare':
        result = prepare()
    elif args.operation == 'adopt':
        result = adopt()
    elif args.operation == 'restore':
        if not args.dump or not args.sha256:
            parser.error('--dump and --sha256 required')
        result = restore(args.dump, args.sha256)
    elif args.operation.startswith('delta-'):
        if not args.artifact:
            parser.error('--artifact required')
        result = delta(args.operation, args.scope, args.artifact)
    else:
        Policy().guard()
        result = {'isolated': True, 'final_freeze_verified': False}
    print(json.dumps(result))


if __name__ == '__main__':
    main()
