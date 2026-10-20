#!/usr/bin/env python3
"""Validate a crane legacy archive and warm cn-prod without starting containers."""
from __future__ import annotations

import fcntl
import hashlib
import json
import os
import re
import shutil
import subprocess
import tarfile
import tempfile
from pathlib import Path

MAX_BYTES = 256 * 1024 * 1024


def sha256(stream):
    result = hashlib.sha256()
    for chunk in iter(lambda: stream.read(1024 * 1024), b''):
        result.update(chunk)
    return result.hexdigest()


def identity(tag, revision):
    if not re.fullmatch(r'\d+\.\d+\.\d+', tag):
        raise ValueError('expected a bare release version')
    if not re.fullmatch(r'[a-f0-9]{40}', revision):
        raise ValueError('expected a full source revision')
    return f'tokenkey-domestic/tokenkey:{tag}-{revision[:9]}'


def validate_receipt(receipt, tag, revision, digest=''):
    identity(tag, revision)
    if receipt['schema_version'] != 1 or (receipt['tag'], receipt['revision']) != (tag, revision):
        raise ValueError('artifact release identity mismatch')
    if not 0 < receipt['bytes'] <= MAX_BYTES:
        raise ValueError('artifact size outside limit')
    if not re.fullmatch('[a-f0-9]{64}', receipt['archive_sha256']):
        raise ValueError('invalid archive checksum')
    if receipt['archive_tag'] != 'tokenkey-domestic/archive:' + receipt['source_digest'].removeprefix('sha256:'):
        raise ValueError('unexpected archive load tag')
    manifest = receipt['manifest']
    for value in [receipt['source_digest'], manifest['config']['digest'],
                  *[layer['digest'] for layer in manifest['layers']]]:
        if not re.fullmatch(r'sha256:[a-f0-9]{64}', value):
            raise ValueError('invalid image digest')
    if digest and receipt['source_digest'] != digest:
        raise ValueError('artifact source digest mismatch (requires amd64 manifest digest)')
    # Preserve the exact registry manifest bytes, not a JSON reserialization.
    raw = receipt['manifest_raw'].encode()
    if json.loads(raw) != manifest or 'sha256:' + hashlib.sha256(raw).hexdigest() != receipt['source_digest']:
        raise ValueError('source manifest checksum mismatch')


def verify_archive(archive, receipt):
    """Validate a crane --format=legacy (docker-save) archive.

    Receipt keeps the registry OCI/docker *manifest* (compressed blob digests)
    for provenance. Legacy archives store *uncompressed* layer tars whose
    digests are config.rootfs.diff_ids — not the registry layer blob digests
    or sizes. Comparing archive members to receipt.manifest.layers sizes was
    the 1.8.310 distribute-cn-image failure mode.
    """
    validate_receipt(receipt, receipt['tag'], receipt['revision'])
    if archive.stat().st_size != receipt['bytes']:
        raise ValueError('archive size mismatch')
    with archive.open('rb') as handle:
        if sha256(handle) != receipt['archive_sha256']:
            raise ValueError('archive checksum mismatch')
    with tarfile.open(archive, 'r:') as bundle:
        members = bundle.getmembers()
        names = [member.name for member in members]
        if len(names) != len(set(names)) or any(not member.isfile() for member in members):
            raise ValueError('archive must contain unique regular files')
        if any(name.startswith('/') or '..' in Path(name).parts for name in names):
            raise ValueError('unsafe archive member')
        items = json.load(bundle.extractfile('manifest.json'))
        if len(items) != 1:
            raise ValueError('archive must contain exactly one image')
        item = items[0]
        raw = bundle.extractfile(item['Config']).read()
        if 'sha256:' + hashlib.sha256(raw).hexdigest() != receipt['manifest']['config']['digest']:
            raise ValueError('config digest mismatch')
        config = json.loads(raw)
        labels = config['config']['Labels']
        if (config['architecture'], config['os'], labels['org.opencontainers.image.version'],
            labels['org.opencontainers.image.revision']) != ('amd64', 'linux', receipt['tag'], receipt['revision']):
            raise ValueError('archive architecture/version/revision mismatch')
        registry_layers = receipt['manifest']['layers']
        diff_ids = config.get('rootfs', {}).get('diff_ids') or []
        if len(item['Layers']) != len(registry_layers) or len(item['Layers']) != len(diff_ids):
            raise ValueError('layer count mismatch')
        # Legacy/docker-save members are uncompressed; digests must equal diff_ids.
        for name, diff_id in zip(item['Layers'], diff_ids):
            if 'sha256:' + sha256(bundle.extractfile(name)) != diff_id:
                raise ValueError('layer digest mismatch')
        # Never let docker load overwrite a mutable public or unrelated local tag.
        if item['RepoTags'] != [receipt['archive_tag']]:
            raise ValueError('archive tag mismatch')
        return config


