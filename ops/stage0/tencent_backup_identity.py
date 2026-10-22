#!/usr/bin/env python3
"""Prepare one Tencent host's backup identity; never changes IAM or a database.

`policy` emits the narrowly scoped policy for review/simulation. `refresh` on the
explicit target copies SSM Agent's temporary identity into tmpfs pgBackRest
configuration after verifying STS identity. No credentials enter output.

Host identity (SSM instance, hostname, Hybrid role name, SSM region) comes from
the Tencent prod matrix, never from constants here. Lookup order:
  1. $TK_TENCENT_PROD_MATRIX (explicit path; host installs set this)
  2. candidate-usw-20261022.json next to this file (host installs may ship it)
  3. deploy/tencent/candidate-usw-20261022.json in a repo checkout
No readable, complete matrix -> every action fails closed.
"""
from __future__ import annotations

import argparse
import configparser
import datetime as dt
import hashlib
import hmac
import json
import os
from pathlib import Path
import socket
import stat
import subprocess
import tempfile
import urllib.request
import xml.etree.ElementTree as ET

from postgres_backup import configuration

MATRIX_NAME = 'candidate-usw-20261022.json'


def matrix_candidates():
    explicit = os.environ.get('TK_TENCENT_PROD_MATRIX', '').strip()
    if explicit:
        # An explicit path is authoritative: do not fall through to a different file.
        return [Path(explicit)]
    here = Path(__file__).resolve().parent
    return [here / MATRIX_NAME, here.parents[1] / 'deploy' / 'tencent' / MATRIX_NAME]


def load_target(candidates=None):
    """Return the identity fields from the first readable matrix, or raise ValueError."""
    for path in candidates if candidates is not None else matrix_candidates():
        try:
            target = json.loads(path.read_text(encoding='utf-8'))['target']
        except (OSError, ValueError, KeyError, TypeError):
            continue
        fields = {key: target.get(key) for key in
                  ('expected_ssm_instance_id', 'hostname', 'ssm_hybrid_role_name', 'ssm_region')}
        if not all(isinstance(v, str) and v for v in fields.values()):
            raise ValueError('incomplete Tencent prod matrix: ' + str(path))
        if not fields['expected_ssm_instance_id'].startswith('mi-'):
            raise ValueError('Tencent prod matrix instance is not an SSM Hybrid id')
        return fields
    raise ValueError('Tencent prod matrix not found')


try:
    _TARGET = load_target()
except ValueError:
    _TARGET = None

ACCOUNT = '682751977094'
# IAM RoleId is not part of the host matrix; it is fixed by the role itself.
ROLE_ID = 'AROAZ55ZQM2DC6BZQWQ4B'
ROLE = _TARGET and _TARGET['ssm_hybrid_role_name']
INSTANCE = _TARGET and _TARGET['expected_ssm_instance_id']
HOST = _TARGET and _TARGET['hostname']
SSM_REGION = _TARGET and _TARGET['ssm_region']
USER_ID = _TARGET and ROLE_ID + ':' + INSTANCE
BUCKET = 'tokenkey-prod-physical-backup-682751977094-us-east-1'
SOURCE_PREFIX = 'prod/postgres'
CANDIDATE_PREFIX = 'candidates/tencent-usw-20261022'
CREDENTIALS = Path('/root/.aws/credentials')
SOURCE_CONFIG = Path('/run/tokenkey-recovery/config/pgbackrest.conf')
CANDIDATE_CONFIG = Path('/run/tokenkey-backup-candidate/config/pgbackrest.conf')


def require_target():
    if _TARGET is None:
        raise ValueError('Tencent prod matrix unavailable; refusing to guess host identity')


def policy():
    require_target()
    bucket = 'arn:aws:s3:::' + BUCKET
    identity = {'StringEquals': {'aws:userid': USER_ID}}
    return {'Version': '2012-10-17', 'Statement': [
        {'Sid': 'TencentBucketLocation', 'Effect': 'Allow',
         'Action': 's3:GetBucketLocation', 'Resource': bucket, 'Condition': identity},
        {'Sid': 'TencentListBackupPrefixes', 'Effect': 'Allow', 'Action': 's3:ListBucket',
         'Resource': bucket, 'Condition': {**identity, 'StringLike': {'s3:prefix': [
             SOURCE_PREFIX, SOURCE_PREFIX + '/*', CANDIDATE_PREFIX, CANDIDATE_PREFIX + '/*']}}},
        {'Sid': 'TencentSourceReadOnly', 'Effect': 'Allow',
         'Action': ['s3:GetObject', 's3:GetObjectVersion'],
         'Resource': bucket + '/' + SOURCE_PREFIX + '/*', 'Condition': identity},
        {'Sid': 'TencentCandidateBackup', 'Effect': 'Allow',
         'Action': ['s3:GetObject', 's3:PutObject', 's3:DeleteObject',
                    's3:AbortMultipartUpload', 's3:ListMultipartUploadParts'],
         'Resource': bucket + '/' + CANDIDATE_PREFIX + '/*', 'Condition': identity},
        {'Sid': 'TencentCannotModifySource', 'Effect': 'Deny',
         'NotAction': ['s3:GetObject', 's3:GetObjectVersion'],
         'Resource': bucket + '/' + SOURCE_PREFIX + '/*', 'Condition': identity},
    ]}


