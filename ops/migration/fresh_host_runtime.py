#!/usr/bin/env python3
"""Validate and activate the held AWS runtime after verified final data sync.

check is read-only. activate never recreates PostgreSQL or its anonymous volume.
On failure, disable/stop candidate writers and restore the persistent hold. Never
restart the source, change DNS, or restore prod mirror scheduling here.
"""
import argparse
import hashlib
import json
import ipaddress
import re
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import time

try:
    from ops.lib.resolve_app_container import CANDIDATES, resolve
except ModuleNotFoundError:
    sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'lib'))
    from resolve_app_container import CANDIDATES, resolve

R = Path('/var/lib/tokenkey')
NET = 'tokenkey_tokenkey-network'


def run(*args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=180)
    if result.returncode:
        raise RuntimeError('runtime command failed: ' + ' '.join(args[:3]) + ' (details withheld)')
    return result.stdout.strip()


def inspect(name):
    return json.loads(run('docker', 'inspect', name))[0]


def compose(file, *args):
    return run('docker', 'compose', '--project-name', 'tokenkey', '--env-file', str(R / '.env'),
               '-f', str(R / file), *args)


def mounts(container):
    return {m['Destination']: (m['Type'], m['Source']) for m in container['Mounts']}


def expected_host(node):
    if node not in ('prod', 'us4', 'us5', 'uk1', 'uk2'):
        raise ValueError('unknown node')
    return 'tokenkey-prod-aws-20261015' if node == 'prod' else f'tokenkey-edge-{node}-aws-20261015'


def check(node):
    if socket.gethostname() != expected_host(node):
        raise ValueError('wrong candidate hostname')
    if not (R / 'migration/DATA_SYNC_HOLD').is_file():
        raise ValueError('candidate hold missing')
    if run('docker', 'ps', '--format', '{{.Names}}') != 'tokenkey-postgres':
        raise ValueError('candidate must run only PostgreSQL')
    network = json.loads(run('docker', 'network', 'inspect', NET))[0]
    if network['Internal'] is not True:
        raise ValueError('candidate network is not internal')
    for unit in ('tokenkey.service', 'tokenkey-pgdump.timer', 'tokenkey-pgdump.service'):
        if run('systemctl', 'show', unit, '-p', 'ActiveState', '--value') != 'inactive':
            raise ValueError('candidate unit not inactive: ' + unit)
    color = (R / 'active-color').read_text().strip()
    if color not in ('blue', 'green'):
        raise ValueError('invalid active color')
    pg = inspect('tokenkey-postgres')
    # Existing PG/worker are kept, so their immutable NetworkMode must resolve
    # by name after the same-named network is recreated. App/Caddy are recreated.
    for name in (('tokenkey-postgres',) if node == 'prod' else ('tokenkey-postgres', 'tokenkey-gemini-web')):
        if inspect(name)['HostConfig']['NetworkMode'] != NET:
            raise ValueError('immutable network ID requires precutover repair: ' + name)
    if node != 'prod':
        run('docker', 'image', 'inspect', 'alpine/socat:1.8.0.0')
        if not Path(__file__).with_name('sync-control-alias-on-host.sh').is_file():
            raise ValueError('control alias helper missing from prepared bundle')
    for file, names in (('docker-compose.yml', ('caddy', 'redis')),
                        ('docker-compose.bluegreen.yml', ('tokenkey-' + color,))):
        config = json.loads(compose(file, 'config', '--format', 'json'))
        if config['networks']['tokenkey-network']['name'] != NET:
            raise ValueError('compose network drift')
        for name in names:
            image = config['services'][name]['image']
            run('docker', 'image', 'inspect', image)  # no pulls in the outage window
            current = inspect('tokenkey-' + name if name in ('redis', 'caddy') else name)
            image_id = json.loads(run('docker', 'image', 'inspect', image))[0]['Id']
            if current['Image'] != image_id:
                raise ValueError('compose/runtime image drift: ' + name)
    # Standard systemd startup invokes Compose for PostgreSQL. Prove that call
    # cannot recreate it; a live PG data volume must never depend on heuristics.
    pg_cmd = ['docker', 'compose', '--env-file', str(R / '.env'), '-f', str(R / 'docker-compose.yml')]
    overlay = R / 'docker-compose.prod-pg.yml'
    if overlay.exists():
        pg_cmd += ['-f', str(overlay)]
    pg_config = json.loads(run(*pg_cmd, 'config', '--format', 'json'))
    pg_image = pg_config['services']['postgres']['image']
    if json.loads(run('docker', 'image', 'inspect', pg_image))[0]['Id'] != pg['Image']:
        raise ValueError('PostgreSQL image drift; resolve before freeze')
    desired = run(*pg_cmd, 'config', '--hash', 'postgres').split()[-1]
    actual = (pg['Config'].get('Labels') or {}).get('com.docker.compose.config-hash')
    if actual != desired:
        raise ValueError('PostgreSQL compose hash differs: prepare canonical container preserving all mounts before freeze')
    return color, network, pg


