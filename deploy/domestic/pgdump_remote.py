#!/usr/bin/env python3
"""cn-prod adapter for the shared logical dump; no cloud credentials on the host."""
import fcntl
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

ROOT = Path('/var/lib/tokenkey/pgdump')
STATE = Path('/var/lib/tokenkey/cn-pgdump')
NAME = re.compile(r'tokenkey-\d{8}T\d{6}Z\.sql\.gz')


def run(args, **kwargs):
    result = subprocess.run(args, capture_output=True, **kwargs)
    if result.returncode:
        raise RuntimeError('backup operation failed: ' + Path(args[0]).name)
    return result.stdout


def artifact(name):
    if not NAME.fullmatch(name):
        raise ValueError('invalid backup filename')
    path = ROOT / name
    if path.is_symlink() or not path.is_file():
        raise ValueError('backup must be a regular file')
    with path.open('rb') as stream:
        digest = hashlib.file_digest(stream, 'sha256').hexdigest()
    return {'name': name, 'bytes': path.stat().st_size, 'sha256': digest}


def save_receipt(data):
    path = STATE / 'latest.json'
    pending = path.with_suffix('.new')
    pending.write_text(json.dumps(data))
    pending.chmod(0o600)
    pending.replace(path)


def main():
    if os.environ.get('APPLY') != 'yes-cn-pgdump' or os.geteuid() != 0:
        raise ValueError('explicit cn-prod backup apply required')
    # Domestic compose and host identity must both match; refuse the AWS layout.
    if not Path('/opt/tokenkey/compose.yml').is_file():
        raise ValueError('domestic compose missing')
    if Path('/etc/tokenkey/pgdump-storage.json').exists():
        raise ValueError('unexpected existing storage identity')
    env = Path('/var/lib/tokenkey/.env')
    if env.exists() and any(line.startswith('TOKENKEY_PGDUMP_S3_URI=') for line in env.read_text().splitlines()):
        raise ValueError('unexpected legacy upload configuration')
    if run(['findmnt', '-n', '-o', 'TARGET', '--target', str(ROOT.parent)], text=True).strip() != str(ROOT.parent):
        raise ValueError('data volume is not mounted')
    ROOT.mkdir(mode=0o700, exist_ok=True)
    STATE.mkdir(mode=0o700, exist_ok=True)
    with (STATE / 'operation.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        action = sys.argv[1]
        if action == 'dump':
            started = time.time()
            run(['bash', '/tmp/tokenkey-pgdump.sh'], timeout=1800)
            paths = [p for p in ROOT.glob('tokenkey-*.sql.gz') if p.stat().st_mtime >= started]
            if len(paths) != 1:
                raise ValueError('expected one fresh backup')
            run(['gzip', '-t', str(paths[0])], timeout=180)
            result = artifact(paths[0].name)
            result.update(target='cn-prod', dumped_at=time.time(), duration_seconds=time.time()-started)
            save_receipt(dict(result, status='local_only'))
        elif action == 'upload':
            request_path = Path(sys.argv[2])
            request = json.loads(request_path.read_text())
            result = artifact(request['artifact']['name'])
            if result != request['artifact']:
                raise ValueError('backup identity changed before upload')
            # URL stays in a private file/stdin; never in argv or diagnostics.
            import urllib.parse
            parsed = urllib.parse.urlsplit(request['url'])
            if parsed.scheme != 'https' or not (parsed.hostname or '').endswith('.amazonaws.com'):
                raise ValueError('expected an HTTPS S3 upload URL')
            if any(c in request['url'] for c in '\r\n"'):
                raise ValueError('invalid upload URL')
            config = 'url = "' + request['url'] + '"\n'
            run(['curl', '--fail', '--silent', '--show-error', '--max-time', '900',
                 '--retry', '2', '--upload-file', str(ROOT/result['name']),
                 '--header', 'x-amz-checksum-sha256: '+base64.b64encode(bytes.fromhex(result['sha256'])).decode(), '--config', '-'],
                input=config.encode(), timeout=1800)
            request_path.unlink()
            result.update(target='cn-prod', status='uploaded_pending_verification')
        elif action == 'receipt':
            request_path = Path(sys.argv[2])
            result = json.loads(request_path.read_text())
            if result['target'] != 'cn-prod' or result['status'] != 'offbox_verified':
                raise ValueError('invalid verified receipt')
            if artifact(result['artifact']['name']) != result['artifact']:
                raise ValueError('backup identity changed before receipt')
            save_receipt(result)
            request_path.unlink()
        else:
            raise ValueError('unknown backup action')
        print(json.dumps(result))


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        print('cn-prod backup failed: '+type(exc).__name__, file=sys.stderr)
        raise SystemExit(1)
