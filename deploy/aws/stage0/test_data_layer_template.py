#!/usr/bin/env python3
"""Behavior contracts for the inert Stage0 RDS CloudFormation template."""

from __future__ import annotations

import pathlib
import unittest

import yaml


REPO = pathlib.Path(__file__).resolve().parents[3]
TEMPLATE = REPO / "deploy/aws/cloudformation/stage0-data.yaml"


class CloudFormationLoader(yaml.SafeLoader):
    pass


def construct_cfn_tag(loader: yaml.Loader, _tag: str, node: yaml.Node):
    if isinstance(node, yaml.ScalarNode):
        return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode):
        return loader.construct_sequence(node)
    return loader.construct_mapping(node)


CloudFormationLoader.add_multi_constructor("!", construct_cfn_tag)


class DataLayerTemplateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.template = yaml.load(TEMPLATE.read_text(), Loader=CloudFormationLoader)

    def test_initial_capacity_matches_current_prod_baseline(self) -> None:
        params = self.template["Parameters"]
        self.assertEqual(params["PgEngineVersion"]["Default"], "18.1")
        self.assertEqual(params["PgInstanceClass"]["Default"], "db.t4g.large")
        self.assertEqual(params["PgAllocatedStorage"]["Default"], 50)
        self.assertEqual(params["PgMaxAllocatedStorage"]["Default"], 200)
        self.assertEqual(params["PgBackupRetentionDays"]["Default"], 14)

    def test_database_is_private_retained_and_observable(self) -> None:
        db = self.template["Resources"]["PgInstance"]
        props = db["Properties"]
        self.assertEqual(db["DeletionPolicy"], "Retain")
        self.assertEqual(db["UpdateReplacePolicy"], "Retain")
        self.assertFalse(props["PubliclyAccessible"])
        self.assertTrue(props["StorageEncrypted"])
        self.assertTrue(props["DeletionProtection"])
        self.assertTrue(props["EnablePerformanceInsights"])
        self.assertEqual(props["PerformanceInsightsRetentionPeriod"], 7)
        self.assertEqual(props["BackupRetentionPeriod"], "PgBackupRetentionDays")
        self.assertEqual(
            props["MasterUserPassword"],
            "{{resolve:ssm-secure:/${ProjectName}/${Environment}/stage0/rds-master-password}}",
        )
        self.assertNotIn("PgMasterPasswordSsmName", self.template["Parameters"])

    def test_connection_alarm_covers_blue_green_overlap_budget(self) -> None:
        params = self.template["Parameters"]
        alarm = self.template["Resources"]["PgConnectionsAlarm"]
        self.assertEqual(params["PgConnectionAlarmThreshold"]["Default"], 120)
        self.assertEqual(alarm["Condition"], "PgConnectionAlarmEnabled")
        self.assertEqual(
            alarm["Properties"]["Threshold"], "PgConnectionAlarmThreshold"
        )

    def test_memory_alarm_matches_recommended_large_class(self) -> None:
        alarm = self.template["Resources"]["PgFreeableMemoryAlarm"]
        self.assertEqual(alarm["Properties"]["Threshold"], 1024 * 1024 * 1024)


if __name__ == "__main__":
    unittest.main()
