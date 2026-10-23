#!/usr/bin/env python3
"""Classify /var/lib/tokenkey without forging a mountpoint.

A dedicated mount (mountpoint exit 0, or findmnt TARGET equal to the data
directory) stays the volume layout. Beijing keeps this directory on the root
filesystem: mountpoint exits 32 and the directory's device matches ``/``.
Any other device, a missing path, or a symlink is refused. Callers must not
bind-mount an empty directory just to satisfy the old check.
"""
import argparse
import os
from pathlib import Path
import subprocess
import sys

NOT_A_MOUNTPOINT = 32


def layout_from_mountpoint(returncode, data_device, root_device):
    if returncode == 0:
        return 'volume'
    if (returncode == NOT_A_MOUNTPOINT
            and data_device is not None
            and root_device is not None
            and data_device == root_device):
        return 'root'
    raise ValueError('domestic data directory is neither a mounted volume nor on the root filesystem')


def layout_from_findmnt(target, data_path):
    found = (target or '').strip()
    data = str(data_path)
    if found == data:
        return 'volume'
    if found == '/':
        return 'root'
    raise ValueError('data volume is not mounted')


def require_directory(path, *, run=None, device=None):
    path = Path(path)
    if path.is_symlink() or not path.is_dir():
        raise ValueError('domestic data directory is missing')
    if run is None:
        run = subprocess.run
    completed = run(['mountpoint', '-q', str(path)], capture_output=True, text=True)
    if completed.returncode == 0:
        return 'volume'
    if device is None:
        device = lambda item: os.stat(item).st_dev
    return layout_from_mountpoint(completed.returncode, device(path), device(Path('/')))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--require', type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        print(require_directory(args.require))
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
