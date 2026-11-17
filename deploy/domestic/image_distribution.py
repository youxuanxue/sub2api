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
    document = json.loads((ROOT / 'image-distribution.json').read_text())
    endpoint = document.get('endpoint')
    if not isinstance(document.get('bucket'), str) or not document['bucket']:
        raise ValueError('image-distribution bucket missing')
    if not isinstance(document.get('region'), str) or not document['region']:
        raise ValueError('image-distribution region missing')
    if not isinstance(endpoint, str) or not endpoint or 'amazonaws.com' in endpoint.lower():
        raise ValueError('image-distribution endpoint must be Tencent COS (not AWS)')
    return document


def endpoint_url(config=None):
    cfg = config or settings()
    return f"https://{cfg['bucket']}.{cfg['endpoint']}"


def _botocore_config():
    """COS-safe botocore Config; older prod hosts lack checksum kwargs (<1.36)."""
    from botocore.config import Config as BotocoreConfig

    # botocore 1.36+ defaults to flexible checksums / aws-chunked bodies. COS
    # rejects UploadPart without Content-Length (MissingContentLength) and the
    # old single put_object path can hang for tens of minutes on the same path.
    try:
        return BotocoreConfig(
            request_checksum_calculation='when_required',
            response_checksum_validation='when_required',
        )
    except TypeError:
        # Ubuntu distro botocore on prod app hosts (e.g. 1.34.x) predates these.
        return BotocoreConfig()


def s3_client(config=None):
    """boto3 S3 client pinned to the COS bucket (virtual-hosted endpoint)."""
    import boto3

    cfg = config or settings()
    kwargs = {
        'region_name': cfg['region'],
        'endpoint_url': endpoint_url(cfg),
        # endpoint_url already embeds `{bucket}.cos...` (virtual-hosted). Do not
        # also set addressing_style=virtual or boto3 doubles the bucket hostname.
        'config': _botocore_config(),
    }
    # Prefer tencent-ops secrets; fall back to standard AWS env for local tooling.
    key = (os.environ.get('TENCENTCLOUD_SECRET_ID') or os.environ.get('AWS_ACCESS_KEY_ID') or '').strip()
    secret = (
        os.environ.get('TENCENTCLOUD_SECRET_KEY') or os.environ.get('AWS_SECRET_ACCESS_KEY') or ''
    ).strip()
    if key and secret:
        kwargs['aws_access_key_id'] = key
        kwargs['aws_secret_access_key'] = secret
    return boto3.client('s3', **kwargs)


def prefix(tag, revision):
    identity(tag, revision)
    return f'cn-prod/images/{tag}/{revision}'


def _head_or_none(client, bucket, key):
    try:
        return client.head_object(Bucket=bucket, Key=key)
    except client.exceptions.ClientError as error:
        code = error.response['Error']['Code']
        if code not in {'404', 'NoSuchKey', 'NotFound'}:
            raise
        return None


def _assert_immutable_head(head, size, digest_hex):
    meta = head.get('Metadata') or {}
    if head['ContentLength'] != size or meta.get('sha256') != digest_hex:
        raise ValueError('immutable S3 object conflicts with this artifact')


def immutable_put(client, bucket, key, body):
    """Conditional creation: retries may reuse identical bytes, never overwrite."""
    digest_hex = hashlib.sha256(body).hexdigest()
    head = _head_or_none(client, bucket, key)
    if head is not None:
        if head['ContentLength'] == len(body) and (head.get('Metadata') or {}).get('sha256') == digest_hex:
            return
        raise ValueError('immutable S3 object conflicts with this artifact')
    client.put_object(
        Bucket=bucket,
        Key=key,
        Body=body,
        ServerSideEncryption='AES256',
        Metadata={'sha256': digest_hex},
    )
    _assert_immutable_head(client.head_object(Bucket=bucket, Key=key), len(body), digest_hex)


def _transfer_config():
    """Parallel multipart beats a single overseas put_object for ~100MB+ archives."""
    from boto3.s3.transfer import TransferConfig

    return TransferConfig(
        multipart_threshold=8 * 1024 * 1024,
        multipart_chunksize=8 * 1024 * 1024,
        max_concurrency=16,
        use_threads=True,
    )


