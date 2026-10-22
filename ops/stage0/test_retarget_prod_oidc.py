import copy
from pathlib import Path
import re
import unittest

import yaml

from ops.stage0.retarget_prod_oidc import CfnLoader, render


def evaluate(node, params, conditions):
    """Minimal CloudFormation evaluator for the SendCommand statement."""
    if isinstance(node, list):
        return [evaluate(item, params, conditions) for item in node]
    if not isinstance(node, dict):
        return node
    if 'Ref' in node:
        return params[node['Ref']]
    if 'Fn::Equals' in node:
        left, right = evaluate(node['Fn::Equals'], params, conditions)
        return left == right
    if 'Fn::Not' in node:
        return not evaluate(node['Fn::Not'][0], params, conditions)
    if 'Fn::Select' in node:
        index, items = node['Fn::Select']
        return evaluate(items, params, conditions)[int(index)]
    if 'Fn::Split' in node:
        sep, value = node['Fn::Split']
        return evaluate(value, params, conditions).split(sep)
    if 'Fn::If' in node:
        name, yes, no = node['Fn::If']
        return evaluate(yes if evaluate(conditions[name], params, conditions) else no, params, conditions)
    if 'Fn::Sub' in node:
        template, subs = node['Fn::Sub']
        values = dict(params, **{k: evaluate(v, params, conditions) for k, v in subs.items()})
        return re.sub(r'\$\{([^}]+)\}', lambda m: values[m.group(1)], template)
    return {k: evaluate(v, params, conditions) for k, v in node.items()}


def send_resources(template, instance_id, region):
    params = {'TargetInstanceId': instance_id, 'TargetInstanceRegion': region,
              'AWS::Region': 'us-east-1', 'AWS::AccountId': '123456789012'}
    policies = template['Resources']['ClusteringRole']['Properties']['Policies']
    statement = next(p for p in policies if p['PolicyName'] == 'SsmSendCommandStage0')['PolicyDocument']['Statement'][0]
    return evaluate(statement, params, template['Conditions'])['Resource']


