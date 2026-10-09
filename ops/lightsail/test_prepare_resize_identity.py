import copy
import importlib.util
import json
import pathlib
import unittest

from prepare_resize_identity import AwsError, ROOT, prepare

spec = importlib.util.spec_from_file_location("edge_target", ROOT / "deploy/aws/lightsail/resolve-edge-lightsail-target.py")
resolver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(resolver)


class PrepareResizeIdentityTest(unittest.TestCase):
    def setUp(self):
        self.target = resolver.resolve_target(resolver.load_matrix(str(resolver.DEFAULT_MATRIX)), "us6")
        self.calls = []
        self.replacement = "us6-resize-test"
        self.payload = None
        self.lookup_error = "NotFoundException"
        self.store_failure = False

    def call(self, region, *args):
        self.calls.append((region, *args))
        if args[:2] == ("lightsail", "get-instance"):
            if args[-1] == self.replacement:
                if self.lookup_error:
                    raise AwsError(self.lookup_error)
                return {"instance": {"name": self.replacement}}
            return {"instance": {"name": self.target["instance_name"], "location": {"regionName": region}}}
        if args[:2] == ("ssm", "create-activation"):
            return {"ActivationId": "activation-test", "ActivationCode": "single-use-secret"}
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
        create = self.calls[2]
        self.assertEqual(create[create.index("--iam-role") + 1], self.target["ssm_hybrid_role_name"])
        self.assertEqual(create[create.index("--registration-limit") + 1], "1")
        store = self.calls[3]
        self.assertEqual(store[store.index("--type") + 1], "SecureString")
        self.assertNotIn("--overwrite", store)
        self.assertEqual([c[1:3] for c in self.calls], [("lightsail", "get-instance"), ("lightsail", "get-instance"), ("ssm", "create-activation"), ("ssm", "put-parameter")])

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
