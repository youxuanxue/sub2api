import copy
import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import unittest

import yaml

from prepare_resize_identity import AwsError, ROOT, prepare

spec = importlib.util.spec_from_file_location("edge_target", ROOT / "deploy/aws/lightsail/resolve-edge-lightsail-target.py")
resolver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(resolver)


class PrepareResizeIdentityTest(unittest.TestCase):
    def setUp(self):
        self.matrix = resolver.load_matrix(str(resolver.DEFAULT_MATRIX))
        # This contract exercises a standby source, independent of fleet cutovers.
        self.matrix["targets"]["us5"].update(deployable=False, retired=False, instance_name="us5-source-test")
        self.target = resolver.resolve_target(self.matrix, "us5", allow_planned=True)
        self.calls = []
        self.replacement = "us5-resize-test"
        self.payload = None
        self.lookup_error = "NotFoundException"
        self.store_failure = False
        self.existing = {"name": self.replacement}
        self.activations = []
        self.managed = []

    def call(self, region, *args):
        self.calls.append((region, *args))
        if args[:2] == ("lightsail", "get-instance"):
            if args[-1] == self.replacement:
                if self.lookup_error:
                    raise AwsError(self.lookup_error)
                return {"instance": self.existing}
            return {"instance": {"name": self.target["instance_name"], "location": {"regionName": region}}}
        if args[:2] == ("ssm", "create-activation"):
            return {"ActivationId": "activation-test", "ActivationCode": "single-use-secret"}
        if args[:2] == ("ssm", "describe-activations"):
            return {"ActivationList": self.activations}
        if args[:2] == ("ssm", "describe-instance-information"):
            return {"InstanceInformationList": self.managed}
        if args[:2] == ("ssm", "put-parameter"):
            path = pathlib.Path(args[-1].removeprefix("file://"))
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.payload = json.loads(path.read_text())
            if self.store_failure:
                raise AwsError("storage failed")
        return {}

    def test_single_use_scoped_identity_and_encrypted_handoff(self):
        result = prepare(self.target, self.replacement, "123-1", self.call)
        self.assertEqual(self.payload["source_instance"], self.target["instance_name"])
        self.assertEqual(self.payload["activation_code"], "single-use-secret")
        self.assertNotIn("single-use-secret", json.dumps(result))
        self.assertEqual(result["parameter"], self.target["ssm_prefix"] + "/resize-activation/123-1")
        create = self.calls[4]
        self.assertEqual(create[create.index("--iam-role") + 1], self.target["ssm_hybrid_role_name"])
        self.assertEqual(create[create.index("--registration-limit") + 1], "1")
        store = self.calls[5]
        self.assertEqual(store[store.index("--type") + 1], "SecureString")
        self.assertNotIn("--overwrite", store)
        self.assertEqual([c[1:3] for c in self.calls], [("lightsail", "get-instance"), ("lightsail", "get-instance"), ("ssm", "describe-activations"), ("ssm", "describe-instance-information"), ("ssm", "create-activation"), ("ssm", "put-parameter")])

    def existing_canary(self):
        self.lookup_error = None
        self.replacement = f"tokenkey-edge-{self.target['edge_id']}-aws-20261015"
        self.arn = f"arn:aws:lightsail:{self.target['lightsail_region']}:123456789012:Instance/fresh-id"
        self.existing = {
            "name": self.replacement, "arn": self.arn,
            "location": {"regionName": self.target["lightsail_region"]},
            "state": {"name": "running"},
            "tags": [{"key": key, "value": value} for key, value in {
                "Project": "tokenkey", "MigrationTarget": self.target["edge_id"],
                "Purpose": "canary", "MigrationBatch": "aws-20261015",
            }.items()],
        }

    def test_existing_canary_requires_exact_identity_and_never_changes_host(self):
        self.existing_canary()
        result = prepare(self.target, self.replacement, "123-1", self.call, replacement_arn=self.arn)
        self.assertEqual(result["replacement_arn"], self.arn)
        self.assertEqual(self.payload["replacement_instance"], self.replacement)
        self.assertEqual([c[1:3] for c in self.calls], [
            ("lightsail", "get-instance"), ("lightsail", "get-instance"),
            ("ssm", "describe-activations"), ("ssm", "describe-instance-information"),
            ("ssm", "create-activation"), ("ssm", "put-parameter"),
        ])

    def test_existing_identity_mismatches_fail_before_mutation(self):
        self.existing_canary()
        original = copy.deepcopy(self.existing)
        mutations = [
            {"arn": self.arn + "-other"}, {"name": "old-host"},
            {"location": {"regionName": "wrong-region"}}, {"state": {"name": "stopped"}},
            {"tags": []},
        ]
        for tag in original["tags"]:
            mutations.append({"tags": [item for item in original["tags"] if item["key"] != tag["key"]]})
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                self.existing = original | mutation
                self.calls = []
                with self.assertRaises(ValueError):
                    prepare(self.target, self.replacement, "123-1", self.call, replacement_arn=self.arn)
                self.assertTrue(all(c[1] == "lightsail" and c[2] == "get-instance" for c in self.calls))

    def test_explicit_existing_identity_does_not_fall_back_to_absent_host(self):
        self.existing_canary()
        self.lookup_error = "NotFoundException"
        with self.assertRaises(AwsError):
            prepare(self.target, self.replacement, "123-1", self.call, replacement_arn=self.arn)
        self.assertEqual(len(self.calls), 2)

    def test_active_or_registered_identity_blocks_second_activation(self):
        for expired, count in [(False, 0), (True, 1)]:
            with self.subTest(expired=expired, count=count):
                self.activations = [{"DefaultInstanceName": self.replacement, "Expired": expired, "RegistrationsCount": count}]
                self.calls = []
                with self.assertRaises(ValueError):
                    prepare(self.target, self.replacement, "123-1", self.call)
                self.assertFalse(any(c[2] == "create-activation" for c in self.calls))
        self.activations = []
        self.managed = [{"ComputerName": self.replacement + ".local"}]
        self.calls = []
        with self.assertRaises(ValueError):
            prepare(self.target, self.replacement, "123-1", self.call)
        self.assertFalse(any(c[2] == "create-activation" for c in self.calls))

    def test_expired_unused_identity_allows_retry(self):
        self.activations = [{"DefaultInstanceName": self.replacement, "Expired": True, "RegistrationsCount": 0}]
        result = prepare(self.target, self.replacement, "123-1", self.call)
        self.assertEqual(result["activation_id"], "activation-test")

    def test_workflow_allows_standby_identity_preparation_but_not_live_operations(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/deploy-edge-lightsail-stage0.yml").read_text())
        step = next(row for row in workflow["jobs"]["edge"]["steps"] if row.get("id") == "edge")
        with tempfile.TemporaryDirectory() as directory:
            fixture = pathlib.Path(directory) / "repo"
            matrix_path = fixture / resolver.DEFAULT_MATRIX.relative_to(ROOT)
            matrix_path.parent.mkdir(parents=True)
            matrix_path.write_text(json.dumps(self.matrix))
            resolver_path = pathlib.Path(resolver.__file__)
            (fixture / resolver_path.relative_to(ROOT)).write_bytes(resolver_path.read_bytes())
            routing = pathlib.Path("ops/stage0/edge_routing_matrix.py")
            (fixture / routing).parent.mkdir(parents=True, exist_ok=True)
            (fixture / routing).write_bytes((ROOT / routing).read_bytes())
            env = os.environ | {
                "OIDC_ROLE_ARN": "test-role", "INPUT_EDGE_ID": self.target["edge_id"],
                "INPUT_CONFIRM_INSTANCE": self.target["instance_name"],
                "GITHUB_OUTPUT": str(pathlib.Path(directory) / "output"),
            }
            for operation in ["prepare-resize", "upgrade", "smoke", "rollback"]:
                with self.subTest(operation=operation):
                    result = subprocess.run(["bash", "-c", step["run"]], cwd=fixture,
                                            env=env | {"INPUT_OPERATION": operation}, capture_output=True, text=True)
                    if operation == "prepare-resize":
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertIn("deployable=false", result.stdout)
                        self.assertIn("instance_name=" + self.target["instance_name"], result.stdout)
                    else:
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("not deployable", result.stderr)

    def test_existing_replacement_or_uncertain_lookup_prevents_mutation(self):
        for error in [None, "AccessDenied", "SSL transport failure"]:
            with self.subTest(error=error):
                self.calls = []
                self.lookup_error = error
                with self.assertRaises((ValueError, AwsError)):
                    prepare(self.target, self.replacement, "123-1", self.call)
                self.assertEqual([c[1:3] for c in self.calls], [("lightsail", "get-instance"), ("lightsail", "get-instance")])

    def test_invalid_source_replacement_and_run_identity_fail_closed(self):
        for replacement, run_id in [(self.target["instance_name"], "123-1"), ("bad\nname", "123-1"), (self.replacement, "../../other")]:
            with self.subTest(replacement=replacement):
                with self.assertRaises(ValueError):
                    prepare(self.target, replacement, run_id, self.call)
                self.assertEqual(self.calls, [])
        target = copy.deepcopy(self.target)
        target["instance_name"] = "wrong-source"
        with self.assertRaises(ValueError):
            prepare(target, self.replacement, "123-1", self.call)
        self.assertEqual(len(self.calls), 1)

    def test_failed_handoff_revokes_only_new_activation(self):
        self.store_failure = True
        with self.assertRaisesRegex(AwsError, "storage failed"):
            prepare(self.target, self.replacement, "123-1", self.call)
        self.assertEqual(self.calls[-1][1:], ("ssm", "delete-activation", "--activation-id", "activation-test"))


if __name__ == "__main__":
    unittest.main()
