#!/usr/bin/env python3
"""Validate host prerequisites and bind explicitly selected runtime identities.

Delivered with the existing blue/green primitive. Uses only Python's standard
library; credential contents never enter Compose, SSM parameters or output.
"""
import argparse
import base64
import re
import json
import os
from pathlib import Path
import stat
import subprocess


def private_file(path, uid):
    try:
        info = path.lstat()
    except FileNotFoundError as err:
        raise ValueError('runtime credentials must be private regular files') from err
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) not in (0o400, 0o600):
        raise ValueError('runtime credentials must be private regular files')
    if info.st_uid != uid:
        raise ValueError('runtime credential owner must match the application UID')
    if info.st_size == 0 or info.st_size > 16384:
        raise ValueError('runtime credential file size is invalid')


def _mounted_ext4(root, run):
    # Exact directory must be a mountpoint, not the host root disk.
    mounted = json.loads(run(['findmnt', '--json', '--mountpoint', str(root),
                             '--output', 'SOURCE,TARGET,FSTYPE'], text=True))['filesystems']
    if len(mounted) != 1 or mounted[0]['target'] != str(root) or mounted[0]['fstype'] != 'ext4':
        raise ValueError('data volume is not mounted at the expected path')
    return Path(mounted[0]['source']).resolve()


def volume_contract(target):
    """Small, validated deployment contract; live matrix changes only at cutover."""
    platform = target.get('volume_platform', 'tencent-luks')
    if platform not in ('tencent-luks', 'hetzner', 'tencent-root-bind'):
        raise ValueError('unsupported host volume platform')
    mapper = target.get('volume_mapper', 'tokenkey_recovery')
    if not re.fullmatch(r'[A-Za-z0-9_-]+', mapper):
        raise ValueError('invalid mapper name')
    result = {'platform': platform, 'mapper_name': mapper}
    if platform == 'tencent-root-bind':
        uuid = target.get('volume_root_uuid', '')
        if not re.fullmatch(r'[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}', uuid):
            raise ValueError('root filesystem UUID must be pinned')
        result['root_uuid'] = uuid.lower()
    return result


def _findmount(path, run, exact=False):
    rows = json.loads(run(['findmnt', '--json', '--mountpoint' if exact else '--target',
                           str(path), '--output', 'SOURCE,TARGET,FSTYPE,UUID,FSROOT'],
                          text=True))['filesystems']
    if len(rows) != 1 or rows[0]['fstype'] != 'ext4':
        raise ValueError('expected one ext4 filesystem')
    if exact and rows[0]['target'] != str(path):
        raise ValueError('expected exact mountpoint')
    return rows[0]


def check_root_bind(root, root_uuid, run):
    # Fixed production layout: no symlinks, nested mounts, dm-crypt, or empty roots.
    if root != Path('/var/lib/tokenkey') or root.is_symlink():
        raise ValueError('ordinary storage requires the canonical app directory')
    host = _findmount(Path('/'), run, exact=True)
    data = _findmount(root, run)
    compat = Path('/srv/tokenkey-rehearsal')
    bind = _findmount(compat, run, exact=True)
    if compat.is_symlink() or not root_uuid:
        raise ValueError('missing ordinary storage identity')
    for row in (host, data, bind):
        device = row['source'].split('[', 1)[0]
        if (row.get('uuid', '').lower() != root_uuid.lower()
                or not re.fullmatch(r'/dev/(?:vd|sd|xvd)[a-z]+[0-9]+|/dev/nvme[0-9]+n[0-9]+p[0-9]+', device)):
            raise ValueError('ordinary storage source does not match pinned root device')
    if (data['target'] != '/' or host.get('fsroot') != '/'
            or data.get('fsroot') != '/' or bind.get('fsroot') != str(root)
            or len({row['source'].split('[', 1)[0] for row in (host, data, bind)}) != 1):
        raise ValueError('compatibility path must bind the canonical root directory')
    if not os.path.samefile(root, compat):
        raise ValueError('storage paths must identify the same directory')
    # Reject a fresh/partial copy before Docker can populate an empty root.
    private_file(root / '.env', 0)
    for path in (root / 'docker/containers', root / 'docker/volumes', root / 'app', root / 'caddy/data'):
        if path.is_symlink() or not path.is_dir() or not any(path.iterdir()):
            raise ValueError('missing retained runtime state')
    return None


