#!/usr/bin/env python3
import unittest
from provision_hcloud_pgdump_bucket import lifecycle_valid, private_acl


class BucketBoundaryTests(unittest.TestCase):
    def test_retention_must_be_enabled_for_every_backup_object(self):
        rule = {"Status": "Enabled", "Filter": {"Prefix": ""}, "Expiration": {"Days": 7},
                "AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 1}}
        self.assertTrue(lifecycle_valid([rule]))
        legacy = {key: value for key, value in rule.items() if key != "Filter"}
        self.assertTrue(lifecycle_valid([{**legacy, "Prefix": ""}]))
        self.assertFalse(lifecycle_valid([{**legacy, "Prefix": "synthetic/"}]))
        self.assertFalse(lifecycle_valid([legacy]))
        for change in [{"Status": "Disabled"}, {"Filter": {"Prefix": "synthetic/"}},
                       {"Expiration": {"Days": 30}}, {"AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 7}}]:
            with self.subTest(change=change):
                self.assertFalse(lifecycle_valid([{**rule, **change}]))
        self.assertFalse(lifecycle_valid([]))
        self.assertFalse(lifecycle_valid([rule, rule]))

    def test_only_owner_full_control_is_private(self):
        grant = {"Permission": "FULL_CONTROL", "Grantee": {"Type": "CanonicalUser", "ID": "synthetic-owner"}}
        self.assertTrue(private_acl({"Owner": {"ID": "synthetic-owner"}, "Grants": [grant]}))
        self.assertFalse(private_acl({"Owner": {"ID": "another-owner"}, "Grants": [grant]}))
        self.assertFalse(private_acl({"Owner": {"ID": "synthetic-owner"}, "Grants": [grant, {"Permission": "READ", "Grantee": {"Type": "Group", "URI": "AllUsers"}}]}))
        self.assertFalse(private_acl({"Owner": {"ID": "synthetic-owner"}, "Grants": [{"Permission": "READ", "Grantee": grant["Grantee"]}]}))


if __name__ == "__main__":
    unittest.main()
