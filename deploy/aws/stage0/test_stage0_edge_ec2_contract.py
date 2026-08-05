#!/usr/bin/env python3
"""Contract tests for the generic Stage0 EC2 Edge CloudFormation template."""

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


def allowed_pattern(parameter_body: str) -> str:
    match = re.search(r"^\s+AllowedPattern:\s*'([^']+)'$", parameter_body, re.M)
    if match is None:
        raise AssertionError("AllowedPattern is missing")
    return match.group(1)


class Stage0EdgeEc2ContractTest(unittest.TestCase):
    def test_graviton_small_uses_explicit_unlimited_credits(self) -> None:
        text = template_text()
        instance_type = section(text, "InstanceType")
        instance = section(text, "Instance")
        self.assertIn("Default: t4g.small", instance_type)
        self.assertNotIn("t4g.micro", instance_type)
        self.assertIn("CreditSpecification:", instance)
        self.assertIn("CPUCredits: unlimited", instance)

    def test_capacity_parameters_only_accept_the_approved_values(self) -> None:
        text = template_text()
        self.assertNotIn("Monthly" + "BudgetUsd", text)
        self.assertIn("AllowedValues: [t4g.small]", section(text, "InstanceType"))
        for parameter in ("RootVolumeSizeGiB", "DataVolumeSizeGiB"):
            with self.subTest(parameter=parameter):
                body = section(text, parameter)
                self.assertIn("MinValue: 20", body)
                self.assertIn("MaxValue: 20", body)
        self.assertIn("AllowedValues: [2]", section(text, "SwapSizeGiB"))
        self.assertIn("AllowedValues: [daily]", section(text, "SnapshotSchedule"))

    def test_ami_contract_is_explicitly_pinned_al2023_arm64(self) -> None:
        ami = section(template_text(), "AmazonLinux2023Arm64Ami")
        self.assertIn("Type: AWS::EC2::Image::Id", ami)
        self.assertNotIn("AWS::SSM::Parameter::Value", ami)
        self.assertNotIn(
            "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64",
            ami,
            "a moving latest alias can replace the instance during an unrelated stack update",
        )

    def test_root_and_retained_data_volumes_are_encrypted_gp3_20_gib(self) -> None:
        text = template_text()
        root_size = section(text, "RootVolumeSizeGiB")
        data_size = section(text, "DataVolumeSizeGiB")
        instance = section(text, "Instance")
        data_volume = section(text, "DataVolume")
        self.assertIn("Default: 20", root_size)
        self.assertIn("Default: 20", data_size)
        self.assertIn("VolumeType: gp3", instance)
        self.assertIn("VolumeSize: !Ref RootVolumeSizeGiB", instance)
        self.assertIn("Encrypted: true", instance)
        self.assertIn("DeletionPolicy: Retain", data_volume)
        self.assertIn("UpdateReplacePolicy: Retain", data_volume)
        self.assertIn("VolumeType: gp3", data_volume)
        self.assertIn("Size: !Ref DataVolumeSizeGiB", data_volume)
        self.assertIn("Encrypted: true", data_volume)

    def test_bootstrap_contract_sets_two_gib_swap_and_edge_caddy_profile(self) -> None:
        text = template_text()
        swap = section(text, "SwapSizeGiB")
        instance = section(text, "Instance")
        self.assertIn("Default: 2", swap)
        self.assertIn("TK_SWAP_SIZE_GIB='${SwapSizeGiB}'", instance)
        self.assertIn("TK_CADDY_PROFILE='edge'", instance)
        self.assertIn("TK_MAIN_GATEWAY_ALLOWED_CIDR='${MainGatewayAllowedCidr}'", instance)

    def test_public_ingress_is_exactly_443_and_8443(self) -> None:
        security_group = section(template_text(), "AppSecurityGroup")
        from_ports = sorted(int(value) for value in re.findall(r"FromPort:\s*(\d+)", security_group))
        to_ports = sorted(int(value) for value in re.findall(r"ToPort:\s*(\d+)", security_group))
        self.assertEqual([443, 8443], from_ports)
        self.assertEqual([443, 8443], to_ports)
        self.assertNotIn("FromPort: 22", security_group)
        self.assertNotIn("FromPort: 80", security_group)

    def test_instance_uses_ssm_profile_and_external_eip_association(self) -> None:
        text = template_text()
        role = section(text, "InstanceRole")
        instance = section(text, "Instance")
        association = section(text, "EIPAssoc")
        self.assertIn("AmazonSSMManagedInstanceCore", role)
        self.assertIn("IamInstanceProfile: !Ref InstanceProfile", instance)
        self.assertNotRegex(text, r"^  [A-Za-z0-9]*ElasticIP:\n", msg="EIP must remain stack-external")
        self.assertIn("AllocationId: !Ref EipAllocationId", association)
        self.assertIn("InstanceId: !Ref Instance", association)

    def test_first_boot_has_egress_before_eip_association(self) -> None:
        subnet = section(template_text(), "PublicSubnet")
        self.assertIn(
            "MapPublicIpOnLaunch: true",
            subnet,
            "UserData needs temporary public egress before the external EIP association exists",
        )

    def test_instance_can_attach_only_its_tagged_data_volume(self) -> None:
        role = section(template_text(), "InstanceRole")
        self.assertIn("Action: ec2:AttachVolume", role)
        self.assertIn("'ec2:ResourceTag/Project': !Ref ProjectName", role)
        self.assertIn("'ec2:ResourceTag/EdgeId': !Ref EdgeId", role)

    def test_cpu_and_disk_alarms_match_unlimited_semantics(self) -> None:
        text = template_text()
        alarms = {
            "InstanceCpuAlarm": ("CPUUtilization", "20", "86400"),
            "CpuSurplusCreditBalanceAlarm": ("CPUSurplusCreditBalance", "0", "300"),
            "CpuSurplusCreditsChargedAlarm": ("CPUSurplusCreditsCharged", "0", "300"),
            "RootVolumeDiskAlarm": ("RootVolumeUsedPercent", "85", "300"),
            "DataVolumeDiskAlarm": ("DataVolumeUsedPercent", "85", "300"),
        }
        for name, (metric, threshold, period) in alarms.items():
            with self.subTest(alarm=name):
                body = section(text, name)
                self.assertIn("Type: AWS::CloudWatch::Alarm", body)
                self.assertIn(f"MetricName: {metric}", body)
                self.assertIn(f"Threshold: {threshold}", body)
                self.assertIn(f"Period: {period}", body)
                self.assertIn("TreatMissingData: notBreaching", body)
        self.assertNotRegex(
            text,
            r"Type: AWS::CloudWatch::Alarm(?:(?!^  [A-Za-z]).)*MetricName: CPUCreditBalance",
            msg="CPUCreditBalance=0 is not an availability alarm in Unlimited mode",
        )

    def test_required_outputs_are_exported(self) -> None:
        text = template_text()
        outputs = text.split("\nOutputs:\n", 1)[1]
        for name in (
            "InstanceId",
            "PublicIP",
            "EipAllocationId",
            "ApiUrl",
            "DataVolumeId",
            "CpuCreditMode",
            "InstanceCpuAlarmName",
            "CpuSurplusCreditBalanceAlarmName",
            "CpuSurplusCreditsChargedAlarmName",
            "RootVolumeDiskAlarmName",
            "DataVolumeDiskAlarmName",
        ):
            with self.subTest(output=name):
                self.assertRegex(outputs, rf"(?m)^  {name}:$")

    def test_snapshot_policy_targets_only_this_edge(self) -> None:
        text = template_text()
        instance = section(text, "Instance")
        policy = section(text, "SnapshotPolicy")
        unique_backup_tag = "!Sub 'stage0-edge-${EdgeId}'"
        self.assertIn(f"Value: {unique_backup_tag}", instance)
        self.assertIn(f"Value: {unique_backup_tag}", policy)

    def test_userdata_parameters_reject_shell_injection_characters(self) -> None:
        text = template_text()
        cases = {
            "AcmeEmail": "ops@example.com",
            "AdminEmail": "ops@example.com",
            "Timezone": "Asia/Shanghai",
            "GhcrImageName": "sub2api",
            "GhcrPatSsmName": "/tokenkey/ghcr/pat",
            "MainGatewayAllowedCidr": "203.0.113.4/32",
        }
        for name, valid in cases.items():
            with self.subTest(parameter=name):
                pattern = allowed_pattern(section(text, name))
                self.assertIsNotNone(re.fullmatch(pattern, valid))
                self.assertIsNone(re.fullmatch(pattern, f"{valid}'$(id)"))


if __name__ == "__main__":
    unittest.main()