def immutable_put_file(client, bucket, key, path):
    """Stream a local file with multipart upload; same immutability as immutable_put."""
    path = Path(path)
    size = path.stat().st_size
    with path.open('rb') as handle:
        digest_hex = sha256(handle)
    head = _head_or_none(client, bucket, key)
    if head is not None:
        if head['ContentLength'] == size and (head.get('Metadata') or {}).get('sha256') == digest_hex:
            return digest_hex
        raise ValueError('immutable S3 object conflicts with this artifact')
    extra = {
        'ServerSideEncryption': 'AES256',
        'Metadata': {'sha256': digest_hex},
    }
    # Prefer upload_file (multipart). MemoryS3 / tiny fakes may only expose put_object.
    if hasattr(client, 'upload_file'):
        upload_kwargs = {'ExtraArgs': extra}
        try:
            upload_kwargs['Config'] = _transfer_config()
        except ImportError:
            # Unit fixtures do not install boto3; production CI installs it.
            pass
        client.upload_file(str(path), bucket, key, **upload_kwargs)
    else:
        client.put_object(Bucket=bucket, Key=key, Body=path.read_bytes(), **{
            'ServerSideEncryption': extra['ServerSideEncryption'],
            'Metadata': extra['Metadata'],
        })
    _assert_immutable_head(client.head_object(Bucket=bucket, Key=key), size, digest_hex)
    return digest_hex

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


def _registry_digest_and_manifest(repository, tag):
    source = f'{repository}:{tag}'
    digest = subprocess.check_output(
        ['crane', 'digest', '--platform=linux/amd64', source], text=True).strip()
    raw = subprocess.check_output(['crane', 'manifest', f'{repository}@{digest}'])
    # crane adds a newline when printing a manifest; keep registry bytes exact.
    if 'sha256:' + hashlib.sha256(raw).hexdigest() != digest:
        raw = raw.rstrip(b'\n')
    return digest, raw


def _finalize_publish(client, bucket, key, archive, receipt, digest, phase):
    config = verify_archive(archive, receipt)
    receipt['diff_ids'] = list(config['rootfs']['diff_ids'])
    phase(f'cos multipart upload bytes={receipt["bytes"]} sha={receipt["archive_sha256"][:12]}')
    immutable_put_file(client, bucket, f'{key}/{receipt["archive_sha256"]}.tar', archive)
    # Manifest is the completion marker, written last.
    body = (json.dumps(receipt, sort_keys=True) + '\n').encode()
    try:
        immutable_put(client, bucket, f'{key}/manifest.json', body)
    except ValueError:
        # Same archive already completed (e.g. older receipt without optional
        # diff_ids). Never overwrite; reuse the published completion marker.
        existing = json.loads(
            client.get_object(Bucket=bucket, Key=f'{key}/manifest.json')['Body'].read())
        validate_receipt(existing, tag=receipt['tag'], revision=receipt['revision'], digest=digest)
        if existing.get('archive_sha256') != receipt['archive_sha256']:
            raise ValueError('immutable S3 object conflicts with this artifact') from None
        phase('reuse existing manifest')
        return existing
    phase('done')
    return receipt


def publish(client, bucket, tag, revision, repository):
    """Legacy CI path: crane pull on the runner, then upload to Beijing COS.

    Prefer :func:`publish_from_docker` on a prod host (standard path).
    """
    import time

    key = prefix(tag, revision)
    t0 = time.monotonic()

    def phase(name):
        print(f'[publish] {name} +{time.monotonic() - t0:.1f}s', flush=True)

    phase(f'digest {repository}:{tag}')
    digest, raw = _registry_digest_and_manifest(repository, tag)
    archive_tag = f'tokenkey-domestic/archive:{digest.removeprefix("sha256:")}'
    with tempfile.TemporaryDirectory(prefix='cn-image-') as temp:
        archive = Path(temp) / 'image.tar'
        original = Path(temp) / 'original.tar'
        # legacy = docker-save: uncompressed layer tars (diff_ids). Receipt still
        # records the registry compressed manifest for provenance; verify_archive
        # checks config digest + diff_ids, not registry layer blob sizes.
        phase(f'crane pull {digest}')
        subprocess.run(['crane', 'pull', f'{repository}@{digest}', str(original),
                        '--format=legacy'], check=True)
        phase(f'retag bytes={original.stat().st_size}')
        retag_archive(original, archive, archive_tag)
        with archive.open('rb') as handle:
            archive_sha = sha256(handle)
        receipt = {
            'schema_version': 1, 'tag': tag, 'revision': revision, 'source_digest': digest,
            'archive_tag': archive_tag, 'archive_sha256': archive_sha,
            'bytes': archive.stat().st_size, 'manifest_raw': raw.decode(),
            'manifest': json.loads(raw), 'publish_source': 'crane-ci',
        }
        return _finalize_publish(client, bucket, key, archive, receipt, digest, phase)


