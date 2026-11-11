#!/usr/bin/env python3
"""Guarded, one-window prod app storage cutover. Default is read-only plan.

Run from a private host bundle with prepare_app_luks_exit.py,
bluegreen_host_runtime.py, resolve_app_container.py and the shared blue/green
shell owner. Old media and all rollback copies are retained.
"""
import argparse
from contextlib import ExitStack
from datetime import datetime, timezone
import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import time

try:
    from . import prepare_app_luks_exit as prep
    from . import bluegreen_host_runtime as runtime
except ImportError:  # private host bundle
    import prepare_app_luks_exit as prep
    import bluegreen_host_runtime as runtime

ROOT = prep.ROOT
WORK = prep.WORK
COMPAT = Path('/srv/tokenkey-rehearsal')
OLD = WORK / 'old-volume'
STAGING = WORK / 'staging'
RECEIPT = WORK / 'cutover.json'
SYSTEM = Path('/etc/systemd/system')
HELPER = Path('/usr/local/lib/tokenkey/bluegreen_host_runtime.py')
DISK_GUARD = SYSTEM / 'tokenkey-disk-metrics.service.d/90-tokenkey-root-storage.conf'
FSTAB = Path('/etc/fstab')
CRYPTTAB = Path('/etc/crypttab')
WINDOW_START = datetime(2026, 11, 11, 14, 0, tzinfo=timezone.utc).timestamp()
WINDOW_END = WINDOW_START + 20 * 60
CONFIRM = 'prod-app-root-bind-20261111T1400Z'
JOBS = ('tokenkey-qa-provision', 'tokenkey-qa-cleanup', 'tokenkey-qa-archive',
        'tokenkey-https-watch', 'tokenkey-disk-metrics')
LOCKS = ('bluegreen-deploy.lock', 'qa-lifecycle/host.lock')
CONTRACT = runtime.volume_contract({'volume_platform': 'tencent-root-bind',
                                    'volume_root_uuid': prep.ROOT_UUID})


