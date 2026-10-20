#!/usr/bin/env python3
"""CI publishes verified amd64 archives; operators render an SSM prewarm command."""
from __future__ import annotations

import argparse
import base64
import hashlib
import io
import json
import os
import subprocess
import tarfile
import tempfile
from pathlib import Path

from image_archive import identity, sha256, validate_receipt, verify_archive

ROOT = Path(__file__).resolve().parent


def settings():
    return json.loads((ROOT / 'image-distribution.json').read_text())


def prefix(tag, revision):
    identity(tag, revision)
    return f'cn-prod/images/{tag}/{revision}'


def immutable_put(client, bucket, key, body):
    """Conditional creation: retries may reuse identical bytes, never overwrite."""
    checksum = base64.b64encode(hashlib.sha256(body).digest()).decode()
    try:
        client.put_object(Bucket=bucket, Key=key, Body=body, ServerSideEncryption='AES256',
                          ChecksumSHA256=checksum, IfNoneMatch='*')
    except client.exceptions.ClientError as error:
        if error.response['Error']['Code'] != 'PreconditionFailed':
            raise
    head = client.head_object(Bucket=bucket, Key=key, ChecksumMode='ENABLED')
    if head['ContentLength'] != len(body) or head.get('ChecksumSHA256') != checksum:
        raise ValueError('immutable S3 object conflicts with this artifact')


def retag_archive(source, target, tag):
    """Only change Docker's load tag; registry config/layer bytes stay exact."""
    with tarfile.open(source, 'r:') as original, tarfile.open(target, 'w') as output:
        for member in original.getmembers():
            handle = original.extractfile(member)
            if member.name == 'manifest.json':
                items = json.load(handle)
                if len(items) != 1:
                    raise ValueError('expected exactly one image')
                items[0]['RepoTags'] = [tag]
                raw = json.dumps(items, sort_keys=True).encode()
                member.size = len(raw)
                handle = io.BytesIO(raw)
            output.addfile(member, handle)


def publish(client, bucket, tag, revision, repository):
    key = prefix(tag, revision)
    source = f'{repository}:{tag}'
    digest = subprocess.check_output(['crane', 'digest', '--platform=linux/amd64', source], text=True).strip()
    raw = subprocess.check_output(['crane', 'manifest', f'{repository}@{digest}'])
    # crane adds a newline when printing a manifest; keep registry bytes exact.
    if 'sha256:' + hashlib.sha256(raw).hexdigest() != digest:
        raw = raw.rstrip(b'\n')
    archive_tag = f'tokenkey-domestic/archive:{digest.removeprefix("sha256:")}'
    with tempfile.TemporaryDirectory(prefix='cn-image-') as temp:
        archive = Path(temp) / 'image.tar'
        original = Path(temp) / 'original.tar'
        # legacy = docker-save: uncompressed layer tars (diff_ids). Receipt still
        # records the registry compressed manifest for provenance; verify_archive
        # checks config digest + diff_ids, not registry layer blob sizes.
        subprocess.run(['crane', 'pull', f'{repository}@{digest}', str(original),
                        '--format=legacy'], check=True)
        retag_archive(original, archive, archive_tag)
        with archive.open('rb') as handle:
            archive_sha = sha256(handle)
        receipt = {'schema_version': 1, 'tag': tag, 'revision': revision, 'source_digest': digest,
                       'archive_tag': archive_tag, 'archive_sha256': archive_sha,
                       'bytes': archive.stat().st_size, 'manifest_raw': raw.decode(), 'manifest': json.loads(raw)}
        config = verify_archive(archive, receipt)
        receipt['diff_ids'] = list(config['rootfs']['diff_ids'])
        immutable_put(client, bucket, f'{key}/{archive_sha}.tar', archive.read_bytes())
        # Manifest is the completion marker, written last.
        immutable_put(client, bucket, f'{key}/manifest.json',
                      (json.dumps(receipt, sort_keys=True) + '\n').encode())
    return receipt


def render(client, bucket, tag, revision, digest=''):
    key = prefix(tag, revision)
    raw = client.get_object(Bucket=bucket, Key=f'{key}/manifest.json')['Body'].read(1024 * 1024 + 1)
    if len(raw) > 1024 * 1024:
        raise ValueError('oversized image manifest')
    receipt = json.loads(raw)
    validate_receipt(receipt, tag, revision, digest)
    url = client.generate_presigned_url('get_object', Params={
        'Bucket': bucket, 'Key': f"{key}/{receipt['archive_sha256']}.tar"}, ExpiresIn=1800)
    if os.environ.get('GITHUB_ACTIONS') == 'true':
        # stderr is not captured into the remote command.
        import sys
        print('::add-mask::' + url, file=sys.stderr)
    payload = base64.b64encode(json.dumps({'receipt': receipt, 'url': url}).encode()).decode()
    code = (ROOT / 'image_archive.py').read_bytes()
    encoded = base64.b64encode(code).decode()
    return '\n'.join([
        'set -euo pipefail', 'umask 077', "python3 - <<'CN_IMAGE_WARM'",
        'import base64, json', f'exec(compile(base64.b64decode({encoded!r}), "image_archive.py", "exec"))',
        f'request = json.loads(base64.b64decode({payload!r}))',
        'print(json.dumps(warm(request["receipt"], request["url"])))', 'CN_IMAGE_WARM',
    ])


def resolve_source(path):
    """Read data from the successful release run; never execute tag checkout code."""
    source = json.loads(path.read_text())
    tag, revision = source['tag'], source['revision']
    identity(tag, revision)
    subprocess.run(['git', 'fetch', '--no-tags', 'origin', f'refs/tags/v{tag}'], check=True)
    actual = subprocess.check_output(['git', 'rev-parse', 'FETCH_HEAD^{commit}'], text=True).strip()
    if actual != revision:
        raise ValueError('release metadata does not match the current git tag')
    return f'tag={tag}\nrevision={revision}\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['publish', 'render', 'resolve'])
    parser.add_argument('--tag')
    parser.add_argument('--revision')
    parser.add_argument('--source-file', type=Path)
    parser.add_argument('--github-output', type=Path)
    parser.add_argument('--digest', default='')
    parser.add_argument('--repository', default='ghcr.io/youxuanxue/sub2api')
    args = parser.parse_args()
    if args.action == 'resolve':
        if not args.source_file or not args.github_output:
            parser.error('resolve requires --source-file and --github-output')
        output = resolve_source(args.source_file)
        with args.github_output.open('a') as handle:
            handle.write(output)
        return
    if not args.tag or not args.revision:
        parser.error('publish/render require --tag and --revision')
    identity(args.tag, args.revision)
    import boto3
    config = settings()
    client = boto3.client('s3', region_name=config['region'])
    if args.action == 'publish':
        result = publish(client, config['bucket'], args.tag, args.revision, args.repository)
        print(json.dumps({k: result[k] for k in ('tag', 'revision', 'source_digest', 'archive_sha256', 'bytes')}))
    else:
        print(render(client, config['bucket'], args.tag, args.revision, args.digest))


if __name__ == '__main__':
    main()
