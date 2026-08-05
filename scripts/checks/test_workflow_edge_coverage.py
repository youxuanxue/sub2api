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

    def _load_workflow(self) -> dict:
        self.assertTrue(self.WORKFLOW.is_file(), "EC2 Edge workflow is not implemented")
        return yaml.safe_load(self.WORKFLOW.read_text(encoding="utf-8")) or {}

    def _load_steps(self) -> list[dict]:
        return self._load_workflow()["jobs"]["edge"]["steps"]

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

    def test_graviton_workflow_cannot_bypass_multi_arch_release_gate(self) -> None:
        doc = self._load_workflow()
        on = doc.get("on", doc.get(True, {}))
        inputs = on["workflow_dispatch"]["inputs"]
        self.assertNotIn("simple_release_override", inputs)
        self.assertNotIn("INPUT_OVERRIDE", doc["jobs"]["edge"].get("env", {}))
        verify_step = next(
            step
            for step in doc["jobs"]["edge"]["steps"]
            if step.get("name") == "Verify released multi-arch image"
        )
        self.assertEqual(
            verify_step["run"],
            'bash ops/stage0/verify_ghcr_manifest.sh "$INPUT_TAG" false',
        )

    def test_ec2_provision_derives_cfn_role_and_runs_preflight_before_eip(self) -> None:
        doc = self._load_workflow()
        job = doc["jobs"]["edge"]
        steps = job["steps"]
        self.assertNotIn("CFN_EXECUTION_ROLE_ARN", job.get("env", {}))

        resolve_step = next(step for step in steps if step.get("id") == "edge")
        resolve_script = str(resolve_step.get("run", ""))
        self.assertNotIn("AWS_EC2_EDGE_CFN_ROLE_ARN", resolve_script)
        self.assertIn("role/tokenkey-cfn-ec2-edge-stage0", resolve_script)
        self.assertIn("cfn_execution_role_arn=", resolve_script)

        credential_index = next(
            i for i, step in enumerate(steps)
            if str(step.get("uses", "")).startswith("aws-actions/configure-aws-credentials@")
        )
        preflight_index = next(
            i for i, step in enumerate(steps)
            if step.get("name") == "Run live migration preflight"
        )
        provision_index = next(i for i, step in enumerate(steps) if step.get("id") == "provision")
        self.assertLess(credential_index, preflight_index)
        self.assertLess(preflight_index, provision_index)

        preflight_step = steps[preflight_index]
        self.assertEqual(preflight_step.get("if"), "inputs.operation == 'provision'")
        self.assertEqual(
            preflight_step.get("env", {}).get("REPORT"),
            "${{ runner.temp }}/all-edge-ec2-migration-preflight.json",
        )
        preflight_script = str(preflight_step.get("run", ""))
        self.assertIn("edge-platform-migration-preflight.sh", preflight_script)
        self.assertIn("jq -e '.blockers == []'", preflight_script)

        provision_script = str(steps[provision_index].get("run", ""))
        self.assertIn("aws ec2 allocate-address", provision_script)
        for step in steps:
            if "aws cloudformation deploy" not in str(step.get("run", "")):
                continue
            self.assertEqual(
                step.get("env", {}).get("CFN_EXECUTION_ROLE_ARN"),
                "${{ steps.edge.outputs.cfn_execution_role_arn }}",
            )

    def test_provision_checks_ghcr_pat_metadata_without_reading_the_secret(self) -> None:
        provision_step = next(
            step for step in self._load_steps() if step.get("id") == "provision"
        )
        script = str(provision_step.get("run", ""))
        self.assertIn("aws ssm describe-parameters", script)
        self.assertIn("Parameters[0].Type", script)
        self.assertNotIn(
            'aws ssm get-parameter --region "$REGION" --name "$GHCR_PAT_SSM_NAME"',
            script,
        )

    def test_provision_uses_the_edge_owned_ghcr_pat_path(self) -> None:
        provision_step = next(
            step for step in self._load_steps() if step.get("id") == "provision"
        )
        self.assertNotIn("GHCR_PAT_SSM_NAME_OVERRIDE", provision_step.get("env", {}))
        script = str(provision_step.get("run", ""))
        self.assertIn('GHCR_PAT_SSM_NAME="${SSM_PREFIX}/ghcr/pat"', script)
        self.assertNotIn("GHCR_PAT_SSM_NAME_OVERRIDE", script)

    def _run_candidate_health(
        self,
        *,
        waiter_exit: int,
        statuses: tuple[str, ...],
    ) -> tuple[subprocess.CompletedProcess[str], dict, str]:
        step = next(
            item for item in self._load_steps()
            if item.get("name") == "Candidate local health via SSM"
        )
        smoke_payload = pathlib.Path("/tmp/tokenkey-edge-local-smoke.json")
        smoke_payload.unlink(missing_ok=True)
        try:
            with tempfile.TemporaryDirectory() as d:
                root = pathlib.Path(d)
                bin_dir = root / "bin"
                bin_dir.mkdir()
                aws_log = root / "aws.log"
                payload_capture = root / "payload.json"
                status_file = root / "statuses"
                status_file.write_text("\n".join(statuses) + "\n", encoding="utf-8")
                aws = bin_dir / "aws"
                aws.write_text(r'''#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$AWS_LOG"
case "$*" in
  "ssm send-command"*)
    for arg in "$@"; do
      case "$arg" in file://*) cp "${arg#file://}" "$PAYLOAD_CAPTURE" ;; esac
    done
    echo cmd-123
    ;;
  "ssm wait command-executed"*) exit "${WAITER_EXIT}" ;;
  *"--query Status --output text"*)
    count="$(cat "$STATUS_COUNT" 2>/dev/null || echo 0)"
    count=$((count + 1))
    printf '%s\n' "$count" > "$STATUS_COUNT"
    value="$(sed -n "${count}p" "$STATUS_FILE")"
    [ -n "$value" ] || value="$(tail -1 "$STATUS_FILE")"
    printf '%s\n' "$value"
    ;;
  *"--query StandardErrorContent --output text"*) echo command-failed ;;
  *) echo "unexpected aws call: $*" >&2; exit 90 ;;
esac
''', encoding="utf-8")
                aws.chmod(0o755)
                sleep = bin_dir / "sleep"
                sleep.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
                sleep.chmod(0o755)
                env = os.environ.copy()
                env.update({
                    "PATH": f"{bin_dir}:{env['PATH']}",
                    "AWS_LOG": str(aws_log),
                    "PAYLOAD_CAPTURE": str(payload_capture),
                    "STATUS_COUNT": str(root / "status-count"),
                    "STATUS_FILE": str(status_file),
                    "WAITER_EXIT": str(waiter_exit),
                    "INPUT_EDGE_ID": "us5",
                    "INSTANCE_ID": "i-11111111",
                    "REGION": "us-west-2",
                })
                completed = subprocess.run(
                    ["bash", "-c", str(step["run"])],
                    cwd=pathlib.Path(__file__).resolve().parents[2],
                    env=env,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                payload = (
                    json.loads(payload_capture.read_text(encoding="utf-8"))
                    if payload_capture.exists()
                    else {}
                )
                return completed, payload, aws_log.read_text(encoding="utf-8")
        finally:
            smoke_payload.unlink(missing_ok=True)

    def test_candidate_health_waits_for_cloud_init_before_docker(self) -> None:
        completed, payload, _ = self._run_candidate_health(
            waiter_exit=0,
            statuses=("Success",),
        )
        self.assertEqual(
            0,
            completed.returncode,
            f"stdout:{completed.stdout}\nstderr:{completed.stderr}",
        )
        commands = payload["commands"]
        cloud_init_commands = [
            index for index, command in enumerate(commands)
            if "cloud-init status --wait" in command
        ]
        self.assertEqual(
            1,
            len(cloud_init_commands),
            f"candidate smoke must wait for cloud-init exactly once: {commands}",
        )
        cloud_init = cloud_init_commands[0]
        docker = next(
            index for index, command in enumerate(commands)
            if "docker compose" in command
        )
        self.assertLess(cloud_init, docker)

    def test_candidate_health_polls_long_command_without_aws_waiter_timeout(self) -> None:
        completed, _, aws_calls = self._run_candidate_health(
            waiter_exit=42,
            statuses=("InProgress", "Success"),
        )
        self.assertEqual(
            0,
            completed.returncode,
            f"stdout:{completed.stdout}\nstderr:{completed.stderr}",
        )
        self.assertNotIn("ssm wait command-executed", aws_calls)

    def test_decommission_skips_candidate_health(self) -> None:
        step = next(
            item for item in self._load_steps()
            if item.get("name") == "Candidate local health via SSM"
        )
        condition = str(step.get("if", ""))
        self.assertIn("inputs.operation != 'decommission'", condition)

    def _run_operation_validation(
        self,
        *,
        deployable: bool,
        migration_candidate: bool,
        allow_candidate: bool,
        active_rotation_ack: bool,
    ) -> subprocess.CompletedProcess:
        steps = self._load_steps()
        script = next(
            step["run"]
            for step in steps
            if step.get("name") == "Validate operation inputs"
        )
        script = script.replace(
            "${{ steps.edge.outputs.migration_candidate }}",
            str(migration_candidate).lower(),
        ).replace(
            "${{ steps.edge.outputs.deployable }}",
            str(deployable).lower(),
        )
        env = os.environ.copy()
        env.update({
            "INPUT_ALLOW_CANDIDATE": str(allow_candidate).lower(),
            "INPUT_OPERATION": "rotate_egress_ip",
            "INPUT_TAG": "",
            "INPUT_ROTATION_REASON": "upstream-risk-block",
            "INPUT_CANDIDATE_ALLOC": "",
            "INPUT_ACTIVE_ROTATION_ACK": str(active_rotation_ack).lower(),
            "INPUT_DECOMMISSION_ACK": "false",
        })
        return subprocess.run(
            ["bash", "-c", script],
            cwd=pathlib.Path(__file__).resolve().parents[2],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_active_rotation_requires_manual_dns_acknowledgement(self) -> None:
        proc = self._run_operation_validation(
            deployable=True,
            migration_candidate=False,
            allow_candidate=False,
            active_rotation_ack=False,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("active EIP rotation requires", proc.stderr + proc.stdout)

    def test_candidate_rotation_does_not_require_active_dns_acknowledgement(self) -> None:
        proc = self._run_operation_validation(
            deployable=False,
            migration_candidate=True,
            allow_candidate=True,
            active_rotation_ack=False,
        )
        self.assertEqual(proc.returncode, 0, f"stdout:{proc.stdout}\nstderr:{proc.stderr}")

    def _run_rotation(
        self,
        *,
        allow_candidate: bool,
        curl_exit: int = 0,
        release_exit: int = 0,
    ) -> tuple[subprocess.CompletedProcess, bool, str]:
        steps = self._load_steps()
        script = next(step["run"] for step in steps if step.get("id") == "rotation")
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            curl_log = root / "curl.log"
            aws_log = root / "aws.log"
            output = root / "github-output"
            aws = bin_dir / "aws"
            aws.write_text("""#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$AWS_LOG"
case \"$*\" in
  *\"ec2 describe-addresses\"*) echo 1.1.1.1 ;;
  *\"ec2 allocate-address\"*) echo '{\"AllocationId\":\"eipalloc-22222222\",\"PublicIp\":\"2.2.2.2\"}' ;;
  *\"cloudformation deploy\"*) ;;
  *\"ssm describe-instance-information\"*) echo Online ;;
  *\"ssm send-command\"*) echo cmd-123 ;;
  *\"ssm wait command-executed\"*) ;;
  *\"ssm get-command-invocation\"*) echo 2.2.2.2 ;;
  *\"ec2 release-address\"*) echo release-denied >&2; exit \"${RELEASE_EXIT:-0}\" ;;
  *) echo \"unexpected aws call: $*\" >&2; exit 90 ;;
esac
""")
            aws.chmod(0o755)
            curl = bin_dir / "curl"
            curl.write_text("""#!/usr/bin/env bash
printf '%s\\n' \"$*\" >> \"$CURL_LOG\"
exit \"${CURL_EXIT:-0}\"
""")
            curl.chmod(0o755)
            env = os.environ.copy()
            env.update({
                "PATH": f"{bin_dir}:{env['PATH']}",
                "CURL_LOG": str(curl_log),
                "AWS_LOG": str(aws_log),
                "GITHUB_OUTPUT": str(output),
                "STACK_NAME": "tokenkey-edge-us5-stage0",
                "EDGE_ID": "us5",
                "REGION": "us-west-2",
                "DOMAIN": "api-us5.tokenkey.dev",
                "INSTANCE_ID": "i-11111111",
                "OLD_ALLOC": "eipalloc-11111111",
                "INPUT_CANDIDATE_ALLOC": "",
                "INPUT_ROTATION_REASON": "migration-probe",
                "INPUT_ALLOW_CANDIDATE": str(allow_candidate).lower(),
                "CURL_EXIT": str(curl_exit),
                "RELEASE_EXIT": str(release_exit),
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
            return proc, curl_log.exists(), aws_log.read_text(encoding="utf-8")

    def test_candidate_rotation_uses_ssm_probe_without_public_tls(self) -> None:
        proc, curl_called, aws_calls = self._run_rotation(allow_candidate=True)
        self.assertEqual(proc.returncode, 0, f"stdout:{proc.stdout}\nstderr:{proc.stderr}")
        self.assertFalse(curl_called, "candidate rotation must not probe public TLS")
        self.assertIn(
            "ec2 release-address --region us-west-2 --allocation-id eipalloc-11111111",
            aws_calls,
        )

    def test_active_rotation_handoff_marks_dns_pending_and_retains_rollback_eip(self) -> None:
        rotation, curl_called, aws_calls = self._run_rotation(allow_candidate=False)
        self.assertEqual(
            rotation.returncode,
            0,
            f"stdout:{rotation.stdout}\nstderr:{rotation.stderr}",
        )
        self.assertTrue(curl_called)
        self.assertNotIn("ec2 release-address", aws_calls)

        steps = self._load_steps()
        step = next((item for item in steps if item.get("id") == "rotation_handoff"), None)
        self.assertIsNotNone(step, "active rotation must emit an explicit DNS handoff")
        with tempfile.TemporaryDirectory() as d:
            summary = pathlib.Path(d) / "summary.md"
            env = os.environ.copy()
            env.update({
                "GITHUB_STEP_SUMMARY": str(summary),
                "EDGE_ID": "us4",
                "REGION": "us-west-2",
                "DOMAIN": "api-us4.tokenkey.dev",
                "OLD_ALLOC": "eipalloc-11111111",
                "OLD_IP": "1.1.1.1",
                "NEW_ALLOC": "eipalloc-22222222",
                "NEW_IP": "2.2.2.2",
            })
            proc = subprocess.run(
                ["bash", "-c", step["run"]],
                cwd=pathlib.Path(__file__).resolve().parents[2],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            rendered = summary.read_text(encoding="utf-8") if summary.exists() else ""
        self.assertEqual(proc.returncode, 0, f"stdout:{proc.stdout}\nstderr:{proc.stderr}")
        self.assertIn("pending_manual_dns", rendered)
        self.assertIn("api-us4.tokenkey.dev", rendered)
        self.assertIn(
            "aws ec2 release-address --region us-west-2 --allocation-id eipalloc-11111111",
            rendered,
        )

    def test_rotation_revert_reports_eip_release_failure(self) -> None:
        proc, curl_called, _aws_calls = self._run_rotation(
            allow_candidate=False,
            curl_exit=88,
            release_exit=42,
        )
        self.assertEqual(proc.returncode, 88)
        self.assertTrue(curl_called)
        self.assertIn(
            "could not release candidate EIP eipalloc-22222222 in us-west-2",
            proc.stderr,
        )


if __name__ == "__main__":
    unittest.main()