def validate_final(receipt, node, now):
    expected_source = 'tokenkey-prod-hz-cax21' if node == 'prod' else f'tokenkey-edge-{node}-hz-cax21'
    if (receipt.get('state') != 'synced' or receipt.get('final_freeze_verified') is not True or
        receipt.get('node') != node or receipt['candidate']['instance_name'] != expected_host(node) or
        receipt['source']['instance_name'] != expected_source or
        not 0 <= now - receipt['completed_at'] <= 600):
        raise ValueError('fresh matching final-freeze sync receipt required')
    if receipt.get('code', {}).get('fresh_host_runtime.py') != hashlib.sha256(Path(__file__).read_bytes()).hexdigest():
        raise ValueError('runtime changed after final sync')
    phases = {c['phase']: c for c in receipt['commands']}
    for phase, host in (('export', 'source'), ('apply', 'candidate'), ('verify-source-still-frozen', 'source')):
        item = phases.get(phase, {})
        if item.get('status') != 'Success' or item.get('target') != receipt[host]['instance_id']:
            raise ValueError('missing successful final phase: ' + phase)


def wait_ready(check_fn, seconds=90):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if check_fn():
                return
        except RuntimeError:
            pass  # A started service can refuse connections until ready; bounded by deadline.
        time.sleep(2)
    raise RuntimeError('runtime readiness deadline exceeded')


def replace_network(network, containers):
    run('docker', 'stop', '--time', '60', 'tokenkey-postgres')
    attached = [c for c in containers if NET in c['NetworkSettings']['Networks']]
    for c in attached:
        run('docker', 'network', 'disconnect', '-f', NET, c['Id'])
    run('docker', 'network', 'rm', NET)
    cmd = ['docker', 'network', 'create', '--driver', network['Driver']]
    for k, v in network.get('Options', {}).items():
        cmd += ['--opt', k + '=' + v]
    if network.get('EnableIPv6'):
        cmd += ['--ipv6']
    for k, v in (network['IPAM'].get('Options') or {}).items():
        cmd += ['--ipam-opt', k + '=' + v]
    for k, v in network.get('Labels', {}).items():
        cmd += ['--label', k + '=' + v]
    for row in network['IPAM']['Config']:
        cmd += ['--subnet', row['Subnet']]
        if row.get('Gateway'):
            cmd += ['--gateway', row['Gateway']]
    run(*cmd, NET)
    for c in attached:
        cmd = ['docker', 'network', 'connect']
        for alias in c['NetworkSettings']['Networks'][NET].get('Aliases') or []:
            cmd += ['--alias', alias]
        run(*cmd, NET, c['Id'])
    run('docker', 'start', 'tokenkey-postgres')
    wait_ready(lambda: inspect('tokenkey-postgres')['State']['Health']['Status'] == 'healthy')


def start_services(color, *, worker=True):
    # Recreate only replaceable containers. NetworkMode may hold a deleted ID;
    # network connect alone cannot fix it. PostgreSQL must NEVER enter compose up.
    compose('docker-compose.yml', 'up', '-d', '--no-deps', '--force-recreate', '--pull', 'never', 'redis', 'caddy')
    compose('docker-compose.bluegreen.yml', 'up', '-d', '--no-deps', '--force-recreate', '--pull', 'never', 'tokenkey-' + color)
    wait_ready(lambda: bool(run('docker', 'exec', 'tokenkey-' + color, 'wget', '-q', '-T', '3', '-O', '-', 'http://localhost:8080/health')))
    if worker:
        # The worker validates its control API at startup. Build the alias only
        # after the gateway is healthy, before starting the worker or systemd.
        run('bash', str(Path(__file__).with_name('sync-control-alias-on-host.sh')))
        run('docker', 'start', 'tokenkey-gemini-web')
        def worker_ready():
            if not inspect('tokenkey-gemini-web')['State']['Running']:
                raise ValueError('worker exited before readiness; inspect startup diagnostics')
            raw = run('docker', 'exec', 'tokenkey-gemini-web', 'python', '-c',
                      "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8091/readyz',timeout=3).read().decode())")
            return json.loads(raw).get('status') == 'ready'
        wait_ready(worker_ready)
    run('systemctl', 'reset-failed', 'tokenkey.service')
    run('systemctl', 'start', 'tokenkey.service')  # ExecStartPost needs the ready worker.
    wait_ready(lambda: bool(run('docker', 'exec', 'tokenkey-' + color, 'wget', '-q', '-T', '3', '-O', '-', 'http://localhost:8080/health')))
    selected, _ = resolve('tokenkey-' + color)
    if selected != 'tokenkey-' + color:
        raise RuntimeError('activated gateway is not running')
    if run('systemctl', 'is-active', 'tokenkey.service') != 'active':
        raise RuntimeError('systemd runtime not active')


