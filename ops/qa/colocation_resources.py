#!/usr/bin/env python3
"""QA resource policy owner; renders a slice and Compose resource override.

This is not a deployment entry point. Images, credential mounts, independent
storage with a hard scratch quota, TLS and deny-by-default egress are required
before a production runtime can use the override.
"""
import argparse
import json
from pathlib import Path


DEFAULT_POLICY = Path(__file__).resolve().parent.parent.parent / 'deploy/hetzner/qa/resource-policy.json'
SLICE = 'tokenkey-qa.slice'


def load_policy(path=DEFAULT_POLICY):
    policy = json.loads(Path(path).read_text())
    if policy['schema_version'] != 1 or policy['target'] != 'edge:uk1' or policy['concurrency'] != 1:
        raise ValueError('unsupported QA colocation policy')
    for scope in ('aggregate', 'worker', 'queue'):
        for key in ('cpu_millis', 'memory_bytes', 'pids'):
            if type(policy[scope][key]) is not int or policy[scope][key] <= 0:
                raise ValueError(f'{scope}.{key} must be a positive integer')
    for key in ('cpu_millis', 'memory_bytes', 'pids'):
        if policy['worker'][key] + policy['queue'][key] > policy['aggregate'][key]:
            raise ValueError(f'QA components exceed aggregate {key}')
    if policy['aggregate']['cpu_millis'] > 1000 or policy['aggregate']['memory_bytes'] > 2 * 1024**3:
        raise ValueError('QA policy exceeds the approved 1 CPU / 2 GiB ceiling')
    if policy['aggregate']['pids'] > 256:
        raise ValueError('QA policy exceeds the approved 256 PID ceiling')
    for key in ('scratch_bytes', 'log_max_bytes', 'log_max_files'):
        if type(policy[key]) is not int or policy[key] <= 0:
            raise ValueError(f'{key} must be a positive integer')
    if policy['scratch_bytes'] > 4 * 1024**3:
        raise ValueError('QA scratch exceeds the approved 4 GiB ceiling')
    return policy


def slice_unit(policy):
    cap = policy['aggregate']
    return f'''[Unit]
Description=TokenKey QA aggregate resource boundary

[Slice]
CPUAccounting=yes
CPUQuota={cap['cpu_millis'] / 10:g}%
MemoryAccounting=yes
MemoryMax={cap['memory_bytes']}
MemorySwapMax=0
TasksAccounting=yes
TasksMax={cap['pids']}
'''


def resource_override(policy):
    services = {}
    for component in ('worker', 'queue'):
        cap = policy[component]
        services['qa-' + component] = {
            'cgroup_parent': SLICE,
            'cpus': cap['cpu_millis'] / 1000,
            'mem_limit': cap['memory_bytes'],
            'memswap_limit': cap['memory_bytes'],
            'pids_limit': cap['pids'],
            'cap_drop': ['ALL'],
            'security_opt': ['no-new-privileges:true', 'apparmor:docker-default'],
            'read_only': True,
            'logging': {'driver': 'local', 'options': {
                'max-size': str(policy['log_max_bytes']), 'max-file': str(policy['log_max_files'])}},
        }
    # Worker replacement must drain first; Docker's default 10 seconds is unsafe.
    services['qa-worker']['stop_grace_period'] = '56m'
    services['qa-worker']['environment'] = {'GOMAXPROCS': '1', 'GOMEMLIMIT': '1280MiB'}
    return {'services': services}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--policy', type=Path, default=DEFAULT_POLICY)
    parser.add_argument('--format', choices=('slice', 'compose'), required=True)
    args = parser.parse_args()
    policy = load_policy(args.policy)
    print(slice_unit(policy) if args.format == 'slice' else json.dumps(resource_override(policy), indent=2))


if __name__ == '__main__':
    main()
