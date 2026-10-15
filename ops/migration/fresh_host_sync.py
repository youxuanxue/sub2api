#!/usr/bin/env python3
"""Stage a precious+logs baseline before downtime, then synchronize without manual gaps.

Never pauses accounts, freezes a source, activates a candidate, or changes routing.
--final only accepts an already frozen source. Preparation expires after 30 minutes;
the delta engine still rechecks the full candidate baseline before each transaction.
"""
import argparse
import base64
import fcntl
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import tarfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
WORK = '/var/lib/tokenkey/migration'
CODE = {n: ROOT / 'ops/migration' / n for n in ('fresh_host_delta.py', 'standard_freeze_delta.py', 'oltp_foreign_keys.py', 'fresh_host_runtime.py')}
CODE['resolve_app_container.py'] = ROOT / 'ops/lib/resolve_app_container.py'
CODE['sync-control-alias-on-host.sh'] = ROOT / 'ops/gemini-web/sync-control-alias-on-host.sh'
FILES = tuple(CODE)
SCOPES = ('precious', 'logs')


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def save(path, data):
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(data, indent=2) + '\n')
    temp.chmod(0o600)
    temp.replace(path)


def aws(region, *args):
    result = subprocess.run(['aws', '--region', region, *args, '--output', 'json'],
                            capture_output=True, text=True, timeout=45)
    if result.returncode:
        if 'InvocationDoesNotExist' in result.stderr:
            return None
        raise RuntimeError('AWS call failed: ' + args[0] + '/' + args[1])
    return json.loads(result.stdout)


def remote_command(script):
    payload = base64.b64encode(gzip.compress(script.encode())).decode()
    command = ("set -eu; umask 077; sync_script=$(mktemp /tmp/tokenkey-sync-XXXXXX.sh); "
            "trap 'rm -f \"$sync_script\"' EXIT; printf '%s' '" + payload +
            "' | base64 -d | gzip -d > \"$sync_script\"; bash \"$sync_script\"")

    # Bound request size independently of SSM's truncated stdout limit; leave
    # room for the RunShellScript JSON document around the encoded script.
    if len(command.encode()) > 48 * 1024:
        raise ValueError('SSM script exceeds migration transport budget')
    return command


def execute(receipt, path, phase, host, script):
    command = aws(host['region'], 'ssm', 'send-command', '--document-name', 'AWS-RunShellScript',
                  '--instance-ids', host['instance_id'], '--parameters',
                  json.dumps({'commands': [remote_command(script)], 'executionTimeout': ['900']}))
    entry = {'phase': phase, 'target': host['instance_id'], 'region': host['region'],
             'command_id': command['Command']['CommandId'], 'status': 'sent'}
    receipt['commands'].append(entry)
    save(path, receipt)  # Persist the ID before polling: never blindly resend on a client timeout.
    print(json.dumps(entry), flush=True)
    deadline = time.monotonic() + 960
    while time.monotonic() < deadline:
        result = aws(host['region'], 'ssm', 'get-command-invocation',
                     '--command-id', entry['command_id'], '--instance-id', host['instance_id'])
        if result and result['Status'] not in ('Pending', 'InProgress', 'Delayed'):
            entry.update(status=result['Status'], elapsed=result.get('ExecutionElapsedTime'),
                         output=result.get('StandardOutputContent', ''),
                         error=result.get('StandardErrorContent', ''))
            save(path, receipt)
            if result['Status'] != 'Success' or result['ResponseCode'] != 0:
                raise RuntimeError(f'{phase} failed; keep candidate held; inspect private receipt {path}')
            print(json.dumps({'phase': phase, 'status': 'Success', 'elapsed': entry['elapsed']}), flush=True)
            return
        time.sleep(2)
    raise RuntimeError('SSM polling deadline; command may still be running; inspect saved ID, do not resend')


def resolve(node):
    inventory = json.loads((ROOT / 'deploy/aws/fresh-migration-20261015.json').read_text())
    candidate = inventory['prod'] if node == 'prod' else inventory['edges'][node]
    if node != 'prod':
        import sys
        sys.path.insert(0, str(ROOT / 'ops/stage0'))
        from edge_routing_matrix import resolve_route_tab
        if resolve_route_tab(ROOT, node)[0] != 'hetzner':
            raise ValueError('candidate is already active; sync forbidden')
    source_name = 'tokenkey-prod-hz-cax21' if node == 'prod' else f'tokenkey-edge-{node}-hz-cax21'
    rows = aws('eu-west-2', 'ssm', 'describe-instance-information')['InstanceInformationList']
    matches = [r for r in rows if r.get('ComputerName') == source_name and r['PingStatus'] == 'Online']
    if len(matches) != 1:
        raise ValueError('expected exactly one Online source identity')
    return candidate, {'instance_name': source_name, 'instance_id': matches[0]['InstanceId'], 'region': 'eu-west-2'}


