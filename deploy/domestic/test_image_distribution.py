"""Behavioral tests for verified distribution; never contact production."""
import base64
import copy
import hashlib
import io
import json
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import image_archive as archive
import image_distribution as distribution
from image_distribution_lifecycle import RULE, desired_rules

TAG = '1.8.309'
REV = 'a' * 40


def digest(raw):
    return 'sha256:' + hashlib.sha256(raw).hexdigest()


def make_archive(path, *, arch='amd64', revision=REV, version=TAG, corrupt_layer=False):
    # Legacy/docker-save stores uncompressed layer bytes (diff_ids). Registry
    # compressed blob digests/sizes intentionally differ — matching real crane output.
    layer = b'fixture uncompressed layer tar'
    compressed_blob = b'fixture-compressed-registry-blob-smaller'
    config = {'architecture': arch, 'os': 'linux', 'config': {'Labels': {
        'org.opencontainers.image.version': version, 'org.opencontainers.image.revision': revision}},
        'rootfs': {'type': 'layers', 'diff_ids': [digest(layer)]}}
    raw = json.dumps(config).encode()
    manifest = {'config': {'digest': digest(raw), 'size': len(raw)},
                'layers': [{'digest': digest(compressed_blob), 'size': len(compressed_blob)}]}
    manifest_raw = json.dumps(manifest)
    source_digest = digest(manifest_raw.encode())
    load_tag = 'tokenkey-domestic/archive:' + source_digest.removeprefix('sha256:')
    docker_manifest = [{'Config': 'config.json', 'RepoTags': [load_tag], 'Layers': ['layer.tar']}]
    body = b'x' * len(layer) if corrupt_layer else layer
    with tarfile.open(path, 'w') as target:
        for name, content in [('config.json', raw), ('manifest.json', json.dumps(docker_manifest).encode()),
                              ('layer.tar', body)]:
            info = tarfile.TarInfo(name)
            info.size = len(content)
            target.addfile(info, io.BytesIO(content))
    return {'schema_version': 1, 'tag': TAG, 'revision': REV, 'bytes': path.stat().st_size,
                'archive_sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
                'archive_tag': load_tag, 'source_digest': source_digest, 'manifest': manifest, 'manifest_raw': manifest_raw}


class ArchiveTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / 'source.tar'
        self.receipt = make_archive(self.path)

    def test_archive_and_retag_preserve_registry_bytes(self):
        config = archive.verify_archive(self.path, self.receipt)
        self.assertEqual(config['architecture'], 'amd64')
        target = self.path.with_name('retag.tar')
        distribution.retag_archive(self.path, target, self.receipt['archive_tag'])
        receipt = {**self.receipt, 'bytes': target.stat().st_size,
                   'archive_sha256': hashlib.sha256(target.read_bytes()).hexdigest()}
        self.assertEqual(archive.verify_archive(target, receipt), config)

    def test_legacy_layers_verify_diff_ids_not_registry_blob_sizes(self):
        """Regression for distribute-cn-image 1.8.310: layer size mismatch.

        Crane legacy archives expand layers; registry manifest.layers[].size is
        the compressed blob. Verification must use config.rootfs.diff_ids.
        """
        with tarfile.open(self.path, 'r:') as bundle:
            member_size = bundle.getmember('layer.tar').size
            diff_id = json.loads(bundle.extractfile('config.json').read())['rootfs']['diff_ids'][0]
        registry_layer = self.receipt['manifest']['layers'][0]
        self.assertNotEqual(registry_layer['size'], member_size)
        self.assertNotEqual(registry_layer['digest'], diff_id)
        archive.verify_archive(self.path, self.receipt)

    def test_identity_and_tampered_archives_fail_closed(self):
        for mode in ('arch', 'revision', 'version', 'layer', 'checksum', 'manifest', 'load-tag'):
            with self.subTest(mode=mode):
                receipt = make_archive(self.path, arch='arm64' if mode == 'arch' else 'amd64',
                                       revision='b' * 40 if mode == 'revision' else REV,
                                       version='0.0.1' if mode == 'version' else TAG,
                                       corrupt_layer=mode == 'layer')
                if mode == 'checksum':
                    receipt['archive_sha256'] = '0' * 64
                if mode == 'manifest':
                    receipt['source_digest'] = 'sha256:' + '0' * 64
                if mode == 'load-tag':
                    receipt['archive_tag'] = 'ghcr.io/example/app:latest'
                with self.assertRaises(ValueError):
                    archive.verify_archive(self.path, receipt)

    def test_expected_identity_and_digest_are_enforced(self):
        for tag, rev, source in [(TAG, 'b' * 40, ''), (TAG, REV, 'sha256:' + '0' * 64),
                                 ('../bad', REV, ''), (TAG, 'short', '')]:
            with self.subTest(tag=tag, rev=rev, source=source), self.assertRaises(ValueError):
                archive.validate_receipt(self.receipt, tag, rev, source)

    def host_run(self, *, download_fail=False, bad_download=False, loaded_revision=REV, existing_id=None):
        data = Path(self.temp.name) / 'data'
        data.mkdir(exist_ok=True)
        config = archive.verify_archive(self.path, self.receipt)
        image = {'Id': 'sha256:loaded-manifest', 'Architecture': 'amd64', 'Os': 'linux',
                 'Config': copy.deepcopy(config['config']), 'RootFS': {'Layers': config['rootfs']['diff_ids']}}
        image['Config']['Labels']['org.opencontainers.image.revision'] = loaded_revision
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if 'curl' in args:
                destination = Path(args[args.index('--output') + 1])
                destination.write_bytes(b'bad' if bad_download else self.path.read_bytes())
                return subprocess.CompletedProcess(args, 22 if download_fail else 0)
            return subprocess.CompletedProcess(args, 0)

        def inspect(ref):
            if ref == self.receipt['archive_tag']:
                return image
            return {**image, 'Id': existing_id} if existing_id else None

        with patch.object(archive.subprocess, 'run', side_effect=run), \
             patch.object(archive, 'inspect_image', side_effect=inspect):
            try:
                result = archive.warm(self.receipt, 'https://private.invalid/signed', data=data)
                return result, calls
            except (ValueError, RuntimeError) as error:
                return error, calls

    def test_warm_downloads_verifies_loads_and_caches_without_service_mutation(self):
        result, calls = self.host_run()
        self.assertEqual(result['local_tag'], archive.identity(TAG, REV))
        self.assertFalse(result['cached'])
        self.assertEqual([cmd[:2] for cmd in calls if cmd[0] == 'docker'], [['docker', 'load'], ['docker', 'tag']])
        cached, calls = self.host_run(existing_id='sha256:loaded-manifest')
        self.assertTrue(cached['cached'])
        self.assertEqual([cmd[0] for cmd in calls], ['mountpoint'])
        self.assertEqual(list((Path(self.temp.name) / 'data/image-distribution').glob('warm-*')), [])

    def test_bad_download_never_reaches_docker(self):
        for options in ({'download_fail': True}, {'bad_download': True}):
            result, calls = self.host_run(**options)
            self.assertIsInstance(result, (ValueError, RuntimeError))
            self.assertEqual([cmd for cmd in calls if cmd[0] == 'docker'], [])
            self.assertEqual(list((Path(self.temp.name) / 'data/image-distribution').glob('warm-*')), [])

    def test_wrong_loaded_revision_or_existing_tag_is_not_tagged(self):
        for options in ({'loaded_revision': 'b' * 40}, {'existing_id': 'sha256:other'}):
            result, calls = self.host_run(**options)
            self.assertIsInstance(result, ValueError)
            self.assertEqual([cmd for cmd in calls if cmd[:2] == ['docker', 'tag']], [])

    def test_busy_host_lock_does_not_download(self):
        data = Path(self.temp.name)
        with patch.object(archive.subprocess, 'run') as run, \
             patch.object(archive.fcntl, 'flock', side_effect=BlockingIOError):
            with self.assertRaises(BlockingIOError):
                archive.warm(self.receipt, 'https://private.invalid/signed', data=data)
            self.assertEqual([call.args[0][0] for call in run.call_args_list], ['mountpoint'])


class ClientError(Exception):
    def __init__(self, code):
        self.response = {'Error': {'Code': code}}


