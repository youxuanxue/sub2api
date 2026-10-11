#!/usr/bin/env python3
"""Bounded, resumable QA pre-copy; never deletes AWS objects or switches producers.

The private SQLite ledger records source versions, SHA256 and original expiry.
Final cutover still requires a frozen inventory, queue ledger and raw restore.
"""
import argparse
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import time

SOURCES = {
    'raw': ('tokenkey-prod-qa-raw-archive-682751977094', 'tokenkey-qa-raw-fsn1-dd2ca9019b22', 'raw/v1/'),
    'bundle': ('tokenkey-prod-qa-bundles-682751977094', 'tokenkey-qa-bundles-fsn1-dd2ca9019b22', 'user-qa/'),
}
PART_SIZE = 8 * 1024**2


def expiry(head):
    # AWS computes lifecycle expiry from source creation, rounded to midnight UTC.
    # Do not approximate it from the time of migration or silently extend retention.
    value = head.get('Expiration', '')
    match = re.search(r'expiry-date="([^"]+)"', value)
    if not match:
        raise ValueError('source object has no authoritative lifecycle expiry')
    return parsedate_to_datetime(match[1]).astimezone(timezone.utc)


def safe_head(client, bucket, key):
    try:
        return client.head_object(Bucket=bucket, Key=key)
    except Exception as exc:
        if getattr(exc, 'response', {}).get('ResponseMetadata', {}).get('HTTPStatusCode') == 404:
            return None
        raise


def ledger_open(path):
    if path.is_symlink() or (path.exists() and path.stat().st_mode & 0o077):
        raise ValueError('migration ledger must be a private regular file')
    if not path.exists():
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        os.close(fd)
    db = sqlite3.connect(path)
    db.execute('PRAGMA synchronous=FULL')
    db.execute('''CREATE TABLE IF NOT EXISTS objects (
      source_bucket TEXT NOT NULL, object_key TEXT NOT NULL, source_etag TEXT NOT NULL,
      target_bucket TEXT NOT NULL, target_etag TEXT NOT NULL, size INTEGER NOT NULL,
      sha256 TEXT NOT NULL, expires_at TEXT NOT NULL, verified_at TEXT NOT NULL,
      PRIMARY KEY(source_bucket,object_key))''')
    return db


def stream_hash(body, rate):
    digest, count, start = hashlib.sha256(), 0, time.monotonic()
    with body:
        while chunk := body.read(PART_SIZE):
            digest.update(chunk)
            count += len(chunk)
            delay = count / rate - (time.monotonic() - start)
            if delay > 0:
                time.sleep(delay)
    return count, digest.hexdigest()


