#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import unittest

import yaml


TEMPLATE = pathlib.Path(__file__).with_name("stage0-archive.yaml")


class CloudFormationLoader(yaml.SafeLoader):
    pass


def _cloudformation_tag(loader: CloudFormationLoader, _suffix: str, node: yaml.Node):
    if isinstance(node, yaml.ScalarNode):
        return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode):
        return loader.construct_sequence(node)
    return loader.construct_mapping(node)


CloudFormationLoader.add_multi_constructor("!", _cloudformation_tag)


class Stage0ArchiveContractTest(unittest.TestCase):
    def test_whole_bucket_expires_in_thirty_days_without_glacier(self) -> None:
        template = yaml.load(TEMPLATE.read_text(encoding="utf-8"), Loader=CloudFormationLoader)
        params = template["Parameters"]
        expiry = params["ArchiveExpireDays"]
        self.assertEqual(expiry["Default"], 30)
        self.assertEqual(expiry["MinValue"], 1)
        self.assertNotIn("ArchiveGlacierTransitionDay", params)
        self.assertNotIn("RawTelemetryExpireDays", params)

        bucket = template["Resources"]["OpsArchiveBucket"]["Properties"]
        rules = bucket["LifecycleConfiguration"]["Rules"]
        self.assertEqual(len(rules), 1)
        rule = rules[0]
        self.assertEqual(rule["Id"], "expire-all-30d")
        self.assertEqual(rule["Status"], "Enabled")
        self.assertEqual(rule["ExpirationInDays"], "ArchiveExpireDays")
        self.assertNotIn("Transitions", rule)
        self.assertNotIn("Prefix", rule)

    def test_instance_role_is_read_only_during_wind_down(self) -> None:
        template = yaml.load(TEMPLATE.read_text(encoding="utf-8"), Loader=CloudFormationLoader)
        statements = template["Resources"]["OpsArchiveBucketPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        sids = {statement["Sid"] for statement in statements}
        self.assertNotIn("AllowAppInstanceRolePutArchive", sids)
        deny_read = next(
            statement
            for statement in statements
            if statement["Sid"] == "DenyAppInstanceRoleReadRawTelemetry"
        )
        self.assertEqual(deny_read["Effect"], "Deny")
        self.assertIn("s3:GetObject", deny_read["Action"])
        self.assertIn("raw-telemetry", deny_read["Resource"])
        read = next(statement for statement in statements if statement["Sid"] == "AllowAppInstanceRoleGetOpsArchive")
        self.assertNotIn("raw-telemetry", read["Resource"])
        listing = next(statement for statement in statements if statement["Sid"] == "AllowAppInstanceRoleListArchive")
        self.assertFalse(any("raw-telemetry" in prefix for prefix in listing["Condition"]["StringLike"]["s3:prefix"]))


if __name__ == "__main__":
    unittest.main()