def run(args, timeout=60):
    result = subprocess.run([str(a) for a in args], capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        # Inspect private system journals; subprocess output may contain secrets.
        raise RuntimeError(f'{args[0]} failed with exit {result.returncode}')
    return result.stdout


def properties(unit, *keys):
    return dict(line.split('=', 1) for line in run(
        ['systemctl', 'show', unit, '-p', ','.join(keys)]).splitlines() if '=' in line)


def check_window(now, confirm):
    # Leave the majority of the approved window for drain/copy/restore.
    if confirm != CONFIRM or not WINDOW_START <= now < WINDOW_START + 120:
        raise ValueError('apply requires the approved confirmation and first two minutes of the window')


def budget(seconds):
    if time.time() + seconds > WINDOW_END:
        raise RuntimeError('insufficient remaining maintenance budget; restore original storage')


def require_recent_copy(now):
    for phase, max_age in (('precopy', 1800), ('delta', 1200)):
        row = json.loads((WORK / (phase + '.json')).read_text())
        if (row['phase'] != phase or row['returncode'] not in (0, 24)
                or not 0 <= now - row['finished_at_unix'] <= max_age
                or row['consistent'] is not False):
            raise ValueError('online preparation is stale, failed or has an invalid receipt')
        if phase == 'delta':
            totals = [int(line.split(':', 1)[1].strip().split()[0].replace(',', ''))
                      for line in row['stats'] if line.startswith('Total transferred file size:')]
            if len(totals) != 1 or not 0 <= totals[0] <= 2 * 1024 ** 3 or row['elapsed_seconds'] > 300:
                raise ValueError('measured delta exceeds the maintenance budget')


def save(state, phase):
    state['phase'] = phase
    state['updated_at'] = datetime.now(timezone.utc).isoformat()
    prep.write_private(RECEIPT, state)
    print(json.dumps({'phase': phase, 'at': state['updated_at']}), flush=True)


def container_rows():
    ids = run(['docker', 'ps', '-aq', '--no-trunc']).split()
    if not ids:
        raise ValueError('no retained containers')
    return json.loads(run(['docker', 'inspect', *ids]))


def retained(row):
    # Never persist Config.Env in an execution receipt.
    return {key: row[key] for key in ('Id', 'Name', 'Image', 'Mounts')} | {
        'running': row['State']['Running'], 'restart_policy': row['HostConfig']['RestartPolicy']}


def ledger(root):
    return {p.stem: json.loads(p.read_text()) for p in (root / 'runtimes').glob('*.json')}


def unresolved(root):
    return sorted(p.stem for p in (root / 'failures').glob('*.json')
                  if json.loads(p.read_text())['status'] == 'unresolved')


def require_drained(root, ids):
    rows = ledger(root)
    for identity in ids:
        row = rows[identity]
        if not row['drained'] or any(h.get(k, 0) for h in row['hours'].values()
                                    for k in ('pending', 'inflight', 'failed')):
            raise ValueError('current capture runtime did not drain cleanly')


def plan(bundle):
    identity = prep.inventory()
    previous = json.loads((WORK / 'identity.json').read_text())
    for key in ('host', 'machine_id', 'root_uuid', 'old_uuid', 'config_sha256'):
        if previous[key] != identity[key]:
            raise ValueError('preparation identity or boot configuration drifted')
    if WORK.is_symlink() or WORK.stat().st_uid != 0 or WORK.stat().st_mode & 0o777 != 0o700:
        raise ValueError('private preparation directory required')
    if STAGING.is_symlink() or STAGING.stat().st_dev != os.stat('/').st_dev:
        raise ValueError('staging is not on the pinned root filesystem')
    if RECEIPT.exists() or OLD.exists():
        raise ValueError('prior cutover state exists; inspect before any new apply')
    if DISK_GUARD.exists() or DISK_GUARD.is_symlink():
        raise ValueError('disk alert storage override already exists; inspect before apply')
    if json.loads(run(['docker', 'info', '--format', '{{json .}}'])).get('LiveRestoreEnabled'):
        raise ValueError('Docker live-restore must be disabled')
    if properties('tokenkey.service', 'ActiveState')['ActiveState'] != 'inactive':
        raise ValueError('stack unit must be inactive; never invoke its recreation/stop hooks')
    try:
        from ops.lib import resolve_app_container as resolver
    except ImportError:
        import resolve_app_container as resolver
    app, _ = resolver.resolve()
    pg, redis = resolver.resolve_postgres(), resolver.resolve_redis()
    if not app or not pg or not redis or resolver.resolve_postgres_server() or resolver.resolve_redis_server():
        raise ValueError('requires one live gateway and remote database client containers only')
    rows = container_rows()
    if sum(r['Name'] == '/tokenkey-caddy' and r['State']['Running'] for r in rows) != 1:
        raise ValueError('requires the existing running Caddy ingress')
    app_row = next(r for r in rows if r['Name'] == '/' + app)
    if app_row['State'].get('Health', {}).get('Status') != 'healthy':
        raise ValueError('current gateway must be healthy')
    env = dict(e.split('=', 1) for e in app_row['Config']['Env'] if '=' in e)
    if env.get('DATABASE_HOST') != '10.4.0.3' or env.get('REDIS_HOST') != '10.4.0.3':
        raise ValueError('gateway data endpoints changed')
    appdata = [m['Source'] for m in app_row['Mounts'] if m['Destination'] == '/app/data']
    if appdata != [str(ROOT / 'app')]:
        raise ValueError('unexpected QA/app bind source')
    if sum(r['State']['Running'] for r in rows if r['Name'] in ('/tokenkey-blue', '/tokenkey-green')) != 1:
        raise ValueError('exactly one app color must be running')
    reviewed_writers = {'/' + name for name in (app, pg, redis, 'tokenkey', 'tokenkey-caddy')}
    for r in rows:
        if r['State']['Running'] and r['Name'] not in reviewed_writers:
            raise ValueError('unreviewed running writer requires a fresh inventory review')
        if r['State']['Paused'] or r['State']['Restarting'] or r['State']['OOMKilled']:
            raise ValueError('container is paused, restarting or OOM killed')
        for m in r['Mounts']:
            if m['Type'] in ('bind', 'volume') and not any(
                    m['Source'].startswith(str(p) + '/') for p in (ROOT, COMPAT)):
                raise ValueError('unreviewed bind or volume source')
    root = ROOT / 'app/qa_capture_ledger'
    current = max(ledger(root).values(), key=lambda r: r['last_snapshot_at'])
    if (datetime.now(timezone.utc) - datetime.fromisoformat(current['last_snapshot_at'].replace('Z', '+00:00'))).total_seconds() > 120:
        raise ValueError('current QA receipt is stale')
    if current['drained'] or any(h.get(k, 0) for h in current['hours'].values() for k in ('failed',)):
        raise ValueError('current capture runtime is not healthy')
    # Shell logic is owned by the existing blue/green primitive.
    drain_script(bundle, app)
    return {'identity': identity, 'containers': [retained(r) for r in rows],
            'app': app_row['Id'], 'app_name': app, 'pg': pg, 'redis': redis,
            'runtime_id': current['runtime_id'], 'unresolved_before': unresolved(root),
            'timers': {job: properties(job + '.timer', 'ActiveState', 'UnitFileState') for job in JOBS},
            'unlock': properties('tokenkey-luks-unlock.service', 'UnitFileState'),
            'bundle_sha256': {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
                              for p in bundle.iterdir() if p.is_file()}}


def drain_script(bundle, app):
    source = (bundle / 'deploy_via_ssm_bluegreen.sh').read_text()
    parts = ['set -euo pipefail']
    for name in ('log', 'die', 'container_health', 'drain_container'):
        start = source.index(name + '() {')
        end = source.index('\n}\n\n', start) + 3
        parts.append(source[start:end])
    parts.append('drain_container ' + shlex.quote(app))
    return '\n'.join(parts)


def backup(state):
    paths = list(prep.configs()) + [str(HELPER), str(SYSTEM / 'tokenkey-disk-metrics.service')]
    state['backup_paths'] = paths
    state['created_config_paths'] = [str(DISK_GUARD)]
    dest = WORK / 'boot-backup'
    dest.mkdir(mode=0o700)
    for value in paths:
        path = Path(value)
        target = dest / path.relative_to('/')
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        shutil.copy2(path, target)
    save(state, 'backed-up')


def lock_storage(stack, directory):
    for relative in LOCKS:
        path = directory / relative
        if path.is_symlink() or not path.is_file() or path.stat().st_size:
            raise ValueError('existing empty lifecycle/deploy lock required')
        handle = stack.enter_context(path.open('r'))
        fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)


