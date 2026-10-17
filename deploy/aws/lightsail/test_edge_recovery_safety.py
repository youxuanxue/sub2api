import os
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[3]
PROVISION = REPO_ROOT / "deploy/aws/lightsail/provision-edge.sh"
WORKFLOW = REPO_ROOT / ".github/workflows/deploy-edge-lightsail-stage0.yml"


class EdgeRecoverySafetyTests(unittest.TestCase):
    def test_ip_rotation_rejects_retired_and_planned_before_aws(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = root / "aws"
            fake.write_text("#!/bin/sh\necho unexpected-cloud-call >&2\nexit 99\n")
            fake.chmod(0o755)
            matrix = json.loads((REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json").read_text())
            for edge, row in matrix["targets"].items():
                if row.get("deployable") and not row.get("retired"):
                    continue
                for flags in ([], ["--apply"]):
                    with self.subTest(edge=edge, flags=flags):
                        result = subprocess.run(["bash", str(REPO_ROOT / "ops/lightsail/rotate-static-ip.sh"), edge, *flags],
                            env={**os.environ, "PATH": directory + ":" + os.environ["PATH"]},
                            capture_output=True, text=True)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("retired" if row.get("retired") else "not deployable", result.stderr)
                        self.assertNotIn("unexpected-cloud-call", result.stderr)

    def test_ip_rotation_plan_uses_resolved_new_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            paths = ("ops/lightsail/rotate-static-ip.sh",
                     "deploy/aws/lightsail/resolve-edge-lightsail-target.py",
                     "ops/stage0/edge_routing_matrix.py")
            for name in paths:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(REPO_ROOT / name, target)
            matrix = json.loads((REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json").read_text())
            row = next(iter(matrix["targets"].values()))
            row.update(retired=False, deployable=True, instance_name="new-reviewed-edge")
            matrix["targets"] = {"zz1": row}
            (root / "deploy/aws/lightsail/edge-targets-lightsail.json").write_text(json.dumps(matrix))
            fake = root / "aws"
            fake.write_text('#!/bin/sh\n[ "$1 $2" = "lightsail get-static-ip" ] || exit 99\necho 192.0.2.10\n')
            fake.chmod(0o755)
            result = subprocess.run(["bash", str(root / paths[0]), "zz1"],
                env={**os.environ, "PATH": directory + ":" + os.environ["PATH"]}, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("instance_name     : new-reviewed-edge", result.stdout)
            self.assertIn("old_ip            : 192.0.2.10", result.stdout)
            self.assertIn("dry run", result.stdout)

    def run_provision_guard(
        self,
        *,
        instance_exists: bool,
        marker_exists: bool,
        allow_generate: bool,
        restore_approved: bool = True,
    ):
        with tempfile.TemporaryDirectory() as raw_tmp:
            tmp = Path(raw_tmp)
            fake_bin = tmp / "bin"
            fake_bin.mkdir()
            aws_log = tmp / "aws.log"
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
set -euo pipefail
printf '%s\\n' \"$*\" >>\"$FAKE_AWS_LOG\"
case \"$1 $2\" in
  \"lightsail get-instance\")
    if [ \"$FAKE_INSTANCE_EXISTS\" = true ]; then echo '{\"instance\":{}}'; else echo 'NotFoundException' >&2; exit 254; fi ;;
  \"ssm get-parameter\")
    if [ \"$FAKE_MARKER_EXISTS\" = true ]; then echo tokenkey-edge-historic; else echo ParameterNotFound >&2; exit 254; fi ;;
  \"ssm create-activation\") echo '{\"ActivationId\":\"act-1\",\"ActivationCode\":\"code-1\"}' ;;
  \"lightsail stop-instance\") exit 91 ;;
  *) echo \"unexpected aws call: $*\" >&2; exit 92 ;;
esac
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            env = os.environ.copy()
            env.pop("CONFIRM_EDGE_RESTORE", None)
            env.update(
                {
                    "PATH": f"{fake_bin}:{env['PATH']}",
                    "FAKE_AWS_LOG": str(aws_log),
                    "FAKE_INSTANCE_EXISTS": str(instance_exists).lower(),
                    "FAKE_MARKER_EXISTS": str(marker_exists).lower(),
                    "RECREATE": "true",
                    "RECREATE_BACKUP_VERIFIED": "false",
                    "ALLOW_SECRET_GENERATE": str(allow_generate).lower(),
                    "GHCR_OWNER": "example",
                }
            )
            if restore_approved:
                env["CONFIRM_EDGE_RESTORE"] = "I_APPROVE_EDGE_RESTORE"
            proc = subprocess.run(
                [
                    "bash", str(PROVISION), "sg1", "1.8.160", "ap-southeast-1",
                    "tokenkey-edge-sg1-ls", "tokenkey-edge-sg1-ls-ip", "ap-southeast-1a",
                    "small_3_0", "amazon_linux_2023", "api-sg1.tokenkey.dev",
                    "ops@example.com", "192.0.2.1/32", "example", "",
                    "/tokenkey/lightsail/sg1", "tokenkey-ls-sg1",
                    "tokenkey-lightsail-ssm-hybrid-sg1",
                ],
                cwd=REPO_ROOT,
                env=env,
                capture_output=True,
                text=True,
            )
            aws_text = aws_log.read_text(encoding="utf-8") if aws_log.exists() else ""
            return proc, aws_text

    def test_refuses_without_edge_restore_approval(self):
        proc, aws_log = self.run_provision_guard(
            instance_exists=True,
            marker_exists=True,
            allow_generate=False,
            restore_approved=False,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("retired", proc.stderr)
        self.assertEqual(aws_log, "")

    def test_recreate_without_verified_backup_aborts_before_activation_or_delete(self):
        proc, aws_log = self.run_provision_guard(
            instance_exists=True, marker_exists=True, allow_generate=False
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("verified secret backup is required before recreate", proc.stderr)
        self.assertNotIn("create-activation", aws_log)
        self.assertNotIn("stop-instance", aws_log)
        self.assertNotIn("delete-instance", aws_log)

    def test_historic_marker_disables_generation_even_when_instance_is_absent(self):
        proc, aws_log = self.run_provision_guard(
            instance_exists=False, marker_exists=True, allow_generate=True
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("secret generation is forbidden for an existing Edge identity", proc.stderr)
        self.assertNotIn("create-activation", aws_log)

    def test_retired_direct_provision_refuses_before_any_aws_call(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = Path(tmp) / "aws"
            fake.write_text("#!/bin/sh\necho unexpected-cloud-call >&2\nexit 99\n")
            fake.chmod(0o755)
            result = subprocess.run(["bash", str(PROVISION), "uk1", "1.8.160", "eu-west-2",
                                     "tokenkey-edge-uk1-aws-20261015"],
                                    env={**os.environ, "PATH": tmp + ":" + os.environ["PATH"]},
                                    capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("retired", result.stderr)
            self.assertNotIn("unexpected-cloud-call", result.stderr)

    def test_workflow_orders_recreate_backup_and_role_checks(self):
        workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
        steps = workflow["jobs"]["edge"]["steps"]
        names = [step.get("name", "") for step in steps]
        approve = names.index("Require edge restore approval")
        prepare = names.index("Prepare safe Lightsail provision")
        provision = names.index("Provision Lightsail Edge")
        resolve = names.index("Resolve SSM managed instance + API URL")
        verify_role = names.index("Verify Edge SSM role")
        backup = names.index("Backup Edge env secrets off-box")
        self.assertLess(approve, prepare)
        self.assertLess(prepare, provision)
        self.assertLess(resolve, verify_role)
        self.assertLess(verify_role, backup)
        prepare_run = steps[prepare]["run"]
        self.assertIn("prepare-edge-provision.sh", prepare_run)
        self.assertIn("ssm_hybrid_role_name", prepare_run)


if __name__ == "__main__":
    unittest.main()
