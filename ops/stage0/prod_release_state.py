#!/usr/bin/env python3
"""Read or commit independently observed prod component release state via SSM."""
from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path
import shlex

import ssm_execution

REMOTE = r'''
import base64, datetime, fcntl, hashlib, json, os, pathlib, subprocess, sys, time
root = pathlib.Path('/var/lib/tokenkey/qa-release')
operation = sys.argv[1]
plan = json.loads(base64.b64decode(sys.argv[2]))
def docker(*args):
    return subprocess.check_output(['docker', *args], text=True)
def runtime():
    ids = docker('ps', '-aq', '--filter', 'name=^/tokenkey-qa-runtime$').strip()
    if not ids:
        return {}
    value = json.loads(docker('inspect', 'tokenkey-qa-runtime'))[0]
    labels = value['Config'].get('Labels') or {}
    if labels.get('dev.tokenkey.qa-runtime') != 'independent-v1' or value['State']['Running']:
        raise ValueError('invalid QA runtime pin')
    host = hashlib.sha256()
    for path in ('/usr/local/bin/tokenkey-qa-maintenance.sh', '/usr/local/bin/tokenkey-qa-boundary.sh',
                 '/usr/local/lib/tokenkey/qa-runtime.sh'):
        host.update(pathlib.Path(path).read_bytes())
    return {'id': value['Id'], 'tag': labels['dev.tokenkey.release-tag'], 'image_id': value['Image'],
            'host_sha': host.hexdigest()}
def read():
    path = root / 'verified.json'
    return {'runtime': runtime(), 'verified': json.loads(path.read_text()) if path.exists() else {},
            'pause_drop': (root / 'pause-drop').exists()}
def atomic(name, payload):
    temp = root / ('.' + name + '.' + str(os.getpid()))
    with temp.open('x') as stream:
        os.chmod(temp, 0o600)
        json.dump(payload, stream, sort_keys=True)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temp, root / name)
if operation == 'read':
    print(json.dumps(read()))
else:
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    lock = pathlib.Path('/var/lib/tokenkey/qa-lifecycle/host.lock')
    lock.parent.mkdir(parents=True, exist_ok=True)
    with lock.open('a') as stream:
        deadline = time.monotonic() + 60
        while True:
            try:
                fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise TimeoutError('QA lifecycle busy; release state was not changed')
                time.sleep(1)
        if operation == 'pause':
            atomic('pause-drop', {'reason': 'legacy_gateway_rollback'})
        elif operation == 'record':
            pin = runtime()
            gateway = json.loads(docker('inspect', os.environ['TK_RELEASE_ACTIVE_CONTAINER']))[0]
            if gateway['Config']['Image'] != 'ghcr.io/youxuanxue/sub2api:' + plan['gateway_tag']:
                raise ValueError('gateway changed during component acceptance')
            if pin.get('tag') != plan['maintenance_tag']:
                raise ValueError('maintenance changed during component acceptance')
            if pin.get('id') != plan.get('runtime_id') or pin.get('host_sha') != plan.get('runtime_host_sha'):
                raise ValueError('QA runtime changed after acceptance started')
            receipt = dict(plan, runtime_id=pin['id'], runtime_host_sha=pin['host_sha'], publisher_tag=plan['target_tag'],
                           verified_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
            atomic('verified.json', receipt)
            if not plan['legacy_rollback']:
                (root / 'pause-drop').unlink(missing_ok=True)
        else:
            raise ValueError('unknown operation')
    print(json.dumps(read()))
'''


def remote_script(operation: str, payload: dict, resolver: str) -> str:
    encoded = base64.b64encode(REMOTE.encode()).decode()
    data = base64.b64encode(json.dumps(payload).encode()).decode()
    return ("set -euo pipefail\n" + resolver
            + "\nTK_RELEASE_ACTIVE_CONTAINER=$(tk_resolve_app_container auto)\nexport TK_RELEASE_ACTIVE_CONTAINER\n"
            + f"printf %s {shlex.quote(encoded)} | base64 -d | python3 - {shlex.quote(operation)} {shlex.quote(data)}\n")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("read", "pause", "bind", "record"))
    parser.add_argument("--instance-id", required=True)
    parser.add_argument("--plan", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    ssm_execution.PROD_REGION = os.environ.get("AWS_REGION", "us-east-1")
    payload = json.loads(args.plan.read_text()) if args.plan else {}
    resolver = (Path(__file__).resolve().parents[1] / "lib/resolve-app-container.sh").read_text()
    script = remote_script("read" if args.operation == "bind" else args.operation, payload, resolver)
    result = json.loads(ssm_execution.run_shell_b64(args.instance_id, base64.b64encode(script.encode()).decode(),
                                                  "prod component release " + args.operation))
    if args.operation == "bind":
        pin = result["runtime"]
        if pin.get("tag") != payload["maintenance_tag"]:
            raise ValueError("selected maintenance runtime was not installed")
        payload.update(runtime_id=pin["id"], runtime_host_sha=pin["host_sha"])
        args.plan.write_text(json.dumps(payload, indent=2) + "\n")
    if args.output:
        args.output.write_text(json.dumps(result, indent=2) + "\n")
    else:
        print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
