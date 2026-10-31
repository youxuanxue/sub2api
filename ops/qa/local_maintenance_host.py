#!/usr/bin/env python3
"""Install/run independent local QA jobs. Never invokes an archive command."""
from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time

STATE = Path('/var/lib/tokenkey/qa-local')
BASE = Path('/usr/local/lib/tokenkey/qa-local')
UNITS = Path('/etc/systemd/system')
HOST_LOCK = Path('/var/lib/tokenkey/qa-lifecycle/host.lock')
HOT_DATA = Path('/var/lib/tokenkey/app')
NODE1_ROOT = Path('/var/lib/tokenkey/qa-node1')
LEGACY = ('tokenkey-qa-maintenance', 'tokenkey-qa-boundary')
PHASES = {
    'provision': '*-*-* *:00:00 UTC',
    'cleanup': '*-*-* *:02/5:00 UTC',
}
ARCHIVE_PHASE = 'archive'
# Layout-exception hosts may rename the Postgres container; DNS alias
# tokenkey-postgres on tokenkey_tokenkey-network keeps PGHOST stable.
POSTGRES_RUNTIME_CANDIDATES = ('tokenkey-postgres', 'tk-archive-standby')
APP_RUNTIME_CANDIDATES = ('tokenkey-green', 'tokenkey-blue', 'tokenkey', 'tk-cutover-prod-app')


def invoke(args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kwargs).stdout


