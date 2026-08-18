import os
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[3]
PROVISION = REPO_ROOT / "deploy/aws/lightsail/provision-edge.sh"
WORKFLOW = REPO_ROOT / ".github/workflows/deploy-edge-lightsail-stage0.yml"


class EdgeRecoverySafetyTests(unittest.TestCase):
    def run_provision_guard(
        self, *, instance_exists: bool, marker_exists: bool, allow_generate: bool
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
            proc = subprocess.run(
                [
                    "bash", str(PROVISION), "us3", "1.8.160", "us-east-2",
                    "tokenkey-edge-us3", "tokenkey-edge-us3-ip", "us-east-2a",
                    "small_3_0", "amazon_linux_2023", "us3.example.com",
                    "ops@example.com", "192.0.2.1/32", "example", "",
                    "/tokenkey/lightsail/us3", "tokenkey-ls-us3",
                    "tokenkey-lightsail-ssm-hybrid-us3",
                ],
                cwd=REPO_ROOT,
                env=env,
                capture_output=True,
                text=True,
            )
            return proc, aws_log.read_text(encoding="utf-8")

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

    def test_workflow_orders_recreate_backup_and_role_checks(self):
        workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
        steps = workflow["jobs"]["edge"]["steps"]
        names = [step.get("name", "") for step in steps]
        prepare = names.index("Prepare safe Lightsail provision")
        provision = names.index("Provision Lightsail Edge")
        resolve = names.index("Resolve SSM managed instance + API URL")
        verify_role = names.index("Verify Edge SSM role")
        backup = names.index("Backup Edge env secrets off-box")
        self.assertLess(prepare, provision)
        self.assertLess(resolve, verify_role)
        self.assertLess(verify_role, backup)
        prepare_run = steps[prepare]["run"]
        self.assertIn("prepare-edge-provision.sh", prepare_run)
        self.assertIn("ssm_hybrid_role_name", prepare_run)


if __name__ == "__main__":
    unittest.main()