def pause_jobs(state):
    # Stopping timers does not interrupt a running archive. Wait for its exit.
    run(['systemctl', 'stop', *(job + '.timer' for job in JOBS)])
    until = time.monotonic() + 90
    while any(properties(job + '.service', 'ActiveState')['ActiveState'] in
              ('active', 'activating', 'deactivating') for job in JOBS):
        if time.monotonic() >= until:
            raise RuntimeError('QA/observer service still running; abort before drain')
        time.sleep(2)
    save(state, 'jobs-paused')


def restore_jobs(state):
    for job, before in state['timers'].items():
        if before['ActiveState'] == 'active':
            run(['systemctl', 'start', job + '.timer'])


def stop_writers(state, bundle, new_copy=False):
    app = state['app']
    if properties('docker.service', 'ActiveState')['ActiveState'] == 'inactive':
        assert_offline()
        root = ROOT / 'app/qa_capture_ledger'
        ids = set(ledger(root)) - set(state['old_runtime_ids']) if new_copy else {state['runtime_id']}
        require_drained(root, ids)
        save(state, 'new-offline' if new_copy else 'old-offline')
        return
    if json.loads(run(['docker', 'inspect', app]))[0]['State']['Running']:
        state['drain_attempted'] = True
        save(state, 'draining-new' if new_copy else 'draining-old')
        # The shared function owns the 120 x 5s graceful drain policy.
        run(['bash', '-c', drain_script(bundle, app)], timeout=660)
        run(['docker', 'stop', '-t', '180', app], timeout=195)
    root = ROOT / 'app/qa_capture_ledger'
    ids = {state['runtime_id']}
    if new_copy:
        ids = set(ledger(root)) - set(state['old_runtime_ids'])
    require_drained(root, ids)
    running = run(['docker', 'ps', '-q', '--no-trunc']).split()
    allowed = {r['Id'] for r in state['containers'] if r['running']}
    if set(running) - allowed:
        raise ValueError('unexpected container started during maintenance')
    if running:
        run(['docker', 'stop', '-t', '60', *running], timeout=150)
    if run(['docker', 'ps', '-q']).strip():
        raise ValueError('container writer remains')
    run(['systemctl', 'stop', 'docker.socket', 'docker.service'], timeout=150)
    assert_offline()
    save(state, 'new-offline' if new_copy else 'old-offline')


