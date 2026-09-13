#!/usr/bin/env python3
"""Prepare the normal blue/green candidate, test it with a universal key, never cut over."""
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
import gateway_capability_matrix as matrix  # noqa: E402


def remote(instance, operation, tag, receipt='', timeout=6000, replace_receipt='', offset=0, key_name='TK_FULLTEST_KEY', case_ids=None):
    files = {name: (ROOT / 'ops/stage0' / name).read_text() for name in
             ('prod_replay.py', 'prod_replay_manifest.py', 'prod-replay-capabilities.json',
              'gateway_capability_host.py', 'gateway_capability_check.py', 'gateway_capability_matrix.py',
              'gateway_capability_scenarios.py', 'gateway-account-supply.json', 'gateway-capability-matrix.json')}
    for fixture in (ROOT / 'ops/stage0/fixtures/gateway').glob('*.json'):
        files['fixtures/gateway/' + fixture.name] = fixture.read_text()
    payload = base64.b64encode(zlib.compress(json.dumps(files).encode(), level=9)).decode()
    # Private per-command path: concurrent delivery cannot replace another run's
    # executor. The host module takes the blue/green deployment lock itself.
    unpack = ('import base64,json,pathlib,sys,zlib; '
              'files=json.loads(zlib.decompress(base64.b64decode(pathlib.Path(sys.argv[1]).read_text()))); '
              '[(pathlib.Path(sys.argv[2],name).parent.mkdir(parents=True,exist_ok=True),'
              'pathlib.Path(sys.argv[2],name).write_text(data)) for name,data in files.items()]')
    entry = 'gateway_capability_host.py' if operation in ('run', 'results') else 'prod_replay.py'
    arguments = ([operation, '--tag', tag, '--offset', str(offset)] if operation in ('run', 'results') else
                 [operation, '--tag', tag, '--receipt', receipt, '--replace-receipt', replace_receipt])
    if operation == 'run':
        arguments += ['--test-key-name', key_name]
        for case_id in case_ids or ():
            arguments += ['--case-id', case_id]
    script = ('set -euo pipefail\numask 077\n'
              'replay_dir=$(mktemp -d /tmp/tk-prod-replay.XXXXXX)\n'
              'trap \'python3 -c "import shutil,sys; shutil.rmtree(sys.argv[1])" "$replay_dir"\' EXIT\n'
              + ''.join('printf %s ' + shlex.quote(payload[i:i+8192]) + ' >> "$replay_dir/payload"\n'
                        for i in range(0, len(payload), 8192))
              + 'python3 -c ' + shlex.quote(unpack) + ' "$replay_dir/payload" "$replay_dir"\n'
              'PYTHONDONTWRITEBYTECODE=1 python3 "$replay_dir/' + entry + '" '
              + shlex.join(arguments) + '\n')
    parameters = json.dumps({'commands': script.splitlines(), 'executionTimeout': [str(timeout)]})
    region = PROD_REGION
    base = ['aws', '--region', region, 'ssm']
    cid = subprocess.check_output(base + ['send-command', '--instance-ids', instance,
        '--document-name', 'AWS-RunShellScript', '--comment', 'prod private replay ' + operation,
        '--parameters', parameters, '--query', 'Command.CommandId', '--output', 'text'], text=True).strip()
    print('replay SSM command=' + cid, file=sys.stderr)
    deadline = time.monotonic() + timeout + 60
    while time.monotonic() < deadline:
        try:
            proc = subprocess.run(base + ['get-command-invocation', '--command-id', cid,
                '--instance-id', instance, '--output', 'json'], capture_output=True, text=True, timeout=30)
        except subprocess.TimeoutExpired:
            # The host run continues independently. Reconnect to the same command;
            # never submit another paid suite after a local observation timeout.
            time.sleep(5)
            continue
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


