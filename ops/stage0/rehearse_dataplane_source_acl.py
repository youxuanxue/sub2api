#!/usr/bin/env python3
"""Real packets through INPUT and Docker-style DNAT in isolated net namespaces.

Run privileged in a disposable container with --network none, never on a host.
"""
import json
import subprocess
import sys
from pathlib import Path

from dataplane_source_acl import SITES, enforce, remove


def run(args):
    return subprocess.check_output(args, text=True)


def probe(namespace, source, target, port, expected):
    code = '''import socket,sys
s=socket.socket();s.settimeout(0.4);s.bind((sys.argv[1],0))
try:
 s.connect((sys.argv[2],int(sys.argv[3])));result=True
except OSError: result=False
assert result == (sys.argv[4]=='yes'), (sys.argv[1:],result)
'''
    run(['ip', 'netns', 'exec', namespace, sys.executable, '-c', code,
         source, target, str(port), 'yes' if expected else 'no'])


def main():
    if not Path('/.dockerenv').exists():
        raise SystemExit('requires a disposable privileged container')
    server = '''import socket,selectors
selector=selectors.DefaultSelector()
for port in (5432,6379,9999):
 s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0',port));s.listen();selector.register(s,selectors.EVENT_READ)
print('ready',flush=True)
while True:
 for key,_ in selector.select():
  c,_=key.fileobj.accept();c.close()
'''
    for site, row in SITES.items():
        run(['ip', 'netns', 'add', 'client']); run(['ip', 'netns', 'add', 'service'])
        processes = []
        try:
            for ns, hostif, peer, hostaddr, address in (
                ('client', 'client-host', 'client-peer', row['data'] + '/24', row['app'] + '/24'),
                ('service', 'service-host', 'service-peer', '172.25.0.1/24', '172.25.0.2/24')):
                run(['ip', 'link', 'add', hostif, 'type', 'veth', 'peer', 'name', peer])
                run(['ip', 'link', 'set', peer, 'netns', ns])
                run(['ip', 'addr', 'add', hostaddr, 'dev', hostif]); run(['ip', 'link', 'set', hostif, 'up'])
                run(['ip', 'netns', 'exec', ns, 'ip', 'link', 'set', 'lo', 'up'])
                run(['ip', 'netns', 'exec', ns, 'ip', 'addr', 'add', address, 'dev', peer])
                run(['ip', 'netns', 'exec', ns, 'ip', 'link', 'set', peer, 'up'])
                run(['ip', 'netns', 'exec', ns, 'ip', 'route', 'add', 'default', 'via', hostaddr.split('/')[0]])
            unauthorized = row['app'].rsplit('.', 1)[0] + '.99'
            run(['ip', 'netns', 'exec', 'client', 'ip', 'addr', 'add', unauthorized + '/24', 'dev', 'client-peer'])
            Path('/proc/sys/net/ipv4/ip_forward').write_text('1')
            run(['iptables', '-N', 'DOCKER-USER']); run(['iptables', '-A', 'FORWARD', '-j', 'DOCKER-USER'])
            run(['iptables', '-N', 'TK_RECOVERY_EGRESS']); run(['iptables', '-A', 'TK_RECOVERY_EGRESS', '-j', 'RETURN'])
            proc = subprocess.Popen(['ip', 'netns', 'exec', 'service', sys.executable, '-u', '-c', server], stdout=subprocess.PIPE, text=True)
            processes.append(proc); assert proc.stdout.readline().strip() == 'ready'
            nat = ['-d', row['data'], '-p', 'tcp', '-m', 'multiport', '--dports', '5432,6379,9999', '-j', 'DNAT', '--to-destination', '172.25.0.2']
            run(['iptables', '-t', 'nat', '-A', 'PREROUTING', *nat])
            enforce(site); enforce(site)
            for port in (5432, 6379):
                probe('client', row['app'], row['data'], port, True)
                probe('client', unauthorized, row['data'], port, False)
            probe('client', unauthorized, row['data'], 9999, True)
            run(['iptables', '-t', 'nat', '-D', 'PREROUTING', *nat])
            host_server = subprocess.Popen([sys.executable, '-u', '-c', server], stdout=subprocess.PIPE, text=True)
            processes.append(host_server); assert host_server.stdout.readline().strip() == 'ready'
            for port in (5432, 6379):
                probe('client', row['app'], row['data'], port, True)
                probe('client', unauthorized, row['data'], port, False)
            remove()
            probe('client', unauthorized, row['data'], 5432, True)
            assert run(['iptables', '-S', 'TK_RECOVERY_EGRESS']).strip().endswith('-A TK_RECOVERY_EGRESS -j RETURN')
            print(json.dumps({'site': site, 'allowed_source': 'passed', 'denied_source': 'passed',
                              'DNAT_and_INPUT': 'passed', 'idempotency_and_rollback': 'passed'}), flush=True)
        finally:
            for proc in processes:
                proc.terminate(); proc.wait(timeout=5)
            for ns in ('client', 'service'):
                subprocess.run(['ip', 'netns', 'del', ns], check=False)
            for table in ('nat', 'filter'):
                run(['iptables', '-t', table, '-F']); run(['iptables', '-t', table, '-X'])


if __name__ == '__main__':
    main()
