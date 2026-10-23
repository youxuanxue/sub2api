"""Domestic post-cutover verification and the shared Feishu release sender.

Runs on the domestic host. No AWS target resolution and no second webhook sender.
"""
from datetime import datetime
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import time
from urllib.parse import urlsplit

ASSETS = {
    'prod-target.json': 'deploy/domestic/prod-target.json',
    'release_post_check.py': 'scripts/release_post_check.py',
    'probe-post-release-tick.sh': 'ops/observability/probe-post-release-tick.sh',
    'resolve_app_container.py': 'ops/lib/resolve_app_container.py',
    'notify-feishu-release.sh': 'ops/stage0/notify-feishu-release.sh',
}


def execute(command, *, env=None):
    result = subprocess.run(command, env=env, capture_output=True, text=True)
    if result.returncode:
        # Never surface child stderr: notification diagnostics may contain secrets.
        raise RuntimeError('domestic release verification command failed: ' + Path(command[0]).name)
    return result.stdout


def read_json(path):
    return json.loads(path.read_text())


def save(path, data):
    temporary = path.with_suffix(path.suffix + '.new')
    temporary.write_text(json.dumps(data, indent=2))
    temporary.chmod(0o600)
    temporary.replace(path)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def timestamp(value):
    parsed = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if parsed.tzinfo is None:
        raise ValueError('cutover time must include timezone')
    return parsed.timestamp()


def asset_source(name):
    adjacent = Path(__file__).with_name(name)
    return adjacent if adjacent.is_file() else Path(__file__).resolve().parents[2] / ASSETS[name]


def configure(args, receipt, inspect):
    """Validate and pin all inputs BEFORE touching the public route."""
    url = urlsplit(args.public_url)
    if url.scheme != 'https' or url.username or url.password or url.query or url.fragment or url.path not in ('', '/'):
        raise ValueError('public-url must be an HTTPS origin')
    # public_origin is the checked entrance. A domain that points at US prod,
    # or any other global IP, cannot stand in for that check.
    if not ipaddress.ip_address(url.hostname).is_global or url.port not in (None, 443):
        raise ValueError('domestic public-url must use its public static IP on 443')
    domestic = read_json(asset_source('prod-target.json'))
    if args.public_url.rstrip('/') != domestic['public_origin'].rstrip('/'):
        raise ValueError(
            'public-url does not match the domestic target manifest public_origin '
            + domestic['public_origin'].rstrip('/'))
    old, target = inspect(receipt['old']), inspect(receipt['target'])
    labels = [item['Config']['Labels'] for item in (old, target)]
    versions = [item['org.opencontainers.image.version'].removeprefix('v') for item in labels]
    if not all(re.fullmatch(r'\d+\.\d+\.\d+(?:-(?:rc|beta)\.\d+)?', v) for v in versions):
        raise ValueError('invalid image version labels')
    plan = read_json(args.postcheck_plan)
    expected = {'live': labels[0]['org.opencontainers.image.revision'], 'new': receipt['revision']}
    if plan.get('range') != expected or labels[1]['org.opencontainers.image.revision'] != expected['new']:
        raise ValueError('postcheck plan does not match live/candidate revisions')
    smoke = read_json(args.smoke_report)
    if (smoke.get('revision') != expected['new'] or smoke.get('container_id') != target['Id']
            or not smoke.get('cases') or not all(case.get('passed') is True for case in smoke['cases'])):
        raise ValueError('candidate smoke evidence must pass and match this container/revision')
    directory = args.state / 'post-release'
    directory.mkdir(mode=0o700, exist_ok=True)
    context_file = directory / 'context.json'
    context = {'public_url': args.public_url.rstrip('/'), 'previous_version': versions[0],
               'version': versions[1], 'range': expected, 'container_id': target['Id']}
    if context_file.exists():
        previous = read_json(context_file)
        if any(previous.get(k) != v for k, v in context.items()):
            raise ValueError('release notification context changed')
        verify_assets(directory, previous)
        return previous
    for name in ASSETS:
        shutil.copyfile(asset_source(name), directory / name)
    save(directory / 'plan.json', plan)
    save(directory / 'smoke.json', smoke)
    context['hashes'] = {name: sha(directory / name) for name in (*ASSETS, 'plan.json', 'smoke.json')}
    save(context_file, context)
    return context


def verify_assets(directory, context):
    for name, expected in context['hashes'].items():
        if sha(directory / name) != expected:
            raise ValueError('pinned post-release input changed: ' + name)


