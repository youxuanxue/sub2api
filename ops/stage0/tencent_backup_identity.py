#!/usr/bin/env python3
"""Render prod's COS-only pgBackRest runtime config; never changes IAM or a database.

2026-11-08 user decision: stop writing AWS repo1, COS (repo2) is the only
repository (docs/approved/design-tencent-cos-backup-node1-qa.md). `refresh`
therefore no longer reads the SSM Agent's AWS session, calls STS or renders any
repo1/AWS option: it copies the root-only COS credential into the tmpfs
candidate config, which tokenkey-pgbackrest-publish-conf mounts into the
database container. No credentials enter output.

`policy` still prints the retired AWS policy so the IAM role / bucket cleanup
(a separate infra step) has an exact record of what the role was allowed to do.

Host identity (hostname, SSM instance) comes from the Tencent prod matrix:
  1. $TK_TENCENT_PROD_MATRIX (explicit path; host installs set this)
  2. candidate-usw-20261022.json next to this file (host installs may ship it)
  3. deploy/tencent/candidate-usw-20261022.json in a repo checkout
No readable, complete matrix -> every action fails closed.
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import socket
import stat
import subprocess
import tempfile

from postgres_backup import cos_configuration

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
# Retired AWS repo1 (no longer written; objects kept for separate cleanup).
BUCKET = 'tokenkey-prod-physical-backup-682751977094-us-east-1'
SOURCE_PREFIX = 'prod/postgres'
CANDIDATE_PREFIX = 'candidates/tencent-usw-20261022'
# Both tmpfs configs get the same COS-only contents. CANDIDATE_CONFIG is what
# tokenkey-pgbackrest-publish-conf copies into the live container mount;
# SOURCE_CONFIG is what ops/stage0/archive_standby_rehearsal.py mounts when it
# recreates the standby. Rewriting SOURCE_CONFIG also replaces the expired AWS
# session token it used to hold.
SOURCE_CONFIG = Path('/run/tokenkey-recovery/config/pgbackrest.conf')
CANDIDATE_CONFIG = Path('/run/tokenkey-backup-candidate/config/pgbackrest.conf')
COS_CREDENTIALS = Path('/etc/tokenkey/cos-backup.env')
COS_BUCKET = 'tokenkey-prod-backup-usw-1320330536'
COS_REGION = 'na-siliconvalley'
COS_ENDPOINT = 'cos.na-siliconvalley.myqcloud.com'
COS_PATH = '/prod/postgres'


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


def read_cos_credentials(path=COS_CREDENTIALS, uid=0):
    """Read the optional root-only COS credentials without exposing their values."""
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd) as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != uid
                or stat.S_IMODE(info.st_mode) not in (0o400, 0o600)
                or not 0 < info.st_size <= 4096):
            raise ValueError('unsafe COS credential file')
        values = {}
        for raw in stream:
            line = raw.strip()
            if not line or line.startswith('#'):
                continue
            key, sep, value = line.partition('=')
            if not sep or key not in ('tencent_SecretId', 'tencent_SecretKey'):
                raise ValueError('invalid COS credential file')
            if not value or any(ch.isspace() for ch in value) or value[0] in "'\"":
                raise ValueError('invalid COS credential value')
            values[key] = value
    if set(values) != {'tencent_SecretId', 'tencent_SecretKey'}:
        raise ValueError('incomplete COS credential file')
    return {'key': values['tencent_SecretId'], 'secret': values['tencent_SecretKey']}


def render_config(cos_credentials):
    """COS-only config: repo2 options plus credentials, nothing for repo1/AWS."""
    result = cos_configuration({
        'bucket': COS_BUCKET,
        'region': COS_REGION,
        'endpoint': COS_ENDPOINT,
        'path': COS_PATH,
        'uri_style': 'host',
    })
    return result.replace(
        'repo2-s3-uri-style=host',
        'repo2-s3-uri-style=host\nrepo2-s3-key-type=shared\n'
        'repo2-s3-key=' + cos_credentials['key'] + '\n'
        'repo2-s3-key-secret=' + cos_credentials['secret'], 1)


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
    # Data machines keep the static pgBackRest file written at cutover. This
    # refresh writes the app host's tmpfs config and must not be pointed at them.
    if os.geteuid() != 0 or socket.gethostname() != HOST:
        raise ValueError(
            'wrong target host or user; data-machine pgBackRest config is static '
            'and this refresh only runs on the app host')
    for parent in (SOURCE_CONFIG.parent, CANDIDATE_CONFIG.parent):
        # /run is tmpfs; do not fall back to disk or follow a host symlink.
        if any(p.is_symlink() for p in (parent, *parent.parents)):
            raise ValueError('unsafe runtime path')
    fs = subprocess.check_output(['findmnt', '-n', '-o', 'FSTYPE', '--target', '/run'], text=True).strip()
    if fs != 'tmpfs':
        raise ValueError('runtime directory is not tmpfs')
    # COS is the only repository now: a missing credential file is a hard
    # failure that keeps the current config, never a fallback to AWS.
    contents = render_config(read_cos_credentials())
    for path in (SOURCE_CONFIG, CANDIDATE_CONFIG):
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
        os.chmod(path.parent.parent, 0o711)
        os.chmod(path.parent, 0o755)
        atomic_private(path, contents)
    return {'status': 'refreshed', 'at': dt.datetime.now(dt.timezone.utc).isoformat(),
            'repositories': ['cos-repo2'], 'aws_repo1': 'retired',
            'cutover_authorized': False}


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
