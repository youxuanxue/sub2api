#!/usr/bin/env python3
"""Render maintenance and old-address forwarding configs; never install/reload.

Inputs must be freshly captured runtime Caddyfiles from both sides. Receipt hashes
bind the later operator action to the inspected inputs. Revalidate at cutover.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[2]


def replace_proxy(text, replacement):
    lines = text.splitlines(keepends=True)
    starts = [i for i, line in enumerate(lines)
              if re.match(r'\s*reverse_proxy tokenkey-(blue|green):8080 \{\s*$', line)]
    if len(starts) != 1:
        raise ValueError('expected one canonical upstream snippet')
    start = starts[0]
    depth = 0
    for end in range(start, len(lines)):
        # Count block delimiters only; request placeholders contain inline braces.
        line = lines[end].strip()
        if line.endswith('{') and not line.startswith('#'):
            depth += 1
        if line == '}':
            depth -= 1
        if depth == 0:
            return ''.join(lines[:start]) + replacement + ''.join(lines[end + 1:])
    raise ValueError('unclosed upstream block')


def canonical_candidate_proxy(text):
    # Reuse the shared owner so copied historical passive cooldown settings cannot
    # reappear on the new fleet. Keep the runtime's actual blue/green selection.
    active = re.findall(r'(?m)^\s*reverse_proxy (tokenkey-(?:blue|green):8080) \{\s*$', text)
    if len(active) != 1:
        raise ValueError('ambiguous candidate active color')
    owner = (ROOT / 'deploy/aws/stage0/Caddyfile.edge').read_text()
    start = owner.index('\treverse_proxy tokenkey:8080 {')
    snippet = owner[start:owner.index('\n}\n', start)]
    return replace_proxy(text, snippet.replace('tokenkey:8080', active[0], 1) + '\n')


def edge_cidr(text, cidrs):
    text, count = re.subn(r'(?m)^(\s*)remote_ip [^\n]+$',
                         lambda m: m[1] + 'remote_ip ' + ' '.join(cidrs), text)
    if count != 1:
        raise ValueError('expected one edge relay CIDR gate')
    return text


def render(inventory, runtime, output, *, nodes, prod_ip):
    available = {'prod': inventory['prod'], **inventory['edges']}
    if not nodes or not set(nodes) <= available.keys():
        raise ValueError('select known migration nodes')
    if prod_ip not in (inventory['prod']['ip'], inventory['prod']['source_ip']):
        raise ValueError('prod IP must match an inspected migration identity')
    output.mkdir(mode=0o700)
    receipt = {'batch': inventory['batch'], 'installed': False, 'prod_ip': prod_ip, 'nodes': {}}
    prod_cidr = prod_ip + '/32'
    for name in sorted(set(nodes)):
        node = available[name]
        source = (runtime / f'{name}-source.Caddyfile').read_text()
        candidate = (runtime / f'{name}-candidate.Caddyfile').read_text()
        candidate_input_sha = hashlib.sha256(candidate.encode()).hexdigest()
        candidate = canonical_candidate_proxy(candidate)
        domains = re.findall(r'(?m)^([a-z0-9.-]+\.[a-z]+) \{$', source)
        candidate_domains = re.findall(r'(?m)^([a-z0-9.-]+\.[a-z]+) \{$', candidate)
        if not domains or domains != candidate_domains or node['domain'] not in domains:
            raise ValueError('source/candidate domain mismatch: ' + name)
        maintenance = replace_proxy(source, '\theader Retry-After "30"\n\trespond "Service maintenance; please retry shortly" 503\n')
        forward = replace_proxy(source, f'''\treverse_proxy https://{node['ip']} {{
\t\tflush_interval -1
\t\theader_up Host {{host}}
\t\theader_up -CF-Connecting-IP
\t\theader_up X-Real-IP {{remote_host}}
\t\theader_up X-Forwarded-For {{remote_host}}
\t\ttransport http {{
\t\t\ttls_server_name {node['domain']}
\t\t\tdial_timeout 5s
\t\t\tcompression off
\t\t}}
\t}}
''')
        if 'trusted_proxies' in candidate:
            raise ValueError('existing trust configuration needs explicit review')
        trust = f'\n{{\n\tservers {{\n\t\ttrusted_proxies static {node["source_ip"]}/32\n\t\ttrusted_proxies_strict\n\t\tclient_ip_headers X-Forwarded-For\n\t}}\n'
        if candidate.count('\n{\n') != 1:
            raise ValueError('expected one global Caddy block')
        transition = candidate.replace('\n{\n', trust, 1)
        for header in ('X-Real-IP', 'X-Forwarded-For'):
            needle = f'header_up {header} {{remote_host}}'
            if transition.count(needle) != 1:
                raise ValueError('unexpected client IP header policy')
            transition = transition.replace(needle, f'header_up {header} {{client_ip}}')
        final = candidate
        if name != 'prod':
            forward = edge_cidr(forward, [prod_cidr])
            transition = edge_cidr(transition, [prod_cidr, node['source_ip'] + '/32'])
            final = edge_cidr(final, [prod_cidr])
        generated = {'source-maintenance': maintenance, 'source-forward': forward,
                     'candidate-transition': transition, 'candidate-final': final}
        for stage, text in generated.items():
            (output / f'{name}-{stage}.Caddyfile').write_text(text)
        receipt['nodes'][name] = {'domains': domains, 'source_ip': node['source_ip'],
            'candidate_ip': node['ip'],
            'source_sha256': hashlib.sha256(source.encode()).hexdigest(),
            'candidate_sha256': candidate_input_sha,
            'outputs': {stage: hashlib.sha256(text.encode()).hexdigest() for stage, text in generated.items()}}
    (output / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime-dir', required=True, type=Path)
    parser.add_argument('--output-dir', required=True, type=Path)
    parser.add_argument('--node', action='append', required=True, choices=['prod', 'uk1', 'uk2', 'us4', 'us5'])
    parser.add_argument('--prod-ip', required=True, help='prod egress IP during this single-node transition')
    args = parser.parse_args()
    inv = json.loads((ROOT / 'deploy/aws/fresh-migration-20261015.json').read_text())
    result = render(inv, args.runtime_dir, args.output_dir, nodes=args.node, prod_ip=args.prod_ip)
    print(json.dumps({'installed': False, 'domains': {n: r['domains'] for n, r in result['nodes'].items()}}))