def atomic_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, name = tempfile.mkstemp(prefix='.' + path.name, dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as handle:
            json.dump(value, handle, sort_keys=True)
            handle.write('\n')
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def resolve_postgres_runtime():
    """Pick a running Postgres container and one Docker network for the QA job.

    Prefer a network that advertises the tokenkey-postgres DNS alias so the
    hardcoded PGHOST in pg_environment resolves. Multi-network standbys
    (layout-exception hosts) are otherwise rejected by a len==1 check.
    """
    last_error = 'PostgreSQL runtime/network is ambiguous'
    for name in POSTGRES_RUNTIME_CANDIDATES:
        try:
            postgres = json.loads(invoke(['docker', 'inspect', name]))[0]
        except subprocess.CalledProcessError as exc:
            last_error = f'{name}: {exc.stderr.strip() or exc}'
            continue
        if not postgres['State']['Running']:
            last_error = f'{name}: not running'
            continue
        networks = postgres['NetworkSettings']['Networks']
        preferred = None
        for net_name, net in networks.items():
            aliases = set(net.get('Aliases') or [])
            if 'tokenkey-postgres' in aliases:
                preferred = net_name
                break
        if preferred is None and len(networks) == 1:
            preferred = next(iter(networks))
        if preferred is None:
            for net_name, net in sorted(networks.items()):
                aliases = set(net.get('Aliases') or [])
                if 'postgres' in aliases:
                    preferred = net_name
                    break
        if preferred is None:
            last_error = f'{name}: no usable network among {sorted(networks)}'
            continue
        return postgres, preferred
    raise ValueError(last_error)


def app_database_env():
    for name in APP_RUNTIME_CANDIDATES:
        try:
            app = json.loads(invoke(['docker', 'inspect', name]))[0]
        except subprocess.CalledProcessError:
            continue
        env = dict(item.split('=', 1) for item in app['Config']['Env'] if '=' in item)
        password = env.get('DATABASE_PASSWORD') or env.get('POSTGRES_PASSWORD') or env.get('PGPASSWORD')
        if not password:
            continue
        return {
            'POSTGRES_PASSWORD': password,
            'POSTGRES_USER': env.get('DATABASE_USER') or env.get('POSTGRES_USER') or 'tokenkey',
            'POSTGRES_DB': env.get('DATABASE_DBNAME') or env.get('POSTGRES_DB') or 'tokenkey',
        }
    return {}


def pg_environment(container):
    env = dict(item.split('=', 1) for item in container['Config']['Env'] if '=' in item)
    if not env.get('POSTGRES_PASSWORD'):
        env.update({k: v for k, v in app_database_env().items() if v})
    if not env.get('POSTGRES_PASSWORD'):
        raise ValueError('PostgreSQL password is unavailable')
    values = {
        'PGHOST': 'tokenkey-postgres', 'PGPORT': '5432',
        'PGUSER': env.get('POSTGRES_USER', 'tokenkey'),
        'PGDATABASE': env.get('POSTGRES_DB', 'tokenkey'),
        'PGPASSWORD': env['POSTGRES_PASSWORD'], 'PGSSLMODE': 'disable',
        'PGCONNECT_TIMEOUT': '10',
    }
    if any('\n' in value or '\r' in value for value in values.values()):
        raise ValueError('database environment contains a newline')
    return ''.join(f'{key}={value}\n' for key, value in values.items())


def require_legacy_paused():
    for job in LEGACY:
        timer = invoke(['systemctl', 'show', job+'.timer', '-p', 'ActiveState,UnitFileState'])
        facts = dict(line.split('=', 1) for line in timer.splitlines() if '=' in line)
        active = invoke(['systemctl', 'show', job+'.service', '-p', 'ActiveState', '--value']).strip()
        if facts.get('ActiveState') != 'inactive' or facts.get('UnitFileState') not in ('disabled', 'masked') or active not in ('inactive', 'failed'):
            raise ValueError('legacy QA owner must remain paused: '+job)


def unit_files(bundle, phase, environment='prod'):
    if phase not in PHASES and phase != ARCHIVE_PHASE:
        raise ValueError('unknown local phase')
    name = 'tokenkey-qa-'+phase
    if phase == 'archive':
        return {
            name+'.service': f'''[Unit]
Description=TokenKey QA node1 hourly archive
After=network-online.target
[Service]
Type=oneshot
ExecStart=/usr/bin/python3 {bundle}/node1_archive.py run --source-root {HOT_DATA} --node1-root {NODE1_ROOT} --environment {environment}
TimeoutStartSec=1800
UMask=0077
Nice=19
IOSchedulingClass=idle
''',
            name+'.timer': f'''[Unit]
Description=TokenKey QA node1 archive schedule
[Timer]
OnCalendar=*-*-* 01/4:00:00 UTC
OnBootSec=7min
Persistent=true
AccuracySec=30s
[Install]
WantedBy=timers.target
''',
        }
    return {
        name+'.service': f'''[Unit]
Description=TokenKey QA local {phase}
After=docker.service network-online.target
[Service]
Type=oneshot
ExecStart=/usr/bin/python3 {bundle}/local_maintenance_host.py run --config {bundle}/config.json --phase {phase}
TimeoutStartSec=960
UMask=0077
Nice=15
Environment=PYTHONDONTWRITEBYTECODE=1
''',
        name+'.timer': f'''[Unit]
Description=TokenKey independent QA {phase} schedule
[Timer]
OnCalendar={PHASES[phase]}
OnBootSec=2min
Persistent=true
AccuracySec=5s
[Install]
WantedBy=timers.target
''',
    }


def cleanup_mounts(node1_root=None):
    mounts = []
    # Resolve HOT_DATA first: layout-exception hosts symlink
    # /var/lib/tokenkey/app → formal/app; the leaf dirs must still be real.
    root = HOT_DATA.resolve()
    for name in ('qa_blobs', 'qa_dlq'):
        path = root/name
        if not path.is_dir() or path.is_symlink() or path.resolve() != path:
            raise ValueError('cleanup requires a canonical real QA directory: '+name)
        mounts += ['--mount', f'type=bind,src={path},dst=/app/data/{name}']
    if node1_root is not None:
        node1_path = Path(node1_root).resolve()
        receipt_root = node1_path / 'receipts'
        if not receipt_root.is_dir() or receipt_root.is_symlink() or node1_path.is_symlink():
            raise ValueError('cleanup requires a readable node1 receipt directory')
        mounts += ['--mount', f'type=bind,src={node1_path},dst=/app/node1,readonly']
    return mounts


def run_phase(config, phase, dry_run=False):
    if phase not in config['phases'] or phase not in PHASES:
        raise ValueError('phase is not installed')
    if socket.gethostname() != config['hostname']:
        raise ValueError('host identity mismatch')
    binary = Path(config['binary'])
    if hashlib.sha256(binary.read_bytes()).hexdigest() != config['sha256']:
        raise ValueError('binary digest mismatch')
    require_legacy_paused()
    started = time.time()
    receipt = {'schema_version': 1, 'phase': phase, 'ok': False, 'started_at': started}
    task_name = f'tokenkey-qa-{phase}-{os.getpid()}'
    try:
        with open(HOST_LOCK, 'a') as lock:
            deadline = time.monotonic()+600
            while True:
                try:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    if time.monotonic() >= deadline:
                        raise TimeoutError('QA host lock busy')
                    time.sleep(1)
            require_legacy_paused()
            postgres, network = resolve_postgres_runtime()
            with tempfile.TemporaryDirectory(prefix='qa-local-', dir='/run') as scratch:
                env = Path(scratch)/'pg.env'
                env.write_text(pg_environment(postgres))
                env.chmod(0o600)
                mounts = cleanup_mounts(config.get('node1_root')) if phase == 'cleanup' else []
                if dry_run:
                    mounts = [arg+',readonly' if arg.startswith('type=bind,') else arg for arg in mounts]
                command = ['docker', 'run', '--rm', '--name', task_name,
                           '--read-only', '--user', '1000:1000', '--cap-drop=ALL',
                           '--security-opt=no-new-privileges', '--memory=256m',
                           '--memory-swap=256m', '--cpus=0.20', '--pids-limit=128',
                           '--network', network, '--env-file', str(env),
                           '--mount', f'type=bind,src={binary},dst=/opt/qa-local-maintenance,readonly',
                           *mounts, '--entrypoint', '/opt/qa-local-maintenance', postgres['Image'],
                           '--phase', phase, '--confirm', f'tokenkey-qa-local-{phase}-v1']
                if dry_run:
                    command.append('--dry-run')
                if phase == 'cleanup' and config.get('node1_environment'):
                    command[command.index('--env-file'):command.index('--env-file')] = [
                        '--env', 'QA_NODE1_RECEIPT_ROOT=/app/node1',
                        '--env', f"QA_NODE1_ENVIRONMENT={config['node1_environment']}",
                    ]
                try:
                    completed = subprocess.run(command, capture_output=True, text=True, timeout=270, check=False)
                    output = completed.stdout
                except subprocess.TimeoutExpired:
                    # Only the unique container owned by this invocation is removed.
                    subprocess.run(['docker', 'rm', '-f', task_name], check=True,
                                   capture_output=True, text=True, timeout=30)
                    raise
                child = json.loads(output)
                receipt['result'] = child
                if (completed.returncode != 0 or child.get('phase') != phase or child.get('ok') is not True
                        or child.get('upload_authorized') is not False
                        or child.get('deletion_authorized') != (phase == 'cleanup' and not dry_run)):
                    raise ValueError('invalid local phase receipt')
                receipt.update(ok=True, result=child)
    except Exception as exc:
        receipt['error'] = str(exc)
        raise
    finally:
        receipt['observed_at'] = time.time()
        atomic_json(STATE/(phase+('-plan' if dry_run else '')+'.json'), receipt)
        print(json.dumps(receipt, sort_keys=True))


def expected_prod_hostname(manifest):
    """Live prod matrix (deploy/tencent/candidate-usw-20261022.json) owns host_watch.hostname."""
    if 'host_watch' in manifest:
        return manifest['host_watch']['hostname']
    return manifest['prod']['instance_name']


def install(args):
    if socket.gethostname() != expected_prod_hostname(json.loads(args.manifest.read_text())):
        raise ValueError('expected current prod hostname')
    require_legacy_paused()
    digest = hashlib.sha256(args.binary.read_bytes()).hexdigest()
    if digest != args.sha256:
        raise ValueError('artifact digest mismatch')
    phases = list(PHASES)
    print(json.dumps({'hostname': socket.gethostname(), 'phases': phases, 'sha256': digest, 'apply': args.apply}))
    if not args.apply:
        run_phase({'hostname':socket.gethostname(), 'binary':str(args.binary.resolve()),
                   'sha256':digest, 'phases':phases}, 'cleanup', dry_run=True)
        return
    bundle_digest = hashlib.sha256(Path(__file__).read_bytes()+digest.encode()).hexdigest()[:16]
    bundle = BASE/bundle_digest
    environment = manifest.get('environment', 'prod')
    config = {'hostname': socket.gethostname(), 'binary': str(bundle/'qa-local-maintenance'), 'sha256': digest,
              'phases': phases, 'node1_root': str(NODE1_ROOT), 'node1_environment': environment}
    BASE.mkdir(parents=True, exist_ok=True, mode=0o755)
    if bundle.exists():
        if (hashlib.sha256((bundle/'qa-local-maintenance').read_bytes()).hexdigest() != digest
                or (bundle/'local_maintenance_host.py').read_bytes() != Path(__file__).read_bytes()
                or (bundle/'node1_archive.py').read_bytes() != Path(__file__).with_name('node1_archive.py').read_bytes()
                or json.loads((bundle/'config.json').read_text()) != config):
            raise ValueError('installed immutable bundle mismatch')
    else:
        with tempfile.TemporaryDirectory(prefix='.stage-', dir=BASE) as scratch:
            staged = Path(scratch)/'bundle'
            staged.mkdir(mode=0o755)
            shutil.copy2(args.binary, staged/'qa-local-maintenance')
            (staged/'qa-local-maintenance').chmod(0o755)
            shutil.copy2(__file__, staged/'local_maintenance_host.py')
            archive_script = Path(__file__).with_name('node1_archive.py')
            shutil.copy2(archive_script, staged/'node1_archive.py')
            atomic_json(staged/'config.json', config)
            staged.rename(bundle)
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    with open(STATE/'install.lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        install_units(bundle, phases)


def install_units(bundle, phases):
    backup = Path('/var/backups/tokenkey-qa-local')/dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
    backup.mkdir(parents=True, mode=0o700)
    files = {name: content for phase in phases for name, content in unit_files(bundle, phase).items()}
    # Validate staged files before changing the active units.
    for name, content in files.items():
        (backup/name).write_text(content)
    invoke(['systemd-analyze', 'verify', *[str(backup/name) for name in files]])
    previous = {}
    timer_states = {}
    for name in files:
        target = UNITS/name
        if target.is_symlink():
            raise ValueError('refusing symlinked unit: '+name)
        previous[name] = target.read_bytes() if target.exists() else None
    for phase in phases:
        job = 'tokenkey-qa-'+phase
        facts = invoke(['systemctl', 'show', job+'.timer', '-p', 'ActiveState,UnitFileState'])
        timer_states[job] = dict(line.split('=', 1) for line in facts.splitlines() if '=' in line)
    atomic_json(backup/'previous.json', {
        'units': {name: data.decode() if data is not None else None for name, data in previous.items()},
        'timers': timer_states,
    })
    try:
        for job in timer_states:
            if timer_states[job].get('ActiveState') == 'active':
                invoke(['systemctl', 'stop', job+'.timer'])
        for job in timer_states:
            state = invoke(['systemctl', 'show', job+'.service', '-p', 'ActiveState', '--value']).strip()
            if state in ('active', 'activating', 'deactivating'):
                raise ValueError('local phase is busy: '+job)
        for name in files:
            os.replace(backup/name, UNITS/name)
            (UNITS/name).chmod(0o644)
        invoke(['systemctl', 'daemon-reload'])
        for job in timer_states:
            invoke(['systemctl', 'start', job+'.service'])
            invoke(['systemctl', 'enable', '--now', job+'.timer'])
    except Exception:
        for job in timer_states:
            invoke(['systemctl', 'disable', '--now', job+'.timer'])
        for name, data in previous.items():
            target = UNITS/name
            if data is None:
                target.unlink(missing_ok=True)
            else:
                target.write_bytes(data)
                target.chmod(0o644)
        invoke(['systemctl', 'daemon-reload'])
        for job, facts in timer_states.items():
            if facts.get('UnitFileState') == 'enabled':
                invoke(['systemctl', 'enable', job+'.timer'])
            if facts.get('ActiveState') == 'active':
                invoke(['systemctl', 'start', job+'.timer'])
        raise
    print(json.dumps({'installed': True, 'bundle': str(bundle), 'backup': str(backup)}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    setup = commands.add_parser('install')
    setup.add_argument('--binary', type=Path, required=True)
    setup.add_argument('--sha256', required=True)
    setup.add_argument('--manifest', type=Path, required=True)
    setup.add_argument('--apply', action='store_true')
    run = commands.add_parser('run')
    run.add_argument('--config', type=Path, required=True)
    run.add_argument('--phase', choices=PHASES, required=True)
    run.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    if args.command == 'install':
        install(args)
    else:
        run_phase(json.loads(args.config.read_text()), args.phase, args.dry_run)


if __name__ == '__main__':
    main()
