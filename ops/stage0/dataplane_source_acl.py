#!/usr/bin/env python3
"""Host source allowlist for published data services, with timed rollback.

Only owns TK_DATA_FORWARD / TK_DATA_INPUT and their two jump rules. Never saves
or restores the full iptables table, so Docker and recovery egress stay intact.
"""
import argparse
import fcntl
import ipaddress
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import uuid

SITES = {
    'prod': {'host': 'VM-0-3-ubuntu', 'data': '10.4.0.3', 'app': '10.4.0.16'},
    'cn-prod': {'host': 'VM-4-5-ubuntu', 'data': '10.2.4.5', 'app': '10.2.4.11'},
}
WORK = Path('/var/lib/tokenkey-data-acl')
PROGRAM = Path('/usr/local/lib/tokenkey/dataplane_source_acl.py')
SERVICE = Path('/etc/systemd/system/tokenkey-data-source-acl.service')
CHAINS = (('DOCKER-USER', 'TK_DATA_FORWARD'), ('INPUT', 'TK_DATA_INPUT'))


def run(args, check=True):
    return subprocess.run(args, check=check, text=True, capture_output=True)


def ipt(*args, check=True):
    return run(['iptables', '-w', '10', *args], check=check)


def validate_source(site, value):
    address = ipaddress.IPv4Address(value)
    subnet = ipaddress.IPv4Network(SITES[site]['data'] + '/24', strict=False)
    if str(address) != value or address not in subnet or address in (subnet.network_address, subnet.broadcast_address):
        raise ValueError('additional source must be an exact app-subnet IPv4 address')


def app_sources(site):
    sources = [SITES[site]['app']]
    path = WORK / 'additional-app-sources.json'
    if path.exists():
        info = path.lstat()
        if path.is_symlink() or info.st_uid != 0 or info.st_mode & 0o077:
            raise ValueError('unsafe additional source configuration')
        config = json.loads(path.read_text())
        if config.get('site') != site or not isinstance(config.get('sources'), list):
            raise ValueError('invalid additional source configuration')
        for value in config['sources']:
            validate_source(site, value)
            if value not in sources:
                sources.append(value)
    return sources


def rules(site, forward):
    row = SITES[site]
    result = []
    for port in ('5432', '6379'):
        match = (['-p', 'tcp', '-m', 'conntrack', '--ctdir', 'ORIGINAL', '--ctorigdst', row['data'],
                  '--ctorigdstport', port] if forward else
                 ['-p', 'tcp', '-d', row['data'], '--dport', port])
        # Local host access is already outside DOCKER-USER; INPUT covers proxy fallback.
        allowed = app_sources(site)
        if not forward:
            allowed += [row['data'], '127.0.0.1']
        for source in allowed:
            result.append(match + ['-s', source + '/32', '-j', 'RETURN'])
        result.append(match + ['-j', 'DROP'])
    result.append(['-j', 'RETURN'])
    return result


def validate(site):
    row = SITES[site]
    if os.geteuid() != 0 or socket.gethostname() != row['host']:
        raise ValueError('data host identity mismatch')
    interfaces = json.loads(run(['ip', '-j', '-4', 'address', 'show']).stdout)
    addresses = [a['local'] for interface in interfaces for a in interface.get('addr_info', [])]
    if row['data'] not in addresses:
        raise ValueError('data private IP mismatch')
    ipt('-S', 'DOCKER-USER')


def enforce(site):
    for parent, chain in CHAINS:
        if ipt('-S', chain, check=False).returncode:
            ipt('-N', chain)
        # Rebuild owned chain atomically: no empty-chain allow window on restarts.
        lines = ['*filter', ':' + chain + ' - [0:0]', '-F ' + chain]
        for rule in rules(site, parent == 'DOCKER-USER'):
            lines.append('-A ' + chain + ' ' + ' '.join(rule))
        lines.append('COMMIT')
        subprocess.run(['iptables-restore', '--wait', '10', '--noflush'],
                       input='\n'.join(lines) + '\n', text=True, check=True, capture_output=True)
        if ipt('-C', parent, '-j', chain, check=False).returncode:
            ipt('-I', parent, '1', '-j', chain)


def remove():
    for parent, chain in CHAINS:
        while ipt('-C', parent, '-j', chain, check=False).returncode == 0:
            ipt('-D', parent, '-j', chain)
        if ipt('-S', chain, check=False).returncode == 0:
            ipt('-F', chain)
            ipt('-X', chain)


def save(state):
    path = WORK / 'receipt.json'
    temporary = WORK / 'receipt.tmp'
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(state, stream, sort_keys=True, indent=2)
        stream.flush(); os.fsync(stream.fileno())
    temporary.replace(path)


