#!/usr/bin/env python3
"""Prepare a private prod compose file with fresh edge IPs; never start services."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess


def prepare(inventory, compose, output):
    if subprocess.check_output(['hostname'], text=True).strip() != inventory['prod']['instance_name']:
        raise SystemExit('not the fresh prod host')
    if not Path('/var/lib/tokenkey/migration/DATA_SYNC_HOLD').is_file():
        raise SystemExit('candidate hold missing')
    running = subprocess.check_output(['docker', 'ps', '--format', '{{.Names}}'], text=True).splitlines()
    if running != ['tokenkey-postgres']:
        raise SystemExit('expected only PostgreSQL running')
    output.mkdir(mode=0o700)
    original = compose.read_bytes()
    command = ['docker', 'compose', '--project-directory', str(compose.parent),
               '--env-file', str(compose.parent / '.env')]
    config = json.loads(subprocess.check_output(command + ['-f', str(compose),
                        'config', '--no-interpolate', '--format', 'json'], text=True))
    pins = {row['domain']: row['ip'] for row in inventory['edges'].values()}
    services = config['services']
    gateways = [name for name in services if name in ('tokenkey-blue', 'tokenkey-green')]
    if not gateways:
        raise SystemExit('no blue/green gateway services')
    for name in gateways:
        hosts = services[name].get('extra_hosts', {})
        if isinstance(hosts, list):
            hosts = dict(item.split('=', 1) if '=' in item else item.split(':', 1) for item in hosts)
        services[name]['extra_hosts'] = {**hosts, **pins}
    pending = output / 'docker-compose.bluegreen.json'
    pending.write_text(json.dumps(config, indent=2) + '\n')
    pending.chmod(0o600)
    subprocess.run(command + ['-f', str(pending), 'config', '--quiet'], check=True)
    # Guard against a deploy changing the source while preparation was in progress.
    if compose.read_bytes() != original:
        raise SystemExit('compose changed during preparation; discard pending output')
    receipt = {'installed': False, 'source': str(compose),
               'source_sha256': hashlib.sha256(original).hexdigest(),
               'pending_sha256': hashlib.sha256(pending.read_bytes()).hexdigest(),
               'services': gateways, 'edge_pins': pins,
               'activation': 'replace the bluegreen compose only after final data checks; recheck source hash',
               'removal': 'after DNS transition, regenerate via normal bluegreen deploy; do not deploy during transition'}
    (output / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
    print(json.dumps(receipt))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--inventory', required=True, type=Path)
    parser.add_argument('--compose', required=True, type=Path)
    parser.add_argument('--output-dir', required=True, type=Path)
    args = parser.parse_args()
    prepare(json.loads(args.inventory.read_text()), args.compose, args.output_dir)
