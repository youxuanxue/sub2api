#!/usr/bin/env python3
"""Run history API/UI rehearsal on a credential-disabled clone, without egress.

The restored baseline stays untouched. No production activation exists here.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import time

if __package__:
    from .node1_candidate import CONTAINER, Policy
else:
    from node1_candidate import CONTAINER, Policy

WORK = Path('/data/tokenkey-node1-ucloud-cn-history-rehearsal')
DATABASE = 'tokenkey_ucloud_cn_history_rehearsal'
APP = 'tokenkey-node1-ucloud-cn-history-app'
REDIS = 'tokenkey-node1-ucloud-cn-history-redis'
APP_IMAGE = 'sha256:a973706d7dc5e63cf349c6d3067fc83e106c07c8ddc7d2faa12360cdc396069a'
REDIS_IMAGE = 'sha256:19918c2d80d003158a26d210099b31ae6434b662afe16d560bcdeb3643909494'
LABEL = 'tokenkey.rehearsal=node1-ucloud-cn-history'


def run(*args, data=None):
    p = subprocess.run(args, input=data, capture_output=True)
    if p.returncode:
        raise RuntimeError('rehearsal command failed (private output withheld): ' + args[0])
    return p.stdout


def inspect(name):
    return json.loads(run('docker', 'inspect', name))[0]


def sql(query, database=DATABASE):
    return run('docker', 'exec', '-i', CONTAINER, 'psql', '-XqAt',
               '-v', 'ON_ERROR_STOP=1', '-U', 'tokenkey', '-d', database,
               data=query.encode()).decode().strip()


def validate(info, pg_id):
    h = info['HostConfig']
    expected_images = {'/' + APP: APP_IMAGE, '/' + REDIS: REDIS_IMAGE}
    if (info['Name'] not in ('/' + APP, '/' + REDIS)
            or info.get('Image') != expected_images.get(info['Name'])
            or info['Config']['Labels'].get('tokenkey.rehearsal') != LABEL.split('=', 1)[1]
            or h['NetworkMode'] != 'container:' + pg_id or h.get('PortBindings')
            or h.get('Privileged') or h.get('CapAdd')
            or h['RestartPolicy']['Name'] != 'no'):
        raise RuntimeError('history rehearsal isolation mismatch')
    for m in info['Mounts']:
        if m['Type'] == 'tmpfs' and m['Destination'] == '/data':
            continue
        if m['Source'] != str(WORK / 'app') or m['Destination'] != '/app/data':
            raise RuntimeError('unexpected rehearsal mount')


HTTP = '''import json,sys,urllib.request,urllib.error
p=json.load(sys.stdin)
r=urllib.request.Request('http://127.0.0.1:8080'+p['path'],
 data=json.dumps(p['body']).encode() if 'body' in p else None,
 headers=p.get('headers',{}))
try:
 with urllib.request.urlopen(r,timeout=30) as o:
  print(json.dumps({'status':o.status,'body':o.read().decode()}))
except urllib.error.HTTPError as e:
 print(json.dumps({'status':e.code,'body':e.read().decode()}))
'''


def http(path, body=None, token=None):
    p = {'path': path, 'headers': {'Content-Type': 'application/json'}}
    if body is not None:
        p['body'] = body
    if token:
        p['headers']['Authorization'] = 'Bearer ' + token
    pid = str(inspect(CONTAINER)['State']['Pid'])
    return json.loads(run('nsenter', '-t', pid, '-n', 'python3', '-c', HTTP,
                          data=json.dumps(p).encode()))


def prepare(credentials, resume_cloned=False):
    policy = Policy()
    policy.guard()
    receipt = json.loads((policy.work / 'usage-reconciliation.json').read_text())
    if receipt.get('matched') is not True:
        raise RuntimeError('baseline history has not been reconciled')
    auth = json.loads(credentials.read_text())
    if not re.fullmatch(r'\$2[aby]\$\d{2}\$[./A-Za-z0-9]{53}', auth['password_hash']):
        raise RuntimeError('expected fresh bcrypt rehearsal password')
    for image in (APP_IMAGE, REDIS_IMAGE):
        info = json.loads(run('docker', 'image', 'inspect', image))[0]
        if info['Id'] != image or info['Architecture'] != 'amd64':
            raise RuntimeError('verified rehearsal image missing or incompatible')
    if WORK.exists() and not resume_cloned:
        raise RuntimeError('existing rehearsal: inspect, never overwrite')
    baseline_sha = json.loads((policy.work / 'restore-receipt.json').read_text())['sha256']
    state = WORK / 'clone-state.json'
    if resume_cloned:
        if json.loads(state.read_text()) != {'database': DATABASE, 'baseline_sha256': baseline_sha, 'state': 'cloned'}:
            raise RuntimeError('matching completed clone receipt required')
        names = run('docker', 'ps', '-a', '--format', '{{.Names}}').decode().splitlines()
        if any(name in names for name in (APP, REDIS)) or (WORK / 'receipt.json').exists():
            raise RuntimeError('cannot resume an already started rehearsal')
    else:
        WORK.mkdir(mode=0o700)
    with (policy.work / 'apply.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        policy.guard()
        if not resume_cloned:
            run('docker', 'exec', CONTAINER, 'createdb', '-U', 'tokenkey',
                '--template=tokenkey', DATABASE)
            state.write_text(json.dumps({'database': DATABASE, 'baseline_sha256': baseline_sha, 'state': 'cloned'}))
        # Only this disposable database is mutated. History rows are preserved.
        sql("BEGIN; UPDATE accounts SET status='disabled',schedulable=false,"
            "credentials='{}',extra='{}'; UPDATE proxies SET status='disabled',"
            "username=NULL,password=NULL; UPDATE api_keys SET status='disabled',"
            "key='rehearsal-disabled-'||id; UPDATE users SET email='user-'||id||"
            "'@node1.invalid',username='Rehearsal '||id,notes='',password_hash='" +
            auth['password_hash'] + "',totp_enabled=false,totp_secret_encrypted=NULL,"
            "balance_notify_enabled=false; DELETE FROM settings WHERE key <> 'qa_hourly_storage_cutover_utc'; COMMIT;")
        if sql("SELECT count(*) FROM accounts /* ops-allow-soft-deleted: clear every copied credential */ WHERE status='active' OR schedulable "
               "OR credentials <> '{}'::jsonb") != '0':
            raise RuntimeError('copied supply was not disabled')
        pg_id = inspect(CONTAINER)['Id']
        created = []
        try:
            def create(name, image, options, command=()):
                run('docker', 'create', '--name', name, '--pull=never', '--label', LABEL,
                    '--restart', 'no', '--network', 'container:' + pg_id,
                    '--cpus', '1', '--memory', '1g', '--memory-swap', '1g',
                    *options, image, *command)
                created.append(name)
                validate(inspect(name), pg_id)
                run('docker', 'start', name)

            create(REDIS, REDIS_IMAGE, ['--tmpfs', '/data'],
                   ['redis-server', '--bind', '127.0.0.1', '--save', '', '--appendonly', 'no'])
            pg_env = dict(line.split('=', 1) for line in
                          (policy.work.parent / 'postgres.env').read_text().splitlines())
            env = dict(AUTO_SETUP='true', TZ='UTC', SERVER_HOST='127.0.0.1',
                SERVER_PORT='8080', DATABASE_HOST='127.0.0.1', DATABASE_PORT='5432',
                DATABASE_USER='tokenkey', DATABASE_PASSWORD=pg_env['POSTGRES_PASSWORD'],
                DATABASE_DBNAME=DATABASE, DATABASE_SSLMODE='disable',
                DATABASE_MAX_OPEN_CONNS='10', DATABASE_MAX_IDLE_CONNS='2',
                REDIS_HOST='127.0.0.1', REDIS_PORT='6379', REDIS_POOL_SIZE='16',
                REDIS_MIN_IDLE_CONNS='2', ADMIN_EMAIL='unused@node1.invalid',
                ADMIN_PASSWORD=secrets.token_hex(32), JWT_SECRET=secrets.token_hex(32),
                TOTP_ENCRYPTION_KEY=secrets.token_hex(32), QA_CAPTURE_ENABLED='false',
                QA_ARCHIVE_ENABLED='false', TELEMETRY_ARCHIVE_ENABLED='false',
                OPS_CLEANUP_ENABLED='false', USAGE_CLEANUP_ENABLED='false',
                GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_CONCURRENCY_MIRROR_ENABLED='false')
            (WORK / 'app.env').write_text(''.join(k+'='+v+'\n' for k,v in env.items()))
            if (WORK / 'app').is_symlink():
                raise RuntimeError('rehearsal app data cannot be redirected')
            (WORK / 'app').mkdir(mode=0o700, exist_ok=resume_cloned)
            os.chown(WORK / 'app', 1000, 1000)
            create(APP, APP_IMAGE, ['--env-file', str(WORK / 'app.env'),
                   '--mount', f'type=bind,src={WORK}/app,dst=/app/data'])
            pid = str(inspect(CONTAINER)['State']['Pid'])
            interfaces = json.loads(run('nsenter', '-t', pid, '-n', 'ip', '-j', 'link'))
            if [i['ifname'] for i in interfaces] != ['lo']:
                raise RuntimeError('unexpected external interface')
            run('nsenter', '-t', pid, '-n', 'python3', '-c',
                'import socket,errno; s=socket.socket(); s.settimeout(2); '
                'assert s.connect_ex(("1.1.1.1",443))==errno.ENETUNREACH')
            for _ in range(90):
                try:
                    if http('/health')['status'] == 200:
                        break
                except RuntimeError:
                    pass
                time.sleep(1)
            else:
                raise RuntimeError('history app readiness timeout')
            (WORK / 'login.private.json').write_text(json.dumps(auth))
            result = {'prepared': True, 'database': DATABASE, 'external_egress_denied': True,
                      'upstream_credentials_cleared': True, 'production_baseline_unchanged': True,
                      'public_ports': [], 'formal_cutover': False, 'ui_verified': False}
            (WORK / 'receipt.json').write_text(json.dumps(result))
            return result
        except BaseException:
            for name in reversed(created):
                validate(inspect(name), pg_id)
                run('docker', 'rm', '-f', '-v', name)
            raise


def main():
    os.umask(0o077)
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--credentials', type=Path, required=True)
    p.add_argument('--resume-cloned', action='store_true',
                   help='Resume a verified completed clone before any app was started')
    args = p.parse_args()
    print(json.dumps(prepare(args.credentials, args.resume_cloned)))


if __name__ == '__main__':
    main()
