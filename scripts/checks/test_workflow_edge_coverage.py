#!/usr/bin/env python3
"""Tests for Gate C (scripts/checks/workflow-edge-coverage.py).

Drives the real module against temp fixtures by patching its path constants, so
the YAML parsing and matrix logic are exercised end-to-end. stdlib + pyyaml.
"""
from __future__ import annotations

import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

import yaml

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "workflow-edge-coverage.py"
_spec = importlib.util.spec_from_file_location("workflow_edge_coverage", _MOD_PATH)
wec = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(wec)


def _matrix(deployable_ids: list[str]) -> str:
    return json.dumps({"targets": {eid: {"deployable": True} for eid in deployable_ids}})


def _workflow(
    input_name: str,
    options: list[str],
    *,
    concurrency_group: str = "",
    cancel_in_progress: bool = False,
) -> str:
    # safe_dump writes the key "on" literally; on reload YAML 1.1 turns it back
    # into boolean True — exactly the real-workflow shape the check handles.
    doc = {
            "name": "T",
            "on": {"workflow_dispatch": {"inputs": {input_name: {"type": "choice", "options": options}}}},
            "jobs": {"x": {"runs-on": "ubuntu-latest", "steps": [{"run": "true"}]}},
        }
    if concurrency_group:
        doc["concurrency"] = {
            "group": concurrency_group,
            "cancel-in-progress": cancel_in_progress,
        }
    return yaml.safe_dump(doc, sort_keys=False)


