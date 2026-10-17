"""Render the cross-region prod IAM correction without replacing unrelated live policy.

Input is CloudFormation get-template JSON. Output is a reviewable update template;
this tool does not call AWS or apply permissions.
"""
import argparse
import copy
import json
from pathlib import Path

import yaml


class CfnLoader(yaml.SafeLoader):
    pass


def intrinsic(loader, tag, node):
    value = (loader.construct_scalar(node) if isinstance(node, yaml.ScalarNode)
             else loader.construct_sequence(node) if isinstance(node, yaml.SequenceNode)
             else loader.construct_mapping(node))
    return {tag if tag in ('Ref', 'Condition') else 'Fn::' + tag: value}


CfnLoader.add_multi_constructor('!', intrinsic)


def render(live, canonical):
    result = copy.deepcopy(live)
    result['Parameters']['TargetInstanceRegion'] = canonical['Parameters']['TargetInstanceRegion']
    result['Conditions']['HasProdRegion'] = canonical['Conditions']['HasProdRegion']
    def statements(template):
        policies = template['Resources']['ClusteringRole']['Properties']['Policies']
        return next(p['PolicyDocument']['Statement'] for p in policies if p['PolicyName'] == 'SsmSendCommandStage0')
    old, new = statements(result), statements(canonical)
    old_send = [s for s in old if s.get('Fn::If', [None])[0] == 'HasProdInstance']
    new_send = [s for s in new if s.get('Fn::If', [None])[0] == 'HasProdInstance']
    if len(old_send) != 1 or len(new_send) != 1:
        raise ValueError('ambiguous prod SendCommand policy')
    old_send[0]['Fn::If'][1]['Resource'] = new_send[0]['Fn::If'][1]['Resource']
    for sid in ('DescribeInstanceInformationForSsmReadiness', 'ReadQaRawArchiveNetworkForDiagnostics', 'ReadEbsSnapshotsForDiagnostics'):
        targets = [s for s in old if s.get('Sid') == sid]
        sources = [s for s in new if s.get('Sid') == sid]
        if len(targets) != 1 or len(sources) != 1:
            raise ValueError('ambiguous diagnostic policy: ' + sid)
        targets[0]['Condition'] = sources[0]['Condition']
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--live-template', type=Path, required=True)
    parser.add_argument('--canonical', type=Path, default=Path('deploy/aws/cloudformation/cicd-oidc.yaml'))
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    live = json.loads(args.live_template.read_text())
    canonical = yaml.load(args.canonical.read_text(), Loader=CfnLoader)
    args.output.write_text(json.dumps(render(live, canonical), indent=2) + '\n')


if __name__ == '__main__':
    main()