class RetargetProdOidcTest(unittest.TestCase):
    def test_region_resources_and_unrelated_policies_are_preserved(self):
        canonical = yaml.load(Path('deploy/aws/cloudformation/cicd-oidc.yaml').read_text(), Loader=CfnLoader)
        live = copy.deepcopy(canonical)
        del live['Parameters']['TargetInstanceRegion']
        del live['Conditions']['HasProdRegion']
        policies = live['Resources']['ClusteringRole']['Properties']['Policies']
        extra = {'PolicyName': 'unrelated', 'PolicyDocument': {'Version': '2012-10-17', 'Statement': []}}
        policies.append(extra)
        fixed = render(live, canonical)
        self.assertEqual(fixed['Resources']['ClusteringRole']['Properties']['Policies'][-1], extra)
        self.assertEqual(fixed['Resources']['ClusteringRole']['Properties']['AssumeRolePolicyDocument'], live['Resources']['ClusteringRole']['Properties']['AssumeRolePolicyDocument'])
        for region in ('ap-northeast-1', ''):
            resolved_region = region or 'us-east-1'
            self.assertEqual(send_resources(fixed, 'i-0123456789abcdef0', region), [
                f'arn:aws:ec2:{resolved_region}:123456789012:instance/i-0123456789abcdef0',
                f'arn:aws:ssm:{resolved_region}::document/AWS-RunShellScript'])
        for name in live['Resources']:
            if name != 'ClusteringRole':
                self.assertEqual(fixed['Resources'][name], live['Resources'][name])

    def test_target_accepts_ec2_and_hybrid_with_matching_arn(self):
        canonical = yaml.load(Path('deploy/aws/cloudformation/cicd-oidc.yaml').read_text(), Loader=CfnLoader)
        pattern = re.compile(canonical['Parameters']['TargetInstanceId']['AllowedPattern'])
        for value in ('', 'i-045f062ce33e1a5af', 'mi-080ed76dc6d97d8b5'):
            self.assertTrue(pattern.fullmatch(value), value)
        for value in ('mi-', 'x-080ed76dc6d97d8b5', 'mi-080ED76DC6D97D8B5', 'arn:aws:ssm:eu-west-2:1:managed-instance/mi-0'):
            self.assertIsNone(pattern.fullmatch(value), value)
        self.assertEqual(send_resources(canonical, 'mi-080ed76dc6d97d8b5', 'eu-west-2'), [
            'arn:aws:ssm:eu-west-2:123456789012:managed-instance/mi-080ed76dc6d97d8b5',
            'arn:aws:ssm:eu-west-2::document/AWS-RunShellScript'])
        self.assertEqual(send_resources(canonical, 'i-045f062ce33e1a5af', 'ap-northeast-1'), [
            'arn:aws:ec2:ap-northeast-1:123456789012:instance/i-045f062ce33e1a5af',
            'arn:aws:ssm:ap-northeast-1::document/AWS-RunShellScript'])

    def test_retarget_from_ec2_only_live_enables_hybrid_and_keeps_pinned_mi_grants(self):
        canonical = yaml.load(Path('deploy/aws/cloudformation/cicd-oidc.yaml').read_text(), Loader=CfnLoader)
        live = copy.deepcopy(canonical)
        # Shape of the deployed 2026-10-17 stack: EC2-only pattern and ARN.
        live['Parameters']['TargetInstanceId']['AllowedPattern'] = '^(i-[0-9a-f]{8,17})?$'
        del live['Conditions']['IsHybridProdInstance']
        policies = live['Resources']['ClusteringRole']['Properties']['Policies']
        send = policies[0]['PolicyDocument']['Statement'][0]['Fn::If'][1]
        region = {'ProdRegion': {'Fn::If': ['HasProdRegion', {'Ref': 'TargetInstanceRegion'}, {'Ref': 'AWS::Region'}]}}
        pinned = 'arn:aws:ssm:eu-west-2:123456789012:managed-instance/mi-0efdf781205cff512'
        send['Resource'] = [
            {'Fn::Sub': ['arn:aws:ec2:${ProdRegion}:${AWS::AccountId}:instance/${TargetInstanceId}', region]},
            {'Fn::Sub': ['arn:aws:ssm:${ProdRegion}::document/AWS-RunShellScript', region]},
            pinned,
        ]
        fixed = render(live, canonical)
        self.assertEqual(fixed['Parameters']['TargetInstanceId'], canonical['Parameters']['TargetInstanceId'])
        self.assertEqual(send_resources(fixed, 'mi-080ed76dc6d97d8b5', 'eu-west-2'), [
            'arn:aws:ssm:eu-west-2:123456789012:managed-instance/mi-080ed76dc6d97d8b5',
            'arn:aws:ssm:eu-west-2::document/AWS-RunShellScript',
            pinned])
        # Re-rendering is idempotent: no duplicated grants.
        again = render(fixed, canonical)
        self.assertEqual(send_resources(again, 'mi-080ed76dc6d97d8b5', 'eu-west-2'),
                         send_resources(fixed, 'mi-080ed76dc6d97d8b5', 'eu-west-2'))
        self.assertEqual(fixed['Resources']['ClusteringRole']['Properties']['AssumeRolePolicyDocument'],
                         live['Resources']['ClusteringRole']['Properties']['AssumeRolePolicyDocument'])

    def test_missing_policy_statement_is_rejected(self):
        template = yaml.load(Path('deploy/aws/cloudformation/cicd-oidc.yaml').read_text(), Loader=CfnLoader)
        live = copy.deepcopy(template)
        live['Resources']['ClusteringRole']['Properties']['Policies'][0]['PolicyDocument']['Statement'] = []
        with self.assertRaisesRegex(ValueError, 'ambiguous'):
            render(live, template)

    def test_backup_role_switch_keeps_media_principal_unchanged(self):
        template = yaml.load(Path('deploy/aws/cloudformation/stage0-backups.yaml').read_text(), Loader=CfnLoader)
        resources = template['Resources']
        backup = resources['PgdumpBackupBucketPolicy']['Properties']['PolicyDocument']['Statement']
        media = resources['MediaBucketPolicy']['Properties']['PolicyDocument']['Statement']
        for configured in (True, False):
            for statement in backup:
                if statement['Effect'] != 'Allow':
                    continue
                cond, yes, no = statement['Principal']['AWS']['Fn::If']
                self.assertEqual(cond, 'HasPgdumpRole')
                self.assertEqual((yes if configured else no)['Ref'], 'PgdumpInstanceRoleArn' if configured else 'AppInstanceRoleArn')
            for statement in media:
                if statement['Effect'] == 'Allow':
                    self.assertEqual(statement['Principal']['AWS'], {'Ref': 'AppInstanceRoleArn'})


if __name__ == '__main__':
    unittest.main()
