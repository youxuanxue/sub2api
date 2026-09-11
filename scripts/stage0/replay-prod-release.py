#!/usr/bin/env python3
"""Prod-only replay orchestrator; remote evidence is produced by prod_replay.py."""
from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
import zlib

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'ops/stage0'))
from ssm_execution import PROD_REGION, resolve_prod_instance  # noqa: E402


def remote(instance, operation, tag, receipt='', timeout=6000):
    source = (ROOT / 'ops/stage0/prod_replay.py').read_bytes()
    payload = base64.b64encode(zlib.compress(source)).decode()
    # Private per-command path: concurrent delivery cannot replace another run's
    # executor. The host module takes the blue/green deployment lock itself.
    script = ('set -euo pipefail\numask 077\n'
              'replay_script=$(mktemp /tmp/tk-prod-replay.XXXXXX.py)\n'
              'trap \'rm -f "$replay_script"\' EXIT\n'
              f'printf %s {shlex.quote(payload)} | python3 -c \"import base64,sys,zlib; sys.stdout.buffer.write(zlib.decompress(base64.b64decode(sys.stdin.buffer.read())))\" > \"$replay_script\"\n'
              'python3 "$replay_script" ' + shlex.join([operation, '--tag', tag, '--receipt', receipt]) + '\n')
    parameters = json.dumps({'commands': [script], 'executionTimeout': [str(timeout)]})
    region = PROD_REGION
    base = ['aws', '--region', region, 'ssm']
    cid = subprocess.check_output(base + ['send-command', '--instance-ids', instance,
        '--document-name', 'AWS-RunShellScript', '--comment', 'prod private replay ' + operation,
        '--parameters', parameters, '--query', 'Command.CommandId', '--output', 'text'], text=True).strip()
    print('replay SSM command=' + cid, file=sys.stderr)
    deadline = time.monotonic() + timeout + 60
    while time.monotonic() < deadline:
        proc = subprocess.run(base + ['get-command-invocation', '--command-id', cid,
            '--instance-id', instance, '--output', 'json'], capture_output=True, text=True, timeout=30)
        if proc.returncode:
            if 'InvocationDoesNotExist' not in proc.stderr:
                raise RuntimeError('cannot read replay SSM invocation')
        else:
            invocation = json.loads(proc.stdout)
            status = invocation.get('Status')
            if status == 'Success':
                if invocation.get('ResponseCode') != 0:
                    raise RuntimeError('replay SSM returned nonzero response')
                return json.loads(invocation['StandardOutputContent'])
            if status not in ('Pending', 'InProgress', 'Delayed'):
                raise RuntimeError('replay host execution failed; command=' + cid)
        time.sleep(5)
    raise RuntimeError('replay observation timed out; host command=' + cid + ' may still be running; do not promote')


def run_replay(tag, instance, out):
    (out / 'replay-receipt.json').write_text(json.dumps({'tag': tag, 'verdict': 'red', 'reason': 'execution_pending'}) + '\n')
    state = remote(instance, 'status', tag, timeout=60)
    if state['needs_prepare']:
        # No caller-supplied deploy environment can turn this into a cutover.
        env = {k: v for k, v in os.environ.items() if not k.startswith('STAGE0_BLUEGREEN_')}
        env.update(STAGE0_BLUEGREEN_STAGE='prepare', STAGE0_BLUEGREEN_WAIT_PHASE='complete',
                   STAGE0_SSM_OUTPUT_DIR=str(out / 'prepare'))
        subprocess.run(['bash', str(ROOT / 'ops/stage0/deploy_via_ssm_bluegreen.sh'), tag,
                        instance, 'prod replay prepare; no cutover'], env=env, check=True)
    remote(instance, 'status', tag, timeout=60)  # Require a matching durable prepared candidate.
    receipt = remote(instance, 'run', tag)
    (out / 'replay-receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
    if receipt.get('verdict') != 'green' or receipt.get('cutover') is not False:
        raise RuntimeError('replay failed; see replay-receipt.json; cutover remains blocked')
    print('replay passed; user approval required: ' + receipt['receipt_sha256'])


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--tag', required=True)
    p.add_argument('--target', choices=('prod',), default='prod')
    p.add_argument('--out', type=Path, default=Path('replay-output'))
    p.add_argument('--approved-replay', default='', help='reviewed receipt SHA; validate only, never cut over')
    args = p.parse_args()
    if not re.fullmatch(r'\d+\.\d+\.\d+', args.tag):
        p.error('invalid release tag')
    if args.approved_replay and not re.fullmatch(r'[a-f0-9]{64}', args.approved_replay):
        p.error('approved replay must be SHA256')
    args.out.mkdir(parents=True, exist_ok=True)
    instance = resolve_prod_instance()  # No arbitrary EC2/edge override.
    if args.approved_replay:
        state = remote(instance, 'gate', args.tag, args.approved_replay, timeout=60)
        if os.environ.get('GITHUB_OUTPUT'):
            with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
                output.write('prepared_receipt=' + state['prepared_receipt'] + '\n')
        print(json.dumps(state))
    else:
        run_replay(args.tag, instance, args.out)


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as exc:
        print('replay: ' + (str(exc) if isinstance(exc, RuntimeError) else 'execution failed'), file=sys.stderr)
        raise SystemExit(1) from None