def assert_offline():
    if properties('docker.service', 'ActiveState')['ActiveState'] != 'inactive':
        raise ValueError('Docker must be inactive')
    targets = run(['findmnt', '-rn', '-o', 'TARGET']).splitlines()
    if any(t.startswith(str(p) + '/') for t in targets for p in (ROOT, COMPAT, OLD)):
        raise ValueError('nested filesystem/overlay writer remains')


def sync_offline(source, destination, state, phase):
    assert_offline()
    command = prep.copy_command(source, destination, checksum=True)
    # Locks on the destination must retain their inodes across rsync and rename.
    command[1:1] = ['--exclude=/' + p for p in LOCKS]
    started = time.monotonic()
    output = run(command, timeout=600)
    state[phase] = {'consistent': True, 'elapsed_seconds': round(time.monotonic() - started, 3),
                    'stats': [l for l in output.splitlines() if l.startswith(
                        ('Number of ', 'Total file size:', 'Total transferred file size:'))]}
    run(['sync'], timeout=60)
    save(state, phase)


def render_fstab(text):
    lines, removed = [], []
    for line in text.splitlines():
        fields = line.split()
        if fields and not line.lstrip().startswith('#') and len(fields) > 1 and fields[1] in (str(ROOT), str(COMPAT)):
            if fields[0] != '/dev/mapper/tokenkey_recovery':
                raise ValueError('unreviewed fstab storage entry')
            removed.append(fields[1])
        else:
            lines.append(line)
    if sorted(removed) != sorted((str(ROOT), str(COMPAT))):
        raise ValueError('expected exactly two old fstab mount entries')
    lines.append(f'{ROOT} {COMPAT} none bind,x-systemd.requires-mounts-for={ROOT} 0 0')
    return '\n'.join(lines) + '\n'


def install_guards(state, bundle):
    shutil.copyfile(bundle / 'bluegreen_host_runtime.py', HELPER)
    HELPER.chmod(0o644)
    contract = base64.b64encode(json.dumps(CONTRACT, sort_keys=True).encode()).decode()
    guard = ('[Unit]\nRequiresMountsFor=/var/lib/tokenkey /srv/tokenkey-rehearsal\n'
             '[Service]\nExecStartPre=/usr/bin/python3 ' + str(HELPER)
             + ' --check-volume --contract-b64 ' + contract + '\n')
    for unit in ('docker', 'tokenkey'):
        path = SYSTEM / f'{unit}.service.d/tokenkey-volume.conf'
        path.write_text(guard)
        path.chmod(0o644)
    # Ordinary app storage shares root capacity. Keep one root-pressure alert;
    # the same pinned contract replaces the old separate-mount assumption.
    DISK_GUARD.parent.mkdir(mode=0o755, exist_ok=True)
    DISK_GUARD.write_text(guard + 'Environment=TOKENKEY_DATA_VOLUME_PATH=\n')
    DISK_GUARD.chmod(0o644)
    for name in ('20-tokenkey-encrypted-storage.conf', '40-tokenkey-luks-unlock.conf'):
        (SYSTEM / 'docker.service.d' / name).unlink()
    FSTAB.write_text(render_fstab(FSTAB.read_text()))
    # crypttab already has noauto,nofail. Retain its recovery entry and key.
    if 'luks,noauto,nofail' not in CRYPTTAB.read_text():
        raise ValueError('old crypttab entry is not optional')
    run(['systemctl', 'disable', 'tokenkey-luks-unlock.service'])
    run(['systemctl', 'daemon-reload'])
    run(['findmnt', '--verify', '--tab-file', '/etc/fstab'])
    runtime.check_volume(ROOT, **CONTRACT)
    env = shlex.split(properties('tokenkey-disk-metrics.service', 'Environment')['Environment'])
    if [v for v in env if v.startswith('TOKENKEY_DATA_VOLUME_PATH=')] != ['TOKENKEY_DATA_VOLUME_PATH=']:
        raise ValueError('disk pressure monitor still assumes a separate data mount')
    for unit in ('docker', 'tokenkey'):
        value = properties(unit + '.service', 'Requires', 'After', 'Wants')
        if any('tokenkey-luks-unlock' in s or 'cryptsetup' in s for s in value.values()):
            raise ValueError('remaining LUKS startup dependency')
        graph = run(['systemctl', 'list-dependencies', '--all', '--plain', '--no-legend', unit + '.service'])
        if any(word in graph for word in ('tokenkey-luks-unlock', 'cryptsetup@tokenkey', 'dev-mapper-tokenkey_recovery')):
            raise ValueError('remaining transitive LUKS startup dependency')
    save(state, 'root-guards-installed')