def activate(node, receipt, caddy, caddy_sha, prod_ip=None):
    validate_final(receipt, node, time.time())
    color, network, pg = check(node)
    if hashlib.sha256(caddy.read_bytes()).hexdigest() != caddy_sha:
        raise ValueError('prepared Caddy checksum mismatch')
    if set(re.findall(r'reverse_proxy (tokenkey-(?:blue|green)):8080', caddy.read_text())) != {'tokenkey-' + color}:
        raise ValueError('Caddy active color differs from the prepared runtime')
    env = (R / '.env').read_text()
    if node != 'prod':
        if not prod_ip or ipaddress.ip_address(prod_ip).version != 4:
            raise ValueError('current prod IPv4 is required for edge activation')
        if not re.search(r'(?m)^\s*remote_ip .*' + re.escape(prod_ip + '/32') + r'(?:\s|$)', caddy.read_text()):
            raise ValueError('prepared Caddy does not allow the selected prod IP')
        env, count = re.subn(r'(?m)^MAIN_GATEWAY_ALLOWED_CIDR=.*$', 'MAIN_GATEWAY_ALLOWED_CIDR=' + prod_ip + '/32', env)
        if count != 1:
            raise ValueError('edge .env CIDR owner missing or ambiguous')
    run('docker', 'run', '--rm', '--network', 'none', '--entrypoint', 'caddy',
        '-v', str(caddy.resolve()) + ':/prepared:ro', inspect('tokenkey-caddy')['Image'],
        'adapt', '--config', '/prepared', '--adapter', 'caddyfile')
    state = R / 'migration' / ('activation-' + time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()))
    state.mkdir(mode=0o700)
    containers = json.loads(run('docker', 'inspect', *run('docker', 'ps', '-aq').splitlines()))
    (state / 'containers.private.json').write_text(json.dumps(containers))
    (state / 'network.json').write_text(json.dumps(network))
    (state / 'final-sync.json').write_text(json.dumps(receipt))
    (state / 'env.before.private').write_bytes((R / '.env').read_bytes())
    hold = R / 'migration/DATA_SYNC_HOLD'
    try:
        replace_network(network, containers)
        after = inspect('tokenkey-postgres')
        if pg['Id'] != after['Id'] or mounts(pg) != mounts(after):
            raise RuntimeError('PostgreSQL identity or volume changed')
        # Preserve the existing bind-mounted Caddyfile inode.
        (state / 'Caddyfile.before').write_bytes((R / 'caddy/Caddyfile').read_bytes())
        (R / 'caddy/Caddyfile').write_bytes(caddy.read_bytes())
        (R / '.env').write_text(env)
        (R / '.env').chmod(0o600)
        for name in ('migration-isolation.conf', 'migration-hold-stop.conf'):
            path = Path('/etc/systemd/system/tokenkey.service.d') / name
            if path.exists():
                # /etc and /var/lib may be different devices on EC2; rename() fails with EXDEV.
                shutil.move(str(path), str(state / name))
        run('systemctl', 'daemon-reload')
        hold.rename(state / 'DATA_SYNC_HOLD.completed')
        start_services(color, worker=node != 'prod')
        restart = ['tokenkey-' + color, 'tokenkey-caddy', 'tokenkey-redis']
        if node != 'prod':
            restart.append('tokenkey-gemini-web')
        run('docker', 'update', '--restart=unless-stopped', *restart)
        run('systemctl', 'enable', 'tokenkey.service')
        (state / 'ACTIVATED').write_text(str(time.time()))
        print(json.dumps({'activated': True, 'node': node, 'receipt': str(state)}))
    except BaseException:
        hold.touch()
        cleanup_errors = []
        # Attempt every writer stop even if an earlier cleanup command fails.
        commands = [('systemctl', 'disable', 'tokenkey.service')]
        for name in run('docker', 'ps', '-a', '--format', '{{.Names}}').splitlines():
            if name in CANDIDATES or name.startswith('tokenkey-gemini-web'):
                commands.extend([('docker', 'update', '--restart=no', name),
                                 ('docker', 'stop', '--time', '60', name)])
        for cmd in commands:
            try:
                run(*cmd)
            except RuntimeError as error:
                cleanup_errors.append(str(error))
        if cleanup_errors:
            (state / 'CLEANUP_FAILED').write_text('\n'.join(cleanup_errors))
            raise RuntimeError('activation and writer cleanup failed; urgent inspection required: ' + str(state))
        # Leave diagnostics and DB intact; never revive the archived source.
        raise


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('check', 'activate'))
    parser.add_argument('--node', required=True, choices=('prod', 'uk1', 'uk2', 'us4', 'us5'))
    parser.add_argument('--final-sync', type=Path)
    parser.add_argument('--caddy', type=Path)
    parser.add_argument('--caddy-sha256')
    parser.add_argument('--prod-ip', help='current prod egress IPv4; required for edges')
    args = parser.parse_args()
    if args.operation == 'check':
        color, _, pg = check(args.node)
        print(json.dumps({'runtime_check': 'passed', 'active_color': color, 'pg_id': pg['Id']}))
    else:
        if not all((args.final_sync, args.caddy, args.caddy_sha256, args.prod_ip)):
            parser.error('activate requires final-sync receipt and prepared Caddy path/hash')
        import fcntl
        with (R / 'migration/sync-session.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            activate(args.node, json.loads(args.final_sync.read_text()), args.caddy, args.caddy_sha256, args.prod_ip)


if __name__ == '__main__':
    main()