def inspect_image(reference):
    result = subprocess.run(['docker', 'image', 'inspect', reference], capture_output=True, text=True, check=False)
    if result.returncode:
        return None
    return json.loads(result.stdout)[0]


def check_loaded(image, receipt, config=None):
    labels = image.get('Config', {}).get('Labels', {})
    if (image.get('Architecture'), image.get('Os'), labels.get('org.opencontainers.image.version'),
        labels.get('org.opencontainers.image.revision')) != ('amd64', 'linux', receipt['tag'], receipt['revision']):
        raise ValueError('loaded image identity mismatch')
    if config is not None and image['RootFS']['Layers'] != config['rootfs']['diff_ids']:
        raise ValueError('loaded filesystem layers mismatch')


def warm(receipt, url, *, data=Path('/var/lib/tokenkey')):
    """Host-only entry. Download is bounded; the shared deploy lock excludes cutover."""
    validate_receipt(receipt, receipt['tag'], receipt['revision'])
    subprocess.run(['mountpoint', '-q', str(data)], check=True)
    os.umask(0o077)
    with (data / 'bluegreen-deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        local = identity(receipt['tag'], receipt['revision'])
        state = data / 'image-distribution'
        state.mkdir(mode=0o700, exist_ok=True)
        saved = state / f"{receipt['tag']}-{receipt['revision']}.json"
        current = inspect_image(local)
        if saved.exists() and current:
            previous = json.loads(saved.read_text())
            if (previous.get('archive_sha256'), previous.get('image_id')) == (receipt['archive_sha256'], current['Id']):
                check_loaded(current, receipt)
                return {**previous, 'cached': True}
        if shutil.disk_usage(data).free < receipt['bytes'] * 8 + 512 * 1024 * 1024:
            raise ValueError('insufficient disk space for image import')
        with tempfile.TemporaryDirectory(prefix='warm-', dir=state) as temp:
            archive = Path(temp) / 'image.tar'
            # URL goes through stdin, never argv/stdout. curl errors are redacted.
            download = subprocess.run([
                'nice', '-n', '15', 'ionice', '-c', '3', 'curl', '--fail', '--silent', '--show-error',
                '--proto', '=https', '--connect-timeout', '20', '--max-time', '600',
                '--retry', '2', '--retry-max-time', '660', '--limit-rate', '512K',
                '--max-filesize', str(MAX_BYTES), '--output', str(archive), '--config', '-',
            ], input='url = ' + json.dumps(url) + '\n', text=True, capture_output=True, timeout=720, check=False)
            if download.returncode:
                raise RuntimeError('S3 image download failed; signed URL withheld')
            config = verify_archive(archive, receipt)
            subprocess.run(['docker', 'load', '--input', str(archive)], check=True, capture_output=True, timeout=240)
            loaded = inspect_image(receipt['archive_tag'])
            check_loaded(loaded or {}, receipt, config)
            # Docker 29 containerd IDs are manifests; compare labels and diff_ids,
            # not the config digest to Docker's image ID.
            if current and current['Id'] != loaded['Id']:
                raise ValueError('refusing to replace an existing release tag')
            subprocess.run(['docker', 'tag', loaded['Id'], local], check=True)
            result = {k: receipt[k] for k in ('tag', 'revision', 'source_digest', 'archive_sha256', 'bytes')}
            result.update(image_id=loaded['Id'], local_tag=local, cached=False)
            temporary = saved.with_suffix('.tmp')
            temporary.write_text(json.dumps(result, indent=2) + '\n')
            temporary.replace(saved)
            return result
