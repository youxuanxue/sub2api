#!/usr/bin/env python3
"""Plan or run an isolated EC2 restore; always terminate the owned instance.

Requires an approved restore resource budget. The worker also schedules its
own shutdown/termination at two hours, including when the caller disappears.
"""

from __future__ import annotations

import argparse
import base64
import json
import re
import shlex
import subprocess
import sys
import time
import uuid
from pathlib import Path

from postgres_backup import BackupError, atomic_json

# Restore host must match the backup image architecture. Values are the approved
# Tokyo-era arm64 shape and its x86 equivalent for the amd64 Tencent image.
SHAPES = {
    "arm64": {"instance_type": "c7g.xlarge", "ami_arch": "arm64"},
    "amd64": {"instance_type": "c7i.xlarge", "ami_arch": "x86_64"},
}


def validate_network(subnet: str) -> None:
    """The restore subnet is an explicit, approved input; never derived from prod.

    Tencent prod is an SSM Hybrid mi-* host with no EC2 subnet, see
    docs/ops/tencent-isolated-restore-plan.md.
    """
    if not re.fullmatch(r"subnet-[a-f0-9]{8,17}", subnet):
        raise BackupError("--subnet must be an explicit EC2 subnet-* id")


def aws(region: str, *args: str, timeout: int = 60):
    result = subprocess.run(
        ["aws", "--region", region, *args, "--output", "json"],
        text=True,
        capture_output=True,
        check=False,
        timeout=timeout,
    )
    if result.returncode:
        raise BackupError(f"AWS {args[0]} {args[1]} failed: {result.stderr[-500:]}")
    return json.loads(result.stdout) if result.stdout.strip() else {}


def worker_command(
    bucket: str,
    key: str,
    digest: str,
    image: str,
    target: str | None,
    target_time: str | None,
) -> str:
    if (
        not re.fullmatch(r"[a-z0-9][a-z0-9.-]+", bucket)
        or not re.fullmatch(r"artifacts/[a-f0-9]{64}\.tar\.gz", key)
        or not re.fullmatch(r"[a-f0-9]{64}", digest)
        or not re.fullmatch(r"sha256:[a-f0-9]{64}", image)
    ):
        raise BackupError("invalid immutable recovery artifact")
    args = [
        "python3",
        "restore_postgres_backup.py",
        "--image",
        image,
        "--config-dir",
        "/opt/tokenkey-restore/config",
        "--billing-check",
        "/opt/tokenkey-restore/backup-billing-check",
        "--receipt",
        "/opt/tokenkey-restore/receipt.json",
    ]
    if target:
        args += ["--target-name", target, "--target-time", target_time or ""]
    return "\n".join(
        [
            "set -eu",
            "install -d -m 0700 /opt/tokenkey-restore",
            "cd /opt/tokenkey-restore",
            f"aws s3 cp {shlex.quote('s3://' + bucket + '/' + key)} bundle.tar.gz --no-progress",
            f"echo {shlex.quote(digest + '  bundle.tar.gz')} | sha256sum -c -",
            "tar -xzf bundle.tar.gz",
            "docker load --input image.tar >/dev/null",
            " ".join(shlex.quote(arg) for arg in args),
            # SSM output has a size limit: return audit fingerprint plus timings;
            # full audit stays in the instance until destruction, never in chat logs.
            'python3 -c \'import hashlib,json; p="receipt.json"; x=json.load(open(p)); x["audit_sha256"]=hashlib.sha256(json.dumps(x.pop("audit"),sort_keys=True).encode()).hexdigest(); print(json.dumps(x))\'',
        ]
    )


