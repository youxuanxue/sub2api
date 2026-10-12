#!/usr/bin/env python3
"""Render the isolated QA stack. Rendering never changes a host or starts jobs."""
import argparse
import ipaddress
import json
import re
from pathlib import Path

try:
    from .colocation_resources import load_policy, resource_override
except ImportError:
    from colocation_resources import load_policy, resource_override

ROOT = Path(__file__).resolve().parents[2]
HOST_ROOT = '/var/lib/tokenkey-qa'
BRIDGE = 'tk-qa0'
SUBNET = '172.30.240.0/28'
QUEUE_IP = '172.30.240.2'
WORKER_IP = '172.30.240.3'
QUEUE_PORT = 55432


def private_ip(value):
    ip = ipaddress.IPv4Address(value)
    if not any(ip in ipaddress.ip_network(net) for net in ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16')):
        raise ValueError('QA producer/queue must use RFC1918 IPv4 addresses')
    if ip in ipaddress.ip_network(SUBNET):
        raise ValueError('private network overlaps QA container subnet')
    return str(ip)


def image_digest(value, repository):
    if not re.fullmatch(re.escape(repository) + r'@sha256:[a-f0-9]{64}', value):
        raise ValueError('immutable image digest from the approved repository required')
    return value


def bind(source, target, read_only=True):
    return {'type': 'bind', 'source': HOST_ROOT + '/' + source, 'target': target,
            'read_only': read_only, 'bind': {'create_host_path': False}}


def render(worker_image, queue_image, queue_host, producer_host, coordinates=None, policy=None):
    queue_host, producer_host = private_ip(queue_host), private_ip(producer_host)
    if queue_host == producer_host:
        raise ValueError('producer and queue must be distinct hosts')
    worker_image = image_digest(worker_image, 'ghcr.io/youxuanxue/sub2api')
    queue_image = image_digest(queue_image, 'postgres')
    coordinates = coordinates or json.loads((ROOT / 'deploy/hetzner/qa/producer.json').read_text())
    # Coordinates have a separate validated resolver; this deployment only supports its reviewed endpoint.
    if (coordinates['provider'], coordinates['endpoint'], coordinates['region']) != (
            'hetzner', 'https://fsn1.your-objectstorage.com', 'fsn1'):
        raise ValueError('unsupported QA storage coordinates')
    services = resource_override(policy or load_policy())['services']
    for component, service in services.items():
        service.update({'container_name': 'tokenkey-' + component, 'restart': 'no',
                        'init': True, 'labels': {'dev.tokenkey.owner': 'qa-hcloud'},
                        'networks': {'qa': {'ipv4_address': QUEUE_IP if component == 'qa-queue' else WORKER_IP}}})
    services['qa-queue'].update({
        'image': queue_image, 'user': '70:70', 'entrypoint': ['postgres'],
        'command': ['-D', '/queue/pgdata', '-c', 'config_file=/etc/qa/postgresql.conf'],
        'ports': [{'host_ip': queue_host, 'published': str(QUEUE_PORT), 'target': 5432, 'protocol': 'tcp'}],
        'volumes': [bind('queue', '/queue', False), bind('config/postgresql.conf', '/etc/qa/postgresql.conf'),
                    bind('config/pg_hba.conf', '/etc/qa/pg_hba.conf'), bind('queue-secrets', '/run/secrets/qa-queue')],
        'tmpfs': ['/tmp:rw,noexec,nosuid,nodev,size=16m,uid=70,gid=70,mode=0700',
                  '/var/run/postgresql:rw,noexec,nosuid,nodev,size=1m,uid=70,gid=70,mode=0700'],
        'healthcheck': {'test': ['CMD', 'pg_isready', '-U', 'postgres', '-d', 'tokenkey_qa_queue'],
                        'interval': '10s', 'timeout': '5s', 'retries': 6},
        'stop_grace_period': '2m'})
    worker = services['qa-worker']
    worker.update({'image': worker_image, 'user': '1000:1000', 'entrypoint': ['/app/sub2api'],
                   'command': ['--qa-bundle-worker'], 'profiles': ['worker'],
                   'depends_on': {'qa-queue': {'condition': 'service_healthy'}},
                   'volumes': [bind('scratch', '/tmp', False), bind('worker-secrets', '/run/secrets/tokenkey-qa')],
                   # The image's HTTP healthcheck is for the gateway, not a queue consumer.
                   'healthcheck': {'disable': True}})
    env = worker['environment']
    env.update({'TZ': 'UTC', 'QA_BUNDLE_ENABLED': 'true', 'QA_ARCHIVE_ENABLED': 'true',
                'QA_BUNDLE_QUEUE_DRIVER': 'postgres', 'QA_BUNDLE_QUEUE_URL': '',
                'QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE': '/run/secrets/tokenkey-qa/queue.dsn',
                'QA_BUNDLE_RESTORE_ROOT': '/tmp', 'AWS_EC2_METADATA_DISABLED': 'true'})
    for prefix, bucket, path, credential in (
            ('QA_BUNDLE', coordinates['bucket'], coordinates['prefix'], 'bundle.json'),
            ('QA_ARCHIVE', coordinates['archive_bucket'], coordinates['archive_prefix'], 'raw.json')):
        env.update({prefix + '_STORAGE_' + key: value for key, value in {
            'DRIVER': 's3', 'REGION': coordinates['region'], 'ENDPOINT': coordinates['endpoint'],
            'BUCKET': bucket, 'PREFIX': path, 'FORCE_PATH_STYLE': 'true',
            'CREDENTIALS_FILE': '/run/secrets/tokenkey-qa/' + credential}.items()})
    return {'name': 'tokenkey-qa', 'services': services,
            'networks': {'qa': {'name': 'tokenkey-qa', 'driver': 'bridge', 'enable_ipv6': False,
                               'driver_opts': {'com.docker.network.bridge.name': BRIDGE},
                               'ipam': {'config': [{'subnet': SUBNET}]}}}}


def postgres_config():
    return '''listen_addresses = '*'
port = 5432
max_connections = 12
shared_buffers = '64MB'
work_mem = '2MB'
maintenance_work_mem = '16MB'
max_wal_size = '128MB'
min_wal_size = '32MB'
password_encryption = 'scram-sha-256'
ssl = on
ssl_min_protocol_version = 'TLSv1.2'
ssl_cert_file = '/run/secrets/qa-queue/server.crt'
ssl_key_file = '/run/secrets/qa-queue/server.key'
hba_file = '/etc/qa/pg_hba.conf'
unix_socket_directories = '/var/run/postgresql'
log_statement = 'none'
log_min_error_statement = 'panic'
logging_collector = off
'''


def hba_config(producer_host):
    producer_host = private_ip(producer_host)
    return f'''local all postgres peer
hostssl tokenkey_qa_queue qa_producer {producer_host}/32 scram-sha-256
hostssl tokenkey_qa_queue qa_consumer {WORKER_IP}/32 scram-sha-256
host all all 0.0.0.0/0 reject
host all all ::/0 reject
'''


def firewall(queue_host, producer_host, storage_ips):
    queue_host, producer_host = private_ip(queue_host), private_ip(producer_host)
    ips = sorted({str(ipaddress.IPv4Address(value)) for value in storage_ips})
    if not ips or any(not ipaddress.ip_address(ip).is_global for ip in ips):
        raise ValueError('object-store allowlist requires public IPv4 addresses')
    # Scoped hooks: no rules for gateway packets. Priority -10 runs before Docker filter chains.
    # DNAT has already happened at this point; ctorigdst avoids accepting another published port.
    return f'''table inet tokenkey_qa {{
  set storage4 {{ type ipv4_addr; elements = {{ {', '.join(ips)} }}; }}
  chain input {{
    type filter hook input priority -10; policy accept;
    iifname "{BRIDGE}" counter drop
  }}
  chain forward {{
    type filter hook forward priority -10; policy accept;
    iifname "{BRIDGE}" jump from_qa
    oifname "{BRIDGE}" jump to_qa
  }}
  chain from_qa {{
    meta nfproto ipv6 counter drop
    ip saddr {WORKER_IP} ip daddr {QUEUE_IP} tcp dport 5432 accept
    ip saddr {QUEUE_IP} ip daddr {{ {WORKER_IP}, {producer_host} }} tcp sport 5432 ct state established accept
    ip saddr {WORKER_IP} ip daddr @storage4 tcp dport 443 accept
    counter drop
  }}
  chain to_qa {{
    meta nfproto ipv6 counter drop
    ip saddr {producer_host} ip daddr {QUEUE_IP} tcp dport 5432 ct original daddr {queue_host} ct original proto-dst {QUEUE_PORT} accept
    ip saddr @storage4 ip daddr {WORKER_IP} tcp sport 443 ct state established accept
    counter drop
  }}
  chain output {{
    type filter hook output priority -10; policy accept;
    oifname "{BRIDGE}" counter drop
  }}
}}
table bridge tokenkey_qa {{
  chain forward {{
    type filter hook forward priority -10; policy accept;
    meta ibrname "{BRIDGE}" meta obrname "{BRIDGE}" jump peers
  }}
  chain peers {{
    ether type arp accept
    ip saddr {WORKER_IP} ip daddr {QUEUE_IP} tcp dport 5432 accept
    ip saddr {QUEUE_IP} ip daddr {WORKER_IP} tcp sport 5432 ct state established accept
    counter drop
  }}
}}
'''


def render_files(worker_image, queue_image, queue_host, producer_host, storage_ips, coordinates=None, policy=None):
    coordinates = coordinates or json.loads((ROOT / 'deploy/hetzner/qa/producer.json').read_text())
    inputs = dict(worker_image=worker_image, queue_image=queue_image, queue_host=queue_host,
                  producer_host=producer_host, storage_ips=storage_ips, coordinates=coordinates)
    return {'render-inputs.json': json.dumps(inputs, indent=2),
            'compose.json': json.dumps(render(worker_image, queue_image, queue_host, producer_host,
                                             coordinates, policy), indent=2),
            'postgresql.conf': postgres_config(), 'pg_hba.conf': hba_config(producer_host),
            'firewall.nft': firewall(queue_host, producer_host, storage_ips)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--worker-image', required=True)
    parser.add_argument('--queue-image', required=True)
    parser.add_argument('--queue-host', required=True)
    parser.add_argument('--producer-host', required=True)
    parser.add_argument('--storage-ip', action='append', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    files = render_files(args.worker_image, args.queue_image, args.queue_host, args.producer_host, args.storage_ip)
    args.output.mkdir(mode=0o700, parents=True, exist_ok=True)
    for name, value in files.items():
        (args.output / name).write_text(value)


if __name__ == '__main__':
    main()
