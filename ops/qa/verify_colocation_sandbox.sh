#!/usr/bin/env bash
# Synthetic, bounded experiment only. No credentials, app traffic, or host mounts.
# A passing result is NOT production QA readiness or independent-kernel isolation.
set -euo pipefail
if [[ "${QA_COLOCATION_SYNTHETIC_OPT_IN:-}" != 1 ]]; then
  echo 'Set QA_COLOCATION_SYNTHETIC_OPT_IN=1 to run the disposable experiment.' >&2
  exit 2
fi
python3 - <<'PY'
import json
import subprocess
import uuid

def run(args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=45, **kwargs)

if run(['hostname']).stdout.strip() != 'tokenkey-edge-uk1-hz-cax21':
    raise SystemExit('this experiment is authorized only on uk1')

image = run(['docker', 'inspect', '--format', '{{.Image}}', 'tokenkey']).stdout.strip()
name = 'tokenkey-qa-isolation-check-' + uuid.uuid4().hex[:12]
# Use the already-present immutable image, bypass its application entrypoint.
# Do not pull, attach to an Edge network, mount host data, or publish any port.
checks = r'''
set -eu
for tool in touch mount wget dd; do command -v "$tool" >/dev/null; done
test "$(id -u)" = 65534
test "$(awk '/^CapEff:/ {print $2}' /proc/self/status)" = 0000000000000000
test "$(awk '/^NoNewPrivs:/ {print $2}' /proc/self/status)" = 1
test "$(awk '/^Seccomp:/ {print $2}' /proc/self/status)" = 2
grep -q 'docker-default (enforce)' /proc/self/attr/current
if touch /qa-forbidden-write 2>/tmp/root-write-error; then exit 11; fi
grep -q 'Read-only file system' /tmp/root-write-error
test ! -e /var/run/docker.sock
test ! -e /run/docker.sock
test ! -e /var/lib/tokenkey/.env
test ! -e /var/lib/tokenkey/aws/credentials
test ! -e /dev/kvm
test "$(ls /sys/class/net)" = lo
if wget -q -T 1 -O /tmp/network-response http://192.0.2.1/ 2>/dev/null; then exit 14; fi
test "$(cat /sys/fs/cgroup/memory.max)" = 100663296
test "$(cat /sys/fs/cgroup/memory.swap.max)" = 0
test "$(cat /sys/fs/cgroup/pids.max)" = 32
test "$(cat /sys/fs/cgroup/cpu.max)" = '25000 100000'
mkdir /tmp/test-mount
if mount -t tmpfs tmpfs /tmp/test-mount 2>/dev/null; then exit 12; fi
printf 'synthetic qa data\n' > /tmp/qa-write
test "$(cat /tmp/qa-write)" = 'synthetic qa data'
if dd if=/dev/zero of=/tmp/qa-full bs=65536 count=32 2>/dev/null; then exit 13; fi
rm /tmp/qa-full
printf 'container_boundary_checks_passed\n'
'''
create_attempted = False
try:
    # Docker may create the container even if the client times out.
    create_attempted = True
    run(['docker', 'create', '--name', name, '--pull=never',
         '--label', 'tokenkey.io/role=qa-isolation-prototype',
         '--label', 'tokenkey.io/verification=' + name,
         '--network', 'none', '--user', '65534:65534', '--read-only',
         '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges=true',
         '--security-opt', 'apparmor=docker-default',
         '--memory', '96m', '--memory-swap', '96m', '--cpus', '0.25',
         '--pids-limit', '32', '--ipc', 'none', '--log-driver', 'none', '--no-healthcheck',
         '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=1m,mode=1777',
         '--entrypoint', '/bin/sh', image, '-c', checks])
    result = run(['docker', 'start', '--attach', name])
    state = json.loads(run(['docker', 'inspect', name]).stdout)[0]
    if state['State']['ExitCode'] != 0 or 'container_boundary_checks_passed' not in result.stdout:
        raise RuntimeError('sandbox checks failed: ' + str(state['State']['ExitCode']) + ' ' + result.stderr)
    if state['Mounts'] or state['HostConfig']['Binds']:
        raise RuntimeError('unexpected data mounts in sandbox')
    receipt = {
        'container': name,
        'checks': ['nonroot', 'no_capabilities', 'no_new_privileges', 'seccomp',
                   'apparmor_enforcing', 'readonly_root', 'no_host_mounts',
                   'no_docker_socket', 'loopback_only', 'external_connect_denied', 'memory_limit',
                   'swap_disabled', 'pids_limit', 'cpu_quota', 'mount_denied',
                   'scratch_write', 'scratch_enospc'],
        'passed': True,
        'independent_kernel': False,
        'production_ready': False,
        'limitations': ['no production egress or queue path tested',
                        'CPU/memory/PID limits read from cgroup; not stress-tested',
                        'shared kernel, Docker daemon, host storage and failure domain'],
    }
finally:
    if create_attempted:
        remaining = run(['docker', 'ps', '-aq', '--filter', 'name=^/' + name + '$']).stdout.strip()
        if remaining:
            owner = run(['docker', 'inspect', '--format', '{{index .Config.Labels "tokenkey.io/verification"}}', name]).stdout.strip()
            if owner != name:
                raise RuntimeError('refusing to remove an unowned sandbox')
            run(['docker', 'rm', '--force', name])
        remaining = run(['docker', 'ps', '-aq', '--filter', 'name=^/' + name + '$']).stdout.strip()
        if remaining:
            raise RuntimeError('sandbox cleanup incomplete')
receipt['cleaned_up'] = True
print(json.dumps(receipt, separators=(',', ':')))
PY