def switch_storage(state, bundle):
    # All locks on the old superblock were opened through OLD, so no live FD
    # prevents unmounting the two public paths. The copied locks remain held too.
    run(['umount', str(COMPAT)])
    run(['umount', str(ROOT)])
    for path, name in ((ROOT, 'underlay-root'), (COMPAT, 'underlay-compat')):
        path.rename(WORK / name)
    STAGING.rename(ROOT)
    COMPAT.mkdir(mode=0o755)
    run(['mount', '--bind', str(ROOT), str(COMPAT)])
    save(state, 'root-mounted')
    install_guards(state, bundle)


def start_retained(state):
    # Even an unexpected restart-policy start, or daemon metadata write, must
    # cross the reverse-sync boundary before the old copy can be used again.
    state['writers_started'] = True
    save(state, 'starting-daemon')
    run(['systemctl', 'start', 'docker.service'], timeout=150)
    after = [retained(r) for r in container_rows()]
    expected = [{k: v for k, v in r.items() if k != 'running'} for r in state['containers']]
    actual = [{k: v for k, v in r.items() if k != 'running'} for r in after]
    if sorted(expected, key=lambda r: r['Id']) != sorted(actual, key=lambda r: r['Id']):
        raise ValueError('container IDs/images/mounts/restart policies changed')
    if any(r['running'] for r in after):
        raise ValueError('unexpected automatic container start')
    # Start clients, app and proxy before public ingress; standby never starts.
    ingress = [r['Id'] for r in state['containers'] if r['Name'] == '/tokenkey-caddy' and r['running']]
    running = [r['Id'] for r in state['containers'] if r['running'] and r['Id'] not in ingress]
    save(state, 'starting-retained')
    run(['docker', 'start', *running], timeout=150)
    deadline = time.monotonic() + 90
    while True:
        app = json.loads(run(['docker', 'inspect', state['app']]))[0]
        if app['State'].get('Health', {}).get('Status') == 'healthy':
            break
        if time.monotonic() >= deadline:
            raise RuntimeError('retained gateway did not become healthy')
        time.sleep(2)
    if run(['docker', 'exec', state['pg'], 'sh', '-c',
            'psql -XAt -U "${PGUSER:-tokenkey}" -d "${PGDATABASE:-tokenkey}" -c "select pg_is_in_recovery();"']).strip() != 'f':
        raise ValueError('database primary check failed')
    if run(['docker', 'exec', state['redis'], 'redis-cli', 'ping']).strip() != 'PONG':
        raise ValueError('Redis check failed')
    run(['docker', 'start', *ingress])
    state['ingress_restored_unix'] = time.time()
    save(state, 'ingress-restored')


