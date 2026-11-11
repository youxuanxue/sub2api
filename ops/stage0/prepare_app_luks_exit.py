#!/usr/bin/env python3
"""Plan, pre-copy and measure prod app storage migration. Never stops services.

Destination is a private, owned staging directory on the pinned ordinary root.
No phase unmounts, edits boot configuration, starts a copy, or deletes old data.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import socket
import stat
import subprocess
import time

ROOT = Path('/var/lib/tokenkey')
WORK = Path('/var/lib/tokenkey-luks-exit')
HOST = 'VM-0-16-ubuntu'
ROOT_UUID = '9842d3d6-a839-4127-bda7-f19137effe71'
OLD_UUID = '12311aff-e0fa-4345-8d1f-5b9f990dfc84'


def run(args):
    result = subprocess.run(args, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError('prerequisite command failed; output withheld')
    return result.stdout


def mount(path, exact=False):
    rows = json.loads(run(['findmnt', '--json', '--mountpoint' if exact else '--target', str(path),
                           '--output', 'SOURCE,TARGET,FSTYPE,UUID,FSROOT']))['filesystems']
    if len(rows) != 1:
        raise ValueError('unexpected mount count')
    return rows[0]


def configs():
    paths = [Path('/etc/fstab'), Path('/etc/crypttab'), Path('/etc/docker/daemon.json')]
    for unit in ('docker', 'tokenkey'):
        paths += sorted(Path('/etc/systemd/system/' + unit + '.service.d').glob('*.conf'))
    return {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in paths if p.exists()}


def inventory():
    if os.geteuid() != 0 or socket.gethostname() != HOST:
        raise ValueError('requires the expected production host and root identity')
    base = mount(Path('/'), True)
    if base != {'source': '/dev/vda2', 'target': '/', 'fstype': 'ext4', 'uuid': ROOT_UUID, 'fsroot': '/'}:
        raise ValueError('root filesystem identity mismatch')
    for path in (ROOT, Path('/srv/tokenkey-rehearsal')):
        old = mount(path, True)
        if path.is_symlink() or old.get('uuid') != OLD_UUID or old.get('source') != '/dev/mapper/tokenkey_recovery' or old.get('fsroot') != '/':
            raise ValueError('original volume identity mismatch')
    if run(['docker', 'info', '--format', '{{.DockerRootDir}}']).strip() != '/srv/tokenkey-rehearsal/docker':
        raise ValueError('Docker root mismatch')
    names = run(['docker', 'ps', '-a', '--no-trunc', '--format', '{{json .}}']).splitlines()
    # ps JSON carries names/state/image, never environment or credentials.
    containers = [{key: row[key] for key in ('ID', 'Names', 'State', 'Image')}
                  for row in map(json.loads, names)]
    used = int(run(['du', '-sx', '--block-size=1', str(ROOT)]).split()[0])
    disk = os.statvfs('/')
    free = disk.f_bavail * disk.f_frsize
    if free < used + 20 * 1024 ** 3:
        raise ValueError('insufficient ordinary disk capacity including safety reserve')
    return {'host': HOST, 'machine_id': Path('/etc/machine-id').read_text().strip(),
            'root_uuid': ROOT_UUID, 'old_uuid': OLD_UUID, 'source_bytes': used,
            'free_bytes': free, 'config_sha256': configs(), 'containers': containers}


def owned_workspace(identity):
    if WORK.is_symlink() or (WORK.exists() and (WORK.stat().st_uid != 0 or stat.S_IMODE(WORK.stat().st_mode) != 0o700)):
        raise ValueError('workspace ownership or permissions mismatch')
    WORK.mkdir(mode=0o700, exist_ok=True)
    marker = WORK / 'identity.json'
    if marker.exists():
        previous = json.loads(marker.read_text())
        for key in ('host', 'machine_id', 'root_uuid', 'old_uuid', 'config_sha256'):
            if previous[key] != identity[key]:
                raise ValueError('host or boot configuration changed since preparation')
    else:
        if list(WORK.iterdir()):
            raise ValueError('refusing unowned staging contents')
        write_private(marker, identity)
    destination = WORK / 'staging'
    if destination.is_symlink() or (destination.exists() and destination.stat().st_dev != WORK.stat().st_dev):
        raise ValueError('staging must remain on the ordinary root device')
    destination.mkdir(mode=0o700, exist_ok=True)
    destination.chmod(0o700)
    return destination


def write_private(path, value):
    temporary = path.with_suffix('.tmp')
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(value, stream, sort_keys=True, indent=2)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)


def copy_command(source, destination, dry_run=False, checksum=False):
    # -x excludes active overlay mounts; do not traverse container merged trees.
    # --no-perms at root would lose state permissions: protect the parent instead.
    command = ['rsync', '-aHAXSx', '--numeric-ids', '--delete-delay', '--stats', '--bwlimit=30720']
    if dry_run or checksum:
        command += ['--checksum']
    if dry_run:
        command += ['--dry-run']
    return command + [str(source) + '/', str(destination) + '/']


def copy_data(destination, dry_run=False):
    started = time.monotonic()
    result = subprocess.run(copy_command(ROOT, destination, dry_run), text=True, capture_output=True)
    # File disappearance is expected only for a live copy. It never proves consistency.
    receipt = {'phase': 'delta' if dry_run else 'precopy', 'elapsed_seconds': round(time.monotonic() - started, 3),
               'returncode': result.returncode, 'consistent': False,
               'stats': [line for line in result.stdout.splitlines() if line.startswith((
                   'Number of ', 'Total file size:', 'Total transferred file size:', 'Literal data:', 'Matched data:'))],
               'finished_at_unix': time.time()}
    write_private(WORK / (receipt['phase'] + '.json'), receipt)
    if result.returncode not in (0, 24):
        raise RuntimeError('copy failed; private receipt saved; source unchanged')
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=('plan', 'precopy', 'delta'), default='plan')
    args = parser.parse_args()
    identity = inventory()
    if args.phase == 'plan':
        print(json.dumps(identity, sort_keys=True))
        return
    destination = owned_workspace(identity)
    with (WORK / 'prepare.lock').open('w') as lock, (ROOT / 'bluegreen-deploy.lock').open('r') as deploy_lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        fcntl.flock(deploy_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        # Revalidate after locking before any destructive destination operation.
        if configs() != identity['config_sha256']:
            raise ValueError('boot configuration changed')
        print(json.dumps(copy_data(destination, args.phase == 'delta'), sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
        raise SystemExit('LUKS preparation refused or failed; no service or boot configuration changed') from None
