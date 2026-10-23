#!/usr/bin/env python3
"""Run cn-prod logical backup over SSM, upload with a short-lived S3 URL and verify."""
import argparse
import base64
import json
from pathlib import Path
import subprocess
import tempfile
import time
import uuid

REPO = Path(__file__).resolve().parents[2]


def validate_head(artifact, head):
    checksum = base64.b64encode(bytes.fromhex(artifact['sha256'])).decode()
    if head.get('ContentLength') != artifact['bytes'] or head.get('ChecksumSHA256') != checksum:
        raise ValueError('remote object size/checksum differs')


def probe(action, request=None):
    with tempfile.TemporaryDirectory(prefix='tk-cn-pgdump-') as directory:
        root = Path(directory)
        payload = root / ('cn-pgdump-request-'+uuid.uuid4().hex+'.json')
        if request is not None:
            payload.write_text(json.dumps(request))
            payload.chmod(0o600)
        script = root / 'cn-pgdump-run.sh'
        script.write_text('#!/bin/bash\nset -euo pipefail\numask 077\n'
                          'test "${APPLY:-}" = yes-cn-pgdump\n'
                          'chmod 600 /tmp/pgdump_remote.py\n'
                          + (f'chmod 600 /tmp/{payload.name}\n' if request is not None else '')
                          + f'python3 /tmp/pgdump_remote.py {action}'
                          + (f' /tmp/{payload.name}' if request is not None else '')+'\n')
        command = ['bash', str(REPO/'ops/observability/run-probe.sh'), '--target', 'cn-prod',
                   '--script', str(script), '--env', 'APPLY=yes-cn-pgdump', '--timeout-seconds', '2100',
                   '--with', str(REPO/'deploy/domestic/pgdump_remote.py'),
                   '--with', str(REPO/'deploy/domestic/data_layout.py'),
                   '--with', str(REPO/'deploy/aws/stage0/tokenkey-pgdump.sh')]
        if request is not None:
            command += ['--with', str(payload)]
        result = subprocess.run(command, cwd=REPO, capture_output=True, text=True)
        if result.returncode:
            # Request payloads contain signed URLs; do not echo transport errors.
            raise RuntimeError('cn-prod SSM backup step failed: '+action)
        return json.loads(result.stdout)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bucket', required=True)
    parser.add_argument('--region', default='us-east-1')
    parser.add_argument('--receipt', type=Path, required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    import boto3
    client = boto3.client('s3', region_name=args.region)
    account = boto3.client('sts').get_caller_identity()['Account']
    if args.bucket != 'tokenkey-cn-prod-pgdump-'+account:
        raise ValueError('expected dedicated domestic backup bucket')
    if not args.apply:
        print(json.dumps({'target':'cn-prod','bucket':args.bucket,'prefix':'cn-prod/pgdump/',
                          'restart_required':False,'apply':False}))
        return
    client.head_bucket(Bucket=args.bucket)
    started = time.time()
    dumped = probe('dump')
    artifact = {k:dumped[k] for k in ('name','bytes','sha256')}
    key = 'cn-prod/pgdump/'+artifact['name']
    url = client.generate_presigned_url('put_object', Params={
        'Bucket':args.bucket,'Key':key,'ChecksumSHA256':base64.b64encode(bytes.fromhex(artifact['sha256'])).decode()}, ExpiresIn=1800)
    probe('upload', {'artifact':artifact,'url':url})
    validate_head(artifact, client.head_object(Bucket=args.bucket, Key=key, ChecksumMode='ENABLED'))
    receipt = {'target':'cn-prod','status':'offbox_verified','artifact':artifact,
               'bucket':args.bucket,'key':key,'completed_at':time.time(),
               'duration_seconds':time.time()-started,'dump_duration_seconds':dumped['duration_seconds']}
    probe('receipt', receipt)
    args.receipt.write_text(json.dumps(receipt, indent=2)+'\n')
    args.receipt.chmod(0o600)
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