def header(host):
    return f'''set -euo pipefail
umask 077
[ "$(hostname)" = {shlex.quote(host['instance_name'])} ]
mkdir -p {WORK}
exec 9>{WORK}/sync-session.lock
flock -n 9
'''


def held():
    return f'''[ -f {WORK}/DATA_SYNC_HOLD ]
[ "$(docker ps --format '{{{{.Names}}}}')" = tokenkey-postgres ]
[ "$(docker network inspect tokenkey_tokenkey-network --format '{{{{.Internal}}}}')" = true ]
'''


def frozen(node):
    return f'''[ -f {WORK}/{node}-cutover/FROZEN ]
[ -f {WORK}/{node}-cutover/ARCHIVAL_HOLD ]
python3 - <<'CHECK'
import subprocess
names=set(subprocess.check_output(['docker','ps','--format','{{{{.Names}}}}'],text=True).splitlines())
assert names <= {{'tokenkey-postgres','tokenkey-redis','tokenkey-caddy'}}, 'source writer running'
CHECK
[ "$(systemctl show tokenkey.service -p ActiveState --value)" = inactive ]
[ "$(systemctl show tokenkey-pgdump.timer -p ActiveState --value)" = inactive ]
[ "$(docker exec tokenkey-postgres psql -XAt -U tokenkey -d tokenkey -c 'SHOW transaction_read_only')" = on ]
[ "$(docker exec tokenkey-postgres psql -XAt -U tokenkey -d tokenkey -c "SELECT count(*) FROM pg_stat_activity WHERE datname='tokenkey' AND pid<>pg_backend_pid() AND backend_type='client backend'")" = 0 ]
'''


def code_check(receipt):
    return 'cd ' + shlex.quote(receipt['remote']) + '\nsha256sum -c code.sha256\n'


def image_report(node):
    return """python3 - <<'IMAGES'
import json, pathlib, subprocess
color=pathlib.Path('/var/lib/tokenkey/active-color').read_text().strip()
assert color in ('blue','green')
from resolve_app_container import resolve
if pathlib.Path('/var/lib/tokenkey/migration/DATA_SYNC_HOLD').is_file():
    names=['tokenkey-'+color]
else:
    app, _ = resolve()
    assert app, 'source gateway is not running'
    names=[app]
""" + ("names.append('tokenkey-gemini-web')\n" if node != 'prod' else '') + """images=[subprocess.check_output(['docker','inspect',n,'--format','{{.Config.Image}}'],text=True).strip() for n in names]
print('IMAGES='+json.dumps(images))
IMAGES
"""


def prepare(receipt, path):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode='w:gz') as tar:
        for name in FILES:
            tar.add(CODE[name], arcname=name)
    encoded = base64.b64encode(buf.getvalue()).decode()
    sha = hashlib.sha256(buf.getvalue()).hexdigest()
    checks = '\n'.join(f'{receipt["code"][n]}  {n}' for n in FILES) + '\n'
    install = (f'mkdir {receipt["remote"]}\ncd {receipt["remote"]}\n'
               f"printf '%s' '{encoded}' | base64 -d > code.tar.gz\n"
               f"printf '%s\\n' '{sha}  code.tar.gz' | sha256sum -c -\n"
               'tar xzf code.tar.gz\n'
               f"printf %s {shlex.quote(checks)} > code.sha256\nsha256sum -c code.sha256\n")
    alias_image = '' if receipt['node'] == 'prod' else 'docker image inspect alpine/socat:1.8.0.0 >/dev/null 2>&1 || docker pull alpine/socat:1.8.0.0 >/dev/null\n'
    script = header(receipt['candidate']) + held() + install + alias_image + f'python3 fresh_host_runtime.py check --node {receipt["node"]}\n' + image_report(receipt['node'])
    for scope in SCOPES:
        script += (f'/usr/bin/time -f BASELINE_{scope}_SECONDS=%e python3 fresh_host_delta.py manifest '
                   f'--scope {scope} --output {scope}-base.json\n'
                   f'aws --region us-east-1 s3 cp --only-show-errors {scope}-base.json {receipt["uri"]}/{scope}-base.json\n')
    script += held()
    execute(receipt, path, 'prepare-candidate', receipt['candidate'], script)
    script = header(receipt['source']) + install + image_report(receipt['node'])
    for scope in SCOPES:
        script += f'aws --region us-east-1 s3 cp --only-show-errors {receipt["uri"]}/{scope}-base.json {scope}-base.json\n'
    execute(receipt, path, 'prepare-source', receipt['source'], script)
    images = [next(line[7:] for line in c['output'].splitlines() if line.startswith('IMAGES='))
              for c in receipt['commands'] if c['phase'].startswith('prepare-')]
    if len(images) != 2 or images[0] != images[1]:
        raise ValueError('source/candidate gateway or worker image mismatch; remain held')
    receipt.update(state='prepared', prepared_at=time.time(), images=json.loads(images[0]))
    save(path, receipt)


