#!/usr/bin/env python3
"""Prepare a single-use Hybrid identity through the existing deployment role.

The operator can register a fresh replacement without acquiring iam:PassRole.
This step never changes an instance, IP, routing, or IAM policy.
"""
from __future__ import annotations

import argparse
import datetime as dt
import importlib.util
import json
import os
import pathlib
import re
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]


class AwsError(RuntimeError):
    pass


def aws(region: str, *args: str) -> dict:
    result = subprocess.run(
        ["aws", *args, "--region", region, "--output", "json"],
        capture_output=True, text=True, timeout=120,
    )
    if result.returncode:
        # Do not echo command arguments or response bodies containing credentials.
        code = "NotFoundException" if "NotFoundException" in result.stderr else "AWS call failed"
        raise AwsError(f"{args[0]} {args[1]}: {code}")
    return json.loads(result.stdout or "{}")


def prepare(target: dict, replacement: str, run_id: str, call=aws, *, replacement_arn: str = "") -> dict:
    source = target["instance_name"]
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,254}", replacement) or replacement == source:
        raise ValueError("replacement must be a distinct valid Lightsail instance name")
    if not re.fullmatch(r"[0-9]+-[0-9]+", run_id):
        raise ValueError("run identity must be GitHub run_id-run_attempt")
    edge, region = target["edge_id"], target["lightsail_region"]
    if replacement_arn and not re.fullmatch(
        rf"arn:aws:lightsail:{re.escape(region)}:[0-9]{{12}}:Instance/[A-Za-z0-9-]+", replacement_arn
    ):
        raise ValueError("replacement ARN must identify a Lightsail instance in the target region")
    old = call(region, "lightsail", "get-instance", "--instance-name", source)["instance"]
    if old["name"] != source or old["location"]["regionName"] != region:
        raise ValueError("live source differs from confirmed matrix target")
    try:
        existing = call(region, "lightsail", "get-instance", "--instance-name", replacement)["instance"]
    except AwsError as exc:
        if replacement_arn or "NotFoundException" not in str(exc):
            raise
    else:
        tags = {tag["key"]: tag["value"] for tag in existing.get("tags", [])}
        batch = tags.get("MigrationBatch", "")
        if not replacement_arn or any((
            existing.get("arn") != replacement_arn,
            existing.get("name") != replacement,
            existing.get("location", {}).get("regionName") != region,
            existing.get("state", {}).get("name") != "running",
            tags.get("Project") != "tokenkey",
            tags.get("MigrationTarget") != edge,
            tags.get("Purpose") != "canary",
            not re.fullmatch(r"aws-[0-9]{8}", batch),
            replacement != f"tokenkey-edge-{edge}-{batch}",
        )):
            raise ValueError("existing replacement requires its exact ARN and matching fresh migration canary identity")
    # Also reject repeat preparation while a previous identity is usable, or
    # after registration. Never mint a second identity for an enrolled host.
    activations = call(region, "ssm", "describe-activations")["ActivationList"]
    for activation in activations:
        if activation.get("DefaultInstanceName") == replacement and (
            activation.get("RegistrationsCount", 0) > 0 or not activation.get("Expired", False)
        ):
            raise ValueError("replacement already has a registered or unexpired activation")
    managed = call(region, "ssm", "describe-instance-information")["InstanceInformationList"]
    if any(row.get("ComputerName", "").split(".")[0] == replacement for row in managed):
        raise ValueError("replacement is already managed by SSM")
    expires = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=4)).isoformat()
    activation = call(
        region, "ssm", "create-activation", "--iam-role", target["ssm_hybrid_role_name"],
        "--default-instance-name", replacement, "--registration-limit", "1",
        "--expiration-date", expires, "--description", f"host replacement {edge} run {run_id}",
        "--tags", "Key=Project,Value=tokenkey", f"Key=EdgeId,Value={edge}",
        "Key=Platform,Value=lightsail",
    )
    parameter = f"{target['ssm_prefix']}/resize-activation/{run_id}"
    payload = {
        "edge_id": edge, "region": region, "source_instance": source,
        "replacement_instance": replacement, "expires_at": expires,
        "activation_id": activation["ActivationId"], "activation_code": activation["ActivationCode"],
        "replacement_arn": replacement_arn,
    }
    try:
        with tempfile.TemporaryDirectory(prefix="edge-resize-identity-") as directory:
            path = pathlib.Path(directory) / "activation.json"
            path.touch(mode=0o600)
            path.write_text(json.dumps(payload), encoding="utf-8")
            call(region, "ssm", "put-parameter", "--name", parameter,
                 "--type", "SecureString", "--value", f"file://{path}")
    except Exception:
        # Revoke only the activation created by this attempt. Never alter an
        # existing node's registration or overwrite another run's parameter.
        call(region, "ssm", "delete-activation", "--activation-id", activation["ActivationId"])
        raise
    return {key: value for key, value in payload.items() if key != "activation_code"} | {"parameter": parameter}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--edge-id", required=True)
    parser.add_argument("--confirm-instance", required=True)
    parser.add_argument("--replacement-instance", required=True)
    parser.add_argument("--replacement-arn", default="", help="Exact ARN when registering an already-created migration canary")
    args = parser.parse_args()
    if os.environ.get("GITHUB_ACTIONS") != "true":
        raise SystemExit("Use the Edge deployment workflow with operation=prepare-resize")
    spec = importlib.util.spec_from_file_location("edge_target", ROOT / "deploy/aws/lightsail/resolve-edge-lightsail-target.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    target = module.resolve_target(module.load_matrix(str(module.DEFAULT_MATRIX)), args.edge_id,
                                   confirm_instance=args.confirm_instance, allow_planned=True)
    result = prepare(target, args.replacement_instance,
                     f"{os.environ['GITHUB_RUN_ID']}-{os.environ['GITHUB_RUN_ATTEMPT']}",
                     replacement_arn=args.replacement_arn)
    print(json.dumps(result, sort_keys=True))
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
        summary.write(f"\nResize registration prepared for `{result['replacement_instance']}`. "
                      f"Single use; expires `{result['expires_at']}`. SecureString: `{result['parameter']}`.\n")


if __name__ == "__main__":
    main()