def cleanup_cloud(
    region: str, request_id: str, instance: str | None, group: str | None
) -> None:
    """Resolve ambiguous creates by ownership and require confirmed destruction."""
    failures, volumes = [], []
    instances = [instance] if instance else []
    try:
        found = aws(
            region,
            "ec2",
            "describe-instances",
            "--filters",
            json.dumps([{"Name": "client-token", "Values": [request_id]}]),
        )
        for reservation in found["Reservations"]:
            for item in reservation["Instances"]:
                if item["InstanceId"] not in instances:
                    instances.append(item["InstanceId"])
                volumes.extend(
                    m["Ebs"]["VolumeId"]
                    for m in item.get("BlockDeviceMappings", [])
                    if "Ebs" in m
                )
    except (BackupError, subprocess.SubprocessError) as exc:
        failures.append(str(exc))
    if instances:
        try:
            aws(region, "ec2", "terminate-instances", "--instance-ids", *instances)
            aws(
                region,
                "ec2",
                "wait",
                "instance-terminated",
                "--instance-ids",
                *instances,
                timeout=600,
            )
            if volumes:
                aws(
                    region,
                    "ec2",
                    "wait",
                    "volume-deleted",
                    "--volume-ids",
                    *volumes,
                    timeout=600,
                )
        except (BackupError, subprocess.SubprocessError) as exc:
            failures.append(str(exc))
    try:
        groups = (
            [group]
            if group
            else [
                item["GroupId"]
                for item in aws(
                    region,
                    "ec2",
                    "describe-security-groups",
                    "--filters",
                    json.dumps([{"Name": "group-name", "Values": [request_id]}]),
                )["SecurityGroups"]
            ]
        )
        for group_id in groups:
            aws(region, "ec2", "delete-security-group", "--group-id", group_id)
    except (BackupError, subprocess.SubprocessError) as exc:
        failures.append(str(exc))
    if failures:
        raise BackupError("restore cloud cleanup incomplete: " + "; ".join(failures))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("region", "bucket", "key", "sha256", "image", "subnet", "profile"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--architecture", choices=sorted(SHAPES), required=True)
    parser.add_argument("--target-name")
    parser.add_argument("--target-time")
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    if bool(args.target_name) != bool(args.target_time):
        parser.error("named target and timestamp must be supplied together")
    validate_network(args.subnet)
    shape = SHAPES[args.architecture]
    command = worker_command(
        args.bucket,
        args.key,
        args.sha256,
        args.image,
        args.target_name,
        args.target_time,
    )
    request_id = "tk-physical-restore-" + uuid.uuid4().hex
    plan = {
        "status": "planned",
        "instance_type": shape["instance_type"],
        "architecture": args.architecture,
        "disk_gib": 100,
        "maximum_lifetime_hours": 2,
        "region": args.region,
        "subnet": args.subnet,
        "profile": args.profile,
        "network": "temporary security group: no inbound, HTTPS outbound only",
        "target": args.target_name or "latest-archived",
        "artifact_sha256": args.sha256,
    }
    if not args.apply:
        print(json.dumps(plan, indent=2))
        return
    image_id = aws(
        args.region,
        "ssm",
        "get-parameter",
        "--name",
        "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-"
        + shape["ami_arch"],
    )["Parameter"]["Value"]
    bootstrap = """#!/bin/bash
set -euo pipefail
# Install the hard stop before package installation or any download.
shutdown -h +120
dnf install -y docker python3
systemctl enable --now docker
"""
    instance, group = None, None
    try:
        subnet = aws(
            args.region, "ec2", "describe-subnets", "--subnet-ids", args.subnet
        )["Subnets"][0]
        group = aws(
            args.region,
            "ec2",
            "create-security-group",
            "--vpc-id",
            subnet["VpcId"],
            "--group-name",
            request_id,
            "--description",
            "Temporary TokenKey isolated physical restore",
        )["GroupId"]
        aws(
            args.region,
            "ec2",
            "revoke-security-group-egress",
            "--group-id",
            group,
            "--ip-permissions",
            json.dumps([{"IpProtocol": "-1", "IpRanges": [{"CidrIp": "0.0.0.0/0"}]}]),
        )
        aws(
            args.region,
            "ec2",
            "authorize-security-group-egress",
            "--group-id",
            group,
            "--ip-permissions",
            json.dumps(
                [
                    {
                        "IpProtocol": "tcp",
                        "FromPort": 443,
                        "ToPort": 443,
                        "IpRanges": [{"CidrIp": "0.0.0.0/0"}],
                    }
                ]
            ),
        )
        launched = aws(
            args.region,
            "ec2",
            "run-instances",
            "--client-token",
            request_id,
            "--image-id",
            image_id,
            "--instance-type",
            shape["instance_type"],
            "--iam-instance-profile",
            json.dumps({"Name": args.profile}),
            "--network-interfaces",
            json.dumps(
                [
                    {
                        "DeviceIndex": 0,
                        "SubnetId": args.subnet,
                        "Groups": [group],
                        "AssociatePublicIpAddress": True,
                    }
                ]
            ),
            "--metadata-options",
            "HttpTokens=required,HttpPutResponseHopLimit=2",
            "--instance-initiated-shutdown-behavior",
            "terminate",
            "--block-device-mappings",
            json.dumps(
                [
                    {
                        "DeviceName": "/dev/xvda",
                        "Ebs": {
                            "VolumeSize": 100,
                            "VolumeType": "gp3",
                            "Encrypted": True,
                            "DeleteOnTermination": True,
                        },
                    }
                ]
            ),
            "--tag-specifications",
            json.dumps(
                [
                    {
                        "ResourceType": "instance",
                        "Tags": [
                            {"Key": "Name", "Value": request_id},
                            {"Key": "tokenkey-purpose", "Value": "physical-restore"},
                            {
                                "Key": "expires-at",
                                "Value": str(int(time.time() + 7200)),
                            },
                        ],
                    }
                ]
            ),
            "--user-data",
            base64.b64encode(bootstrap.encode()).decode(),
        )
        instance = launched["Instances"][0]["InstanceId"]
        print(
            json.dumps({"instance": instance, "status": "starting"}),
            file=sys.stderr,
            flush=True,
        )
        deadline = time.monotonic() + 900
        while time.monotonic() < deadline:
            status = aws(
                args.region,
                "ssm",
                "describe-instance-information",
                "--filters",
                json.dumps([{"Key": "InstanceIds", "Values": [instance]}]),
            )["InstanceInformationList"]
            if status and status[0]["PingStatus"] == "Online":
                break
            time.sleep(10)
        else:
            raise BackupError("isolated restore host did not join SSM")
        # cloud-init must finish Docker install before the worker begins.
        parameters = {
            "commands": ["cloud-init status --wait >/dev/null", command],
            "executionTimeout": ["6000"],
        }
        sent = aws(
            args.region,
            "ssm",
            "send-command",
            "--instance-ids",
            instance,
            "--document-name",
            "AWS-RunShellScript",
            "--parameters",
            json.dumps(parameters),
            "--timeout-seconds",
            "600",
            "--comment",
            "approved isolated physical restore",
        )
        command_id = sent["Command"]["CommandId"]
        deadline = time.monotonic() + 6300
        while time.monotonic() < deadline:
            invocation = aws(
                args.region,
                "ssm",
                "list-command-invocations",
                "--command-id",
                command_id,
                "--details",
            )["CommandInvocations"]
            if invocation and invocation[0]["Status"] not in (
                "Pending",
                "InProgress",
                "Delayed",
            ):
                break
            time.sleep(15)
        else:
            raise BackupError("isolated restore SSM deadline exceeded")
        if invocation[0]["Status"] != "Success":
            raise BackupError(
                "isolated restore failed; inspect SSM command " + command_id
            )
        output = invocation[0]["CommandPlugins"][0]["Output"]
        result = json.loads(output.strip().splitlines()[-1])
        if result.get("status") != "ok" or not result.get("cleanup_verified"):
            raise BackupError("restore returned incomplete acceptance")
    finally:
        cleanup_cloud(args.region, request_id, instance, group)
    result.update(instance_terminated=True, volumes_deleted=True, instance_id=instance)
    atomic_json(args.receipt, result)
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (
        BackupError,
        ValueError,
        KeyError,
        OSError,
        subprocess.SubprocessError,
    ) as exc:
        print(f"isolated physical restore failed: {exc}", file=sys.stderr)
        sys.exit(1)
