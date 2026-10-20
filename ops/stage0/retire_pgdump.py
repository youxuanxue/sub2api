#!/usr/bin/env python3
"""Retire only the local prod pgdump timer after physical recovery acceptance.

Default is read-only. Retained files, database containers and physical backup
jobs are never removed or restarted. S3 retention is a separately verified gate.
"""
from __future__ import annotations

import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import time

from postgres_backup import BackupError, atomic_json, fresh, run

STATE = Path('/var/lib/tokenkey/postgres-backup')
ROOT = Path('/var/lib/tokenkey')
MARKER = STATE / 'pgdump-retired.json'
DROPIN = Path('/etc/systemd/system/tokenkey-pgdump.service.d/retired.conf')
EXPORTER = Path('/usr/local/bin/tokenkey-pgdump.sh')


def exporter_guard() -> str:
    return f'if [ -e {MARKER} ]; then echo pgdump-retired >&2; exit 1; fi\n'


def validate_health(health: dict, now: float) -> None:
    if health.get('status') != 'ok' or health.get('reasons'):
        raise BackupError('physical backup is not healthy')
    if not fresh(health.get('archive_check'), 'started_at', now, 900):
        raise BackupError('physical archive evidence is stale')
    if not 0 <= now - health.get('latest_backup_at', 0) <= 8100:
        raise BackupError('physical backup is stale')
    accepted = health.get('restore_acceptance') or {}
    if not fresh(accepted, 'completed_at', now, 8 * 86400):
        raise BackupError('physical restore evidence is stale')
    paths = accepted.get('paths', [])
    if 'latest-archived' not in paths or not any(p.startswith('tk_') for p in paths):
        raise BackupError('latest and historical physical restore required')
    if not fresh(health.get('repository_verification'), 'completed_at', now, 8 * 86400):
        raise BackupError('repository verification missing or stale')


def validate_retention(receipt: dict, now: float, expected_uri: str) -> None:
    if (receipt.get('status') != 'ok' or receipt.get('s3_uri') != expected_uri
            or receipt.get('expiration_disabled') is not True
            or receipt.get('retained_objects', 0) < 1
            or not 0 <= now - receipt.get('observed_at', 0) <= 3600
            or receipt.get('retain_until', 0) < now + 30 * 86400):
        raise BackupError('fresh matching S3 retention evidence for at least 30 days required')


def properties(unit: str) -> dict:
    return dict(line.split('=', 1) for line in run([
        'systemctl', 'show', unit, '--property=ActiveState,UnitFileState,Result'
    ]).splitlines() if '=' in line)


