#!/usr/bin/env python3
"""Exercise the production rsync command against synthetic Linux state only.

Run in tokenkey-luks-rehearsal image; no host mounts or network required.
"""
import json
import os
from pathlib import Path
import subprocess
import tempfile

from prepare_app_luks_exit import copy_command


def run(args):
    return subprocess.check_output(args, text=True)


def main():
    with tempfile.TemporaryDirectory() as directory:
        source = Path(directory) / 'original'
        target = Path(directory) / 'ordinary'
        source.mkdir(); target.mkdir()
        env = source / '.env'
        env.write_text('synthetic-private-fixture\n'); env.chmod(0o600)
        ledger = source / 'app/qa_capture_ledger/runtime.json'
        ledger.parent.mkdir(parents=True); ledger.write_text('{"drained":true}\n')
        os.chown(ledger, 1000, 1000); ledger.chmod(0o600)
        os.link(ledger, ledger.with_name('hardlink.json'))
        (source / 'link').symlink_to('app/qa_capture_ledger/runtime.json')
        sparse = source / 'sparse'
        with sparse.open('wb') as stream:
            stream.seek(128 * 1024 * 1024); stream.write(b'end')
        run(['setfattr', '-n', 'user.tokenkey-test', '-v', 'retained', str(ledger)])
        run(['setfacl', '-m', 'u:1001:r', str(ledger)])
        run(copy_command(source, target, checksum=True))
        # Final offline delta after synthetic writer stops.
        ledger.write_text('{"drained":true,"captures":4}\n')
        (source / 'removed').write_text('retired')
        run(copy_command(source, target, checksum=True))
        (source / 'removed').unlink()
        run(copy_command(source, target, checksum=True))
        copied = target / ledger.relative_to(source)
        assert copied.read_bytes() == ledger.read_bytes()
        assert (target / '.env').stat().st_mode & 0o777 == 0o600
        assert copied.stat().st_uid == 1000 and copied.stat().st_gid == 1000
        assert copied.stat().st_ino == copied.with_name('hardlink.json').stat().st_ino
        assert (target / 'link').readlink() == Path('app/qa_capture_ledger/runtime.json')
        assert not (target / 'removed').exists()
        assert (target / 'sparse').stat().st_blocks * 512 < 1024 * 1024
        assert run(['getfattr', '--only-values', '-n', 'user.tokenkey-test', str(copied)]).strip() == 'retained'
        assert run(['getfacl', '-cp', str(copied)]) == run(['getfacl', '-cp', str(ledger)])
        # Reverse synchronization models rollback after local state advanced.
        copied.write_text('{"drained":true,"captures":5}\n')
        run(copy_command(target, source, checksum=True))
        assert ledger.read_bytes() == copied.read_bytes()
        print(json.dumps({'metadata_and_delta': 'passed', 'reverse_sync': 'passed',
                          'production_state_used': False}))


if __name__ == '__main__':
    main()