def check_volume(root, run=subprocess.check_output, platform='tencent-luks',
                 mapper_name='tokenkey_recovery', root_uuid=''):
    """Validate the encrypted/data volume for the explicit platform.

    ``tencent-luks`` (default) requires the named dm-crypt mapper and refuses
    Hetzner volume identities. ``hetzner`` remains only as a historical gate
    that requires a Hetzner Cloud Volume id symlink (prod Hetzner is retired).
    """
    if platform == 'tencent-root-bind':
        return check_root_bind(root, root_uuid, run)
    source = _mounted_ext4(root, run)
    hetzner_devices = list(Path('/dev/disk/by-id').glob('scsi-0HC_Volume_*'))
    if platform == 'hetzner':
        if not any(device.resolve() == source for device in hetzner_devices):
            raise ValueError('prod data source is not a Hetzner volume')
        return
    if platform == 'tencent-luks':
        expected = Path('/dev/mapper') / mapper_name
        if os.path.realpath(source) != os.path.realpath(expected):
            raise ValueError('tencent data source is not the expected LUKS mapper')
        if any(os.path.realpath(device) == os.path.realpath(source) for device in hetzner_devices):
            raise ValueError('tencent platform cannot use a Hetzner volume identity')
        return
    raise ValueError('unsupported host volume platform')


def bindings(root, hybrid_prod, uid=1000):
    mounts, environment = [], []
    if hybrid_prod:
        directory = root / 'aws'
        # Symlinks are always unsafe. A missing directory is allowed: Tencent layout
        # exception / first legacy→bluegreen cutover may still run like today's
        # tokenkey (no /aws mount). When the directory exists, credentials must
        # pass the same private-file gates as before.
        if directory.is_symlink():
            raise ValueError('Hybrid credential directory is missing or unsafe')
        if directory.is_dir():
            info = directory.stat()
            searchable = (info.st_mode & 0o001 or (info.st_uid == uid and info.st_mode & 0o100)
                          or (info.st_gid == uid and info.st_mode & 0o010))
            if not searchable:
                raise ValueError('Hybrid credential directory is not accessible to the application UID')
            private_file(directory / 'credentials', uid)
            mounts.append({'type': 'bind', 'source': str(directory), 'target': '/aws',
                           'read_only': True, 'bind': {'create_host_path': False}})
            environment += ['AWS_SHARED_CREDENTIALS_FILE=/aws/credentials', 'AWS_SDK_LOAD_CONFIG=1']
    return mounts, environment


def patch_compose(text, mounts, environment):
    for marker, items in (('# HOST_RUNTIME_VOLUMES', mounts), ('# HOST_RUNTIME_ENVIRONMENT', environment)):
        if text.count(marker) != 2:
            raise ValueError('blue/green runtime markers must identify exactly two app colors')
        replacement = '\n'.join('      - ' + json.dumps(item) for item in items)
        text = text.replace('      ' + marker, replacement)
    return text


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path('/var/lib/tokenkey'))
    parser.add_argument('--hybrid-prod', action='store_true')
    parser.add_argument('--check-volume', action='store_true')
    parser.add_argument('--platform', choices=('hetzner', 'tencent-luks', 'tencent-root-bind'), default='tencent-luks',
                        help='volume identity gate; default tencent-luks (Hetzner Cloud retired)')
    parser.add_argument('--mapper-name', default='tokenkey_recovery',
                        help='dm-crypt mapper basename when --platform=tencent-luks')
    parser.add_argument('--root-uuid', default='')
    parser.add_argument('--contract-b64', default='', help='validated non-secret site storage identity')
    parser.add_argument('--compose', type=Path)
    args = parser.parse_args()
    try:
        contract = {'platform': args.platform, 'mapper_name': args.mapper_name, 'root_uuid': args.root_uuid}
        if args.contract_b64:
            raw = json.loads(base64.b64decode(args.contract_b64, validate=True))
            contract = volume_contract({'volume_platform': raw['platform'],
                                        'volume_mapper': raw['mapper_name'],
                                        'volume_root_uuid': raw.get('root_uuid', '')})
        if args.check_volume or args.hybrid_prod:
            check_volume(args.root, **contract)
        if args.compose:
            mounts, environment = bindings(args.root, args.hybrid_prod)
            args.compose.write_text(patch_compose(args.compose.read_text(), mounts, environment))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        # Exception text could include a credential path or host command output.
        raise SystemExit('blue/green runtime prerequisite failed: check data volume and private credential mounts') from None


if __name__ == '__main__':
    main()
