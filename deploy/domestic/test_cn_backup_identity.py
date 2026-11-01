from __future__ import annotations

import importlib.util
import os
import pathlib
import stat
import tempfile
import unittest


HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("cn_backup_identity", HERE / "cn_backup_identity.py")
identity = importlib.util.module_from_spec(spec)
assert spec and spec.loader
spec.loader.exec_module(identity)


class CnBackupIdentityTest(unittest.TestCase):
    def test_render_is_aws_only_without_cos_credentials(self) -> None:
        aws = {"aws_access_key_id": "AKIA", "aws_secret_access_key": "AWSSECRET",
               "aws_session_token": "SESSION"}
        config = identity.render_config(aws)
        self.assertIn("repo1-s3-bucket=tokenkey-cn-prod-physical-backup-", config)
        self.assertNotIn("repo2-type=", config)
        self.assertNotIn("tencent_Secret", config)

    def test_render_adds_beijing_cos_as_repo2(self) -> None:
        aws = {"aws_access_key_id": "AKIA", "aws_secret_access_key": "AWSSECRET",
               "aws_session_token": "SESSION"}
        cos = {"key": "sid", "secret": "skey"}
        config = identity.render_config(aws, cos)
        self.assertIn("repo1-path=/cn-prod/postgres", config)
        self.assertIn("repo2-type=s3", config)
        self.assertIn("repo2-s3-bucket=tokenkey-cnprod-backup-bj-1320330536", config)
        self.assertIn("repo2-s3-region=ap-beijing", config)
        self.assertIn("repo2-s3-endpoint=cos.ap-beijing.myqcloud.com", config)
        self.assertIn("repo2-path=/cn-prod/postgres", config)

    def test_cos_credentials_require_root_only_exact_fields(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / "cos.env"
            path.write_text("tencent_SecretId=sid\ntencent_SecretKey=skey\n")
            path.chmod(0o600)
            if os.geteuid() == 0:
                os.chown(path, 0, 0)
            self.assertEqual(identity.read_cos_credentials(path, os.getuid()), {"key": "sid", "secret": "skey"})
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                identity.read_cos_credentials(path, os.getuid())

    def test_cos_credentials_reject_unknown_fields(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / "cos.env"
            path.write_text("tencent_SecretId=sid\ntencent_SecretKey=skey\nEXTRA=x\n")
            path.chmod(0o600)
            if os.geteuid() == 0:
                os.chown(path, 0, 0)
            with self.assertRaises(ValueError):
                identity.read_cos_credentials(path, os.getuid())

    def test_target_manifest_is_required_and_binds_ssm_identity(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / "prod-target.json"
            path.write_text(
                '{"instance_name":"tokenkey-cn-prod",'
                '"ssm_instance_id":"mi-06568963b4b3c3239",'
                '"ssm_region":"eu-west-2"}'
            )
            target = identity.load_target((path,))
            self.assertEqual(target["ssm_region"], "eu-west-2")
            identity.validate_identity(
                {"Account": identity.EXPECTED_ACCOUNT,
                 "UserId": identity.ROLE_ID + ":mi-06568963b4b3c3239"},
                target,
            )
            with self.assertRaises(ValueError):
                identity.validate_identity(
                    {"Account": identity.EXPECTED_ACCOUNT,
                     "UserId": identity.ROLE_ID + ":mi-wrong"},
                    target,
                )


if __name__ == "__main__":
    unittest.main()
