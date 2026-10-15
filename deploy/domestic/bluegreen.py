#!/usr/bin/env python3
"""cn-prod SSH host adapter for the shared Stage0 cutover/drain primitives.

Run on cn-prod as root. prepare never changes the public route. promote requires
the exact private receipt hash, rechecks container/config identities, then uses
the shared Caddy transaction and verified drain. No database or Redis recreation.
"""
import argparse
import copy
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess


ROOT = Path('/opt/tokenkey')
DATA = Path('/var/lib/tokenkey')
DOMAIN = 'api-cn.tokenkey.dev'
FUNCTIONS = (
    'log', 'die', 'record_cutover', 'color_container', 'container_health',
    'wait_healthy', 'wait_ready', 'drain_container', 'render_caddy_with_upstream',
    'preserve_target_cutover', 'preserve_unconfirmed_reload', 'commit_cutover',
    'write_active_color', 'observe_routed_health',
)


def run(*args):
    result = subprocess.run(args, capture_output=True, text=True)
    if result.returncode:
        # Docker/Compose diagnostics can include resolved environment values.
        raise RuntimeError(f'{args[0]} failed (exit {result.returncode}); inspect private host logs')
    return result.stdout


def inspect(name):
    return json.loads(run('docker', 'inspect', name))[0]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_private(path, value):
    temporary = path.with_suffix(path.suffix + '.new')
    with temporary.open('w') as stream:
        os.chmod(temporary, 0o600)
        json.dump(value, stream, indent=2)
    temporary.replace(path)


def active_container(caddy):
    routes = re.findall(r'^\s*reverse_proxy\s+(tokenkey(?:-blue|-green)?):8080\s*\{', caddy, re.M)
    if len(routes) != 1:
        raise ValueError('expected exactly one domestic application upstream')
    if DOMAIN + ' {' not in caddy:
        raise ValueError('refusing a non-domestic Caddy configuration')
    return routes[0]


def identity(container):
    return {key: container[key] for key in ('Id', 'Image', 'RestartCount')} | {
        'StartedAt': container['State']['StartedAt'],
        'runtime_hash': hashlib.sha256(json.dumps({
            'Config': container['Config'], 'HostConfig': container['HostConfig'],
        }, sort_keys=True).encode()).hexdigest(),
    }


def candidate_compose(service, current, image, target):
    env = dict(item.split('=', 1) for item in current['Config']['Env'])
    if env.get('SERVER_FRONTEND_URL', '').rstrip('/') != 'https://' + DOMAIN:
        raise ValueError('refusing non-cn-prod application environment')
    if env.get('QA_CAPTURE_ENABLED', '').lower() != 'false':
        raise ValueError('domestic QA capture must remain disabled')
    networks = current['NetworkSettings']['Networks']
    if len(networks) != 1:
        raise ValueError('expected one existing private application network')
    candidate = copy.deepcopy(service)
    for key in ('depends_on', 'ports', 'container_name', 'networks', 'labels'):
        candidate.pop(key, None)
    candidate.update(image=image, container_name=target, pull_policy='never',
                     environment=env, networks=['existing'])
    # Runtime environment may contain literal dollar signs. Compose must not
    # reinterpret any existing secret/config value as a new interpolation.
    candidate['environment'] = {k: v.replace('$', '$$') for k, v in env.items()}
    candidate['ports'] = [{'target': 8080, 'published': '18081' if target.endswith('blue') else '18082',
                           'host_ip': '127.0.0.1', 'protocol': 'tcp'}]
    candidate['restart'] = 'unless-stopped'
    return {'services': {target: candidate}, 'networks': {
        'existing': {'external': True, 'name': next(iter(networks))},
    }}


def shared_library(source):
    text = source.read_text()
    functions = []
    for name in FUNCTIONS:
        matches = re.findall(r'^' + name + r'\(\) \{\n.*?^\}', text, re.M | re.S)
        if len(matches) != 1:
            raise ValueError('shared Stage0 function missing or ambiguous: ' + name)
        functions.append(matches[0])
    # cn-prod has no Gemini worker or control alias. Refuse one rather than
    # running the overseas worker/network adaptation on this host.
    functions.append('sync_gemini_web_control_alias() { ! docker inspect tokenkey-gemini-web >/dev/null 2>&1; }')
    functions.append('env_get() { [[ "$1" = API_DOMAIN ]] && echo api-cn.tokenkey.dev; }')
    return '\n'.join(functions)


