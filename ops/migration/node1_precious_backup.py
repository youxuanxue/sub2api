#!/usr/bin/env python3
"""Run the fleet precious backup with Node1 host-only credentials and verify upload."""
import hashlib
import json
import os
from pathlib import Path
import re
import time

if __package__:
    from . import node1_full_backup as backup
else:
    import node1_full_backup as backup

SCRIPT = Path('/usr/local/lib/tokenkey/node1-tokenkey-pgdump.sh')
PREFIX = 'node1/precious/'


def main():
    os.umask(0o077)
    backup.host_guard()
    approval = json.loads(backup.private(backup.ACTIVATED))
    if approval.get('target') != 'zhupeng-prod' or approval.get('cutover_approved') is not True:
        raise RuntimeError('separate production activation approval is required')
    env = backup.backup_environment(json.loads(backup.private(backup.CONFIG)))
    if os.path.lexists('/etc/tokenkey/pgdump-storage.json'):
        raise RuntimeError('unexpected alternate backup storage identity')
    values = [line.partition('=')[2] for line in backup.private(Path('/var/lib/tokenkey/.env')).splitlines()
              if line.startswith('TOKENKEY_PGDUMP_S3_URI=')]
    if values != ['s3://' + backup.BUCKET + '/' + PREFIX.rstrip('/')]:
        raise RuntimeError('precious backup must use the Node1 object prefix')
    begin = time.time()
    backup.run(['bash', str(SCRIPT)], env=env, capture_output=True, timeout=2400)
    directory = Path('/var/lib/tokenkey/pgdump')
    files = [p for p in directory.iterdir() if not p.is_symlink() and p.is_file()
             and re.fullmatch(r'tokenkey-\d{8}T\d{6}Z\.sql\.gz', p.name)
             and p.stat().st_mtime >= begin]
    if len(files) != 1:
        raise RuntimeError('expected exactly one new precious backup')
    artifact = files[0]
    key = PREFIX + artifact.name
    result = backup.run(['aws', 's3api', 'head-object', '--bucket', backup.BUCKET, '--key', key],
                        env=env, capture_output=True, timeout=60)
    head = json.loads(result.stdout)
    if head['ContentLength'] != artifact.stat().st_size:
        raise RuntimeError('precious off-box copy size mismatch')
    with artifact.open('rb') as source:
        digest = hashlib.file_digest(source, 'sha256').hexdigest()
    receipt = {'completed_at': time.time(), 'bytes': artifact.stat().st_size,
               'sha256': digest, 'bucket': backup.BUCKET, 'key': key}
    (directory / 'node1-last-success.json').write_text(json.dumps(receipt))
    print(json.dumps(receipt))


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        raise SystemExit('Node1 precious backup failed: ' + type(exc).__name__) from None
