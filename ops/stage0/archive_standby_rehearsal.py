#!/usr/bin/env python3
"""Isolated archive standby on the encrypted Tencent rehearsal volume.

No promote, production target resolution, source SQL, or repository writes.
Data survives stop/restart; an existing data directory is never overwritten.
Credentials must already be delivered to the protected tmpfs config directory.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import socket
import subprocess
from pathlib import Path

ROOT = Path('/srv/tokenkey-rehearsal')
DATA = ROOT / 'standby-pg'
CONFIG = Path('/run/tokenkey-recovery/config')
NAME = 'tk-archive-standby'
PGDATA = '/var/lib/postgresql/data'
LABEL = 'tokenkey.rehearsal=archive-standby'


def run(args, timeout=60):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        # Commands may read credentials or restored data; never echo their output.
        raise RuntimeError('command failed; output withheld')
    return result.stdout.strip()


def restore_options(label, timeline):
    if not re.fullmatch(r'\d{8}-\d{6}F(?:_\d{8}-\d{6}[DI])?', label):
        raise ValueError('invalid backup label')
    if type(timeline) is not int or not 0 < timeline <= 0xFFFFFFFF:
        raise ValueError('invalid source timeline')
    return ['--stanza=tokenkey', f'--set={label}', '--type=standby',
            f'--target-timeline={timeline}', '--archive-mode=off', '--process-max=4', 'restore']


def check_host(expected):
    if socket.gethostname() != expected:
        raise ValueError('host mismatch')
    if not os.path.ismount(ROOT) or ROOT.is_symlink():
        raise ValueError('encrypted workspace is not mounted')
    source = run(['findmnt', '-n', '-o', 'SOURCE', '--target', str(ROOT)])
    if source != '/dev/mapper/tokenkey_recovery':
        raise ValueError('unexpected workspace device')
    if run(['swapon', '--show', '--noheadings']):
        raise ValueError('swap must be disabled')
    if run(['docker', 'info', '--format', '{{.DockerRootDir}}']) != str(ROOT / 'docker'):
        raise ValueError('docker root is outside encrypted workspace')
    names = run(['docker', 'ps', '-a', '--format', '{{.Names}}']).splitlines()
    if any(n in names for n in ('tokenkey-postgres', 'tokenkey-blue', 'tokenkey-green')):
        raise ValueError('refusing a production/application host')
    run(['iptables', '-C', 'DOCKER-USER', '-i', 'docker0', '-j', 'TK_RECOVERY_EGRESS'])


def inspect_owned():
    obj = json.loads(run(['docker', 'inspect', NAME]))[0]
    if obj['Config']['Labels'].get('tokenkey.rehearsal') != 'archive-standby':
        raise ValueError('container ownership mismatch')
    if obj['HostConfig']['PortBindings'] or obj['HostConfig']['NetworkMode'] != 'bridge':
        raise ValueError('unexpected network exposure')
    return obj


def common(image):
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', image):
        raise ValueError('immutable image ID required')
    return ['--label', LABEL, '--network', 'bridge', '--memory', '3g', '--cpus', '2',
            '--shm-size', '256m', '--tmpfs', '/tmp:exec',
            '--tmpfs', '/var/run/postgresql:uid=70,gid=70',
            '-v', f'{DATA}:{PGDATA}', '-v', f'{CONFIG}:/etc/pgbackrest:ro',
            '--user', '70:70']


def status():
    inspect_owned()
    query = """SELECT json_build_object('in_recovery',pg_is_in_recovery(),
      'replay_lsn',pg_last_wal_replay_lsn()::text,
      'system_identifier',(SELECT system_identifier::text FROM pg_control_system()),
      'checkpoint_timeline',(SELECT timeline_id FROM pg_control_checkpoint()),
      'read_only',current_setting('transaction_read_only'),
      'archive_mode',current_setting('archive_mode'),
      'at',clock_timestamp());"""
    result = json.loads(run(['docker', 'exec', NAME, 'psql', '-U', 'tokenkey',
                            '-d', 'tokenkey', '-XAtq', '-v', 'ON_ERROR_STOP=1', '-c', query]))
    expected = json.loads((ROOT / 'standby-manifest.json').read_text())
    if result['in_recovery'] is not True or result['read_only'] != 'on' or result['archive_mode'] != 'off':
        raise ValueError('standby safety invariant failed')
    if result['system_identifier'] != expected['system_identifier'] or result['checkpoint_timeline'] != expected['timeline']:
        raise ValueError('standby source identity mismatch')
    print(json.dumps(result))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['prepare', 'start', 'status', 'restart', 'stop'])
    parser.add_argument('--expected-host', required=True)
    parser.add_argument('--image')
    parser.add_argument('--backup-label')
    parser.add_argument('--timeline', type=int)
    parser.add_argument('--system-identifier')
    args = parser.parse_args()
    os.umask(0o077)
    check_host(args.expected_host)
    if args.action == 'prepare':
        opts = restore_options(args.backup_label or '', args.timeline)
        common(args.image or '')
        if not re.fullmatch(r'[1-9][0-9]*', args.system_identifier or ''):
            raise ValueError('source system identifier required')
        if DATA.exists() or (ROOT / 'standby-manifest.json').exists():
            raise ValueError('existing standby data refused')
        if not (CONFIG / 'pgbackrest.conf').is_file():
            raise ValueError('read-only repository config missing')
        DATA.mkdir(mode=0o700)
        os.chown(DATA, 70, 70)
        run(['docker', 'run', '--rm', '--name', NAME + '-restore', *common(args.image),
             '--entrypoint', 'pgbackrest', args.image, *opts], timeout=1800)
        if not (DATA / 'standby.signal').is_file():
            raise ValueError('restore did not create standby.signal')
        control = run(['docker', 'run', '--rm', '--network', 'none', '--user', '70:70',
                       '-v', f'{DATA}:{PGDATA}:ro', '--entrypoint', 'pg_controldata',
                       args.image, PGDATA])
        match = re.search(r'Database system identifier:\s*(\d+)', control)
        if not match or match[1] != args.system_identifier:
            raise ValueError('restored database system identifier mismatch')
        (ROOT / 'standby-manifest.json').write_text(json.dumps({
            'image': args.image, 'label': args.backup_label,
            'system_identifier': args.system_identifier, 'timeline': args.timeline}))
        print('standby_restored_and_retained=true')
    elif args.action == 'start':
        manifest = json.loads((ROOT / 'standby-manifest.json').read_text())
        image = manifest['image']
        if not (DATA / 'standby.signal').is_file():
            raise ValueError('standby.signal missing; refusing start')
        timeline = manifest['timeline']
        restore_options(manifest['label'], timeline)
        command = ('test -f ' + PGDATA + '/standby.signal && exec postgres -D ' + PGDATA
                   + " -c listen_addresses='' -c archive_mode=off -c hot_standby=on"
                   + ' -c max_connections=200 -c shared_buffers=512MB'
                   + " -c primary_conninfo='' -c primary_slot_name=''"
                   + ' -c default_transaction_read_only=on'
                   + ' -c recovery_target_timeline=' + str(timeline)
                   + " -c restore_command='pgbackrest --stanza=tokenkey archive-get %f %p'")
        run(['docker', 'run', '-d', '--name', NAME, '--restart=no',
             *common(image), '--entrypoint', 'sh', image, '-ec', command])
        print('standby_container_started=true')
    elif args.action == 'status':
        status()
    else:
        inspect_owned()
        run(['docker', args.action, '--time', '60', NAME], timeout=90)
        print(f'standby_{args.action}=true')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.TimeoutExpired):
        # Never print config, logs, commands, restored rows, or credential values.
        print('standby operation failed; private state retained for diagnosis')
        raise SystemExit(1)
