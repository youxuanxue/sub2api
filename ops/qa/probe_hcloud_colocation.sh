#!/usr/bin/env bash
# Read-only host facts for QA colocation. Never print container env or credentials.
set -euo pipefail
python3 - <<'PY'
import json
import os
import pathlib
import shutil
import subprocess

def command(*args):
    return subprocess.check_output(args, text=True, timeout=20).strip()

info = json.loads(command('docker', 'info', '--format', '{{json .}}'))
ids = command('docker', 'ps', '-q').split()
containers = json.loads(command('docker', 'inspect', *ids)) if ids else []
mem = dict(line.split(':', 1) for line in pathlib.Path('/proc/meminfo').read_text().splitlines())
result = {
    'hostname': command('hostname'),
    'architecture': command('uname', '-m'),
    'kernel': command('uname', '-r'),
    'cpus': os.cpu_count(),
    'mem_available_bytes': int(mem['MemAvailable'].split()[0]) * 1024,
    'root_available_bytes': shutil.disk_usage('/').free,
    'kvm_device_present': pathlib.Path('/dev/kvm').exists(),
    'cgroup_version': info.get('CgroupVersion'),
    'cgroup_driver': info.get('CgroupDriver'),
    'synthetic_slice_files': [p.name for p in pathlib.Path('/run/systemd/system').glob('tkqaverify*.slice')],
    'security_options': info.get('SecurityOptions'),
    'runtimes': sorted(info.get('Runtimes', {})),
    'synthetic_containers': command('docker', 'ps', '-aq', '--filter', 'label=tokenkey.io/role=qa-isolation-prototype').split(),
    'apparmor_enabled': pathlib.Path('/sys/module/apparmor/parameters/enabled').read_text().strip() if pathlib.Path('/sys/module/apparmor/parameters/enabled').exists() else None,
    'containers': [],
}
for c in containers:
    h = c['HostConfig']
    result['containers'].append({
        'name': c['Name'].lstrip('/'), 'image_id': c['Image'],
        'started_at': c['State']['StartedAt'], 'restart_count': c['RestartCount'],
        'user': c['Config']['User'], 'privileged': h['Privileged'],
        'network_mode': h['NetworkMode'], 'pid_mode': h['PidMode'],
        'readonly_rootfs': h['ReadonlyRootfs'], 'cap_add': h['CapAdd'],
        'memory_bytes': h['Memory'], 'nano_cpus': h['NanoCpus'],
        'pids_limit': h['PidsLimit'],
        'docker_socket_mounted': any(m['Source'].endswith('/docker.sock') for m in c['Mounts']),
        'mount_destinations': [m['Destination'] for m in c['Mounts']],
        'networks': sorted(c['NetworkSettings']['Networks']),
    })
print(json.dumps(result, separators=(',', ':')))
PY
