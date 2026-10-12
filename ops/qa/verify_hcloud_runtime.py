#!/usr/bin/env python3
"""Read-only acceptance of the isolated uk1 queue/worker (no stress/load)."""
import json
from pathlib import Path
import subprocess
from urllib.parse import urlparse, parse_qs
try:
    from .hcloud_host import guard, owned_container, status, check_mount, check_slice, ROOT
    from .colocation_resources import load_policy
    from .hcloud_runtime import QUEUE_IP
except ImportError:
    from hcloud_host import guard, owned_container, status, check_mount, check_slice, ROOT
    from colocation_resources import load_policy
    from hcloud_runtime import QUEUE_IP


SELF_CHECK_EXEMPT = {'consumer_query': 'Executes caller SQL via psql; does not generate SQL.'}

def iter_self_check_sql():
    return []


def exec_container(name, *args):
    return subprocess.run(['docker','exec',name,*args],capture_output=True,text=True,timeout=15)


def consumer_query(query):
    # libpq does not expand a URI supplied through PGDATABASE. A private service
    # file keeps credentials out of argv, Docker configuration and diagnostics.
    dsn = urlparse((ROOT/'worker-secrets/queue.dsn').read_text().strip())
    options = parse_qs(dsn.query)
    fields = {'host': dsn.hostname, 'port': str(dsn.port), 'user': dsn.username,
              'password': dsn.password, 'dbname': dsn.path.lstrip('/'),
              'sslmode': 'verify-full', 'sslrootcert': options['sslrootcert'][0],
              'connect_timeout': '5'}
    if any('\n' in str(v) or '\r' in str(v) for v in fields.values()):
        raise RuntimeError('unexpected queue service credential format')
    content = '[verify]\n' + ''.join(f'{k}={v}\n' for k,v in fields.items())
    script = "umask 077; file=$(mktemp /tmp/qa-verify.XXXXXX); trap 'rm -f \"$file\"' EXIT; cat >\"$file\"; PGSERVICEFILE=\"$file\" psql service=verify -XAt -v ON_ERROR_STOP=1 -c \"$1\""
    return subprocess.run(['docker','exec','-i','tokenkey-qa-worker','sh','-c',script,'verify',query],
                          input=content,capture_output=True,text=True,timeout=15)


def require(value, name):
    if not value:
        raise RuntimeError(name)


def main():
    guard()
    policy = load_policy(ROOT/'resource-policy.json')
    checks = {}
    check_slice(policy)
    checks['parent_resource_limits'] = True
    for component, uid in [('queue','70:70'),('worker','1000:1000')]:
        name = 'tokenkey-qa-' + component
        info = owned_container(name)
        host = info['HostConfig']
        require(info['State']['Running'] and not info['State']['OOMKilled'], 'QA container is not running')
        require(info['Config']['User'] == uid, 'QA user differs')
        require(host['Memory'] == policy[component]['memory_bytes'] and host['MemorySwap'] == host['Memory'], 'memory/swap limit differs')
        require(host['NanoCpus'] == policy[component]['cpu_millis'] * 1000000, 'CPU limit differs')
        require(host['PidsLimit'] == policy[component]['pids'] and host['CgroupParent'] == 'tokenkey-qa.slice','PID/parent differs')
        require(host['ReadonlyRootfs'] and host['CapDrop'] == ['ALL'],'privileged QA filesystem/capabilities')
        require('no-new-privileges:true' in host['SecurityOpt'] and info['AppArmorProfile'] == 'docker-default','QA sandbox differs')
        require(all(m['Source'].startswith(str(ROOT)+'/') for m in info['Mounts'] if m['Type']=='bind'),'foreign mount')
        group = Path(f'/proc/{info["State"]["Pid"]}/cgroup').read_text()
        require('tokenkey-qa.slice' in group, 'actual cgroup placement differs')
        checks[component+'_sandbox'] = True
    check_mount('scratch',policy['scratch_bytes'])
    check_mount('queue',policy['queue_disk_bytes'])
    checks['bounded_filesystems'] = True
    state = status()
    require(state['paused'] and state['active_leases'] == 0,'verification requires a paused drained queue')
    query = "SELECT json_build_object('user',current_user,'version',qa_queue.version(),'tls',(SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()))"
    response = consumer_query(query)
    require(response.returncode == 0 and json.loads(response.stdout)=={'user':'qa_consumer','version':1,'tls':True},'consumer TLS connection failed')
    for query in ('SELECT count(*) FROM qa_queue.jobs','UPDATE qa_queue.control SET paused=false',
                  "SELECT qa_queue.enqueue('qa-bundles/v1/jobs/"+'0'*64+"/spec.json')"):
        response = consumer_query(query)
        require(response.returncode != 0 and 'permission denied' in response.stderr,'consumer exceeded role')
    checks['consumer_tls_and_role_denial'] = True
    require(exec_container('tokenkey-qa-worker','nc','-z','-w','2',QUEUE_IP,'5432').returncode == 0,
            'TCP probe cannot establish allowed queue connection')
    targets = [('169.254.169.254',80),('1.1.1.1',443),('172.30.240.1',22),('188.245.112.147',22)]
    gateway = None
    for name,port in [('tokenkey-blue',8080),('tokenkey-green',8080),('tokenkey-postgres',5432),('tokenkey-redis',6379)]:
        p = subprocess.run(['docker','inspect',name],capture_output=True,text=True)
        if p.returncode:
            continue
        info = json.loads(p.stdout)[0]
        if info['State']['Running']:
            for network in info['NetworkSettings']['Networks'].values():
                targets.append((network['IPAddress'],port))
            if port==8080:
                gateway=name
    for address,port in targets:
        result = exec_container('tokenkey-qa-worker','nc','-z','-w','2',address,str(port))
        require(result.returncode != 0,'QA escaped egress boundary')
    require(gateway is not None,'gateway container not found')
    require(exec_container(gateway,'nc','-z','-w','2','127.0.0.1','8080').returncode == 0,
            'reverse TCP probe cannot establish its allowed control connection')
    reverse = exec_container(gateway,'nc','-z','-w','2',QUEUE_IP,'5432')
    require(reverse.returncode != 0,'Edge gateway can reach QA queue')
    checks['host_edge_metadata_public_and_reverse_denied'] = True
    # An authenticated object-store roundtrip is performed separately using synthetic keys.
    print(json.dumps({'checks':checks,'queue':state,'production_cutover_ready':False}))


if __name__=='__main__':
    main()
