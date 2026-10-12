#!/usr/bin/env python3
"""Guarded uk1 QA host operations. Never changes the gateway/Docker service."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time

try:
    from .colocation_resources import load_policy, slice_unit
    from .hcloud_runtime import HOST_ROOT, render_files
except ImportError:
    from colocation_resources import load_policy, slice_unit
    from hcloud_runtime import HOST_ROOT, render_files

ROOT = Path(HOST_ROOT)
OWNER = 'qa-hcloud'


def run(*args, input=None):
    result = subprocess.run(args, input=input, text=True, capture_output=True, timeout=120)
    if result.returncode:
        # psql and Compose errors may contain private input. Do not echo them.
        raise RuntimeError(f'{args[0]} failed (exit {result.returncode}); QA operation stopped')
    return result.stdout.strip()


def guard():
    if os.geteuid() != 0 or socket.gethostname() != 'tokenkey-edge-uk1-hz-cax21':
        raise RuntimeError('requires root on the approved uk1 host')
    if not Path('/sys/fs/cgroup/cgroup.controllers').is_file():
        raise RuntimeError('cgroup v2 is required')
    if run('docker', 'info', '--format', '{{.CgroupDriver}}') != 'systemd':
        raise RuntimeError('systemd cgroup driver is required')


def owned_container(name):
    info = json.loads(run('docker', 'inspect', name))[0]
    if info['Config'].get('Labels', {}).get('dev.tokenkey.owner') != OWNER:
        raise RuntimeError('refusing an unowned container')
    return info


def gateway_snapshot():
    rows = [json.loads(row) for row in run('docker', 'ps', '--format', '{{json .Names}}').splitlines()]
    names = [n for n in rows if n.startswith('tokenkey') and not n.startswith('tokenkey-qa-')]
    if not any(n in names for n in ('tokenkey-blue', 'tokenkey-green')):
        raise RuntimeError('active gateway container missing')
    return {n: {k: json.loads(run('docker', 'inspect', n))[0]['State'][k]
                for k in ('StartedAt', 'Running')} for n in names}


def config(policy):
    directory = ROOT / 'config'
    inputs = json.loads((directory / 'render-inputs.json').read_text())
    expected = render_files(**inputs, policy=policy)
    for name, value in expected.items():
        actual = (directory / name).read_text()
        if (json.loads(actual) != json.loads(value) if name.endswith('.json') else actual != value):
            raise RuntimeError('QA configuration differs from the approved renderer')
    doc = json.loads(expected['compose.json'])
    for service in doc['services'].values():
        for mount in service['volumes']:
            # Exact renderer comparison rejects traversal; resolution also rejects
            # a symlink replacing any component of an otherwise approved bind.
            source = Path(mount['source'])
            if source.resolve() != source:
                raise RuntimeError('QA bind path must not contain symlinks')
    return doc


def root_io_device():
    root_device = run('findmnt', '-no', 'SOURCE', '/')
    parent = run('lsblk', '-no', 'PKNAME', root_device)
    return '/dev/' + parent if parent else root_device


def check_slice(policy):
    if run('systemctl', 'is-active', 'tokenkey-qa.slice') != 'active':
        raise RuntimeError('QA parent slice is not active; prepare it first')
    group = run('systemctl', 'show', '--property=ControlGroup', '--value', 'tokenkey-qa.slice')
    if group != '/tokenkey.slice/tokenkey-qa.slice':
        raise RuntimeError('unexpected QA parent cgroup')
    path = Path('/sys/fs/cgroup') / group.lstrip('/')
    cap = policy['aggregate']
    expected = {'memory.max': cap['memory_bytes'], 'memory.swap.max': 0, 'pids.max': cap['pids']}
    if any((path / name).read_text().strip() != str(value) for name, value in expected.items()):
        raise RuntimeError('QA parent memory/swap/PID limits differ')
    quota, period = (path / 'cpu.max').read_text().split()
    if quota == 'max' or int(quota) * 1000 != int(period) * cap['cpu_millis']:
        raise RuntimeError('QA parent CPU limit differs')
    device = run('lsblk', '-dnro', 'MAJ:MIN', root_io_device())
    rows = {row.split()[0]: dict(item.split('=') for item in row.split()[1:])
            for row in (path / 'io.max').read_text().splitlines()}
    expected_io = {'rbps': str(policy['io_bytes_per_second']), 'wbps': str(policy['io_bytes_per_second']),
                   'riops': str(policy['io_iops']), 'wiops': str(policy['io_iops'])}
    if any(rows.get(device, {}).get(key) != value for key, value in expected_io.items()):
        raise RuntimeError('QA parent physical disk IO limits differ')


def compose(*args):
    return run('docker', 'compose', '-f', str(ROOT/'config/compose.json'), '--profile', 'worker', *args)


def check_mount(name, expected):
    path = ROOT / name
    data = json.loads(run('findmnt', '--json', '--mountpoint', str(path), '--output', 'TARGET,SOURCE,FSTYPE,OPTIONS'))['filesystems']
    if len(data) != 1 or data[0]['fstype'] != 'ext4' or not data[0]['source'].startswith('/dev/loop'):
        raise RuntimeError('QA data must use its dedicated bounded loop filesystem')
    options = set(data[0]['options'].split(','))
    if not {'nodev', 'nosuid', 'noexec'} <= options:
        raise RuntimeError('QA filesystem mount options are incomplete')
    backing = run('losetup', '--noheadings', '--output', 'BACK-FILE', data[0]['source'])
    expected_file = ROOT / 'disks' / (name + '.ext4')
    if backing != str(expected_file) or expected_file.stat().st_size != expected:
        raise RuntimeError('unexpected QA backing file')


def prepare(policy):
    config(policy)
    if shutil.disk_usage(ROOT).free < 16 * 1024**3:
        raise RuntimeError('uk1 free disk below 16 GiB preparation floor')
    if int(next(line.split()[1] for line in Path('/proc/meminfo').read_text().splitlines()
                if line.startswith('MemAvailable:'))) < 3 * 1024**2:
        raise RuntimeError('uk1 available memory below 3 GiB preparation floor')
    for name, uid, size in [('scratch', 1000, policy['scratch_bytes']), ('queue', 70, policy['queue_disk_bytes'])]:
        image = ROOT/'disks'/f'{name}.ext4'
        mount = ROOT/name
        image.parent.mkdir(mode=0o700, exist_ok=True)
        mount.mkdir(mode=0o700, exist_ok=True)
        if not image.exists():
            # Preallocation, not a sparse file: QA cannot grow unbounded on Edge's root disk.
            run('fallocate', '-l', str(size), str(image))
            image.chmod(0o600)
            run('mkfs.ext4', '-q', '-m', '0', str(image))
        elif image.stat().st_size != size or run('blkid', '-s', 'TYPE', '-o', 'value', str(image)) != 'ext4':
            raise RuntimeError('existing QA disk is not the expected ext4 filesystem')
        unit = run('systemd-escape', '--path', '--suffix=mount', str(mount))
        text = f'''[Unit]
Description=Bounded TokenKey QA {name}
Before=tokenkey-qa.service

[Mount]
What={image}
Where={mount}
Type=ext4
Options=loop,nodev,nosuid,noexec

[Install]
WantedBy=multi-user.target
'''
        write_unit(unit, text)
        run('systemctl', 'daemon-reload')
        run('systemctl', 'start', unit)
        check_mount(name, size)
        os.chown(mount, uid, uid)
        mount.chmod(0o700)
    # io.max on the physical parent covers loop-backed writes too; verify live cgroup counters.
    device = root_io_device()
    unit = slice_unit(policy) + (f'IOAccounting=yes\nIOWeight=10\n'
                               f'IOReadBandwidthMax={device} {policy["io_bytes_per_second"]}\n'
                               f'IOWriteBandwidthMax={device} {policy["io_bytes_per_second"]}\n'
                               f'IOReadIOPSMax={device} {policy["io_iops"]}\n'
                               f'IOWriteIOPSMax={device} {policy["io_iops"]}\n')
    write_unit('tokenkey-qa.slice', unit)
    run('systemctl', 'daemon-reload')
    run('systemctl', 'start', 'tokenkey-qa.slice')
    check_slice(policy)
    run('nft', '--check', '--file', str(ROOT/'config/firewall.nft'))


def write_unit(name, body):
    path = Path('/etc/systemd/system') / name
    if path.exists() and path.read_text() != body:
        raise RuntimeError('refusing to overwrite different QA unit; drain and review changes first')
    path.write_text(body)
    path.chmod(0o644)


def verify_private_files():
    for directory, uid, filenames in (
            ('queue-secrets', 70, {'server.crt', 'server.key'}),
            ('worker-secrets', 1000, {'bundle.json', 'raw.json', 'queue.dsn', 'queue-ca.pem'})):
        path = ROOT / directory
        if path.is_symlink() or path.stat().st_uid != uid or path.stat().st_mode & 0o077:
            raise RuntimeError('QA secret directory owner/mode invalid')
        if {p.name for p in path.iterdir()} != filenames:
            raise RuntimeError('unexpected QA secret files')
        for name in filenames:
            file = path / name
            if file.is_symlink() or not file.is_file() or file.stat().st_uid != uid or file.stat().st_mode & 0o777 not in (0o400, 0o600):
                raise RuntimeError('QA secret file owner/mode invalid')


def load_firewall():
    # Replace only our own table, as one nft transaction; never flush the host ruleset.
    prefix = ''
    for family in ('inet', 'bridge'):
        found = subprocess.run(['nft', 'list', 'table', family, 'tokenkey_qa'], capture_output=True).returncode == 0
        if found:
            prefix += f'delete table {family} tokenkey_qa\n'
    text = prefix + (ROOT/'config/firewall.nft').read_text()
    run('nft', '--check', '--file', '-', input=text)
    run('nft', '--file', '-', input=text)


def bootstrap(policy):
    doc = config(policy)
    check_slice(policy)
    check_mount('queue', policy['queue_disk_bytes'])
    if (ROOT/'queue/pgdata').exists():
        raise RuntimeError('queue data already exists; bootstrap refuses to reinitialize it')
    service = doc['services']['qa-queue']
    script = '''set -eu
initdb -D /queue/pgdata --auth-local=peer --auth-host=scram-sha-256 >/dev/null
pg_ctl -D /queue/pgdata -o "-c listen_addresses='' -c unix_socket_directories=/tmp" -w start >/dev/null
trap 'pg_ctl -D /queue/pgdata -m fast -w stop >/dev/null' EXIT
createdb -h /tmp tokenkey_qa_queue
psql -X -h /tmp -d tokenkey_qa_queue -v ON_ERROR_STOP=1 -f /bootstrap.sql >/dev/null
'''
    sql = ROOT/'bootstrap.sql'
    if sql.stat().st_uid != 70 or sql.stat().st_mode & 0o077:
        raise RuntimeError('bootstrap SQL must be private and owned by queue UID')
    run('docker', 'run', '--rm', '--name', 'tokenkey-qa-bootstrap', '--label', 'dev.tokenkey.owner=qa-hcloud',
        '--network', 'none', '--user', '70:70', '--read-only', '--cap-drop', 'ALL',
        '--security-opt', 'no-new-privileges:true', '--security-opt', 'apparmor=docker-default',
        '--cgroup-parent', 'tokenkey-qa.slice', '--cpus', str(service['cpus']),
        '--memory', str(service['mem_limit']), '--memory-swap', str(service['memswap_limit']),
        '--pids-limit', str(service['pids_limit']), '--log-driver', 'none',
        '--mount', f'type=bind,src={ROOT}/queue,dst=/queue',
        '--mount', f'type=bind,src={sql},dst=/bootstrap.sql,readonly',
        '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=16m,uid=70,gid=70,mode=0700',
        '--entrypoint', '/bin/sh', service['image'], '-c', script)
    sql.unlink()


def sql(query):
    owned_container('tokenkey-qa-queue')
    return run('docker', 'exec', '--user', '70:70', 'tokenkey-qa-queue', 'psql', '-XAt',
               '-U', 'postgres', '-d', 'tokenkey_qa_queue', '-v', 'ON_ERROR_STOP=1', '-c', query)


def status():
    return json.loads(sql("SELECT json_build_object('paused',(SELECT paused FROM qa_queue.control),"
                          "'jobs',(SELECT coalesce(json_object_agg(state,n),'{}'::json) FROM "
                          "(SELECT state,count(*) n FROM qa_queue.jobs GROUP BY state) s),"
                          "'active_leases',(SELECT count(*) FROM qa_queue.jobs WHERE state='leased' "
                          "AND lease_until > clock_timestamp()))"))


def drain():
    sql('UPDATE qa_queue.control SET paused=true')
    deadline = time.monotonic() + 56*60
    while status()['active_leases']:
        if time.monotonic() >= deadline:
            raise RuntimeError('QA drain timed out; worker left running and queue paused')
        time.sleep(5)
    owned_container('tokenkey-qa-worker')
    compose('stop', 'qa-worker')


def runtime_boundary(policy):
    config(policy)
    check_mount('queue', policy['queue_disk_bytes'])
    check_mount('scratch', policy['scratch_bytes'])
    check_slice(policy)
    verify_private_files()
    load_firewall()


def start_component(operation, policy):
    runtime_boundary(policy)
    existing = run('docker', 'ps', '-a', '--filter', 'name=^tokenkey-qa-queue$', '--format', '{{.Names}}')
    running = bool(existing) and owned_container('tokenkey-qa-queue')['State']['Running']
    if operation == 'worker-start' and not running:
        raise RuntimeError('start the queue before preparing a worker')
    if running:
        state = status()
        if not state['paused'] or state['active_leases']:
            raise RuntimeError('pause and drain before replacing QA components')
    else:
        # A restarted database may contain paused=false. No consumer may reconnect
        # before it is explicitly paused; HBA admits only this worker address.
        worker = run('docker', 'ps', '-a', '--filter', 'name=^tokenkey-qa-worker$', '--format', '{{.Names}}')
        if worker and owned_container('tokenkey-qa-worker')['State']['Running']:
            raise RuntimeError('stop the worker before recovering a stopped queue')
    component = 'qa-queue' if operation == 'queue-start' else 'qa-worker'
    compose('up', '-d', '--no-build', '--no-deps', '--pull', 'never', component)
    if not running:
        deadline = time.monotonic() + 60
        while True:
            info = owned_container('tokenkey-qa-queue')
            if not info['State']['Running']:
                raise RuntimeError('recovered QA queue failed to start')
            if info['State'].get('Health', {}).get('Status') == 'healthy':
                break
            if time.monotonic() >= deadline:
                raise RuntimeError('QA queue readiness timed out; keep worker stopped')
            time.sleep(1)
        sql('UPDATE qa_queue.control SET paused=true')
        if not status()['paused']:
            raise RuntimeError('recovered QA queue is not paused; keep worker stopped')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('prepare', 'bootstrap', 'queue-start', 'worker-start', 'status', 'pause', 'resume', 'drain'))
    parser.add_argument('--policy', type=Path, required=True)
    args = parser.parse_args()
    guard()
    ROOT.mkdir(mode=0o700, exist_ok=True)
    with (ROOT/'operation.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        before = gateway_snapshot()
        policy = load_policy(args.policy)
        if args.operation == 'prepare':
            prepare(policy)
        elif args.operation == 'bootstrap':
            bootstrap(policy)
        elif args.operation in ('queue-start', 'worker-start'):
            start_component(args.operation, policy)
        elif args.operation == 'pause':
            sql('UPDATE qa_queue.control SET paused=true')
        elif args.operation == 'resume':
            runtime_boundary(policy)
            sql('UPDATE qa_queue.control SET paused=false')
        elif args.operation == 'drain':
            drain()
        else:
            print(json.dumps(status()))
        if before != gateway_snapshot():
            raise RuntimeError('gateway container state changed during QA operation; stop and investigate')
    print(json.dumps({'operation': args.operation, 'gateway_containers_unchanged': True}))


if __name__ == '__main__':
    main()