def validate_prepared(receipt, candidate, source, now):
    if receipt['state'] != 'prepared' or not 0 <= now - receipt['prepared_at'] <= 1800:
        raise ValueError('preparation used, incomplete or expired; prepare a new baseline')
    if receipt['candidate'] != candidate or receipt['source'] != source:
        raise ValueError('source/candidate identity changed')
    if receipt['code'] != {n: digest(CODE[n]) for n in FILES}:
        raise ValueError('migration code changed; prepare again')


def sync(receipt, path, final):
    candidate, source = resolve(receipt['node'])
    validate_prepared(receipt, candidate, source, time.time())
    receipt.update(state='syncing', final_requested=final)
    save(path, receipt)  # Single use; failed/uncertain apply cannot be replayed automatically.
    guard = frozen(receipt['node']) if final else ''
    script = header(source) + guard + code_check(receipt)
    for scope in SCOPES:
        script += (f'/usr/bin/time -f EXPORT_{scope}_SECONDS=%e nice -n 10 python3 fresh_host_delta.py export '
                   f'--scope {scope} --base {scope}-base.json --output {scope}\n')
    script += guard + 'tar cf bundle.tar precious logs\nsha256sum bundle.tar > bundle.tar.sha256\n'
    for name in ('bundle.tar', 'bundle.tar.sha256'):
        script += f'aws --region us-east-1 s3 cp --only-show-errors {name} {receipt["uri"]}/{name}\n'
    execute(receipt, path, 'export', source, script)
    script = header(candidate) + held() + code_check(receipt)
    for name in ('bundle.tar', 'bundle.tar.sha256'):
        script += f'aws --region us-east-1 s3 cp --only-show-errors {receipt["uri"]}/{name} {name}\n'
    script += 'sha256sum -c bundle.tar.sha256\ntar xf bundle.tar\n'
    for scope in SCOPES:
        script += (f'/usr/bin/time -f APPLY_{scope}_SECONDS=%e python3 fresh_host_delta.py apply '
                   f'--scope {scope} --bundle {scope} --expected-candidate {shlex.quote(candidate["instance_name"])}\n')
    script += held()
    execute(receipt, path, 'apply', candidate, script)
    if final:
        execute(receipt, path, 'verify-source-still-frozen', source, header(source) + guard)
    receipt.update(state='synced', final_freeze_verified=final, completed_at=time.time())
    save(path, receipt)


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('prepare', 'sync'))
    parser.add_argument('--node', choices=('prod', 'uk1', 'uk2', 'us4', 'us5'), required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    parser.add_argument('--final', action='store_true')
    args = parser.parse_args()
    if args.final and args.operation != 'sync':
        parser.error('--final requires sync and an already frozen source')
    with args.receipt.with_suffix('.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.operation == 'prepare':
            if args.receipt.exists():
                parser.error('receipt already exists; choose a new path')
            candidate, source = resolve(args.node)
            batch = f'sync-{args.node}-' + time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()) + '-' + uuid.uuid4().hex[:8]
            prefix = 'prod' if args.node == 'prod' else 'edge/' + args.node
            receipt = {'node': args.node, 'candidate': candidate, 'source': source,
                       'remote': WORK + '/' + batch,
                       'uri': f's3://tokenkey-prod-pgdump-682751977094/{prefix}/pgdump/migration-pre-sync/aws-20261015/{batch}',
                       'code': {n: digest(CODE[n]) for n in FILES},
                       'commands': [], 'state': 'preparing', 'final_freeze_verified': False}
            save(args.receipt, receipt)
            prepare(receipt, args.receipt)
        else:
            receipt = json.loads(args.receipt.read_text())
            if receipt['node'] != args.node:
                parser.error('receipt node mismatch')
            sync(receipt, args.receipt, args.final)
        print(json.dumps({'state': receipt['state'], 'final_freeze_verified': receipt['final_freeze_verified']}))


if __name__ == '__main__':
    main()