def verify_active(state, data, receipt, context, inspect, active_container):
    current = inspect(receipt['target'])
    if (current['Id'] != context['container_id'] or current['Image'] != receipt['image_id']
            or not current['State'].get('Running')
            or current['State'].get('Health', {}).get('Status') != 'healthy'
            or active_container((data / 'caddy/Caddyfile').read_text()) != receipt['target']):
        raise ValueError('this release is not the healthy active domestic deployment')
    cutover = read_json(data / ('bluegreen-cutover-' + state.name + '.json'))
    if cutover.get('token') != state.name or cutover.get('tag') != state.name:
        raise ValueError('cutover identity mismatch')
    if timestamp(cutover['cutover_at']) > time.time() + 5:
        raise ValueError('cutover timestamp is in the future')
    return cutover['cutover_at']


def credentials(path):
    mode = path.stat()
    if mode.st_uid != 0 or mode.st_mode & 0o077:
        raise ValueError('host alert credentials must be root-owned and private')
    values = {}
    for line in path.read_text().splitlines():
        name, sep, value = line.partition('=')
        if sep and name in ('TOKENKEY_FEISHU_WEBHOOK_URL', 'TOKENKEY_FEISHU_WEBHOOK_SECRET'):
            values[name] = value.strip().strip('"').strip("'")
    return {'TK_FEISHU_WEBHOOK_URL': values.get('TOKENKEY_FEISHU_WEBHOOK_URL', ''),
            'TK_FEISHU_SIGNING_SECRET': values.get('TOKENKEY_FEISHU_WEBHOOK_SECRET', '')}


def finish(args, receipt, context, *, data, inspect, active_container):
    directory = args.state / 'post-release'
    verify_assets(directory, context)
    cutover_at = verify_active(args.state, data, receipt, context, inspect, active_container)
    notice = directory / 'notification.json'
    if notice.exists() and read_json(notice).get('status') == 'sent':
        print('cn-prod release notification already sent; not duplicated')
        return
    planner = ['python3', str(directory / 'release_post_check.py')]
    plan_args = ['--plan-file', str(directory / 'plan.json')]
    env = os.environ.copy()
    env.update(SINCE=cutover_at, CONTAINER=receipt['target'],
               HOOK_PATTERNS=execute(planner + ['hook-patterns', *plan_args]).strip(),
               TRAFFIC_PATHS=execute(planner + ['traffic-paths', *plan_args]).strip())
    for phase in ('immediate', 'delayed'):
        if phase == 'delayed':
            while (remaining := timestamp(cutover_at) + 300 - time.time()) > 0:
                time.sleep(min(remaining, 30))
        verify_active(args.state, data, receipt, context, inspect, active_container)
        health = execute(['curl', '--noproxy', '*', '--fail', '--silent', '--show-error',
                 '--max-time', '15', context['public_url'] + '/health'])
        if json.loads(health).get('status') != 'ok':
            raise ValueError('public health response is not healthy')
        tick = execute(['bash', str(directory / 'probe-post-release-tick.sh')], env=env)
        tick_file = directory / ('tick-' + phase + '.txt')
        tick_file.write_text(tick)
        evaluation = execute(planner + ['evaluate', *plan_args, '--tick-file', str(tick_file),
                                        '--control-plane-ok', 'true', '--phase', phase])
        save(directory / ('evaluation-' + phase + '.json'), json.loads(evaluation))
    evaluations = ['--evaluation-file', str(directory / 'evaluation-delayed.json'),
                   '--immediate-evaluation-file', str(directory / 'evaluation-immediate.json')]
    execute(planner + ['gate', *evaluations])
    (directory / 'summary.md').write_text(execute(planner + ['summary', *evaluations]))
    # Sending is best-effort and never rolls back a healthy deployment. A failed
    # or uncertain delivery can be retried via notify, without repeating cutover.
    try:
        verify_active(args.state, data, receipt, context, inspect, active_container)
        env.update(credentials(Path('/opt/tokenkey/host-alerts.env')))
        env['GITHUB_REPOSITORY'] = 'youxuanxue/sub2api'
        notes = '\n'.join(change['subject'] for change in read_json(directory / 'plan.json')['changes'])
        output = execute(['bash', str(directory / 'notify-feishu-release.sh'), context['version'],
                          context['public_url'], '--previous-tag', context['previous_version'],
                          '--environment', 'cn-prod', '--deployed-at', cutover_at,
                          '--notes', notes, '--result-file', str(notice)], env=env)
        print(output.strip())
        if not notice.exists():
            save(notice, {'status': 'failed', 'reason': 'sender produced no receipt'})
    except Exception:
        save(notice, {'status': 'failed', 'reason': 'notification setup or transport failed'})
        print('::warning::cn-prod release healthy; Feishu delivery failed (retry with notify)')
