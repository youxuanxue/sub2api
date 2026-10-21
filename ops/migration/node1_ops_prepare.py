#!/usr/bin/env python3
"""Install dormant Node1 host monitoring; never enable timers or send messages."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

if __package__:
    from .node1_candidate import host_guard
else:
    from node1_candidate import host_guard

FILES = ('host_https_watch.py', 'edge_https_health.py', 'edge_health_delivery.py',
         'edge_model_health_alert.py', 'tokenkey-disk-metrics-edge.sh')
APPROVAL = '/etc/tokenkey/node1-prod-approved.json'


def install_owned(path, content, mode=0o644, previous=None):
    if path.is_symlink():
        raise RuntimeError('refusing an existing different host artifact: ' + str(path))
    if path.exists():
        old = path.read_bytes()
        if old == content:
            return
        if not previous or previous.get(str(path)) != hashlib.sha256(old).hexdigest():
            raise RuntimeError('refusing an existing different host artifact: ' + str(path))
        install_owned(path.with_name(path.name + '.before-' + hashlib.sha256(old).hexdigest()[:16]), old, mode)
    temporary = path.with_name(path.name + '.preparation-tmp')
    with temporary.open('xb') as output:
        output.write(content)
    temporary.chmod(mode)
    temporary.replace(path)


def prepare(bundle, previous=None):
    inventory = host_guard()
    if Path(APPROVAL).exists():
        raise RuntimeError('preparation is forbidden after activation')
    for name in ('tokenkey-pgdump', 'tokenkey-full-history-backup',
                 'tokenkey-disk-metrics', 'tokenkey-https-watch'):
        for suffix in ('.service', '.timer'):
            state = subprocess.check_output(['systemctl', 'show', name + suffix,
                '--property=ActiveState,UnitFileState,DropInPaths'], text=True)
            values = dict(line.split('=', 1) for line in state.splitlines())
            allowed = ('', 'disabled', 'static') if suffix == '.service' else ('', 'disabled')
            if (values.get('ActiveState') != 'inactive' or values.get('DropInPaths')
                    or values.get('UnitFileState', '') not in allowed):
                raise RuntimeError('host jobs must be inactive, disabled and without overrides')
    config = {'node': 'node1', 'targets': {'ucloud-cn-prod': 'https://' + inventory['domain'] + '/health'},
              'jobs': ['tokenkey-pgdump', 'tokenkey-full-history-backup', 'tokenkey-disk-metrics']}
    material = {name: (bundle / name).read_bytes() for name in FILES}
    material['watch.json'] = json.dumps(config, sort_keys=True).encode()
    digest = hashlib.sha256(b''.join(name.encode() + b'\0' + data + b'\0'
                                   for name, data in sorted(material.items()))).hexdigest()[:16]
    base = Path('/usr/local/lib/tokenkey/node1-ops') / digest
    base.mkdir(parents=True, mode=0o700, exist_ok=True)
    for name, content in material.items():
        install_owned(base / name, content, 0o600 if name == 'watch.json' else 0o700)
    for source, target in [('tokenkey-pgdump.sh', 'node1-tokenkey-pgdump.sh'),
                           ('node1_precious_backup.py', 'node1_precious_backup.py')]:
        install_owned(Path('/usr/local/lib/tokenkey') / target, (bundle / source).read_bytes(), 0o700)
    alerts = Path('/etc/tokenkey/node1-alerts.env')
    if alerts.stat().st_mode & 0o077 or alerts.stat().st_uid != 0:
        raise RuntimeError('notification configuration must be root-private')
    check = subprocess.run(['bash', str(base / 'tokenkey-disk-metrics-edge.sh'), '--selftest'],
                           capture_output=True)
    if check.returncode:
        raise RuntimeError('shared host metrics self-test failed')
    common = ('[Unit]\nAfter=network-online.target\nWants=network-online.target\n'
              'RequiresMountsFor=/var/lib/tokenkey\nConditionPathExists=' + APPROVAL + '\n')
    units = {
        'tokenkey-pgdump.service': common + '''
[Service]
Type=oneshot
UMask=0077
ExecStart=/usr/bin/python3 /usr/local/lib/tokenkey/node1_precious_backup.py
Nice=19
CPUQuota=100%
MemoryMax=1G
TimeoutStartSec=45min
''',
        'tokenkey-disk-metrics.service': common + '''
[Service]
Type=oneshot
UMask=0077
Environment=TOKENKEY_HOST_ALERT_ENV_FILE=/etc/tokenkey/node1-alerts.env
Environment=TOKENKEY_DATA_VOLUME_PATH=/var/lib/tokenkey
ExecStart=/bin/bash ''' + str(base / 'tokenkey-disk-metrics-edge.sh') + '''
TimeoutStartSec=60
Nice=10
''',
        'tokenkey-https-watch.service': common + '''
[Service]
Type=oneshot
UMask=0077
Environment=PYTHONDONTWRITEBYTECODE=1
ExecStart=/usr/bin/python3 ''' + str(base / 'host_https_watch.py') + ' --config ' + str(base / 'watch.json') + ''' --env-file /etc/tokenkey/node1-alerts.env --state /var/lib/tokenkey/https-watch/state.json
TimeoutStartSec=110
Nice=10
CPUQuota=10%
MemoryMax=128M
''',
    }
    for name, seconds in [('tokenkey-disk-metrics', 300), ('tokenkey-https-watch', 120)]:
        units[name + '.timer'] = ('[Unit]\nConditionPathExists=' + APPROVAL + '\n'
            '[Timer]\nOnBootSec=90\nOnUnitActiveSec=' + str(seconds) + '\n'
            'Unit=' + name + '.service\n[Install]\nWantedBy=timers.target\n')
    units['tokenkey-pgdump.timer'] = ('[Unit]\nConditionPathExists=' + APPROVAL + '\n'
        '[Timer]\nOnCalendar=*-*-* 00/2:15:00 UTC\nRandomizedDelaySec=60\n'
        'Persistent=true\nUnit=tokenkey-pgdump.service\n[Install]\nWantedBy=timers.target\n')
    paths = []
    for name, content in units.items():
        path = Path('/etc/systemd/system') / name
        install_owned(path, content.encode(), previous=previous)
        paths.append(str(path))
    subprocess.run(['systemd-analyze', 'verify', *paths], check=True)
    subprocess.run(['systemctl', 'daemon-reload'], check=True)
    for name in ('tokenkey-pgdump', 'tokenkey-full-history-backup',
                 'tokenkey-disk-metrics', 'tokenkey-https-watch'):
        # Safe to exercise the systemd condition: no ExecStart can run without approval.
        subprocess.run(['systemctl', 'start', name + '.service'], check=True)
        state = subprocess.check_output(['systemctl', 'show', name + '.service',
                    '--property=ConditionResult,ActiveState'], text=True)
        if 'ConditionResult=no' not in state or 'ActiveState=inactive' not in state:
            raise RuntimeError('preapproval service guard failed')
        timer = subprocess.check_output(['systemctl', 'show', name + '.timer',
                    '--property=ActiveState,UnitFileState'], text=True)
        if 'ActiveState=inactive' not in timer or 'UnitFileState=disabled' not in timer:
            raise RuntimeError('prepared timer must remain disabled')
    return {'prepared': True, 'bundle': str(base), 'timers_enabled': False,
            'preapproval_start_blocked': True, 'notifications_sent': 0,
            'public_probe_acceptance_pending': True,
            'unit_hashes': {p: hashlib.sha256(Path(p).read_bytes()).hexdigest() for p in paths}}


if __name__ == '__main__':
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', type=Path, required=True)
    parser.add_argument('--previous-units', type=Path,
                        help='Reviewed path-to-SHA256 manifest for replacing dormant owned units')
    args = parser.parse_args()
    previous = json.loads(args.previous_units.read_text()) if args.previous_units else None
    print(json.dumps(prepare(args.bundle, previous)))
