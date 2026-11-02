from __future__ import annotations

import contextlib
import importlib.util
import json
import os
import pathlib
import stat
import tempfile
import unittest
from unittest import mock


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


class Repo2EnablementPreflightTest(unittest.TestCase):
    """The repo2 enablement order is what keeps WAL archiving alive.

    archive_command is `pgbackrest --stanza=tokenkey archive-push %p` with no
    --repo, so pgBackRest fans WAL out to every configured repository. If repo2
    reaches the live config before its stanza exists, every push fails until
    archive-timeout expires. That stalled cn-prod archiving on 2026-11-02 and
    failed the 14:30 backup while repo1 was perfectly healthy.
    """

    AWS = {"aws_access_key_id": "AKIA", "aws_secret_access_key": "AWSSECRET",
           "aws_session_token": "AWSTOKEN"}
    COS = {"key": "AKIDxxxx", "secret": "cossecret"}

    def _present(self, config: pathlib.Path) -> pathlib.Path:
        """A real existing path standing in for the COS credential file."""
        marker = config.parent / "cos-backup.env"
        marker.write_text("tencent_SecretId=x\ntencent_SecretKey=y\n")
        return marker

    def _arena(self, stack, *, live_body: str | None = None):
        root = stack.enter_context(tempfile.TemporaryDirectory())
        config = pathlib.Path(root) / "config" / "pgbackrest.conf"
        config.parent.mkdir(parents=True)
        if live_body is not None:
            config.write_text(live_body)
        stack.enter_context(mock.patch.object(identity, "CONFIG", config))
        stack.enter_context(mock.patch.object(
            identity, "STAGING", config.with_name(".pgbackrest-staging.conf")))
        return config

    def test_staging_is_verified_in_order_before_live_config_is_written(self) -> None:
        calls: list[tuple[str, ...]] = []
        with contextlib.ExitStack() as stack:
            config = self._arena(stack)

            def fake_staged(container, *args, timeout):
                calls.append(args)
                # The live config must NOT yet carry repo2 while we verify.
                self.assertNotIn("repo2-type=", config.read_text() if config.exists() else "")
                self.assertTrue(identity.STAGING.exists(), "staged config must exist during checks")
                if args[-1] == "info":
                    return json.dumps([{"name": "tokenkey", "status": {"code": 0}}])
                return ""

            stack.enter_context(mock.patch.object(identity, "_container", return_value="tokenkey-postgres"))
            stack.enter_context(mock.patch.object(identity, "_staged", side_effect=fake_staged))
            stack.enter_context(mock.patch.object(identity.os, "chown"))
            stack.enter_context(mock.patch.object(identity.os, "fchown"))
            identity.prepare_repo2(identity.render_config(self.AWS, self.COS))

        self.assertEqual(calls[0], ("stanza-create",))
        self.assertEqual(calls[1], ("check",))
        self.assertEqual(calls[2], ("--repo=1", "--output=json", "info"))
        self.assertFalse(identity.STAGING.exists(), "staged config must be cleaned up")

    def test_failure_leaves_live_config_untouched_and_cleans_staging(self) -> None:
        aws_only = identity.render_config(self.AWS)
        with contextlib.ExitStack() as stack:
            config = self._arena(stack, live_body=aws_only)
            stack.enter_context(mock.patch.object(identity, "_container", return_value="tokenkey-postgres"))
            stack.enter_context(mock.patch.object(
                identity, "_staged", side_effect=ValueError("staged pgbackrest check failed: [055]")))
            stack.enter_context(mock.patch.object(identity.os, "chown"))
            stack.enter_context(mock.patch.object(identity.os, "fchown"))
            with self.assertRaises(ValueError):
                identity.prepare_repo2(identity.render_config(self.AWS, self.COS))
            self.assertEqual(config.read_text(), aws_only)
            self.assertNotIn("repo2-type=", config.read_text())
            self.assertFalse(identity.STAGING.exists())

    def test_degraded_primary_chain_blocks_enablement(self) -> None:
        with contextlib.ExitStack() as stack:
            self._arena(stack)
            stack.enter_context(mock.patch.object(identity, "_container", return_value="tokenkey-postgres"))
            stack.enter_context(mock.patch.object(identity, "_staged", side_effect=lambda c, *a, timeout: (
                json.dumps([{"name": "tokenkey", "status": {"code": 2}}]) if a[-1] == "info" else "")))
            stack.enter_context(mock.patch.object(identity.os, "chown"))
            stack.enter_context(mock.patch.object(identity.os, "fchown"))
            with self.assertRaises(ValueError):
                identity.prepare_repo2(identity.render_config(self.AWS, self.COS))

    def test_lock_collision_defers_instead_of_failing(self) -> None:
        # [050] is the 2-hourly backup holding the lock, not a repo2 fault.
        # Deferring lets the next 15-minute refresh retry; failing would turn a
        # timing collision into a stuck enablement needing a human.
        aws_only = identity.render_config(self.AWS)
        with contextlib.ExitStack() as stack:
            config = self._arena(stack, live_body=aws_only)
            stack.enter_context(mock.patch.object(identity, "load_target", return_value={
                "instance_name": "tokenkey-cn-prod", "ssm_instance_id": "mi-x", "ssm_region": "eu-west-2"}))
            stack.enter_context(mock.patch.object(identity.os, "geteuid", return_value=0))
            stack.enter_context(mock.patch.object(identity.socket, "gethostname", return_value="tokenkey-cn-prod"))
            stack.enter_context(mock.patch.object(identity.subprocess, "check_output", return_value="tmpfs\n"))
            stack.enter_context(mock.patch.object(identity, "read_aws_credentials", return_value=self.AWS))
            stack.enter_context(mock.patch.object(identity, "validate_identity"))
            stack.enter_context(mock.patch.object(identity, "caller_identity", return_value={}))
            stack.enter_context(mock.patch.object(identity, "read_cos_credentials", return_value=self.COS))
            stack.enter_context(mock.patch.object(identity, "COS_CREDENTIALS", self._present(config)))
            stack.enter_context(mock.patch.object(
                identity, "prepare_repo2", side_effect=identity.RepositoryBusy("stanza-create")))
            stack.enter_context(mock.patch.object(identity.os, "chown"))
            stack.enter_context(mock.patch.object(identity.os, "fchown"))
            result = identity.refresh()
            live_after = config.read_text()

        self.assertEqual(result["status"], "deferred")
        self.assertEqual(result["cos_repo2"], "pending")
        self.assertEqual(live_after, aws_only, "live config must stay AWS-only")

    def test_steady_state_does_not_rerun_the_preflight(self) -> None:
        live = identity.render_config(self.AWS, self.COS)
        with contextlib.ExitStack() as stack:
            config = self._arena(stack, live_body=live)
            self.assertTrue(identity.repo2_live())
            prepare = stack.enter_context(mock.patch.object(identity, "prepare_repo2"))
            stack.enter_context(mock.patch.object(identity, "load_target", return_value={
                "instance_name": "tokenkey-cn-prod", "ssm_instance_id": "mi-x", "ssm_region": "eu-west-2"}))
            stack.enter_context(mock.patch.object(identity.os, "geteuid", return_value=0))
            stack.enter_context(mock.patch.object(identity.socket, "gethostname", return_value="tokenkey-cn-prod"))
            stack.enter_context(mock.patch.object(identity.subprocess, "check_output", return_value="tmpfs\n"))
            stack.enter_context(mock.patch.object(identity, "read_aws_credentials", return_value=self.AWS))
            stack.enter_context(mock.patch.object(identity, "validate_identity"))
            stack.enter_context(mock.patch.object(identity, "caller_identity", return_value={}))
            stack.enter_context(mock.patch.object(identity, "read_cos_credentials", return_value=self.COS))
            stack.enter_context(mock.patch.object(identity, "COS_CREDENTIALS", self._present(config)))
            stack.enter_context(mock.patch.object(identity.os, "chown"))
            stack.enter_context(mock.patch.object(identity.os, "fchown"))
            result = identity.refresh()

        prepare.assert_not_called()
        self.assertEqual(result["cos_repo2"], "enabled")

    def test_identity_is_reread_after_the_preflight(self) -> None:
        # The SSM agent rotates /root/.aws/credentials on its own ~25-minute
        # schedule and the preflight can burn ~11 minutes, so the config
        # rendered beforehand may carry a superseded session token. Installing
        # it would put repo1 on a stale token until the next refresh.
        rotated = dict(self.AWS, aws_session_token="ROTATEDTOKEN")
        with contextlib.ExitStack() as stack:
            config = self._arena(stack)
            reads = stack.enter_context(mock.patch.object(
                identity, "read_aws_credentials", side_effect=[self.AWS, rotated]))
            stack.enter_context(mock.patch.object(identity, "prepare_repo2"))
            stack.enter_context(mock.patch.object(identity, "load_target", return_value={
                "instance_name": "tokenkey-cn-prod", "ssm_instance_id": "mi-x", "ssm_region": "eu-west-2"}))
            stack.enter_context(mock.patch.object(identity.os, "geteuid", return_value=0))
            stack.enter_context(mock.patch.object(identity.socket, "gethostname", return_value="tokenkey-cn-prod"))
            stack.enter_context(mock.patch.object(identity.subprocess, "check_output", return_value="tmpfs\n"))
            validate = stack.enter_context(mock.patch.object(identity, "validate_identity"))
            stack.enter_context(mock.patch.object(identity, "caller_identity", return_value={}))
            stack.enter_context(mock.patch.object(identity, "read_cos_credentials", return_value=self.COS))
            stack.enter_context(mock.patch.object(identity, "COS_CREDENTIALS", self._present(config)))
            stack.enter_context(mock.patch.object(identity.os, "chown"))
            stack.enter_context(mock.patch.object(identity.os, "fchown"))
            identity.refresh()
            live = config.read_text()

        self.assertEqual(reads.call_count, 2, "identity must be re-read after the preflight")
        self.assertEqual(validate.call_count, 2, "the rotated identity must be re-validated")
        self.assertIn("repo1-s3-token=ROTATEDTOKEN", live)
        self.assertNotIn("repo1-s3-token=AWSTOKEN", live)

    def test_no_cos_credentials_keeps_the_previous_behaviour(self) -> None:
        with contextlib.ExitStack() as stack:
            config = self._arena(stack)
            prepare = stack.enter_context(mock.patch.object(identity, "prepare_repo2"))
            stack.enter_context(mock.patch.object(identity, "load_target", return_value={
                "instance_name": "tokenkey-cn-prod", "ssm_instance_id": "mi-x", "ssm_region": "eu-west-2"}))
            stack.enter_context(mock.patch.object(identity.os, "geteuid", return_value=0))
            stack.enter_context(mock.patch.object(identity.socket, "gethostname", return_value="tokenkey-cn-prod"))
            stack.enter_context(mock.patch.object(identity.subprocess, "check_output", return_value="tmpfs\n"))
            stack.enter_context(mock.patch.object(identity, "read_aws_credentials", return_value=self.AWS))
            stack.enter_context(mock.patch.object(identity, "validate_identity"))
            stack.enter_context(mock.patch.object(identity, "caller_identity", return_value={}))
            stack.enter_context(mock.patch.object(
                identity, "COS_CREDENTIALS", config.parent / "absent-cos.env"))
            stack.enter_context(mock.patch.object(identity.os, "chown"))
            stack.enter_context(mock.patch.object(identity.os, "fchown"))
            result = identity.refresh()
            live_after = config.read_text()

        prepare.assert_not_called()
        self.assertEqual(result["cos_repo2"], "disabled")
        self.assertNotIn("repo2-type=", live_after)


if __name__ == "__main__":
    unittest.main()
