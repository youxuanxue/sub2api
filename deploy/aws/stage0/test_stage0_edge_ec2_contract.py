#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import re
import unittest


TEMPLATE = pathlib.Path(__file__).resolve().parents[1] / "cloudformation/stage0-edge-ec2.yaml"


def template_text() -> str:
    return TEMPLATE.read_text(encoding="utf-8")


def section(text: str, name: str) -> str:
    match = re.search(
        rf"^  {re.escape(name)}:\n(.*?)(?=^  [A-Za-z][A-Za-z0-9]*:\n|^Outputs:\n|\Z)",
        text,
        re.M | re.S,
    )
    if match is None:
        raise AssertionError(f"CloudFormation section {name} is missing")
    return match.group(1)


class Stage0EdgeEc2ContractTest(unittest.TestCase):
    def test_graviton_small_uses_unlimited_credits(self) -> None:
        text = template_text()
        self.assertIn("Default: t4g.small", section(text, "InstanceType"))
        instance = section(text, "Instance")
        self.assertIn("CreditSpecification:", instance)
        self.assertIn("CPUCredits: unlimited", instance)

    def test_al2023_arm64_ami_is_an_explicit_input(self) -> None:
        ami = section(template_text(), "AmazonLinux2023Arm64Ami")
        self.assertIn("Type: AWS::EC2::Image::Id", ami)
        self.assertNotIn("AWS::SSM::Parameter::Value", ami)

    def test_storage_is_encrypted_gp3_and_data_survives_stack_deletion(self) -> None:
        text = template_text()
        instance = section(text, "Instance")
        data = section(text, "DataVolume")
        self.assertIn("VolumeType: gp3", instance)
        self.assertIn("Encrypted: true", instance)
        self.assertIn("DeletionPolicy: Retain", data)
        self.assertIn("UpdateReplacePolicy: Retain", data)
        self.assertIn("VolumeType: gp3", data)
        self.assertIn("Encrypted: true", data)

    def test_bootstrap_receives_two_gib_swap_and_edge_profile(self) -> None:
        text = template_text()
        self.assertIn("Default: 2", section(text, "SwapSizeGiB"))
        instance = section(text, "Instance")
        self.assertIn("TK_SWAP_SIZE_MB='2048'", instance)
        self.assertNotIn("TK_SWAP_SIZE_GIB", instance)
        self.assertIn("TK_CADDY_PROFILE='edge'", instance)
        self.assertIn("TK_CANDIDATE_MODE='1'", instance)

    def test_instance_has_ssm_imdsv2_and_external_eip(self) -> None:
        text = template_text()
        self.assertIn("AmazonSSMManagedInstanceCore", section(text, "InstanceRole"))
        instance = section(text, "Instance")
        self.assertIn("HttpTokens: required", instance)
        self.assertIn("IamInstanceProfile: !Ref InstanceProfile", instance)
        association = section(text, "EIPAssoc")
        self.assertIn("AllocationId: !Ref EipAllocationId", association)
        self.assertIn("InstanceId: !Ref Instance", association)

    def test_alarms_cover_cpu_unlimited_and_both_disks(self) -> None:
        text = template_text()
        expected = {
            "InstanceCpuAlarm": "CPUUtilization",
            "CpuSurplusCreditBalanceAlarm": "CPUSurplusCreditBalance",
            "CpuSurplusCreditsChargedAlarm": "CPUSurplusCreditsCharged",
            "RootVolumeDiskAlarm": "RootVolumeUsedPercent",
            "DataVolumeDiskAlarm": "DataVolumeUsedPercent",
        }
        for name, metric in expected.items():
            with self.subTest(alarm=name):
                body = section(text, name)
                self.assertIn(f"MetricName: {metric}", body)
                self.assertIn("TreatMissingData: notBreaching", body)
        self.assertNotIn("MetricName: CPUCreditBalance", text)

    def test_template_has_no_lightsail_or_cost_gate(self) -> None:
        text = template_text()
        self.assertNotIn("AWS::Lightsail", text)
        self.assertNotIn("MonthlyBudget", text)
        self.assertNotIn("max_monthly_budget", text)

    def test_platform_identity_parameters_are_fixed(self) -> None:
        text = template_text()
        self.assertIn("AllowedValues: [tokenkey]", section(text, "ProjectName"))
        self.assertIn("AllowedValues: [edge]", section(text, "Environment"))

    def test_root_volume_inherits_edge_ownership_tags(self) -> None:
        instance = section(template_text(), "Instance")
        self.assertIn("PropagateTagsToVolumeOnCreation: true", instance)

    def test_instance_profile_name_matches_execution_role_scope(self) -> None:
        profile = section(template_text(), "InstanceProfile")
        self.assertIn("InstanceProfileName: !Sub '${ProjectName}-edge-${EdgeId}-stage0-profile'", profile)


if __name__ == "__main__":
    unittest.main()