def service(site):
    return ('[Unit]\nDescription=TokenKey data service source allowlist\nRequires=docker.service\n'
            'After=docker.service\nPartOf=docker.service\n[Service]\nType=oneshot\nRemainAfterExit=yes\n'
            f'ExecStart=/usr/bin/python3 {PROGRAM} --site {site} --phase enforce\n'
            '[Install]\nWantedBy=docker.service\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--site', choices=tuple(SITES), required=True)
    parser.add_argument('--phase', choices=('plan', 'stage', 'commit', 'rollback', 'enforce', 'add-source'), default='plan')
    parser.add_argument('--token', default='')
    parser.add_argument('--source', default='')
    args = parser.parse_args()
    validate(args.site)
    if args.phase == 'plan':
        print(json.dumps({'site': args.site, **SITES[args.site], 'forward': rules(args.site, True),
                          'input': rules(args.site, False)}))
        return
    if WORK.is_symlink():
        raise ValueError('unsafe receipt directory')
    WORK.mkdir(mode=0o700, exist_ok=True)
    if WORK.stat().st_uid != 0 or WORK.stat().st_mode & 0o077:
        raise ValueError('unsafe receipt directory permissions')
    commit_requested = False
    with (WORK / 'lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        receipt = WORK / 'receipt.json'
        state = json.loads(receipt.read_text()) if receipt.exists() else None
        if state and state['site'] != args.site:
            raise ValueError('site changed since staging')
        if args.phase == 'stage':
            if state and state['status'] != 'rolled-back':
                raise ValueError('existing ACL receipt requires explicit resolution')
            # Refuse pre-existing chains; never claim another installation's rules.
            if any(ipt('-S', chain, check=False).returncode == 0 for _, chain in CHAINS):
                raise ValueError('unowned ACL chain already exists')
            PROGRAM.parent.mkdir(parents=True, exist_ok=True)
            PROGRAM.write_bytes(Path(__file__).read_bytes()); PROGRAM.chmod(0o644)
            SERVICE.write_text(service(args.site))
            token = uuid.uuid4().hex
            state = {'site': args.site, 'status': 'pending', 'token': token, 'staged_at': time.time()}
            save(state)
            # Arm rollback before touching packet rules. Timer persists across SSM loss.
            run(['systemd-run', '--unit=tokenkey-data-acl-rollback-' + token, '--on-active=5m',
                 '/usr/bin/python3', str(PROGRAM), '--site', args.site, '--phase', 'rollback', '--token', token])
            enforce(args.site)
        elif args.phase == 'add-source':
            if not state or state['status'] != 'committed':
                raise ValueError('source addition requires a committed ACL')
            validate_source(args.site, args.source)
            sources = app_sources(args.site)
            if args.source not in sources:
                sources.append(args.source)
            path = WORK / 'additional-app-sources.json'
            temporary = WORK / 'additional-app-sources.tmp'
            fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
            with os.fdopen(fd, 'w') as stream:
                json.dump({'site': args.site, 'sources': sources}, stream)
                stream.flush(); os.fsync(stream.fileno())
            temporary.replace(path)
            enforce(args.site)
            state['additional_app_sources'] = sources
            save(state)
        elif args.phase == 'enforce':
            if not state or state['status'] != 'committed':
                raise ValueError('persistent ACL has no committed receipt')
            enforce(args.site)
        else:
            if not state or not args.token or state['token'] != args.token:
                raise ValueError('receipt token mismatch')
            if args.phase == 'rollback':
                if state['status'] != 'pending':
                    print(json.dumps({'status': 'rollback-skipped', 'reason': 'receipt no longer pending'}))
                    return
                remove()
                run(['systemctl', 'disable', '--now', 'tokenkey-data-source-acl.service'], check=False)
                state['status'] = 'rolled-back'; save(state)
            elif args.phase == 'commit':
                if state['status'] != 'pending':
                    raise ValueError('only pending ACL can be committed')
                enforce(args.site)
                run(['systemctl', 'daemon-reload'])
                # Service ExecStart acquires this same lock. Release it before start.
                run(['systemctl', 'enable', 'tokenkey-data-source-acl.service'])
                state['status'] = 'committed'; save(state)
                commit_requested = True
    if commit_requested:
        try:
            run(['systemctl', 'start', 'tokenkey-data-source-acl.service'])
            run(['systemctl', 'is-active', '--quiet', 'tokenkey-data-source-acl.service'])
        except subprocess.SubprocessError:
            # Roll back immediately even if the timer already observed committed.
            with (WORK / 'lock').open('w') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                remove()
                run(['systemctl', 'disable', '--now', 'tokenkey-data-source-acl.service'], check=False)
                state['status'] = 'rolled-back'; save(state)
            raise
        run(['systemctl', 'stop', 'tokenkey-data-acl-rollback-' + args.token + '.timer'])
    print(json.dumps(state, sort_keys=True))



if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError):
        raise SystemExit('data source ACL failed; inspect private receipt; pending rules auto-rollback in five minutes') from None