def shell(state, body):
    library = state / 'stage0-functions.sh'
    prefix = '\n'.join(f'{k}={shlex.quote(str(v))}' for k, v in {
        'ROOT': DATA, 'ACTIVE_FILE': DATA / 'active-color',
        'CADDY_DIR': DATA / 'caddy', 'LIVE_CADDY': DATA / 'caddy/Caddyfile',
        'DEPLOY_TOKEN': state.name, 'TAG': state.name,
        'ROUTE_SWITCHED': 0, 'CUTOVER_COMMITTED': 0,
    }.items())
    subprocess.run(['bash', '-c', 'set -euo pipefail\n' + prefix + '\nsource ' +
                    shlex.quote(str(library)) + '\n' + body], check=True)


def prepare(args):
    state = args.state
    if state.exists():
        raise ValueError('release state already exists; inspect it before retrying')
    caddy = DATA / 'caddy/Caddyfile'
    old = active_container(caddy.read_text())
    current = inspect(old)
    if current['State'].get('Health', {}).get('Status') != 'healthy':
        raise ValueError('current application is not healthy')
    available = int(re.search(r'^MemAvailable:\s+(\d+)', Path('/proc/meminfo').read_text(), re.M)[1]) * 1024
    if available < 1536 * 1024 * 1024:
        raise ValueError('less than 1.5 GiB available for the temporary second domestic process')
    image = inspect(args.image)
    if image['Architecture'] != 'amd64' or image['Config']['Labels'].get('org.opencontainers.image.revision') != args.revision:
        raise ValueError('candidate architecture/revision does not match reviewed artifact')
    target = 'tokenkey-green' if old == 'tokenkey-blue' else 'tokenkey-blue'
    existing = run('docker', 'ps', '-a', '--filter', 'name=^/' + target + '$', '--format', '{{.ID}}').strip()
    if existing:
        raise ValueError('candidate name already exists; preserve it for explicit inspection')
    service = json.loads(run('docker', 'compose', '--project-directory', str(ROOT),
                            '-f', str(ROOT / 'compose.yml'), '-f', str(ROOT / 'compose.override.yml'),
                            '-f', str(ROOT / 'compose.local.yml'), 'config', '--format', 'json'))['services']['tokenkey']
    compose = candidate_compose(service, current, image['Id'], target)
    state.mkdir(mode=0o700, parents=True)
    write_private(state / 'compose.json', compose)
    library = state / 'stage0-functions.sh'
    library.write_text(shared_library(args.shared_source))
    library.chmod(0o600)
    (state / 'Caddyfile.before').write_text(caddy.read_text())
    receipt = {'old': old, 'old_identity': identity(current), 'target': target,
               'image_id': image['Id'], 'revision': args.revision,
               'caddy_hash': digest(caddy), 'compose_hash': digest(state / 'compose.json'),
               'library_hash': digest(library)}
    write_private(state / 'before.json', receipt)
    run('docker', 'compose', '-p', 'cn-prod-bluegreen', '-f', str(state / 'compose.json'),
        'up', '-d', '--no-deps', target)
    shell(state, f'wait_healthy {target}\nwait_ready {target}')
    receipt['target_identity'] = identity(inspect(target))
    write_private(state / 'prepared.json', receipt)
    print(json.dumps({'stage': 'prepared', 'target': target, 'revision': args.revision,
                      'receipt_sha256': digest(state / 'prepared.json')}))


def verify_receipt(state, expected):
    if digest(state / 'prepared.json') != expected:
        raise ValueError('prepared receipt changed')
    receipt = json.loads((state / 'prepared.json').read_text())
    for path, key in ((DATA / 'caddy/Caddyfile', 'caddy_hash'),
                      (state / 'compose.json', 'compose_hash'),
                      (state / 'stage0-functions.sh', 'library_hash')):
        if digest(path) != receipt[key]:
            raise ValueError('prepared configuration changed')
    for name, key in ((receipt['old'], 'old_identity'), (receipt['target'], 'target_identity')):
        if identity(inspect(name)) != receipt[key]:
            raise ValueError('prepared container identity/configuration changed')
    return receipt


