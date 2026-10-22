#!/usr/bin/env python3
"""Validate host prerequisites and bind explicitly selected runtime identities.

Delivered with the existing blue/green primitive. Uses only Python's standard
library; credential contents never enter Compose, SSM parameters or output.
"""
import argparse
import json
import os
from pathlib import Path
import stat
import subprocess
import sys


QA_FILES = {
    'QA_ARCHIVE_STORAGE_CREDENTIALS_FILE': 'raw.json',
}
QA_CONTAINER_ROOT = '/run/secrets/tokenkey-qa'


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


def check_volume(root, run=subprocess.check_output, platform='tencent-luks',
                 mapper_name='tokenkey_recovery'):
    """Validate the encrypted/data volume for the explicit platform.

    ``tencent-luks`` (default) requires the named dm-crypt mapper and refuses
    Hetzner volume identities. ``hetzner`` remains only as a historical gate
    that requires a Hetzner Cloud Volume id symlink (prod Hetzner is retired).
    """
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


def bindings(root, env, hybrid_prod, uid=1000, *, qa_maintenance=False):
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

    selected = {key: name for key, name in QA_FILES.items() if qa_maintenance and env.get(key)}
    if selected:
        directory = root / 'qa-secrets'
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError('QA credential directory is missing or unsafe')
        # Mount individual files: do not expose unrelated identities in this directory.
        for key, name in selected.items():
            if env[key] != QA_CONTAINER_ROOT + '/' + name:
                raise ValueError('unsupported QA credential path')
            private_file(directory / name, uid)
            mounts.append({'type': 'bind', 'source': str(directory / name), 'target': env[key],
                           'read_only': True, 'bind': {'create_host_path': False}})
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
    parser.add_argument('--platform', choices=('hetzner', 'tencent-luks'), default='tencent-luks',
                        help='volume identity gate; default tencent-luks (Hetzner Cloud retired)')
    parser.add_argument('--mapper-name', default='tokenkey_recovery',
                        help='dm-crypt mapper basename when --platform=tencent-luks')
    parser.add_argument('--compose', type=Path)
    parser.add_argument('--docker-mounts', action='store_true')
    parser.add_argument('--qa-maintenance', action='store_true', help='mount raw identity only for maintenance children')
    parser.add_argument('--env-file', type=Path)
    args = parser.parse_args()
    try:
        if args.check_volume or args.hybrid_prod:
            check_volume(args.root, platform=args.platform, mapper_name=args.mapper_name)
        if args.compose or args.docker_mounts:
            env = {}
            for line in (args.env_file or args.root / '.env').read_text().splitlines():
                key, sep, value = line.partition('=')
                if sep:
                    env[key] = value.strip().strip('\"\'')
            mounts, environment = bindings(args.root, env, args.hybrid_prod, qa_maintenance=args.qa_maintenance)
            if args.compose:
                args.compose.write_text(patch_compose(args.compose.read_text(), mounts, environment))
            if args.docker_mounts:
                for mount in mounts:
                    # Unlike --volume, --mount refuses to create a missing source.
                    value = f"type=bind,src={mount['source']},dst={mount['target']},readonly"
                    sys.stdout.buffer.write(('--mount\0' + value + '\0').encode())
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        # Exception text could include a credential path or host command output.
        raise SystemExit('blue/green runtime prerequisite failed: check data volume and private credential mounts') from None


if __name__ == '__main__':
    main()
