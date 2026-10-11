import json
import os
from pathlib import Path
import subprocess
import socket
import ssl
import threading
import tempfile
import unittest
from unittest.mock import patch

from ops.qa.hcloud_runtime import render, firewall, private_ip
from ops.qa.hcloud_credentials import prepare

WORKER = 'ghcr.io/youxuanxue/sub2api@sha256:' + 'a' * 64
QUEUE = 'postgres@sha256:' + 'b' * 64


class HcloudRuntimeTest(unittest.TestCase):
    def test_compose_validates_and_does_not_start_consumer_by_default(self):
        config = render(WORKER, QUEUE, '10.77.0.3', '10.77.0.2')
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, 'compose.json')
            path.write_text(json.dumps(config))
            result = subprocess.run(['docker', 'compose', '-f', str(path), 'config', '--services'],
                                    text=True, capture_output=True, check=True)
        self.assertEqual(result.stdout.split(), ['qa-queue'])
        worker = config['services']['qa-worker']
        self.assertEqual(worker['user'], '1000:1000')
        self.assertEqual(worker['entrypoint'], ['/app/sub2api'])
        self.assertEqual(worker['environment']['QA_BUNDLE_QUEUE_DRIVER'], 'postgres')
        self.assertEqual(worker['networks'], {'qa': {'ipv4_address': '172.30.240.3'}})
        self.assertTrue(all(m['source'].startswith('/var/lib/tokenkey-qa/') for m in worker['volumes']))
        self.assertEqual(config['services']['qa-queue']['ports'][0]['host_ip'], '10.77.0.3')

    def test_rejects_mutable_images_public_endpoints_and_network_overlap(self):
        for image in ('ghcr.io/youxuanxue/sub2api:latest', 'other@sha256:' + 'a'*64):
            with self.assertRaises(ValueError):
                render(image, QUEUE, '10.77.0.3', '10.77.0.2')
        for host in ('8.8.8.8', '127.0.0.1', '172.30.240.5', '169.254.169.254', '::1'):
            with self.assertRaises(ValueError):
                private_ip(host)
        with self.assertRaises(ValueError):
            firewall('10.77.0.3', '10.77.0.2', ['127.0.0.1'])
        with self.assertRaises(ValueError):
            firewall('10.77.0.3', '10.77.0.2', [])

    def test_credentials_are_private_separate_queue_roles_and_tls_verifies(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {
                'HCLOUD_S3_Access_Key': 'synthetic-access', 'HCLOUD_S3_Secret_Key': 'synthetic-secret'}):
            output = Path(directory, 'credentials')
            prepare(output, '10.77.0.3')
            producer = output / 'producer-secrets'
            worker = output / 'worker-secrets'
            self.assertEqual(json.loads((worker/'raw.json').read_text()),
                             json.loads((producer/'raw.json').read_text()))
            self.assertNotEqual((worker/'queue.dsn').read_text(), (producer/'queue.dsn').read_text())
            self.assertIn('qa_producer:', (producer/'queue.dsn').read_text())
            self.assertIn('qa_consumer:', (worker/'queue.dsn').read_text())
            for file in output.rglob('*'):
                if file.is_file():
                    self.assertEqual(file.stat().st_mode & 0o777, 0o400)
            for address, success in [('10.77.0.3', True), ('172.30.240.2', True), ('10.77.0.4', False)]:
                server = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
                server.load_cert_chain(output/'queue-secrets/server.crt', output/'queue-secrets/server.key')
                client = ssl.create_default_context(cafile=str(output/'operator/ca.pem'))
                left, right = socket.socketpair()
                left.settimeout(3)
                right.settimeout(3)
                def serve():
                    try:
                        with server.wrap_socket(left, server_side=True) as connection:
                            connection.sendall(b'qa')
                    except ssl.SSLError:
                        pass  # Wrong-IP case intentionally aborts the server handshake.
                thread = threading.Thread(target=serve)
                thread.start()
                try:
                    if success:
                        with client.wrap_socket(right, server_hostname=address) as connection:
                            self.assertEqual(connection.recv(2), b'qa')
                    else:
                        with self.assertRaises(ssl.SSLCertVerificationError):
                            client.wrap_socket(right, server_hostname=address)
                finally:
                    right.close()
                    thread.join(timeout=4)
                    self.assertFalse(thread.is_alive())
            with self.assertRaises(FileExistsError):
                prepare(output, '10.77.0.3')


if __name__ == '__main__':
    unittest.main()
