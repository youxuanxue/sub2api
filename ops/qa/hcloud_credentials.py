#!/usr/bin/env python3
"""Prepare private QA files locally; never print secrets or send them in argv."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
from urllib.parse import urlencode

try:
    from .hcloud_runtime import ROOT, QUEUE_IP, QUEUE_PORT, private_ip
except ImportError:
    from hcloud_runtime import ROOT, QUEUE_IP, QUEUE_PORT, private_ip


def write_private(path, data):
    # Refuse overwrites/symlinks: a second run must not rotate only half the identities.
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as stream:
        stream.write(data)
    path.chmod(0o400)


def prepare(output, queue_host):
    queue_host = private_ip(queue_host)
    access, secret = os.environ.get('HCLOUD_S3_Access_Key'), os.environ.get('HCLOUD_S3_Secret_Key')
    if not access or not secret:
        raise ValueError('existing Hetzner S3 environment credentials are required')
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    for directory in ('operator', 'queue-secrets', 'worker-secrets', 'producer-secrets'):
        (output / directory).mkdir(mode=0o700)
    def openssl(*args):
        result = subprocess.run(['openssl', *args], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        if result.returncode:
            raise RuntimeError('QA certificate generation failed')
    ca = output / 'operator'
    server = output / 'queue-secrets'
    openssl('req', '-x509', '-newkey', 'rsa:3072', '-nodes', '-days', '365', '-sha256',
            '-keyout', str(ca / 'ca.key'), '-out', str(ca / 'ca.pem'), '-subj', '/CN=TokenKey QA private queue CA')
    openssl('req', '-new', '-newkey', 'rsa:3072', '-nodes', '-keyout', str(server / 'server.key'),
            '-out', str(ca / 'server.csr'), '-subj', '/CN=TokenKey QA queue')
    write_private(ca / 'server.ext', f'subjectAltName=IP:{queue_host},IP:{QUEUE_IP}\nextendedKeyUsage=serverAuth\n')
    openssl('x509', '-req', '-in', str(ca / 'server.csr'), '-CA', str(ca / 'ca.pem'),
            '-CAkey', str(ca / 'ca.key'), '-CAcreateserial', '-out', str(server / 'server.crt'),
            '-days', '90', '-sha256', '-extfile', str(ca / 'server.ext'))
    passwords = {role: secrets.token_hex(32) for role in ('producer', 'consumer')}
    sql = (ROOT / 'deploy/hetzner/qa/queue.sql').read_text()
    sql += '\nBEGIN;\nUPDATE qa_queue.control SET paused=true;\n'
    for role, password in passwords.items():
        sql += (f"CREATE ROLE qa_{role} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS "
                f"PASSWORD '{password}' IN ROLE qa_queue_{role};\n")
    sql += 'COMMIT;\n'
    write_private(ca / 'bootstrap.sql', sql)
    storage = json.dumps({'access_key_id': access, 'secret_access_key': secret})
    for directory, role, host, port in [('producer-secrets', 'producer', queue_host, QUEUE_PORT),
                                       ('worker-secrets', 'consumer', QUEUE_IP, 5432)]:
        target = output / directory
        for filename in ('bundle.json', 'raw.json'):
            write_private(target / filename, storage)
        query = urlencode({'sslmode': 'verify-full', 'sslrootcert': '/run/secrets/tokenkey-qa/queue-ca.pem',
                           'connect_timeout': '5'})
        write_private(target / 'queue.dsn', f'postgresql://qa_{role}:{passwords[role]}@{host}:{port}/tokenkey_qa_queue?{query}\n')
        write_private(target / 'queue-ca.pem', (ca / 'ca.pem').read_text())
    for file in output.rglob('*'):
        if file.is_file():
            file.chmod(0o400)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--queue-host', required=True)
    args = parser.parse_args()
    os.umask(0o077)
    prepare(args.output, args.queue_host)
    print('Private QA files prepared; CA/bootstrap remain operator-only. No live configuration changed.')


if __name__ == '__main__':
    main()
