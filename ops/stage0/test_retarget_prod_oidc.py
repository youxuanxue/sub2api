import copy
from pathlib import Path
import unittest

import yaml

from ops.stage0.retarget_prod_oidc import CfnLoader, render


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
        statements = fixed['Resources']['ClusteringRole']['Properties']['Policies'][0]['PolicyDocument']['Statement']
        resources = statements[0]['Fn::If'][1]['Resource']
        for region in ('ap-northeast-1', ''):
            resolved = []
            for resource in resources:
                template, substitutions = resource['Fn::Sub']
                condition, yes, no = substitutions['ProdRegion']['Fn::If']
                self.assertEqual(condition, 'HasProdRegion')
                self.assertEqual(yes, {'Ref': 'TargetInstanceRegion'})
                self.assertEqual(no, {'Ref': 'AWS::Region'})
                for key, value in {'ProdRegion': region or 'us-east-1', 'AWS::AccountId': '123456789012', 'TargetInstanceId': 'i-0123456789abcdef0'}.items():
                    template = template.replace('${' + key + '}', value)
                resolved.append(template)
            self.assertEqual(resolved, [f'arn:aws:ec2:{region or "us-east-1"}:123456789012:instance/i-0123456789abcdef0', f'arn:aws:ssm:{region or "us-east-1"}::document/AWS-RunShellScript'])
        for name in live['Resources']:
            if name != 'ClusteringRole':
                self.assertEqual(fixed['Resources'][name], live['Resources'][name])

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