def copy_one(source, target, db, source_bucket, target_bucket, key, rate):
    head = source.head_object(Bucket=source_bucket, Key=key)
    deadline = expiry(head)
    if deadline <= datetime.now(timezone.utc):
        return 'expired'
    previous = db.execute('SELECT source_etag,target_etag,size,sha256 FROM objects WHERE source_bucket=? AND object_key=?',
                          (source_bucket, key)).fetchone()
    dest = safe_head(target, target_bucket, key)
    if dest:
        same_source = dest.get('Metadata', {}).get('tk-migration-source-etag') == head['ETag'].strip('"')
        same_expiry = dest.get('Metadata', {}).get('tk-migration-expires-at') == deadline.isoformat()
        same_size = dest['ContentLength'] == head['ContentLength']
        if same_source and same_expiry and same_size:
            if previous and previous[:3] == (head['ETag'], dest['ETag'], head['ContentLength']):
                return 'verified'
            # Recover a crash between successful upload/readback and ledger commit.
            # Metadata alone is not proof; compare the entire current source and target.
            original = source.get_object(Bucket=source_bucket, Key=key, IfMatch=head['ETag'])
            source_size, source_hash = stream_hash(original['Body'], rate)
            copied = target.get_object(Bucket=target_bucket, Key=key, IfMatch=dest['ETag'])
            target_size, target_hash = stream_hash(copied['Body'], rate)
            if (source_size, source_hash) != (target_size, target_hash):
                raise ValueError('uncommitted target differs from source')
            if source.head_object(Bucket=source_bucket, Key=key)['ETag'] != head['ETag']:
                raise ValueError('source changed during recovery')
            db.execute('INSERT OR REPLACE INTO objects VALUES(?,?,?,?,?,?,?,?,?)',
                       (source_bucket, key, head['ETag'], target_bucket, dest['ETag'], target_size,
                        target_hash, deadline.isoformat(), datetime.now(timezone.utc).isoformat()))
            db.commit()
            return 'recovered'
        if previous is None or dest['ETag'] != previous[1]:
            raise ValueError('target object is not owned by this migration ledger')
        # Only the mutable raw commit pointer may change during pre-copy.
        if not (source_bucket == SOURCES['raw'][0] and key.endswith('/commit.json')):
            raise ValueError('source immutable object changed')
    response = source.get_object(Bucket=source_bucket, Key=key, IfMatch=head['ETag'])
    metadata = dict(head.get('Metadata', {}))
    for field in ('tk-migration-source-etag', 'tk-migration-expires-at'):
        if field in metadata:
            raise ValueError('source has reserved migration metadata')
    metadata.update({'tk-migration-source-etag': head['ETag'].strip('"'),
                     'tk-migration-expires-at': deadline.isoformat()})
    fields = {k: head[k] for k in ('ContentType', 'ContentEncoding', 'ContentDisposition', 'CacheControl', 'ContentLanguage', 'Expires') if k in head}
    fields.update(Bucket=target_bucket, Key=key, Metadata=metadata)
    upload = None
    start, size, digest = time.monotonic(), 0, hashlib.sha256()
    try:
        # All objects use multipart for bounded memory, including >5GiB objects.
        upload = target.create_multipart_upload(**fields)['UploadId']
        parts = []
        with response['Body'] as body:
            while chunk := body.read(PART_SIZE):
                digest.update(chunk)
                size += len(chunk)
                number = len(parts) + 1
                if number > 10000:
                    raise ValueError('source exceeds bounded multipart size')
                part = target.upload_part(Bucket=target_bucket, Key=key, UploadId=upload, PartNumber=number, Body=chunk)
                parts.append({'PartNumber': number, 'ETag': part['ETag']})
                delay = size / rate - (time.monotonic() - start)
                if delay > 0:
                    time.sleep(delay)
        if size != head['ContentLength'] or source.head_object(Bucket=source_bucket, Key=key)['ETag'] != head['ETag']:
            raise ValueError('source changed while copying')
        if not parts:
            part = target.upload_part(Bucket=target_bucket, Key=key, UploadId=upload, PartNumber=1, Body=b'')
            parts = [{'PartNumber': 1, 'ETag': part['ETag']}]
        condition = {'IfMatch': dest['ETag'].strip('"')} if dest else {'IfNoneMatch': '*'}
        output = target.complete_multipart_upload(Bucket=target_bucket, Key=key, UploadId=upload,
                                                  MultipartUpload={'Parts': parts}, **condition)
        upload = None
        downloaded = target.get_object(Bucket=target_bucket, Key=key, IfMatch=output['ETag'])
        target_size, target_hash = stream_hash(downloaded['Body'], rate)
        if target_size != size or target_hash != digest.hexdigest():
            raise ValueError('target readback SHA256 mismatch')
        db.execute('INSERT OR REPLACE INTO objects VALUES(?,?,?,?,?,?,?,?,?)',
                   (source_bucket, key, head['ETag'], target_bucket, output['ETag'], size,
                    target_hash, deadline.isoformat(), datetime.now(timezone.utc).isoformat()))
        db.commit()
        return 'copied'
    finally:
        response['Body'].close()
        if upload:
            target.abort_multipart_upload(Bucket=target_bucket, Key=key, UploadId=upload)


def clients():
    import boto3
    from botocore.config import Config
    config = Config(signature_version='s3v4', connect_timeout=10, read_timeout=60,
                    retries={'max_attempts': 2}, s3={'addressing_style': 'path'},
                    request_checksum_calculation='when_required', response_checksum_validation='when_required')
    if not os.environ.get('HCLOUD_S3_Access_Key') or not os.environ.get('HCLOUD_S3_Secret_Key'):
        raise ValueError('existing Hetzner S3 credentials required')
    return (boto3.client('s3', region_name='us-east-1', config=config),
            boto3.client('s3', region_name='fsn1', endpoint_url='https://fsn1.your-objectstorage.com', config=config,
                         aws_access_key_id=os.environ['HCLOUD_S3_Access_Key'], aws_secret_access_key=os.environ['HCLOUD_S3_Secret_Key']))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--kind', choices=SOURCES, required=True)
    parser.add_argument('--ledger', type=Path, required=True)
    parser.add_argument('--mib-per-second', type=int, default=10, choices=range(1, 21))
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    source, target = clients()
    src, dst, prefix = SOURCES[args.kind]
    count, total, stats = 0, 0, {}
    db = ledger_open(args.ledger) if args.apply else None
    try:
        for page in source.get_paginator('list_objects_v2').paginate(Bucket=src, Prefix=prefix):
            for row in page.get('Contents', []):
                count += 1
                total += row['Size']
                if args.apply:
                    outcome = copy_one(source, target, db, src, dst, row['Key'], args.mib_per_second * 1024**2)
                    stats[outcome] = stats.get(outcome, 0) + 1
                    print(json.dumps({'objects_seen': count, 'bytes_seen': total, 'results': stats}), flush=True)
        print(json.dumps({'apply': args.apply, 'objects': count, 'bytes': total, 'results': stats, 'cutover_ready': False}))
    finally:
        if db:
            db.close()


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # SDK exception bodies can contain identifiers and signed request details.
        print(json.dumps({'status': 'stopped', 'error_type': type(error).__name__, 'cutover_ready': False}))
        raise SystemExit(1) from None
