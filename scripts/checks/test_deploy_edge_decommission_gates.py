"""Structural tests for the decommission op added to deploy-edge-stage0.yml.

The workflow logic itself runs inside GHA — we can't easily black-box it from
the test suite. What we CAN do is parse the YAML and assert the safety
contracts hold at the structural level:

  1. operation=decommission is wired through the same resolver as the other
     ops (so confirm_stack is enforced).
  2. The Validate step requires both i_understand_destroys_data=true AND
     matrix.deployable=false BEFORE the destructive steps run.
  3. The EBS snapshot step runs BEFORE the delete-stack step.
  4. release_eip default is false (additive opt-in).
  5. The decommission steps don't accidentally apply to provision / upgrade /
     smoke / rollback / rotate_egress_ip ops (no over-firing).

stdlib-only.
"""
from __future__ import annotations

import pathlib
import re
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github/workflows/deploy-edge-stage0.yml"


class DecommissionGatesTests(unittest.TestCase):
    def setUp(self):
        self.text = WORKFLOW.read_text(encoding="utf-8")

    def test_decommission_is_a_workflow_dispatch_option(self):
        self.assertIn("- decommission\n", self.text)

    def test_i_understand_destroys_data_default_false(self):
        block = re.search(
            r"i_understand_destroys_data:.*?default:\s*(\S+)",
            self.text,
            re.DOTALL,
        )
        self.assertIsNotNone(block, "i_understand_destroys_data input missing")
        self.assertEqual(block.group(1), "false", "destructive flag must default to false")

    def test_release_eip_default_false(self):
        block = re.search(r"release_eip:.*?default:\s*(\S+)", self.text, re.DOTALL)
        self.assertIsNotNone(block, "release_eip input missing")
        self.assertEqual(block.group(1), "false")

    def test_validate_step_gates_both_understand_and_deployable_false(self):
        # Both gates must appear inside the `decommission)` case branch of the
        # Validate step. We pin on the literal error strings rather than line
        # positions so cosmetic refactors don't break this test.
        decommission_case = re.search(
            r"decommission\)(.*?)(?:\*\)|esac)", self.text, re.DOTALL
        )
        self.assertIsNotNone(decommission_case, "validate step has no decommission case")
        body = decommission_case.group(1)
        self.assertIn(
            'requires i_understand_destroys_data=true',
            body,
            "decommission case must gate on i_understand_destroys_data",
        )
        self.assertIn(
            'requires the edge to be deployable=false',
            body,
            "decommission case must gate on matrix deployable=false",
        )

    def test_snapshot_step_precedes_delete_step(self):
        snapshot_idx = self.text.find("Snapshot EBS root volume before decommission")
        delete_idx = self.text.find("Delete CloudFormation stack")
        self.assertGreater(snapshot_idx, 0, "snapshot step missing")
        self.assertGreater(delete_idx, 0, "delete step missing")
        self.assertLess(snapshot_idx, delete_idx, "snapshot must run BEFORE delete-stack")

    def test_release_eip_step_gated_on_input(self):
        # `if: inputs.operation == 'decommission' && inputs.release_eip == true`
        # is the only path that triggers the release.
        self.assertRegex(
            self.text,
            r"if:\s*inputs\.operation\s*==\s*'decommission'\s*&&\s*inputs\.release_eip\s*==\s*true",
            "release-eip step must be gated on inputs.release_eip == true",
        )

    def test_resolver_passes_allow_planned_only_for_decommission(self):
        # Pin the conditional logic — provision/upgrade/etc must still get the
        # default resolver behaviour (rejects deployable=false).
        self.assertIn(
            'if [ "$INPUT_OPERATION" = "decommission" ]; then',
            self.text,
        )
        self.assertIn('ALLOW_PLANNED="--allow-planned"', self.text)

    def test_snapshot_tagged_for_30_day_retention(self):
        # Audit / rollback contract: snapshots from decommission must carry
        # RetentionDays=30 so out-of-band tooling (or a future janitor job)
        # can age them out predictably.
        self.assertIn("RetentionDays", self.text)
        self.assertRegex(self.text, r"Key=RetentionDays,Value=30")

    def test_snapshot_resolves_root_device_dynamically(self):
        # R-001: root device must NOT be hard-coded /dev/xvda in the
        # describe-instances query. The decommission path resolves
        # RootDeviceName from the instance first. Hard-coded device names
        # silently break when the instance family / AMI shifts (e.g. AL2023
        # on t4g → xvda today, but Nitro nvme0n1 tomorrow).
        self.assertIn("RootDeviceName", self.text,
                      "snapshot step must resolve RootDeviceName dynamically (R-001)")
        # Defensive: the BlockDeviceMappings query must reference the dynamic
        # ${ROOT_DEV} variable, not a hard-coded literal. We exclude lines that
        # start with '#' so the regression-comment that explains "/dev/xvda
        # today, nvme tomorrow" doesn't trip the gate.
        snapshot_step = self._extract_step("Snapshot EBS root volume before decommission")
        code_lines = [
            line for line in snapshot_step.splitlines()
            if line.strip() and not line.strip().startswith("#")
        ]
        code_only = "\n".join(code_lines)
        self.assertNotIn("/dev/xvda", code_only,
                         "snapshot step must not hard-code /dev/xvda in any executable line (R-001)")
        # And explicitly assert the query uses the variable substitution form.
        self.assertIn("DeviceName=='${ROOT_DEV}'", code_only,
                      "BlockDeviceMappings query must reference ${ROOT_DEV}, not a literal device path (R-001)")

    def test_snapshot_failure_aborts_decommission(self):
        # R-001 part 2: if we cannot identify the root EBS volume, we MUST fail
        # the step (not log a warning and proceed). Snapshot is part of the
        # decommission contract; a silent skip would destroy the operator's
        # rollback window without their knowledge.
        snapshot_step = self._extract_step("Snapshot EBS root volume before decommission")
        self.assertIn("refusing to decommission without an EBS snapshot", snapshot_step,
                      "snapshot step must fail-fast (not warn) when root EBS cannot be resolved (R-001)")

    def _extract_step(self, name: str) -> str:
        """Pull the run-block of a single named step by literal name."""
        # Find the step header then the next "- name:" or end-of-file.
        marker = f"- name: {name}"
        start = self.text.find(marker)
        self.assertGreater(start, 0, f"step '{name}' missing")
        # End boundary: next step at the same indent level.
        next_step = self.text.find("\n      - name:", start + len(marker))
        end = next_step if next_step > 0 else len(self.text)
        return self.text[start:end]

    def test_job_summary_includes_decommission_audit_block(self):
        self.assertIn("### Decommission audit", self.text)
        self.assertIn("pre-decommission EBS snapshot", self.text)
        self.assertIn("preserved EIP allocation", self.text)


if __name__ == "__main__":
    unittest.main()
