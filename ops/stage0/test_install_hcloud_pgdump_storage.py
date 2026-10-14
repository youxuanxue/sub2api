#!/usr/bin/env python3
"""Exercise prepare/activate/rollback against a private host filesystem fixture."""
import datetime as dt
import json
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

import install_hcloud_pgdump_storage as installer
from pgdump_storage import StorageError
import test_pgdump_storage as storage_fixture


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.fixture = storage_fixture.StorageTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.root = self.fixture.root
        self.bundle = self.root / "bundle"
        self.bundle.mkdir()
        self.fixture.values["credentials_file"] = str(self.fixture.credentials)
        self.fixture.write_config()
        (self.bundle / "pgdump-storage.json").write_bytes(self.fixture.config.read_bytes())
        (self.bundle / "pgdump-storage.json").chmod(0o600)
        (self.bundle / "pgdump_storage.py").write_bytes(Path(installer.__file__).with_name("pgdump_storage.py").read_bytes())
        (self.bundle / "tokenkey-pgdump.sh").write_text("#!/bin/bash\nexit 0\n")
        self.data = self.root / "data"
        (self.data / "pgdump").mkdir(parents=True)
        self.dump = self.data / "pgdump/tokenkey-20261013T120000Z.sql.gz"
        self.dump.write_bytes(b"synthetic existing dump")
        (self.data / ".env").write_text("TOKENKEY_PGDUMP_S3_URI=s3://old-aws/edge/uk1/pgdump\nUNRELATED=original\n")
        self.installed = self.root / "pgdump.sh"
        self.installed.write_text("old script")
        self.helper = self.root / "helper.py"
        self.config = self.root / "active.json"
        self.releases = self.root / "releases"
        self.calls = []

    def run_operation(self, operation):
        def command(args):
            self.calls.append(args)
            if args[0] == "date":
                return str(int(dt.datetime.now().timestamp()) + 3600)
            return "next-fire"

        with patch.multiple(installer, ROOT=self.data, CONFIG=self.config, RELEASES=self.releases,
                            INSTALLED=self.installed, HELPER=self.helper), \
                patch.object(installer, "identity"), patch.object(installer, "command", side_effect=command), \
                patch.object(installer, "os", types.SimpleNamespace(geteuid=lambda: 0, close=os.close, replace=os.replace)), \
                patch.object(sys, "argv", ["installer", "--target", "edge:uk1", "--bundle", str(self.bundle),
                                            "--release", "test-release", "--operation", operation]), \
                patch("pgdump_storage.Storage.upload", return_value=f"s3://tokenkey-pgdump-test/edge/uk1/pgdump/{self.dump.name}"):
            return installer.main()

    def receipt(self, **changes):
        artifact = json.loads((self.releases / "test-release/artifact.json").read_text())
        value = {"target": "edge:uk1", "source_s3_uri": artifact["s3_uri"],
                 "source_endpoint": self.fixture.values["endpoint"], "artifact_sha256": artifact["sha256"],
                 "cleanup_verified": True, "source_mutated": False, "s3_round_trip_verified": True,
                 "completed_at": dt.datetime.now(dt.timezone.utc).isoformat()}
        value.update(changes)
        path = self.data / "pgdump-canary/test-release/latest.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value))

    def test_plan_and_prepare_keep_active_backup_and_gateway_configuration(self):
        original = (self.data / ".env").read_bytes()
        self.run_operation("plan")
        self.assertFalse(self.releases.exists())
        self.run_operation("prepare")
        self.assertEqual((self.data / ".env").read_bytes(), original)
        self.assertEqual(self.installed.read_text(), "old script")
        self.assertFalse(self.config.exists())
        self.assertEqual(self.dump.read_bytes(), b"synthetic existing dump")
        self.assertFalse(any("systemctl" in args for args in self.calls))

    def test_invalid_restore_receipt_rejects_activation_without_live_mutation(self):
        self.run_operation("prepare")
        for change in [{"target": "prod"}, {"cleanup_verified": False}, {"artifact_sha256": "wrong"},
                       {"source_mutated": True}, {"s3_round_trip_verified": False},
                       {"source_endpoint": "https://wrong"}, {"completed_at": "2020-01-01T00:00:00Z"}]:
            with self.subTest(change=change):
                self.receipt(**change)
                with self.assertRaises(StorageError):
                    self.run_operation("activate")
                self.assertFalse(self.config.exists())
                self.assertEqual(self.installed.read_text(), "old script")

    def test_activate_and_rollback_restore_only_owned_fields(self):
        self.run_operation("prepare")
        self.receipt()
        self.run_operation("activate")
        self.assertEqual(json.loads(self.config.read_text())["target"], "edge:uk1")
        self.assertIn("s3://tokenkey-pgdump-test/edge/uk1/pgdump", (self.data / ".env").read_text())
        self.assertEqual(self.installed.read_bytes(), (self.bundle / "tokenkey-pgdump.sh").read_bytes())
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o600)
        (self.data / ".env").write_text((self.data / ".env").read_text().replace("UNRELATED=original", "UNRELATED=later"))
        self.run_operation("rollback")
        self.assertFalse(self.config.exists())
        self.assertFalse(self.helper.exists())
        self.assertEqual(self.installed.read_text(), "old script")
        self.assertIn("UNRELATED=later", (self.data / ".env").read_text())
        self.assertIn("s3://old-aws/edge/uk1/pgdump", (self.data / ".env").read_text())
        self.assertFalse(any("systemctl" in args[:1] and any(v in args for v in ["start", "stop", "restart"]) for args in self.calls))
        self.assertFalse((self.data / "pgdump-canary/latest.json").exists())

    def test_failed_activation_restores_previous_identity_and_receipt(self):
        self.run_operation("prepare")
        self.receipt()
        previous = self.data / "pgdump-canary/latest.json"
        previous.write_text('{"previous":"AWS"}\n')
        original = installer.atomic_copy

        def fail_commit(source, destination, mode):
            if destination == self.config:
                raise OSError("synthetic disk failure")
            return original(source, destination, mode)

        with patch.object(installer, "atomic_copy", side_effect=fail_commit):
            with self.assertRaises(OSError):
                self.run_operation("activate")
        self.assertEqual(self.installed.read_text(), "old script")
        self.assertFalse(self.config.exists())
        self.assertFalse(self.helper.exists())
        self.assertIn("s3://old-aws/edge/uk1/pgdump", (self.data / ".env").read_text())
        self.assertEqual(previous.read_text(), '{"previous":"AWS"}\n')


if __name__ == "__main__":
    unittest.main()