def run_replay(tag, instance, out, replace_receipt='', key_name='TK_FULLTEST_KEY', case_ids=None):
    (out / 'replay-receipt.json').write_text(json.dumps({'tag': tag, 'verdict': 'red', 'reason': 'execution_pending'}) + '\n')
    state = remote(instance, 'status', tag, timeout=60, replace_receipt=replace_receipt)
    if state['needs_prepare']:
        # No caller-supplied deploy environment can turn this into a cutover.
        env = {k: v for k, v in os.environ.items() if not k.startswith('STAGE0_BLUEGREEN_')}
        env.update(AWS_REGION=PROD_REGION, STAGE0_BLUEGREEN_STAGE='prepare', STAGE0_BLUEGREEN_WAIT_PHASE='complete',
                   STAGE0_SSM_OUTPUT_DIR=str(out / 'prepare'))
        if replace_receipt:
            env['STAGE0_BLUEGREEN_REPLACE_RECEIPT'] = replace_receipt
        subprocess.run(['bash', str(ROOT / 'ops/stage0/deploy_via_ssm_bluegreen.sh'), tag,
                        instance, 'prod replay prepare; no cutover'], env=env, check=True)
    remote(instance, 'status', tag, timeout=60)  # Require a matching durable prepared candidate.
    receipt = remote(instance, 'run', tag, key_name=key_name, case_ids=case_ids)
    (out / 'replay-receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
    results = []
    for offset in range(0, receipt['total'], 5):
        page = remote(instance, 'results', tag, timeout=60, offset=offset)
        if page.get('tag') != tag:
            raise RuntimeError('replay result tag changed')
        results.extend(page['rows'])
    try:
        details = matrix.replay_details(receipt, results)
    except ValueError as exc:
        raise RuntimeError(str(exc)) from exc
    if case_ids is not None and sorted(case_ids) != receipt.get('selected_case_ids'):
        raise RuntimeError('replay case selection changed')
    (out / 'replay-results.json').write_text(json.dumps(details, indent=2) + '\n')
    verdict = 'selected_verdict' if case_ids is not None else 'verdict'
    if receipt.get(verdict) != 'green' or receipt.get('cutover') is not False:
        raise RuntimeError('replay failed; see replay-receipt.json; cutover remains blocked')
    if case_ids is not None:
        print('selected replay passed; full plan verdict=' + receipt['verdict'] + '; cutover remains blocked')
    else:
        print('replay passed; user approval required: ' + receipt['receipt_sha256'])


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--tag', required=True)
    p.add_argument('--case-id', action='append', help='rerun explicit stable case IDs; retain full plan coverage')
    p.add_argument('--target', choices=('prod',), default='prod')
    p.add_argument('--out', type=Path, default=Path('replay-output'))
    p.add_argument('--test-key-name', default='TK_FULLTEST_KEY',
                   help='prod api_keys.name of an active universal test key (not secrets.TK_FULLTEST_KEY material)')
    p.add_argument('--replace-receipt', default='', help='existing prepared fingerprint; replace inactive candidate only')
    p.add_argument('--approved-replay', default='', help='reviewed receipt SHA; validate only, never cut over')
    args = p.parse_args()
    if not re.fullmatch(r'\d+\.\d+\.\d+', args.tag):
        p.error('invalid release tag')
    if args.replace_receipt and (args.approved_replay or not re.fullmatch(r'[a-f0-9]{64}', args.replace_receipt)):
        p.error('replacement requires SHA256 and cannot accompany approval')
    if args.approved_replay and not re.fullmatch(r'[a-f0-9]{64}', args.approved_replay):
        p.error('approved replay must be SHA256')
    if args.case_id is not None:
        if args.approved_replay:
            p.error('case selection cannot accompany approval')
        try:
            plan = matrix.build(json.loads(matrix.DEFAULT_INVENTORY.read_text()), matrix.load())
            matrix.selected_case_ids(plan, args.case_id)
        except ValueError as exc:
            p.error(str(exc))
    args.out.mkdir(parents=True, exist_ok=True)
    instance = resolve_prod_instance()  # No arbitrary EC2/edge override.
    if args.approved_replay:
        state = remote(instance, 'gate', args.tag, args.approved_replay, timeout=60)
        if os.environ.get('GITHUB_OUTPUT'):
            with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
                output.write('prepared_receipt=' + state['prepared_receipt'] + '\n')
        print(json.dumps(state))
    else:
        run_replay(args.tag, instance, args.out, args.replace_receipt, args.test_key_name, args.case_id)


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as exc:
        print('replay: ' + (str(exc) if isinstance(exc, RuntimeError) else 'execution failed'), file=sys.stderr)
        raise SystemExit(1) from None