def promote(args):
    state = args.state
    receipt = verify_receipt(state, args.receipt)
    target, old = receipt['target'], receipt['old']
    color = target.removeprefix('tokenkey-')
    # The shared validation primitive uses this local image name. Bind it to
    # the running pinned Caddy image; no registry/network pull is permitted.
    run('docker', 'tag', inspect('tokenkey-caddy')['Image'], 'caddy:2-alpine')
    shell(state, f'wait_healthy {target}\nwait_ready {target}\n'
          f'commit_cutover {color}\nobserve_routed_health {color}\n'
          f'drain_container {old}\ndocker stop -t 180 {old} >/dev/null')
    write_private(state / 'completed.json', receipt | {'stage': 'complete'})
    print(json.dumps({'stage': 'complete', 'active': target, 'previous': old,
                      'revision': receipt['revision']}))


def rollback(args):
    state = args.state
    if digest(state / 'prepared.json') != args.receipt:
        raise ValueError('prepared receipt changed')
    receipt = json.loads((state / 'prepared.json').read_text())
    old, target = receipt['old'], receipt['target']
    caddy = DATA / 'caddy/Caddyfile'
    before = state / 'Caddyfile.before'
    if digest(before) != receipt['caddy_hash'] or digest(state / 'stage0-functions.sh') != receipt['library_hash']:
        raise ValueError('rollback configuration or shared implementation changed')
    if active_container(caddy.read_text()) != target:
        raise ValueError('public route no longer points to this release')
    normalized = caddy.read_text().replace('reverse_proxy ' + target + ':8080', 'reverse_proxy ' + old + ':8080')
    if normalized != before.read_text():
        raise ValueError('Caddy changed since release; refusing to overwrite it')
    for name, key in ((old, 'old_identity'), (target, 'target_identity')):
        actual = inspect(name)
        if any(actual[k] != receipt[key][k] for k in ('Id', 'Image')):
            raise ValueError('rollback container replaced')
    # Starting the preserved process clears its SIGUSR1 drain state. Keep the
    # new process available until the old route is confirmed healthy.
    run('docker', 'start', old)
    shell(state, f'wait_healthy {old}\nwait_ready {old}')
    current = caddy.read_text()
    caddy.write_text(before.read_text())
    try:
        run('docker', 'exec', 'tokenkey-caddy', 'caddy', 'reload', '--config', '/etc/caddy/Caddyfile', '--adapter', 'caddyfile')
    except RuntimeError:
        caddy.write_text(current)
        run('docker', 'exec', 'tokenkey-caddy', 'caddy', 'reload', '--config', '/etc/caddy/Caddyfile', '--adapter', 'caddyfile')
        raise
    if old == 'tokenkey':
        (DATA / 'active-color').unlink(missing_ok=True)
    else:
        (DATA / 'active-color').write_text(old.removeprefix('tokenkey-') + '\n')
    shell(state, f'observe_routed_health {shlex.quote(old)}\ndrain_container {target}\ndocker stop -t 180 {target} >/dev/null')
    write_private(state / 'rolled-back.json', {'active': old, 'previous': target})
    print(json.dumps({'stage': 'rolled-back', 'active': old}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('prepare', 'promote', 'rollback'))
    parser.add_argument('--state', type=Path, required=True)
    parser.add_argument('--image')
    parser.add_argument('--revision')
    parser.add_argument('--shared-source', type=Path)
    parser.add_argument('--receipt')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('run only on cn-prod as root')
    args.state = args.state.resolve()
    if args.state.parent != ROOT / 'releases' or not re.fullmatch(r'[a-zA-Z0-9_.-]+', args.state.name):
        parser.error('state must be one release directory under /opt/tokenkey/releases')
    if args.action == 'prepare' and not all((args.image, args.revision, args.shared_source)):
        parser.error('prepare requires image, revision and shared-source')
    if args.action in ('promote', 'rollback') and not re.fullmatch(r'[a-f0-9]{64}', args.receipt or ''):
        parser.error('promote/rollback requires exact prepared receipt SHA256')
    os.umask(0o077)
    run('mountpoint', '-q', str(DATA))
    with (DATA / 'bluegreen-deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        {'prepare': prepare, 'promote': promote, 'rollback': rollback}[args.action](args)


if __name__ == '__main__':
    main()
