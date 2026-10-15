#!/usr/bin/env python3
"""Prepare a selected-edge cutover patch and actions; never change live pointers."""
import argparse
import difflib
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def render(edges):
    inventory = json.loads((ROOT / 'deploy/aws/fresh-migration-20261015.json').read_text())
    if not edges or not set(edges) <= set(inventory['edges']):
        raise ValueError('select at least one known fresh edge')
    before, after = {}, {}
    def put(name, content):
        before[name] = (ROOT / name).read_text()
        after[name] = content
    name = 'deploy/aws/lightsail/edge-targets-lightsail.json'
    matrix = json.loads((ROOT / name).read_text())
    matrix['max_monthly_budget_usd'] = 84
    for edge in edges:
        row = inventory['edges'][edge]
        matrix['targets'][edge].update(deployable=True, standby=False,
            instance_name=row['instance_name'], static_ip_name=row['static_ip_name'],
            porkbun_a_ipv4=row['ip'], bundle_id='xlarge_3_0', monthly_budget_usd=84,
            purpose='fresh AWS active edge; historical hosts are archival only')
    put(name, json.dumps(matrix, indent=2) + '\n')
    name = 'deploy/hetzner/edge-targets-hetzner.json'
    matrix = json.loads((ROOT / name).read_text())
    for edge in edges:
        matrix['targets'][edge]['deployable'] = False
        matrix['targets'][edge]['purpose'] = 'historical read-only data; traffic forwarded to fresh AWS'
    put(name, json.dumps(matrix, indent=2) + '\n')
    name = 'deploy/hetzner/README.md'
    text = (ROOT / name).read_text()
    text = text.replace('# TokenKey 舰队 · Hetzner Cloud（现网）', '# TokenKey 舰队 · Hcloud / AWS 分批迁移')
    lines = text.splitlines()
    for edge in edges:
        row = inventory['edges'][edge]
        prefix = f'| **{edge}** |'
        matches = [i for i, line in enumerate(lines) if line.startswith(prefix)]
        if len(matches) != 1:
            raise ValueError('fleet README row drift: ' + edge)
        lines[matches[0]] = f'| **{edge}** | `{row["domain"]}` | `{row["ip"]}` | `{row["instance_id"]}` | AWS Lightsail · {row["instance_name"]} |'
    text = '\n'.join(lines) + '\n'
    text = text.replace('- 矩阵：`edge-targets-hetzner.json` + `prod-target-hetzner.json`（`deployable=true`）',
        '- 活矩阵：Hcloud `edge-targets-hetzner.json` + AWS `../aws/lightsail/edge-targets-lightsail.json`；每个 edge 仅一个平台 deployable。prod 仍为 Hcloud。')
    text = text.replace('（共享解析器只选现网 HZ）', '（共享解析器按活矩阵选择 Hcloud 或 AWS）')
    text = text.replace('# edge（平台 auto → HZ）', '# edge（平台 auto → 当前活矩阵）')
    text = text.replace('AWS EIP `34.194.234.88` 与四边旧 Lightsail 仅为 ≥7d standby / 回滚，**不是**现网写路径。',
        '旧 AWS 与旧 Hcloud 节点保留历史数据，不作为回滚写库；全新 AWS 节点按上表逐台接流量。')
    notice = ('\n本轮 AWS 活节点：' + ', '.join(sorted(k for k, row in json.loads(after['deploy/aws/lightsail/edge-targets-lightsail.json'])['targets'].items() if row.get('deployable') is True)) + '。新边仅放行现网 Hcloud prod；旧 edge 的单地址转发信任只在 DNS 过渡期间保留。\n'
              '后续旧平台专用 provision、卷与告警操作仅适用于仍在 Hcloud 的节点，不能用于已切换 AWS 的 edge。\n')
    text = re.sub(r'\n本轮 AWS 活节点：.*?后续旧平台专用 provision[^\n]*\n', '', text, flags=re.S)
    text = text.replace('## 现网不变量', notice + '\n## 现网不变量', 1)
    put(name, text)
    return inventory, before, after


