#!/usr/bin/env python3
"""Bounded synthetic verification of the approved QA limits on uk1.

Creates only a uniquely named transient slice and two empty, networkless
containers. Never mounts host data or supplies credentials. Always cleans up.
"""
import concurrent.futures
import json
import os
from pathlib import Path
import subprocess
import uuid

from colocation_resources import load_policy, slice_unit


def run(*args, check=True, timeout=45):
    return subprocess.run(args, capture_output=True, text=True, check=check, timeout=timeout)


def parse_counters(text):
    return dict((key, int(value)) for key, value in (line.split() for line in text.splitlines()))


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def verify():
    if os.environ.get('QA_COLOCATION_SYNTHETIC_OPT_IN') != '1':
        raise SystemExit('QA_COLOCATION_SYNTHETIC_OPT_IN=1 is required')
    if run('hostname').stdout.strip() != 'tokenkey-edge-uk1-hz-cax21':
        raise SystemExit('this experiment is authorized only on uk1')
    policy = load_policy(Path('/tmp/resource-policy.json'))
    available = int(next(x.split()[1] for x in Path('/proc/meminfo').read_text().splitlines() if x.startswith('MemAvailable:'))) * 1024
    if available < policy['aggregate']['memory_bytes'] + 2 * 1024**3:
        raise SystemExit('insufficient headroom for bounded QA experiment')
    info = json.loads(run('docker', 'info', '--format', '{{json .}}').stdout)
    if info['CgroupVersion'] != '2' or info['CgroupDriver'] != 'systemd':
        raise SystemExit('requires cgroup v2 and systemd Docker driver')
    image = run('docker', 'inspect', '--format', '{{.Image}}', 'tokenkey').stdout.strip()
    name = 'tkqaverify' + uuid.uuid4().hex[:12]
    unit = name + '.slice'
    unit_file = Path('/run/systemd/system') / unit
    containers = []
    unit_created = False
    result = None
    try:
        with unit_file.open('x') as handle:
            unit_created = True
            handle.write(slice_unit(policy))
        run('systemctl', 'daemon-reload')
        run('systemctl', 'start', unit)
        group = Path('/sys/fs/cgroup') / run('systemctl', 'show', '--value', '-p', 'ControlGroup', unit).stdout.strip().lstrip('/')
        aggregate = policy['aggregate']
        require((group / 'memory.max').read_text().strip() == str(aggregate['memory_bytes']), 'aggregate memory cap mismatch')
        quota, period = map(int, (group / 'cpu.max').read_text().split())
        require(quota * 1000 == period * aggregate['cpu_millis'], 'aggregate CPU quota mismatch')
        require((group / 'memory.swap.max').read_text().strip() == '0', 'aggregate swap must be disabled')
        require((group / 'pids.max').read_text().strip() == str(aggregate['pids']), 'aggregate PID cap mismatch')
        parent_throttled_before = parse_counters((group / 'cpu.stat').read_text())['nr_throttled']
        before = {}
        for component in ('worker', 'queue'):
            cap = policy[component]
            container = name + '-' + component
            # A client timeout can occur after Docker has created the container.
            containers.append(container)
            run('docker', 'create', '--name', container, '--pull=never', '--network', 'none',
                '--label', 'tokenkey.io/role=qa-isolation-prototype', '--user', '65534:65534',
                '--label', 'tokenkey.io/verification=' + name,
                '--cgroup-parent', unit, '--cpus', str(cap['cpu_millis'] / 1000),
                '--memory', str(cap['memory_bytes']), '--memory-swap', str(cap['memory_bytes']),
                '--pids-limit', str(cap['pids']), '--cap-drop', 'ALL', '--read-only',
                '--security-opt', 'no-new-privileges=true', '--security-opt', 'apparmor=docker-default',
                '--log-driver', 'none', '--no-healthcheck', '--entrypoint', '/bin/sh', image,
                '-c', 'sleep 120')
            run('docker', 'start', container)
            values = run('docker', 'exec', container, 'sh', '-c',
                         'cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.max /sys/fs/cgroup/pids.max /sys/fs/cgroup/cpu.max').stdout.splitlines()
            require(values[:3] == [str(cap['memory_bytes']), '0', str(cap['pids'])], 'component memory/swap/PID cap mismatch')
            quota, period = map(int, values[3].split())
            require(quota * 1000 == period * cap['cpu_millis'], 'component CPU quota mismatch')
            pid = run('docker', 'inspect', '--format', '{{.State.Pid}}', container).stdout.strip()
            require('/' + unit + '/' in Path('/proc', pid, 'cgroup').read_text(), 'container escaped aggregate slice')
            before[container] = parse_counters(run('docker', 'exec', container, 'cat', '/sys/fs/cgroup/cpu.stat').stdout)
        # Two bounded workloads together exercise the shared budget.
        def burn(container):
            result = run('docker', 'exec', container, 'sh', '-c',
                         "command -v timeout >/dev/null && timeout -s KILL 4 sh -c 'while :; do :; done'", check=False, timeout=12)
            if result.returncode != 137:
                raise RuntimeError('CPU workload did not terminate at its bound')
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            list(pool.map(burn, containers))
        throttled = {}
        for container in containers:
            after = parse_counters(run('docker', 'exec', container, 'cat', '/sys/fs/cgroup/cpu.stat').stdout)
            throttled[container] = after['nr_throttled'] - before[container]['nr_throttled']
        parent_throttled = parse_counters((group / 'cpu.stat').read_text())['nr_throttled'] - parent_throttled_before
        require(sum(throttled.values()) + parent_throttled > 0, 'CPU quota did not throttle the workload')
        queue = containers[1]
        oom_before = parse_counters(run('docker', 'exec', queue, 'cat', '/sys/fs/cgroup/memory.events').stdout)['oom_kill']
        allocation = run('docker', 'exec', queue, 'sh', '-c',
                         '''command -v awk >/dev/null && awk 'BEGIN { s=sprintf("%1048576s", ""); for(i=0;i<1024;i++) a[i]=s i; exit 42 }' ''',
                         check=False, timeout=35)
        oom_after = parse_counters(run('docker', 'exec', queue, 'cat', '/sys/fs/cgroup/memory.events').stdout)['oom_kill']
        require(allocation.returncode == 137 and oom_after > oom_before, 'queue memory limit did not contain allocation')
        result = {'passed': True, 'aggregate': aggregate, 'worker': policy['worker'], 'queue': policy['queue'],
                  'cpu_throttling_observed': True, 'queue_memory_oom_contained': True,
                  'worker_memory_limit_stress_tested': False, 'scratch_4g_quota_tested': False,
                  'production_ready': False}
    finally:
        failures = []
        for container in reversed(containers):
            try:
                owned = run('docker', 'inspect', '--format', '{{index .Config.Labels "tokenkey.io/verification"}}', container, check=False)
                if owned.returncode:
                    # Distinguish an absent container from an unavailable daemon.
                    run('docker', 'info', '--format', '{{.ServerVersion}}')
                    require('No such object' in owned.stderr, 'cannot establish container absence')
                else:
                    require(owned.stdout.strip() == name, 'refusing to remove an unowned container')
                    run('docker', 'rm', '--force', container)
            except Exception as exc:
                failures.append(container + ': ' + type(exc).__name__)
        if unit_created:
            try:
                run('systemctl', 'stop', unit)
                unit_file.unlink()
            except Exception as exc:
                failures.append(unit + ': ' + type(exc).__name__)
            try:
                run('systemctl', 'daemon-reload')
            except Exception as exc:
                failures.append('daemon-reload: ' + type(exc).__name__)
        if failures:
            raise RuntimeError('cleanup incomplete: ' + ', '.join(failures))
    result['cleaned_up'] = True
    print(json.dumps(result, separators=(',', ':')))


if __name__ == '__main__':
    verify()