def operator() -> Path:
    unit = run(['systemctl', 'show', 'tokenkey-postgres-backup.service', '--property=ExecStart', '--value'])
    match = re.search(r'/usr/local/lib/tokenkey/postgres-backup/[a-f0-9]+/postgres_backup.py', unit)
    if not match:
        raise BackupError('installed physical backup operator not found')
    return Path(match.group())


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--expected-host', required=True)
    parser.add_argument('--retention-receipt', type=Path, required=True)
    parser.add_argument('--watch-config', type=Path, required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    if socket.gethostname() != args.expected_host:
        raise BackupError('host identity mismatch')
    if MARKER.exists():
        receipt = json.loads(MARKER.read_text())
        timer = properties('tokenkey-pgdump.timer')
        service = properties('tokenkey-pgdump.service')
        config = json.loads(args.watch_config.read_text())
        if (receipt.get('status') != 'retired' or receipt.get('host') != args.expected_host
                or timer.get('ActiveState') != 'inactive' or timer.get('UnitFileState') != 'disabled'
                or service.get('ActiveState') not in ('inactive', 'failed')
                or not DROPIN.exists()
                or DROPIN.read_text() != '[Unit]\nConditionPathExists=!' + str(MARKER) + '\n'
                or 'tokenkey-pgdump' not in config.get('paused_jobs', {})
                or not EXPORTER.is_file() or exporter_guard() not in EXPORTER.read_text()):
            raise BackupError('prior retirement incomplete or drifted; inspect saved plan before repair')
        print(json.dumps({'status': 'already-retired', 'receipt': receipt}))
        return
    now = time.time()
    health = json.loads(run(['python3', str(operator()), 'status']))
    validate_health(health, now)
    uri = next((line.split('=', 1)[1].strip().strip('"').strip("'") for line in (ROOT / '.env').read_text().splitlines()
                if line.startswith('TOKENKEY_PGDUMP_S3_URI=')), '')
    # Explicit private storage overrides require their own reviewed retention path.
    if Path('/etc/tokenkey/pgdump-storage.json').exists() or not uri.startswith('s3://'):
        raise BackupError('unexpected pgdump storage identity')
    retention = json.loads(args.retention_receipt.read_text())
    validate_retention(retention, now, uri)
    config = json.loads(args.watch_config.read_text())
    if config.get('node') != 'prod' or 'tokenkey-pgdump' not in config.get('jobs', []):
        raise BackupError('unexpected watcher configuration')
    if EXPORTER.is_symlink() or not EXPORTER.is_file() or not EXPORTER.read_text().startswith('#!/bin/bash\n'):
        raise BackupError('unexpected installed pgdump exporter')
    service = properties('tokenkey-pgdump.service')
    if service.get('ActiveState') not in ('inactive', 'failed'):
        raise BackupError('pgdump still running; let it finish, then retry')
    timer = properties('tokenkey-pgdump.timer')
    files = [{'name': p.name, 'bytes': p.stat().st_size} for p in sorted((ROOT / 'pgdump').glob('*.sql.gz'))]
    if not files:
        raise BackupError('no retained local pgdump files')
    result = {'status': 'planned', 'host': args.expected_host, 'observed_at': now,
              's3_retention': retention, 'local_files': files,
              'before_timer': timer, 'physical_health': health,
              'database_restarted': False, 'data_deleted': False}
    if not args.apply:
        print(json.dumps(result, indent=2))
        return
    if os.geteuid() != 0:
        raise BackupError('apply requires root')
    # Same lock as pgdump/QA; fail rather than queue or kill any active work.
    with (ROOT / 'qa-lifecycle/host.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if properties('tokenkey-pgdump.service').get('ActiveState') not in ('inactive', 'failed'):
            raise BackupError('pgdump started during planning; retry after completion')
        if DROPIN.exists():
            raise BackupError('retirement drop-in already exists without receipt; inspect prior attempt')
        before = STATE / 'pgdump-retirement-before'
        before.mkdir(mode=0o700, exist_ok=False)
        shutil.copy2(args.watch_config, before / 'watch-config.json')
        shutil.copy2(EXPORTER, before / 'tokenkey-pgdump.sh')
        atomic_json(before / 'plan.json', result)
        atomic_json(MARKER, dict(result, status='retiring'))
        original = EXPORTER.read_text()
        temporary = EXPORTER.with_suffix('.retiring')
        temporary.write_text(original.replace('#!/bin/bash\n', '#!/bin/bash\n' + exporter_guard(), 1))
        temporary.chmod(EXPORTER.stat().st_mode & 0o777)
        temporary.replace(EXPORTER)
        DROPIN.parent.mkdir(parents=True, exist_ok=True)
        DROPIN.write_text('[Unit]\nConditionPathExists=!'+str(MARKER)+'\n')
        # Stopping a timer never terminates its service; running dump was gated above.
        run(['systemctl', 'disable', '--now', 'tokenkey-pgdump.timer'])
        run(['systemctl', 'daemon-reload'])
        config.setdefault('paused_jobs', {})['tokenkey-pgdump'] = 'Retired after pgBackRest latest and historical recovery acceptance; retained backups are not deleted.'
        atomic_json(args.watch_config, config)
        after = properties('tokenkey-pgdump.timer')
        if after.get('ActiveState') != 'inactive' or after.get('UnitFileState') != 'disabled':
            raise BackupError('timer retirement incomplete; inspect saved plan')
        result.update(status='retired', completed_at=time.time(), after_timer=after)
        atomic_json(MARKER, result)
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    try:
        main()
    except (BackupError, OSError, ValueError, subprocess.SubprocessError) as exc:
        raise SystemExit(f'pgdump retirement failed: {exc}') from None