class MemoryS3:
    class exceptions:
        ClientError = ClientError

    def __init__(self):
        self.objects = {}
        self.puts = []

    def put_object(self, **kwargs):
        key = kwargs['Key']
        if kwargs['IfNoneMatch'] != '*':
            raise AssertionError('must be conditional')
        if key in self.objects:
            raise ClientError('PreconditionFailed')
        self.puts.append(key)
        self.objects[key] = kwargs['Body']

    def head_object(self, **kwargs):
        body = self.objects[kwargs['Key']]
        return {'ContentLength': len(body), 'ChecksumSHA256': base64.b64encode(hashlib.sha256(body).digest()).decode()}

    def get_object(self, **kwargs):
        return {'Body': io.BytesIO(self.objects[kwargs['Key']])}

    def generate_presigned_url(self, *args, **kwargs):
        if kwargs['ExpiresIn'] != 1800:
            raise AssertionError('unexpected signing TTL')
        self.signed_key = kwargs['Params']['Key']
        return 'https://private.invalid/signed'


class DistributionTests(unittest.TestCase):
    def test_immutable_retry_and_conflict(self):
        client = MemoryS3()
        distribution.immutable_put(client, 'bucket', 'key', b'first')
        distribution.immutable_put(client, 'bucket', 'key', b'first')
        with self.assertRaises(ValueError):
            distribution.immutable_put(client, 'bucket', 'key', b'other')
        self.assertEqual(client.objects['key'], b'first')

    def test_publish_manifest_last_and_remote_script_executes(self):
        with tempfile.TemporaryDirectory() as temp:
            source = Path(temp) / 'source.tar'
            receipt = make_archive(source)
            client = MemoryS3()

            def pull(args, **kwargs):
                Path(args[3]).write_bytes(source.read_bytes())
                return subprocess.CompletedProcess(args, 0)

            with patch.object(distribution.subprocess, 'check_output', side_effect=[receipt['source_digest'], receipt['manifest_raw'].encode() + b'\n']), \
                 patch.object(distribution.subprocess, 'run', side_effect=pull):
                published = distribution.publish(client, 'bucket', TAG, REV, 'ghcr.io/example/app')
            self.assertEqual(client.puts[-1], distribution.prefix(TAG, REV) + '/manifest.json')
            script = distribution.render(client, 'bucket', TAG, REV, receipt['source_digest'])
            self.assertEqual(client.signed_key, distribution.prefix(TAG, REV) + '/' + published['archive_sha256'] + '.tar')
            # Execute the generated Python bootstrap, replacing only host-side warm.
            lines = script.splitlines()
            bootstrap = '\n'.join(lines[3:-2])
            namespace = {}
            exec(bootstrap, namespace)  # noqa: S102 - execute our generated bootstrap against an in-memory fixture
            self.assertEqual(namespace['request']['receipt'], published)
            self.assertEqual(namespace['request']['url'], 'https://private.invalid/signed')
            with self.assertRaises(ValueError):
                distribution.render(client, 'bucket', TAG, REV, 'sha256:' + '0' * 64)

    def test_release_artifact_is_only_data_and_must_match_git_tag(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'source.json'
            path.write_text(json.dumps({'tag': TAG, 'revision': REV}))
            with patch.object(distribution.subprocess, 'run') as run, \
                 patch.object(distribution.subprocess, 'check_output', return_value=REV + '\n'):
                self.assertEqual(distribution.resolve_source(path), f'tag={TAG}\nrevision={REV}\n')
                self.assertEqual(run.call_args.args[0], ['git', 'fetch', '--no-tags', 'origin', 'refs/tags/v' + TAG])
            with patch.object(distribution.subprocess, 'run'), \
                 patch.object(distribution.subprocess, 'check_output', return_value='b' * 40), self.assertRaises(ValueError):
                distribution.resolve_source(path)
            path.write_text(json.dumps({'tag': '$(touch /tmp/bad)', 'revision': REV}))
            with patch.object(distribution.subprocess, 'run') as run:
                with self.assertRaises(ValueError):
                    distribution.resolve_source(path)
                run.assert_not_called()

    def test_lifecycle_preserves_other_prefixes_and_is_idempotent(self):
        other = {'ID': 'other', 'Status': 'Enabled', 'Filter': {'Prefix': 'other/'}, 'Expiration': {'Days': 90}}
        result = desired_rules([other])
        self.assertEqual(result, [other, RULE])
        self.assertEqual(desired_rules(result), result)


if __name__ == '__main__':
    unittest.main()