def publish_from_docker(client, bucket, tag, revision, repository, image_ref=''):
    """Standard path: on a prod host, crane-legacy export + upload to Beijing COS.

    CLI action remains ``publish-docker`` (compatibility alias). Modern
    ``docker save`` emits OCI layouts that fail ``verify_archive``; archive bytes
    come from ``crane pull --format=legacy`` of ``repository@digest``. The win vs
    GHA is COS upload egress from the USW prod host. ``image_ref`` is ignored
    (kept only so old callers with ``--image`` do not break).
    """
    import time
    _ = image_ref  # compatibility no-op; archive always comes from crane@digest

    key = prefix(tag, revision)
    t0 = time.monotonic()

    def phase(name):
        print(f'[publish-docker] {name} +{time.monotonic() - t0:.1f}s', flush=True)

    phase(f'digest {repository}:{tag}')
    digest, raw = _registry_digest_and_manifest(repository, tag)
    archive_tag = f'tokenkey-domestic/archive:{digest.removeprefix("sha256:")}'
    with tempfile.TemporaryDirectory(prefix='cn-image-') as temp:
        archive = Path(temp) / 'image.tar'
        original = Path(temp) / 'original.tar'
        # Same legacy layout as CI publish(); egress is still from this prod host.
        phase(f'crane pull legacy {repository}@{digest}')
        subprocess.run(
            ['crane', 'pull', f'{repository}@{digest}', str(original), '--format=legacy'],
            check=True,
        )
        phase(f'retag bytes={original.stat().st_size}')
        retag_archive(original, archive, archive_tag)
        with archive.open('rb') as handle:
            archive_sha = sha256(handle)
        receipt = {
            'schema_version': 1, 'tag': tag, 'revision': revision, 'source_digest': digest,
            'archive_tag': archive_tag, 'archive_sha256': archive_sha,
            'bytes': archive.stat().st_size, 'manifest_raw': raw.decode(),
            'manifest': json.loads(raw), 'publish_source': 'prod-crane',
        }
        return _finalize_publish(client, bucket, key, archive, receipt, digest, phase)


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
    parser.add_argument(
        'action',
        choices=['publish', 'publish-docker', 'render', 'resolve'],
        help='publish=legacy GHA crane; publish-docker=standard prod crane-legacy→COS',
    )
    parser.add_argument('--tag')
    parser.add_argument('--revision')
    parser.add_argument('--source-file', type=Path)
    parser.add_argument('--github-output', type=Path)
    parser.add_argument('--digest', default='')
    parser.add_argument('--repository', default='ghcr.io/youxuanxue/sub2api')
    parser.add_argument(
        '--image', default='',
        help='ignored compatibility flag for publish-docker (archive always crane@digest)',
    )
    args = parser.parse_args()
    if args.action == 'resolve':
        if not args.source_file or not args.github_output:
            parser.error('resolve requires --source-file and --github-output')
        output = resolve_source(args.source_file)
        with args.github_output.open('a') as handle:
            handle.write(output)
        return
    if not args.tag or not args.revision:
        parser.error('publish/publish-docker/render require --tag and --revision')
    identity(args.tag, args.revision)
    config = settings()
    client = s3_client(config)
    if args.action == 'publish':
        result = publish(client, config['bucket'], args.tag, args.revision, args.repository)
        print(json.dumps({k: result[k] for k in ('tag', 'revision', 'source_digest', 'archive_sha256', 'bytes',
                                                   'publish_source') if k in result}))
    elif args.action == 'publish-docker':
        result = publish_from_docker(
            client, config['bucket'], args.tag, args.revision, args.repository, args.image)
        print(json.dumps({k: result[k] for k in ('tag', 'revision', 'source_digest', 'archive_sha256', 'bytes',
                                                   'publish_source') if k in result}))
    else:
        print(render(client, config['bucket'], args.tag, args.revision, args.digest))

if __name__ == '__main__':
    main()