def verify(state):
    runtime.check_volume(ROOT, **CONTRACT)
    expected = {r['Id'] for r in state['containers'] if r['running']}
    if set(run(['docker', 'ps', '-q', '--no-trunc']).split()) != expected:
        raise ValueError('running container set changed')
    for domain in ('tokenkey.dev', 'api.tokenkey.dev', 'callmodel.io', 'api.callmodel.io'):
        path = '/health' if domain.startswith('api.') else '/'
        run(['curl', '--fail', '--silent', '--show-error', '--max-time', '15',
             '--resolve', domain + ':443:127.0.0.1', 'https://' + domain + path])
    root = ROOT / 'app/qa_capture_ledger'
    if unresolved(root) != state['unresolved_before']:
        raise ValueError('QA unresolved failures changed across cutover')
    new_ids = set(ledger(root)) - set(state['old_runtime_ids'])
    if len(new_ids) != 1:
        raise ValueError('expected one new capture runtime')
    row = ledger(root)[next(iter(new_ids))]
    if row['drained'] or any(h.get(k, 0) for h in row['hours'].values() for k in ('failed',)):
        raise ValueError('new capture runtime is unhealthy')
    state['new_runtime_id'] = row['runtime_id']
    save(state, 'verified')


def restore_boot(state):
    for value in state.get('created_config_paths', []):
        Path(value).unlink(missing_ok=True)
    for value in state['backup_paths']:
        path = Path(value)
        shutil.copy2(WORK / 'boot-backup' / path.relative_to('/'), path)
    if state['unlock']['UnitFileState'] == 'enabled':
        run(['systemctl', 'enable', 'tokenkey-luks-unlock.service'])
    run(['systemctl', 'daemon-reload'])


def acceptance_facts(state):
    root = ROOT / 'app/qa_capture_ledger'
    if unresolved(root) != state['unresolved_before']:
        raise ValueError('new QA failure after traffic restoration')
    row = ledger(root)[state['new_runtime_id']]
    age = (datetime.now(timezone.utc) - datetime.fromisoformat(
        row['last_snapshot_at'].replace('Z', '+00:00'))).total_seconds()
    if age > 120 or row['drained'] or any(h.get('failed', 0) for h in row['hours'].values()):
        raise ValueError('capture runtime failed or stopped advancing')
    since = float(state['ingress_restored_unix'])
    count = int(run(['docker', 'exec', state['pg'], 'sh', '-c',
                     'psql -XAt -U "${PGUSER:-tokenkey}" -d "${PGDATABASE:-tokenkey}" '
                     f'-c "SELECT count(*) FROM usage_logs WHERE created_at >= to_timestamp({since});"']).strip())
    timers = all(properties(job + '.timer', 'ActiveState')['ActiveState'] == 'active'
                 for job, before in state['timers'].items() if before['ActiveState'] == 'active')
    return {'metered_requests_after_ingress': count,
            'new_runtime_persisted': sum(h.get('persisted', 0) for h in row['hours'].values()),
            'original_timers_restored': timers}


def accept(state):
    # Observe genuine user traffic; do not create synthetic chargeable requests.
    deadline = time.monotonic() + 300
    while True:
        try:
            state['acceptance'] = facts = acceptance_facts(state)
        except (OSError, ValueError, RuntimeError, KeyError, subprocess.SubprocessError) as error:
            state['acceptance_error_type'] = type(error).__name__
            save(state, 'acceptance-failed')
            raise
        if (facts['metered_requests_after_ingress'] > 0 and facts['new_runtime_persisted'] > 0
                and facts['original_timers_restored']):
            save(state, 'accepted')
            return
        if time.monotonic() >= deadline:
            save(state, 'acceptance-unproven')
            raise RuntimeError('no observed metered/captured traffic within five minutes; serving copy retained')
        time.sleep(5)


