#!/usr/bin/env python3
"""Add one CN SOCKS source without restarting its service or flushing rules."""
import argparse
import ipaddress
import json
import os
from pathlib import Path
import stat
import subprocess

UNIT = Path('/etc/systemd/system/tokenkey-proxy-firewall.service')


def patched_unit(text, source, old_source):
    for value in (source, old_source):
        if str(ipaddress.IPv4Address(value)) != value:
            raise ValueError('expected a canonical IPv4 source')
    old = f'/usr/sbin/iptables -A TK_CN_PROXY -s {old_source}/32 -j ACCEPT;'
    new = f'/usr/sbin/iptables -A TK_CN_PROXY -s {source}/32 -j ACCEPT;'
    if text.count(old) != 1 or text.count('ExecStart=') != 1 or text.count('/usr/sbin/iptables -A TK_CN_PROXY -j DROP;') != 1:
        raise ValueError('firewall unit differs from the reviewed CN proxy contract')
    if new in text:
        if text.count(new) != 1:
            raise ValueError('duplicate source in firewall unit')
        return text
    return text.replace(old, old + ' ' + new)


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', required=True)
    parser.add_argument('--old-source', default='43.162.83.233')
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    info = UNIT.lstat()
    if os.geteuid() != 0 or not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
        raise ValueError('requires root and a regular owned firewall unit')
    text = UNIT.read_text()
    updated = patched_unit(text, args.source, args.old_source)
    before = run('systemctl', 'show', 'tokenkey-proxy.service', '--property=MainPID', '--value')
    if before == '0':
        raise ValueError('proxy must be running before source addition')
    run('iptables', '-w', '10', '-C', 'TK_CN_PROXY', '-s', args.old_source + '/32', '-j', 'ACCEPT')
    if args.apply:
        backup = UNIT.with_suffix('.service.parallel-previous')
        if not backup.exists():
            fd = os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            with os.fdopen(fd, 'w') as stream:
                stream.write(text)
        temporary = UNIT.with_suffix('.service.parallel-new')
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o644)
        with os.fdopen(fd, 'w') as stream:
            stream.write(updated)
            stream.flush(); os.fsync(stream.fileno())
        temporary.replace(UNIT)
        run('systemctl', 'daemon-reload')
        check = subprocess.run(['iptables', '-w', '10', '-C', 'TK_CN_PROXY', '-s', args.source + '/32', '-j', 'ACCEPT'],
                               capture_output=True)
        if check.returncode:
            run('iptables', '-w', '10', '-I', 'TK_CN_PROXY', '1', '-s', args.source + '/32', '-j', 'ACCEPT')
        for value in (args.old_source, args.source):
            run('iptables', '-w', '10', '-C', 'TK_CN_PROXY', '-s', value + '/32', '-j', 'ACCEPT')
    after = run('systemctl', 'show', 'tokenkey-proxy.service', '--property=MainPID', '--value')
    if before != after:
        raise ValueError('proxy process changed during source addition')
    print(json.dumps({'source': args.source, 'old_source_retained': args.old_source,
                      'applied': args.apply, 'proxy_pid_unchanged': True}))


if __name__ == '__main__':
    main()