class WorkflowEdgeCoverageTest(unittest.TestCase):
    def _run(self, *, ec2: list[str], lightsail: list[str], wf_options: list[str],
             input_name: str = "edge_id", option_prefix: str = "",
             required_set: str = "ec2-deployable", opt_out: list[dict] | None = None) -> int:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            (root / "deploy/aws/stage0").mkdir(parents=True)
            (root / "deploy/aws/lightsail").mkdir(parents=True)
            (root / ".github/workflows").mkdir(parents=True)
            ec2_path = root / "deploy/aws/stage0/edge-targets.json"
            ls_path = root / "deploy/aws/lightsail/edge-targets-lightsail.json"
            ec2_path.write_text(_matrix(ec2))
            ls_path.write_text(_matrix(lightsail))
            wf_rel = ".github/workflows/wf.yml"
            (root / wf_rel).write_text(_workflow(input_name, wf_options))
            reg = root / "registry.json"
            reg.write_text(json.dumps({"workflows": {wf_rel: {
                "input": input_name, "option_prefix": option_prefix,
                "required_set": required_set, "opt_out_edges": opt_out or [],
            }}}))
            with mock.patch.object(wec, "REPO_ROOT", root), \
                 mock.patch.object(wec, "REGISTRY", reg), \
                 mock.patch.object(wec, "EC2_MATRIX", ec2_path), \
                 mock.patch.object(wec, "LIGHTSAIL_MATRIX", ls_path):
                return wec.main()

    def test_covered_passes(self) -> None:
        self.assertEqual(self._run(ec2=["us1"], lightsail=[], wf_options=["fra1", "us1"]), 0)

    def test_new_deployable_edge_missing_from_options_fails(self) -> None:
        # us9 added to the matrix but not to the workflow options -> drift.
        self.assertEqual(self._run(ec2=["us1", "us9"], lightsail=[], wf_options=["us1"]), 1)

    def test_opt_out_covers_missing_edge(self) -> None:
        self.assertEqual(
            self._run(ec2=["us1", "us9"], lightsail=[], wf_options=["us1"],
                      opt_out=[{"id": "us9", "reason": "pilot only"}]),
            0,
        )

    def test_stale_opt_out_fails(self) -> None:
        # us9 opted out but no longer deployable -> registry must be cleaned.
        self.assertEqual(
            self._run(ec2=["us1"], lightsail=[], wf_options=["us1"],
                      opt_out=[{"id": "us9", "reason": "pilot only"}]),
            1,
        )

    def test_opt_out_without_reason_fails(self) -> None:
        self.assertEqual(
            self._run(ec2=["us1", "us9"], lightsail=[], wf_options=["us1"],
                      opt_out=[{"id": "us9", "reason": "  "}]),
            1,
        )

    def test_prefixed_option_pg_dump_shape(self) -> None:
        # edge id us1 must appear as option 'edge-us1' when option_prefix='edge-'.
        self.assertEqual(
            self._run(ec2=["us1"], lightsail=[], wf_options=["prod", "edge-us1", "all"],
                      input_name="target", option_prefix="edge-", required_set="all-deployable"),
            0,
        )

    def _run_concurrency_contract(
        self,
        *,
        ec2_group: str,
        lightsail_group: str,
        ec2_cancel: bool = False,
        lightsail_cancel: bool = False,
    ) -> int:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            (root / "deploy/aws/stage0").mkdir(parents=True)
            (root / "deploy/aws/lightsail").mkdir(parents=True)
            (root / ".github/workflows").mkdir(parents=True)
            ec2_matrix = root / "deploy/aws/stage0/edge-targets.json"
            lightsail_matrix = root / "deploy/aws/lightsail/edge-targets-lightsail.json"
            ec2_matrix.write_text(_matrix([]))
            lightsail_matrix.write_text(_matrix([]))
            ec2_rel = ".github/workflows/ec2.yml"
            lightsail_rel = ".github/workflows/lightsail.yml"
            (root / ec2_rel).write_text(
                _workflow("edge_id", [], concurrency_group=ec2_group, cancel_in_progress=ec2_cancel)
            )
            (root / lightsail_rel).write_text(
                _workflow(
                    "edge_id", [], concurrency_group=lightsail_group,
                    cancel_in_progress=lightsail_cancel,
                )
            )
            registry = root / "registry.json"
            registry.write_text(json.dumps({
                "workflows": {
                    ec2_rel: {"input": "edge_id", "required_set": "ec2-deployable"},
                    lightsail_rel: {"input": "edge_id", "required_set": "lightsail-deployable"},
                },
                "concurrency_contracts": [{
                    "name": "edge-stage0",
                    "workflows": [ec2_rel, lightsail_rel],
                    "group": "edge-stage0-${{ inputs.edge_id }}",
                    "cancel_in_progress": False,
                }],
            }))
            with mock.patch.object(wec, "REPO_ROOT", root), \
                 mock.patch.object(wec, "REGISTRY", registry), \
                 mock.patch.object(wec, "EC2_MATRIX", ec2_matrix), \
                 mock.patch.object(wec, "LIGHTSAIL_MATRIX", lightsail_matrix):
                return wec.main()

    def test_shared_edge_workflows_use_same_concurrency_group(self) -> None:
        expected = "edge-stage0-${{ inputs.edge_id }}"
        self.assertEqual(
            self._run_concurrency_contract(ec2_group=expected, lightsail_group=expected),
            0,
        )

    def test_concurrency_group_drift_fails(self) -> None:
        self.assertEqual(
            self._run_concurrency_contract(
                ec2_group="edge-stage0-${{ inputs.edge_id }}",
                lightsail_group="deploy-edge-lightsail-${{ inputs.edge_id }}",
            ),
            1,
        )

    def test_cancel_in_progress_must_remain_false(self) -> None:
        expected = "edge-stage0-${{ inputs.edge_id }}"
        self.assertEqual(
            self._run_concurrency_contract(
                ec2_group=expected,
                lightsail_group=expected,
                ec2_cancel=True,
            ),
            1,
        )