def rollback(state, bundle):
    """Preserve all new writes by an offline reverse sync before using old media."""
    save(state, 'rolling-back')
    is_new = ROOT.exists() and not os.path.ismount(ROOT) and ROOT.stat().st_ino == state['staging_inode']
    if is_new and state.get('writers_started'):
        stop_writers(state, bundle, new_copy=True)
        sync_offline(ROOT, OLD, state, 'reverse-synced')
    elif properties('docker.service', 'ActiveState')['ActiveState'] != 'inactive':
        # Drain timeout still requires restarting the original gateway to clear
        # SIGUSR1. No storage move is attempted, and any shutdown gap is reported.
        running = run(['docker', 'ps', '-q']).split()
        if running:
            run(['docker', 'stop', '-t', '180', *running], timeout=210)
        run(['systemctl', 'stop', 'docker.socket', 'docker.service'], timeout=150)
    assert_offline()
    if os.path.ismount(COMPAT):
        run(['umount', str(COMPAT)])
    if os.path.ismount(ROOT):
        run(['umount', str(ROOT)])
    if is_new:
        ROOT.rename(WORK / 'retained-new-after-rollback')
    for path, name in ((ROOT, 'underlay-root'), (COMPAT, 'underlay-compat')):
        saved = WORK / name
        if saved.exists():
            if path.exists():
                path.rmdir()  # only the empty compat directory created by us
            saved.rename(path)
    restore_boot(state)
    for path in (ROOT, COMPAT):
        run(['mount', '-t', 'ext4', '/dev/mapper/tokenkey_recovery', str(path)])
    runtime.check_volume(ROOT)
    start_retained(state)
    save(state, 'rolled-back')


def apply(bundle, confirm):
    check_window(time.time(), confirm)
    state = plan(bundle)
    require_recent_copy(time.time())
    state['staging_inode'] = STAGING.stat().st_ino
    state['old_runtime_ids'] = list(ledger(ROOT / 'app/qa_capture_ledger'))
    with (WORK / 'prepare.lock').open('a') as prepare_lock:
        fcntl.flock(prepare_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        OLD.mkdir(mode=0o700)
        run(['mount', '-t', 'ext4', '/dev/mapper/tokenkey_recovery', str(OLD)])
        try:
            with ExitStack() as locks:
                # Deploy lock first; QA lock is acquired only after jobs finish.
                handle = locks.enter_context((OLD / LOCKS[0]).open('r'))
                fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
                if prep.configs() != state['identity']['config_sha256']:
                    raise ValueError('configuration changed before lock acquisition')
                backup(state)
                try:
                    pause_jobs(state)
                    handle = locks.enter_context((OLD / LOCKS[1]).open('r'))
                    fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    lock_storage(locks, STAGING)
                    # Snapshot again under both locks immediately before drain.
                    if [retained(r) for r in container_rows()] != state['containers']:
                        raise ValueError('containers changed during preparation')
                    budget(9 * 60)
                    stop_writers(state, bundle)
                    budget(8 * 60)
                    sync_offline(ROOT, STAGING, state, 'final-synced')
                    budget(4 * 60)
                    switch_storage(state, bundle)
                    start_retained(state)
                    verify(state)
                except Exception as error:
                    state['failure_type'] = type(error).__name__
                    save(state, 'failed')
                    if state.get('drain_attempted'):
                        rollback(state, bundle)
                    raise
        finally:
            # Locks must be released before restarting timer-driven owners or
            # unmounting their private alias. Never unmount lazily or forcefully.
            restore_jobs(state)
            run(['umount', str(OLD)])
        save(state, 'complete')
    return state


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=('plan', 'apply', 'verify', 'accept'), default='plan')
    parser.add_argument('--confirm', default='')
    parser.add_argument('--bundle', type=Path, default=Path(__file__).resolve().parent)
    args = parser.parse_args()
    if args.phase == 'plan':
        state = plan(args.bundle)
        print(json.dumps(state, sort_keys=True))
    elif args.phase == 'apply':
        state = apply(args.bundle, args.confirm)
        accept(state)
    elif args.phase == 'accept':
        accept(json.loads(RECEIPT.read_text()))
    else:
        state = json.loads(RECEIPT.read_text())
        verify(state)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError, KeyError):
        raise SystemExit('storage cutover refused or failed; inspect private cutover.json and system journal') from None
