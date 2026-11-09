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
    COS = {"key": "sid", "secret": "skey"}

    def test_render_is_cos_only_with_no_repo1_or_aws_endpoint(self) -> None:
        # 2026-11-08: AWS repo1 retired. Nothing in the rendered config may
        # reach AWS, and COS keeps index repo2 so `--repo=2` stays COS.
        config = identity.render_config(self.COS)
        self.assertNotIn("repo1-", config)
        self.assertNotIn("amazonaws.com", config)
        self.assertNotIn("s3-token", config)
        self.assertNotIn("ap-northeast-1", config)
        self.assertIn("repo2-type=s3", config)
        self.assertIn("repo2-s3-bucket=tokenkey-cnprod-backup-bj-1320330536", config)
        self.assertIn("repo2-s3-region=ap-beijing", config)
        self.assertIn("repo2-s3-endpoint=cos.ap-beijing.myqcloud.com", config)
        self.assertIn("repo2-path=/cn-prod/postgres", config)
        self.assertIn("repo2-s3-uri-style=host", config)
        self.assertIn("repo2-retention-full=7", config)

    def test_render_parses_as_a_single_cos_repository(self) -> None:
        import configparser
        import re
        parser = configparser.ConfigParser(interpolation=None)
        parser.read_string(identity.render_config(self.COS))
        indices = {int(m.group(1)) for key in parser["global"]
                   if (m := re.match(r"repo(\d+)-", key))}
        self.assertEqual(indices, {2})
        self.assertEqual(parser["tokenkey"]["pg1-path"], "/var/lib/postgresql/data")

    def test_module_no_longer_touches_aws(self) -> None:
        body = (HERE / "cn_backup_identity.py").read_text(encoding="utf-8")
        for needle in ("/root/.aws", "sts.", "aws_session_token", "AWS_BUCKET"):
            self.assertNotIn(needle, body)

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

    def test_target_manifest_is_required(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / "prod-target.json"
            path.write_text(
                '{"instance_name":"tokenkey-cn-prod",'
                '"ssm_instance_id":"mi-06568963b4b3c3239",'
                '"ssm_region":"eu-west-2"}'
            )
            target = identity.load_target((path,))
            self.assertEqual(target["instance_name"], "tokenkey-cn-prod")
            with self.assertRaises(ValueError):
                identity.load_target((pathlib.Path(root) / "missing.json",))


class CosOnlyRefreshTest(unittest.TestCase):
    TARGET = {"instance_name": "tokenkey-cn-prod", "ssm_instance_id": "mi-x", "ssm_region": "eu-west-2"}

    def _arena(self, stack: contextlib.ExitStack) -> pathlib.Path:
        root = pathlib.Path(stack.enter_context(tempfile.TemporaryDirectory()))
        config = root / "run" / "config" / "pgbackrest.conf"
        stack.enter_context(mock.patch.object(identity, "CONFIG", config))
        stack.enter_context(mock.patch.object(identity, "load_target", return_value=self.TARGET))
        stack.enter_context(mock.patch.object(identity.os, "geteuid", return_value=0))
        stack.enter_context(mock.patch.object(identity.socket, "gethostname", return_value="tokenkey-cn-prod"))
        stack.enter_context(mock.patch.object(identity.subprocess, "check_output", return_value="tmpfs\n"))
        stack.enter_context(mock.patch.object(identity.os, "fchown"))
        stack.enter_context(mock.patch.object(identity.os, "chown"))
        return config

    def test_refresh_writes_cos_only_config(self) -> None:
        with contextlib.ExitStack() as stack:
            config = self._arena(stack)
            stack.enter_context(mock.patch.object(
                identity, "read_cos_credentials", return_value={"key": "sid", "secret": "skey"}))
            result = identity.refresh()
            live = config.read_text()
            mode = stat.S_IMODE(config.stat().st_mode)
        self.assertEqual(result["status"], "refreshed")
        self.assertEqual(result["aws_repo1"], "retired")
        self.assertNotIn("repo1-", live)
        self.assertIn("repo2-s3-key=sid", live)
        self.assertEqual(mode, 0o600)
        self.assertNotIn("sid", json.dumps(result))
        self.assertNotIn("skey", json.dumps(result))

    def test_missing_cos_credentials_fail_and_keep_the_current_config(self) -> None:
        # No AWS fallback: without COS credentials the refresh fails and the
        # last good config stays in place for archive_command.
        with contextlib.ExitStack() as stack:
            config = self._arena(stack)
            config.parent.mkdir(parents=True)
            config.write_text("previous")
            absent = config.parent / "absent.env"
            stack.enter_context(mock.patch.object(
                identity, "read_cos_credentials",
                side_effect=lambda: _ORIGINAL_READ_COS(absent)))
            with self.assertRaises(OSError):
                identity.refresh()
            self.assertEqual(config.read_text(), "previous")

    def test_failure_output_withholds_credentials(self) -> None:
        import io
        out = io.StringIO()
        with mock.patch.object(identity, "refresh", side_effect=RuntimeError("skey-secret")), \
                contextlib.redirect_stdout(out):
            self.assertEqual(identity.main(), 1)
        self.assertNotIn("skey-secret", out.getvalue())


_ORIGINAL_READ_COS = identity.read_cos_credentials


if __name__ == "__main__":
    unittest.main()
