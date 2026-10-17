#!/usr/bin/env python3
"""Fail before Docker starts if approved Node1 production data is not mounted.

Staged only during preparation. The Docker and gateway drop-ins using this guard
are installed as part of separately approved activation, never by test deploys.
"""
import json
import argparse
from pathlib import Path
import subprocess

if __package__:
    from .node1_candidate import host_guard, ROOT
    from .node1_full_backup import private, ACTIVATED
else:
    from node1_candidate import host_guard, ROOT
    from node1_full_backup import private, ACTIVATED


def mount_unit():
    """Render only; the mount and the guard must select the same candidate."""
    return f'''[Unit]
Description=Adopt reconciled Node1 PostgreSQL data after approved cutover
RequiresMountsFor=/data /var/lib/tokenkey
Before=tokenkey.service
ConditionPathExists={ACTIVATED}
[Mount]
What={ROOT}/postgres/18/docker
Where=/var/lib/tokenkey/postgres
Type=none
Options=bind
[Install]
WantedBy=multi-user.target
'''


def verify():
    host_guard()
    approval = json.loads(private(ACTIVATED))
    if approval.get('target') != 'zhupeng-prod' or approval.get('cutover_approved') is not True:
        raise RuntimeError('separate activation approval required')
    runtime = Path('/var/lib/tokenkey')
    backing = Path('/data/tokenkey')
    pg = runtime / 'postgres'
    source = ROOT / 'postgres/18/docker'
    for path in (runtime, backing, pg, source):
        if path.is_symlink() or not path.is_dir():
            raise RuntimeError('production data paths must be existing directories')
    for path in (runtime, pg):
        subprocess.run(['findmnt', '--noheadings', '--mountpoint', str(path)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if not runtime.samefile(backing) or not pg.samefile(source):
        raise RuntimeError('production data mount does not match the reconciled candidate')
    if (pg / 'PG_VERSION').read_text().strip() != '18' or not (pg / 'global/pg_control').is_file():
        raise RuntimeError('reconciled PostgreSQL cluster is missing')
    return {'activation_mounts_verified': True}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--render-mount', action='store_true',
                        help='Print the pending mount unit; never install or activate it')
    args = parser.parse_args()
    print(mount_unit() if args.render_mount else json.dumps(verify()), end='\n')
