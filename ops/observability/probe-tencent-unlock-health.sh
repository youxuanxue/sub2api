#!/bin/bash
# Read-only unlock/mount/docker health signals. Never unlocks, remounts, or starts writers.
# Repo copy of the operator helper helpers/unlock-health-readonly.sh (2026-10-22, no key material).
#   bash ops/observability/run-probe.sh --target prod --script ops/observability/probe-tencent-unlock-health.sh
set -euo pipefail
set +x
[ "$(hostname)" = VM-0-16-ubuntu ]

python3 - <<'PY'
import json, pathlib, subprocess, time

def sh(*args):
    return subprocess.run(args, capture_output=True, text=True)

root = pathlib.Path('/srv/tokenkey-rehearsal')
mapper = '/dev/mapper/tokenkey_recovery'
findmnt = sh('findmnt', '-n', '-o', 'SOURCE,TARGET,FSTYPE', str(root))
src = (findmnt.stdout.split() or [''])[0] if findmnt.returncode == 0 else ''
mounted = findmnt.returncode == 0 and src == mapper
mapper_exists = pathlib.Path(mapper).exists()

docker_root = None
dr = sh('docker', 'info', '-f', '{{.DockerRootDir}}')
if dr.returncode == 0:
    docker_root = dr.stdout.strip()
docker_on_luks = bool(docker_root and docker_root.startswith(str(root)))

swap = sh('swapon', '--show=NAME', '--noheadings')
swap_empty = swap.returncode == 0 and not swap.stdout.strip()

# Post-cutover live writer is primary (pg_is_in_recovery=false). Only alert if probe fails.
standby = None
pg_primary = None
st = sh('docker', 'exec', 'tk-archive-standby', 'psql', '-U', 'tokenkey', '-d', 'tokenkey',
        '-XAtq', '-c', 'SELECT pg_is_in_recovery();')
if st.returncode == 0:
    standby = st.stdout.strip() == 't'
    pg_primary = not standby

formal_env = (root / 'formal' / '.env').is_file()
layout = {}
lp = root / 'formal' / 'LAYOUT.json'
if lp.is_file():
    layout = json.loads(lp.read_text())

alerts = []
if not mapper_exists:
    alerts.append('mapper_missing')
if not mounted:
    alerts.append('luks_mount_missing_or_wrong_source')
if not docker_on_luks:
    alerts.append('docker_root_not_on_luks')
if not swap_empty:
    alerts.append('swap_present')

var_lib = pathlib.Path('/var/lib/tokenkey')
fm2 = sh('findmnt', '-n', '-o', 'SOURCE', str(var_lib))
var_lib_on_luks = fm2.returncode == 0 and (fm2.stdout.strip() == mapper)
if not var_lib_on_luks:
    alerts.append('var_lib_tokenkey_not_on_luks')
if standby is None:
    alerts.append('postgres_recovery_probe_failed')
if layout.get('cutover_authorized') is True:
    alerts.append('cutover_authorized_true_unexpected')

severity = 'ok' if not alerts else ('critical' if any(a.startswith('mapper') or a.startswith('luks') or a.startswith('docker') for a in alerts) else 'warn')

print(json.dumps({
    'at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
    'kind': 'unlock-health-readonly',
    'hostname': pathlib.Path('/etc/hostname').read_text().strip(),
    'mapper_exists': mapper_exists,
    'mounted_on_luks': mounted,
    'findmnt_source': src or None,
    'docker_root': docker_root,
    'docker_on_luks': docker_on_luks,
    'swap_empty': swap_empty,
    'standby_in_recovery': standby,
    'pg_primary': pg_primary,
    'var_lib_on_luks': var_lib_on_luks,
    'formal_env_present': formal_env,
    'production_secrets': layout.get('production_secrets'),
    'boot_order_required': ['unlock', 'mount', 'docker', 'standby_or_app'],
    'alerts': alerts,
    'severity': severity,
    'cutover_authorized': False,
}))
PY
