#!/usr/bin/env python3
"""Preserve a dedicated legacy pgdump bucket; never copy or delete backup objects."""
from __future__ import annotations
import argparse
import json
from pathlib import Path
import subprocess
import time
from postgres_backup import BackupError, atomic_json


def retained_lifecycle(configuration: dict) -> dict:
    rules = configuration.get('Rules', [])
    # Only the known, dedicated pgdump rule is retired. Unknown policies require
    # inspection rather than changing unrelated retention in a shared bucket.
    if len(rules) != 1 or rules[0].get('ID') not in ('expire-pgdump-copies', 'abort-incomplete-pgdump-upload'):
        raise BackupError('unexpected legacy pgdump lifecycle; refusing a broad policy change')
    if rules[0].get('Filter') != {'Prefix': ''}:
        raise BackupError('unexpected legacy lifecycle scope')
    return {'Rules': [{'ID': 'abort-incomplete-pgdump-upload', 'Status': 'Enabled',
                       'Filter': {'Prefix': ''},
                       'AbortIncompleteMultipartUpload': {'DaysAfterInitiation': 1}}]}


def aws(*args):
    result = subprocess.run(['aws', '--region', 'us-east-1', *args, '--output', 'json'],
                            check=True, capture_output=True, text=True, timeout=180)
    return json.loads(result.stdout) if result.stdout.strip() else {}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--bucket', required=True)
    p.add_argument('--receipt', type=Path, required=True)
    p.add_argument('--apply', action='store_true')
    args = p.parse_args()
    if args.receipt.exists():
        raise BackupError('receipt already exists; use a new path to preserve the original baseline')
    account = aws('sts', 'get-caller-identity')['Account']
    if args.bucket != f'tokenkey-prod-pgdump-{account}':
        raise BackupError('only the dedicated same-account prod pgdump bucket is supported')
    before = aws('s3api', 'get-bucket-lifecycle-configuration', '--bucket', args.bucket)
    after = retained_lifecycle(before)
    objects = aws('s3api', 'list-objects-v2', '--bucket', args.bucket, '--prefix', 'prod/pgdump/').get('Contents', [])
    dumps = [x for x in objects if x['Key'].endswith('.sql.gz') and x['Size'] > 2048]
    if not dumps:
        raise BackupError('no retained SQL dumps in dedicated bucket')
    receipt = {'status': 'planned', 's3_uri': f's3://{args.bucket}/prod/pgdump',
               'observed_at': time.time(), 'retain_until': time.time() + 31 * 86400,
               'retained_objects': len(dumps), 'retained_bytes': sum(x['Size'] for x in dumps),
               'before_lifecycle': before, 'after_lifecycle': after,
               'data_deleted': False, 'expiration_disabled': False}
    # Persist the rollback baseline before the reversible lifecycle mutation.
    atomic_json(args.receipt, receipt)
    if args.apply:
        aws('s3api', 'put-bucket-lifecycle-configuration', '--bucket', args.bucket,
            '--lifecycle-configuration', json.dumps(after))
        confirmed = aws('s3api', 'get-bucket-lifecycle-configuration', '--bucket', args.bucket)
        if confirmed['Rules'] != after['Rules']:
            raise BackupError('retention readback differs; do not stop pgdump')
        receipt.update(status='ok', observed_at=time.time(), expiration_disabled=True)
        atomic_json(args.receipt, receipt)
    print(json.dumps(receipt, indent=2))


if __name__ == '__main__':
    main()
