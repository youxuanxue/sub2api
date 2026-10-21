#!/usr/bin/env python3
"""SSH transport for the shared run-probe payload; explicit owned host only."""
import argparse
import importlib.util
import json
from pathlib import Path
import subprocess
import sys

from target import load_target

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('ssh_deploy', HERE / 'ssh-deploy.py')
transport = importlib.util.module_from_spec(spec)
spec.loader.exec_module(transport)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', required=True)
    parser.add_argument('--timeout-seconds', type=int, required=True)
    parser.add_argument('--compressed-output', action='store_true')
    args = parser.parse_args()
    target = load_target(args.target)
    if target.get('preparation_only') is True or target.get('inventory_status') != 'verified':
        raise RuntimeError(
            'Remote probes are disabled until the node1 inventory is verified and cutover is approved'
        )
    if not 30 <= args.timeout_seconds <= 2592000:
        parser.error('probe timeout outside supported bounds')
    payload = json.load(sys.stdin)
    if set(payload) != {'commands'} or not all(isinstance(c, str) for c in payload['commands']):
        raise ValueError('expected canonical run-probe command payload')
    transport.probe(target, require_runtime=True, capture_output=True)
    guard = ('set -euo pipefail\numask 077\n'
             'python3 /usr/local/lib/tokenkey/verify-host.py --target-file /opt/tokenkey/target.json --require-runtime >/dev/null\n')
    command = ('flock -n /run/lock/tokenkey-ssh-probe.lock timeout --signal=TERM --kill-after=15 '
               + str(args.timeout_seconds) + ' bash -s')
    result = transport.ssh(target, command, input=guard + '\n'.join(payload['commands']) + '\n',
                           text=True, capture_output=True)
    if args.compressed_output:
        subprocess.run(['python3', str(HERE.parents[1] / 'ops/observability/probe_output_transport.py'),
                        'decode'], input=result.stdout, text=True, check=True)
    else:
        print(result.stdout, end='')
    if result.stderr:
        print(result.stderr, end='', file=sys.stderr)


if __name__ == '__main__':
    main()