class Ec2WorkflowSafetyContractTest(unittest.TestCase):
    WORKFLOW = pathlib.Path(__file__).resolve().parents[2] / ".github/workflows/deploy-edge-stage0.yml"

    def _load_steps(self) -> list[dict]:
        self.assertTrue(self.WORKFLOW.is_file(), "EC2 Edge workflow is not implemented")
        doc = yaml.safe_load(self.WORKFLOW.read_text(encoding="utf-8")) or {}
        return doc["jobs"]["edge"]["steps"]

    def test_stack_confirmation_and_candidate_gate_precede_oidc(self) -> None:
        steps = self._load_steps()
        credential_index = next(
            i for i, step in enumerate(steps)
            if str(step.get("uses", "")).startswith("aws-actions/configure-aws-credentials@")
        )
        resolve_index = next(i for i, step in enumerate(steps) if step.get("id") == "edge")
        resolve_script = str(steps[resolve_index].get("run", ""))
        self.assertLess(resolve_index, credential_index)
        self.assertIn("--confirm-stack", resolve_script)
        self.assertIn("--allow-migration-candidate", resolve_script)

    def test_workflow_exposes_all_migration_operations(self) -> None:
        self.assertTrue(self.WORKFLOW.is_file(), "EC2 Edge workflow is not implemented")
        doc = yaml.safe_load(self.WORKFLOW.read_text(encoding="utf-8")) or {}
        on = doc.get("on", doc.get(True, {}))
        operations = on["workflow_dispatch"]["inputs"]["operation"]["options"]
        self.assertEqual(
            operations,
            ["provision", "upgrade", "rollback", "smoke", "rotate_egress_ip", "decommission"],
        )

    def test_candidate_rotation_uses_ssm_probe_without_public_tls(self) -> None:
        steps = self._load_steps()
        script = next(step["run"] for step in steps if step.get("id") == "rotation")
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            curl_log = root / "curl.log"
            output = root / "github-output"
            aws = bin_dir / "aws"
            aws.write_text("""#!/usr/bin/env bash
set -euo pipefail
case \"$*\" in
  *\"ec2 describe-addresses\"*) echo 1.1.1.1 ;;
  *\"ec2 allocate-address\"*) echo '{\"AllocationId\":\"eipalloc-22222222\",\"PublicIp\":\"2.2.2.2\"}' ;;
  *\"cloudformation deploy\"*) ;;
  *\"ssm describe-instance-information\"*) echo Online ;;
  *\"ssm send-command\"*) echo cmd-123 ;;
  *\"ssm wait command-executed\"*) ;;
  *\"ssm get-command-invocation\"*) echo 2.2.2.2 ;;
  *) echo \"unexpected aws call: $*\" >&2; exit 90 ;;
esac
""")
            aws.chmod(0o755)
            curl = bin_dir / "curl"
            curl.write_text("""#!/usr/bin/env bash
printf '%s\\n' \"$*\" >> \"$CURL_LOG\"
exit 88
""")
            curl.chmod(0o755)
            env = os.environ.copy()
            env.update({
                "PATH": f"{bin_dir}:{env['PATH']}",
                "CURL_LOG": str(curl_log),
                "GITHUB_OUTPUT": str(output),
                "STACK_NAME": "tokenkey-edge-us5-stage0",
                "EDGE_ID": "us5",
                "REGION": "us-west-2",
                "DOMAIN": "api-us5.tokenkey.dev",
                "INSTANCE_ID": "i-11111111",
                "OLD_ALLOC": "eipalloc-11111111",
                "INPUT_CANDIDATE_ALLOC": "",
                "INPUT_ROTATION_REASON": "migration-probe",
                "INPUT_ALLOW_CANDIDATE": "true",
                "CFN_EXECUTION_ROLE_ARN": "arn:aws:iam::123456789012:role/tokenkey-cfn-ec2-edge-stage0",
            })
            proc = subprocess.run(
                ["bash", "-c", script],
                cwd=pathlib.Path(__file__).resolve().parents[2],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, f"stdout:{proc.stdout}\nstderr:{proc.stderr}")
            self.assertFalse(curl_log.exists(), "candidate rotation must not probe public TLS")


if __name__ == "__main__":
    unittest.main()
