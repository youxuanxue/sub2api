#!/usr/bin/env python3
"""Node1 full-history backup. Installed dormant until separately approved cutover.

Complements the fleet's precious backup; includes usage_logs and excludes only
QA raw row data. Off-box failures are fatal and preserve the local artifact.
"""
import configparser
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess

if __package__:
    from .node1_candidate import host_guard
else:
    from node1_candidate import host_guard

CONFIG = Path('/etc/tokenkey/node1-backup/config.json')
BUCKET = 'tokenkey-prod-pgdump-682751977094'
PREFIX = 'node1/full-history/'
DEST = Path('/var/lib/tokenkey/full-backup')
ACTIVATED = Path('/etc/tokenkey/node1-prod-approved.json')


def private(path):
    s = path.lstat()
    if not stat.S_ISREG(s.st_mode) or s.st_uid != os.geteuid() or stat.S_IMODE(s.st_mode) & 0o077:
        raise RuntimeError('backup configuration must be a private owned regular file')
    return path.read_text()


def backup_environment(config):
    if set(config) != {'bucket', 'prefix', 'region', 'credentials_file'}:
        raise ValueError('unsupported backup configuration')
    if (config['bucket'], config['prefix'], config['region']) != (BUCKET, PREFIX, 'us-east-1'):
        raise ValueError('backup must use the dedicated Node1 object prefix')
    path = Path(config['credentials_file'])
    if not path.is_absolute():
        raise ValueError('absolute credentials path required')
    credentials = configparser.ConfigParser(interpolation=None)
    credentials.read_string(private(path))
    if credentials.sections() != ['node1-backup'] or set(credentials['node1-backup']) != {'aws_access_key_id', 'aws_secret_access_key'}:
        raise ValueError('dedicated backup credentials required')
    if not all(credentials['node1-backup'].values()):
        raise ValueError('empty backup credentials')
    env = {k: v for k, v in os.environ.items() if not k.startswith('AWS_')}
    env.update(AWS_SHARED_CREDENTIALS_FILE=str(path), AWS_CONFIG_FILE='/dev/null',
               AWS_PROFILE='node1-backup', AWS_EC2_METADATA_DISABLED='true',
               AWS_DEFAULT_REGION='us-east-1', AWS_MAX_ATTEMPTS='3')
    return env


def run(args, **kwargs):
    if not kwargs.get('capture_output'):
        kwargs['stderr'] = subprocess.DEVNULL
    p = subprocess.run(args, **kwargs)
    if p.returncode:
        raise RuntimeError('full-history backup command failed; local artifact retained')
    return p


def dump_archive(container, database, part):
    with part.open('xb') as output:
        run(['docker', 'exec', container, 'nice', '-n', '19',
             'pg_dump', '-U', 'tokenkey', '-d', database, '--format=custom',
             '--no-owner', '--exclude-table-data=qa_records*'], stdout=output, timeout=3600)
    if part.stat().st_size < 2048:
        raise RuntimeError('full backup is unexpectedly empty')
    with part.open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def upload_archive(artifact, key, digest, env):
    if not key.startswith(PREFIX) or '..' in key.split('/'):
        raise ValueError('full backup object must remain in its Node1 prefix')
    run(['aws', 's3', 'cp', '--only-show-errors', '--sse', 'AES256', str(artifact),
         's3://' + BUCKET + '/' + key, '--metadata', 'sha256=' + digest], env=env, timeout=7200)
    head = run(['aws', 's3api', 'head-object', '--bucket', BUCKET, '--key', key],
               env=env, capture_output=True, timeout=60)
    metadata = json.loads(head.stdout)
    if (metadata['ContentLength'] != artifact.stat().st_size
            or metadata.get('Metadata', {}).get('sha256') != digest):
        raise RuntimeError('uploaded backup metadata does not match local artifact')


def main():
    os.umask(0o077)
    host_guard()
    approval = json.loads(private(ACTIVATED))
    if approval.get('target') != 'zhupeng-prod' or approval.get('cutover_approved') is not True:
        raise RuntimeError('separate production activation approval is required')
    config = json.loads(private(CONFIG))
    env = backup_environment(config)
    mount = subprocess.check_output(['findmnt', '-n', '-o', 'SOURCE', '--mountpoint', '/var/lib/tokenkey'], text=True).strip()
    if '[/tokenkey]' not in mount:
        raise RuntimeError('production runtime is not on the Node1 data bind mount')
    info = json.loads(subprocess.check_output(['docker', 'inspect', 'tokenkey-postgres']))[0]
    if not info['State']['Running'] or not any(
            m['Source'] == '/var/lib/tokenkey/postgres' and m['Destination'] == '/var/lib/postgresql/data'
            for m in info['Mounts']):
        raise RuntimeError('production PostgreSQL mount mismatch')
    if shutil.disk_usage('/var/lib/tokenkey').free < 40 * 1024**3:
        raise RuntimeError('insufficient backup disk headroom')
    DEST.mkdir(mode=0o700, exist_ok=True)
    with (DEST / 'backup.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
        artifact = DEST / ('tokenkey-full-' + stamp + '.dump')
        part = artifact.with_suffix('.part')
        # Share the fleet backup/DDL lease; QA remains separately deferred.
        lifecycle = Path('/var/lib/tokenkey/qa-lifecycle/host.lock')
        lifecycle.parent.mkdir(parents=True, exist_ok=True)
        with lifecycle.open('a') as lease:
            fcntl.flock(lease, fcntl.LOCK_SH | fcntl.LOCK_NB)
            digest = dump_archive('tokenkey-postgres', 'tokenkey', part)
        part.rename(artifact)
        key = PREFIX + artifact.name
        upload_archive(artifact, key, digest, env)
        receipt = {'completed_at': stamp, 'bytes': artifact.stat().st_size, 'sha256': digest,
                   'usage_logs_included': True, 'bucket': BUCKET, 'key': key}
        (DEST / 'last-success.json').write_text(json.dumps(receipt))
        # Only exact owned completed artifacts are eligible, after off-box success.
        files = sorted(p for p in DEST.iterdir() if p.is_file() and not p.is_symlink()
                       and re.fullmatch(r'tokenkey-full-\d{8}T\d{6}Z\.dump', p.name))
        for old in files[:-3]:
            old.unlink()
        print(json.dumps(receipt))


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        # Credential parsing or transport errors must not expose private values.
        raise SystemExit('Node1 full backup failed: ' + type(exc).__name__) from None