def verify(inventory, files, edges):
    with tempfile.TemporaryDirectory(prefix='aws-mixed-cutover-') as tmp:
        root = Path(tmp)
        tracked = subprocess.check_output(['git', 'ls-files', 'ops', 'deploy', 'scripts', '.github'], cwd=ROOT, text=True)
        for name in tracked.splitlines():
            source = ROOT / name
            if source.is_file() and True:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(source, target)
        for name, content in files.items():
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(content)
        env = dict(os.environ)
        env.pop('PROD_SSM_TARGET', None)
        subprocess.run([sys.executable, '-m', 'unittest',
            'deploy.hetzner.test_resolve_edge_hetzner_target',
            'deploy.hetzner.test_render_bootstrap',
            'ops.stage0.test_resolve_prod_ssm_target', 'ops.stage0.test_edge_routing_matrix',
            'ops.stage0.test_edge_ssm_execution', 'ops.stage0.test_hcloud_ops_contract',
            'ops.stage0.test_edge_admin_resolve_target',
            'ops.observability.test_remediate_hcloud_prod_host_alerts',
            'deploy.aws.stage0.test_resolve_edge_target_prod_ops_matrix_lightsail',
            'deploy.aws.stage0.test_resolve_edge_target_list_deployable',
            'scripts.test_resolve_edge_deploy_route', 'ops.observability.test_probe_release_control_plane'],
            cwd=root, env=env, check=True)
        subprocess.run([sys.executable, 'ops/stage0/edge_routing_matrix.py'], cwd=root, check=True)
        subprocess.run(['bash', 'deploy/hetzner/render-bootstrap.sh', '--check'], cwd=root, check=True)
        subprocess.run(['bash', 'deploy/aws/lightsail/test_restore_edge_env_secrets.sh'], cwd=root, check=True)
        subprocess.run(['bash', 'ops/lightsail/test_migrate_edge_ssm_roles.sh'], cwd=root, check=True)
        subprocess.run([sys.executable, 'scripts/checks/check-hetzner-edge-bluegreen-workflow.py'], cwd=root, check=True)
        for edge in inventory['edges']:
            raw = subprocess.check_output([sys.executable, 'scripts/stage0/resolve-edge-deploy-route.py',
                                           '--edge-id', edge, '--json'], cwd=root, text=True)
            actual = json.loads(raw)
            expected = inventory['edges'][edge]['instance_name'] if edge in edges else json.loads(
                subprocess.check_output([sys.executable, 'scripts/stage0/resolve-edge-deploy-route.py',
                                         '--edge-id', edge, '--json'], cwd=ROOT, text=True))['confirm_value']
            if actual['confirm_value'] != expected:
                raise ValueError('wrong cutover identity: ' + edge)
        subprocess.run([sys.executable, 'scripts/checks/workflow-edge-coverage.py'], cwd=root, check=True)


def actions(inventory, edges):
    parameters = []
    for edge in edges:
        row = inventory['edges'][edge]
        for key in ('instance_name', 'static_ip_name', 'public_ip', 'ssm_managed_instance_id'):
            source = {'public_ip': 'ip', 'ssm_managed_instance_id': 'instance_id'}.get(key, key)
            parameters.append({'region': row['region'], 'name': row['ssm_prefix'] + '/' + key,
                               'type': 'String', 'value': row[source]})
    return {'applied': False, 'edges': edges, 'prod_changed': False,
            'requires': 'source drained and frozen; final sync verified; candidate basic checks passed',
            'ssm_parameters': parameters, 'github_variables_changed': {},
            'edge_allowed_prod_cidr': inventory['prod']['source_ip'] + '/32',
            'dns_a': {inventory['edges'][edge]['domain']: inventory['edges'][edge]['ip'] for edge in edges}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--edge', action='append', required=True, choices=['us4', 'us5', 'uk1', 'uk2'])
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    edges = sorted(set(args.edge))
    inventory, before, after = render(edges)
    verify(inventory, after, edges)
    args.output.write_text(''.join(''.join(difflib.unified_diff(before[name].splitlines(True), after[name].splitlines(True),
                            fromfile='a/' + name, tofile='b/' + name)) for name in sorted(after)))
    args.output.with_suffix('.actions.json').write_text(json.dumps(actions(inventory, edges), indent=2) + '\n')
    subprocess.run(['git', 'apply', '--check', str(args.output.resolve())], cwd=ROOT, check=True)
    print('Selected-edge cutover patch validated; prod and unselected pointers unchanged.')


if __name__ == '__main__':
    main()
