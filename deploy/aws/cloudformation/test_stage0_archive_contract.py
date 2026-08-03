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
    def test_raw_telemetry_has_lifecycle_and_instance_role_access(self) -> None:
        template = yaml.load(TEMPLATE.read_text(encoding="utf-8"), Loader=CloudFormationLoader)
        bucket = template["Resources"]["OpsArchiveBucket"]["Properties"]
        rules = bucket["LifecycleConfiguration"]["Rules"]
        raw = next(rule for rule in rules if rule["Id"] == "tier-and-expire-raw-telemetry")
        self.assertEqual(raw["Status"], "Enabled")
        self.assertEqual(raw["Prefix"], "${Environment}/raw-telemetry/")
        self.assertEqual(raw["ExpirationInDays"], "RawTelemetryExpireDays")

        statements = template["Resources"]["OpsArchiveBucketPolicy"]["Properties"]["PolicyDocument"]["Statement"]
        write = next(statement for statement in statements if statement["Sid"] == "AllowAppInstanceRolePutGetArchive")
        self.assertIn("s3:PutObject", write["Action"])
        self.assertTrue(any("raw-telemetry" in resource for resource in write["Resource"]))


if __name__ == "__main__":
    unittest.main()