def read_credentials(path=CREDENTIALS, uid=0):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd) as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != uid
                or stat.S_IMODE(info.st_mode) not in (0o400, 0o600)
                or not 0 < info.st_size <= 16384):
            raise ValueError('unsafe credential file')
        parser = configparser.ConfigParser(interpolation=None)
        parser.read_string(stream.read())
    values = parser['default']
    result = {key: values[key] for key in
              ('aws_access_key_id', 'aws_secret_access_key', 'aws_session_token')}
    if any(not v or any(ch.isspace() for ch in v) for v in result.values()):
        raise ValueError('invalid temporary credential fields')
    return result


def caller_identity(credentials, opener=urllib.request.urlopen):
    region, service = SSM_REGION, 'sts'
    host = 'sts.' + region + '.amazonaws.com'
    body = b'Action=GetCallerIdentity&Version=2011-06-15'
    stamp = dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    day = stamp[:8]
    headers = {'content-type': 'application/x-www-form-urlencoded; charset=utf-8',
               'host': host, 'x-amz-date': stamp,
               'x-amz-security-token': credentials['aws_session_token']}
    signed = ';'.join(sorted(headers))
    canonical = ('POST\n/\n\n' + ''.join(k + ':' + headers[k] + '\n' for k in sorted(headers))
                 + '\n' + signed + '\n' + hashlib.sha256(body).hexdigest())
    scope = day + '/' + region + '/' + service + '/aws4_request'
    message = ('AWS4-HMAC-SHA256\n' + stamp + '\n' + scope + '\n'
               + hashlib.sha256(canonical.encode()).hexdigest())
    key = ('AWS4' + credentials['aws_secret_access_key']).encode()
    for part in (day, region, service, 'aws4_request'):
        key = hmac.new(key, part.encode(), hashlib.sha256).digest()
    headers['Authorization'] = ('AWS4-HMAC-SHA256 Credential=' + credentials['aws_access_key_id']
        + '/' + scope + ', SignedHeaders=' + signed + ', Signature='
        + hmac.new(key, message.encode(), hashlib.sha256).hexdigest())
    request = urllib.request.Request('https://' + host + '/', data=body, headers=headers)
    with opener(request, timeout=15) as response:
        root = ET.fromstring(response.read())
    return {node.tag.split('}')[-1]: node.text for node in root.iter()
            if node.tag.split('}')[-1] in ('Arn', 'UserId', 'Account')}


def validate_identity(identity):
    require_target()
    if identity != {'Account': ACCOUNT, 'UserId': USER_ID,
                    'Arn': 'arn:aws:sts::' + ACCOUNT + ':assumed-role/' + ROLE + '/' + INSTANCE}:
        raise ValueError('unexpected SSM runtime identity')


def render_config(credentials, candidate=False):
    result = configuration(BUCKET, 'us-east-1')
    result = result.replace('repo1-s3-key-type=auto',
        'repo1-s3-key-type=shared\nrepo1-s3-key=' + credentials['aws_access_key_id']
        + '\nrepo1-s3-key-secret=' + credentials['aws_secret_access_key']
        + '\nrepo1-s3-token=' + credentials['aws_session_token'])
    if candidate:
        result = result.replace('repo1-path=/' + SOURCE_PREFIX,
                                'repo1-path=/' + CANDIDATE_PREFIX + '/postgres')
    return result


def atomic_private(path, contents, uid=70):
    if path.is_symlink() or path.parent.is_symlink():
        raise ValueError('symlink configuration refused')
    fd, temporary = tempfile.mkstemp(prefix='.identity-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as stream:
            os.fchmod(stream.fileno(), 0o600)
            os.fchown(stream.fileno(), uid, uid)
            stream.write(contents)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def refresh():
    require_target()
    if os.geteuid() != 0 or socket.gethostname() != HOST:
        raise ValueError('wrong target host or user')
    for parent in (SOURCE_CONFIG.parent, CANDIDATE_CONFIG.parent):
        # /run is tmpfs; do not fall back to disk or follow a host symlink.
        if any(p.is_symlink() for p in (parent, *parent.parents)):
            raise ValueError('unsafe runtime path')
        fs = subprocess.check_output(['findmnt', '-n', '-o', 'FSTYPE', '--target', '/run'], text=True).strip()
        if fs != 'tmpfs':
            raise ValueError('runtime directory is not tmpfs')
    credentials = read_credentials()
    validate_identity(caller_identity(credentials))
    for path, candidate in ((SOURCE_CONFIG, False), (CANDIDATE_CONFIG, True)):
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
        os.chmod(path.parent.parent, 0o711)
        os.chmod(path.parent, 0o755)
        atomic_private(path, render_config(credentials, candidate))
    return {'status': 'refreshed', 'at': dt.datetime.now(dt.timezone.utc).isoformat(),
            'identity': USER_ID, 'source_repository': 'read-only',
            'candidate_prefix': CANDIDATE_PREFIX, 'cutover_authorized': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('policy', 'refresh'))
    args = parser.parse_args()
    try:
        print(json.dumps(policy() if args.action == 'policy' else refresh()))
    except Exception:
        # SDK/HTTP/config errors may contain credentials. Keep old config on
        # authentication failure and let systemd record a generic failed unit.
        print(json.dumps({'status': 'failed', 'credentials_withheld': True,
                          'cutover_authorized': False}))
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
