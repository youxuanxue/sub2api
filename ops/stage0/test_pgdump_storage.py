#!/usr/bin/env python3
"""Credential isolation and encrypted upload/download behavior."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from pgdump_storage import Storage, StorageError


class StorageTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.credentials = self.root / "credentials"
        self.credentials.write_text("[pgdump]\naws_access_key_id=synthetic-access\naws_secret_access_key=synthetic-secret\n")
        self.credentials.chmod(0o600)
        self.key = self.root / "key"
        self.key.write_bytes(b"x" * 32)
        self.key.chmod(0o600)
        self.config = self.root / "config.json"
        self.values = {"schema_version": 1, "target": "edge:uk1",
                       "s3_uri": "s3://tokenkey-pgdump-test/edge/uk1/pgdump",
                       "endpoint": "https://fsn1.your-objectstorage.com", "region": "fsn1",
                       "credentials_file": str(self.credentials), "sse_c_key_file": str(self.key)}
        self.write_config()
        self.env = self.root / ".env"
        self.env.write_text("TOKENKEY_PGDUMP_S3_URI=s3://old-aws/prod/pgdump\n")

    def write_config(self):
        self.config.write_text(json.dumps(self.values))
        self.config.chmod(0o600)

    def load(self):
        return Storage(target="edge:uk1", config_path=self.config, env_path=self.env)

    def test_encrypted_upload_and_read_use_private_identity_despite_ambient_aws(self):
        with patch.dict(os.environ, {"AWS_ACCESS_KEY_ID": "wrong", "AWS_PROFILE": "wrong",
                                     "AWS_ENDPOINT_URL": "https://wrong", "AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://wrong"}):
            storage = self.load()
            command = storage.command(["s3", "cp", "source", "dest"], encrypted=True)
        self.assertEqual(storage.bucket, "tokenkey-pgdump-test")
        self.assertEqual(storage.prefix, "edge/uk1/pgdump")
        self.assertIn("AWS_PROFILE=pgdump", command)
        for name in ["AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_ENDPOINT_URL", "AWS_CONTAINER_CREDENTIALS_FULL_URI"]:
            self.assertIn(name, command[:command.index("aws")])
        self.assertEqual(command[-4:], ["--sse-c", "AES256", "--sse-c-key", f"fileb://{self.key}"])
        self.assertNotIn("synthetic-secret", " ".join(command))
        dump = self.root / "tokenkey-20261013T120000Z.sql.gz"
        dump.write_bytes(b"synthetic")
        with patch("pgdump_storage.subprocess.run", return_value=subprocess.CompletedProcess([], 0)) as run:
            uri = storage.upload(dump)
        self.assertEqual(uri, f"s3://tokenkey-pgdump-test/edge/uk1/pgdump/{dump.name}")
        self.assertIn(uri, run.call_args.args[0])
        self.assertIn("--sse-c-key", run.call_args.args[0])

    def test_invalid_explicit_config_never_falls_back_to_valid_aws_env(self):
        for change in [{"endpoint": "http://fsn1.your-objectstorage.com"},
                       {"endpoint": "https://attacker.invalid"}, {"target": "prod"},
                       {"s3_uri": "s3://tokenkey-pgdump-test/prod/pgdump"},
                       {"credentials_file": "relative"}, {"schema_version": 2}, {"schema_version": True}]:
            with self.subTest(change=change):
                original = self.values.copy()
                self.values.update(change)
                self.write_config()
                with self.assertRaises(StorageError):
                    self.load()
                self.values = original
        self.config.write_text("invalid")
        with self.assertRaises(StorageError):
            self.load()

    def test_secret_permissions_missing_key_and_symlink_fail_closed(self):
        self.credentials.chmod(0o644)
        with self.assertRaises(StorageError):
            self.load()
        self.credentials.chmod(0o600)
        self.key.write_bytes(b"too short")
        with self.assertRaises(StorageError):
            self.load()
        self.key.unlink()
        self.key.symlink_to(self.root / "absent")
        with self.assertRaises(StorageError):
            self.load()

    def test_upload_failure_retains_local_source_and_redacts_provider_error(self):
        dump = self.root / "tokenkey-20261013T120000Z.sql.gz"
        dump.write_bytes(b"synthetic")
        with patch("pgdump_storage.subprocess.run", return_value=subprocess.CompletedProcess([], 1, stderr="synthetic-secret")):
            with self.assertRaisesRegex(StorageError, "local dump retained") as error:
                self.load().upload(dump)
        self.assertEqual(dump.read_bytes(), b"synthetic")
        self.assertNotIn("synthetic-secret", str(error.exception))

    def test_legacy_aws_restore_uses_original_command_without_hetzner_headers(self):
        self.config.unlink()
        storage = Storage(target="prod", config_path=self.config, env_path=self.env)
        self.assertEqual(storage.bucket, "old-aws")
        self.assertEqual(storage.command(["s3", "cp", "source", "dest"], encrypted=True),
                         ["aws", "s3", "cp", "source", "dest"])


if __name__ == "__main__":
    unittest.main()
