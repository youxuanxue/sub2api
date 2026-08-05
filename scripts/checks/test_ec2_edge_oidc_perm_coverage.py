"""Contract tests for the EC2 Edge OIDC addon permission gate."""
from __future__ import annotations

import copy
import importlib.util
import pathlib
import subprocess
import sys
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts/checks/ec2-edge-oidc-perm-coverage.py"


def _load_module():
    spec = importlib.util.spec_from_file_location("ec2_edge_oidc_perm_coverage", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec and spec.loader
    spec.loader.exec_module(module)
    return module


def _remove_value(value: object, needle: str) -> object:
    """Return a deep copy with one IAM action/scope token removed everywhere."""
    if isinstance(value, dict):
        return {key: _remove_value(item, needle) for key, item in value.items()}
    if isinstance(value, list):
        return [_remove_value(item, needle) for item in value if item != needle]
    if isinstance(value, str) and needle in value:
        return value.replace(needle, "removed-by-test")
    return value


class Ec2EdgeOidcPermCoverageTest(unittest.TestCase):
    def setUp(self) -> None:
        self.assertTrue(SCRIPT.is_file(), "EC2 Edge OIDC permission checker is not implemented")
        self.mod = _load_module()
        self.addon = self.mod.load_cfn(self.mod.ADDON_CFN)
        self.base = self.mod.load_cfn(self.mod.BASE_CFN)

    def _assert_missing(self, token: str) -> None:
        mutated = _remove_value(copy.deepcopy(self.addon), token)
        failures = self.mod.validate_contract(mutated, self.base)
        self.assertTrue(
            any(token in failure for failure in failures),
            f"removing {token} must fail its permission contract; got {failures}",
        )

    def test_missing_ec2_instance_permission_fails(self) -> None:
        self._assert_missing("ec2:RunInstances")

    def test_missing_cloudformation_permission_fails(self) -> None:
        self._assert_missing("cloudformation:CreateChangeSet")

    def test_missing_eip_permission_fails(self) -> None:
        self._assert_missing("ec2:AllocateAddress")

    def test_missing_ssm_permission_fails(self) -> None:
        self._assert_missing("ssm:SendCommand")

    def test_missing_pass_role_permission_fails(self) -> None:
        self._assert_missing("iam:PassRole")

    def test_missing_credit_specification_permission_fails(self) -> None:
        self._assert_missing("ec2:ModifyInstanceCreditSpecification")

    def test_missing_allowed_region_fails(self) -> None:
        self._assert_missing("us-west-2")

    def test_missing_edge_stack_scope_fails(self) -> None:
        self._assert_missing("tokenkey-edge-*-stage0")

    def test_caller_stack_resource_cannot_widen_to_star(self) -> None:
        mutated = copy.deepcopy(self.addon)
        policy = mutated["Resources"]["Ec2EdgeAddonPolicy"]["Properties"]["PolicyDocument"]
        statement = next(
            item for item in policy["Statement"]
            if item.get("Sid") == "ReadAndDeployApprovedEdgeStacks"
        )
        statement["Resource"] = "*"
        failures = self.mod.validate_contract(mutated, self.base)
        self.assertTrue(any("stack scope" in failure for failure in failures), failures)

    def test_caller_policy_itself_must_cover_both_regions(self) -> None:
        mutated = copy.deepcopy(self.addon)
        caller = mutated["Resources"]["Ec2EdgeAddonPolicy"]
        mutated["Resources"]["Ec2EdgeAddonPolicy"] = _remove_value(caller, "us-west-2")
        failures = self.mod.validate_contract(mutated, self.base)
        self.assertTrue(
            any("caller policy missing allowed region us-west-2" in failure for failure in failures),
            failures,
        )

    def test_base_role_cannot_absorb_ec2_provisioning(self) -> None:
        mutated = copy.deepcopy(self.base)
        policies = mutated["Resources"]["ClusteringRole"]["Properties"]["Policies"]
        policies[0]["PolicyDocument"]["Statement"].append({
            "Effect": "Allow",
            "Action": "ec2:RunInstances",
            "Resource": "*",
        })
        failures = self.mod.validate_contract(self.addon, mutated)
        self.assertTrue(any("base OIDC role" in failure for failure in failures), failures)

    def test_real_repo_contract_passes(self) -> None:
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "--quiet"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, f"stdout:{proc.stdout}\nstderr:{proc.stderr}")
        self.assertIn("ok:", proc.stdout)


if __name__ == "__main__":
    unittest.main()
