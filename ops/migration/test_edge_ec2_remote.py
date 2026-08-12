#!/usr/bin/env python3
from __future__ import annotations

import copy
import hashlib
import io
import json
import os
import pathlib
import stat
import tarfile
import tempfile
import unittest
from unittest import mock

from ops.migration.edge_ec2_remote import (
    InventoryError,
    _database_report,
    _redis_report,
    build_inventory,
    build_remote_plan,
    compare_inventories,
    compare_redis_reports,
    validate_bundle_archive,
    validate_data_archive,
    write_receipt_atomic,
)


class EdgeEC2RemoteInventoryTest(unittest.TestCase):
    def _tree(self, root: pathlib.Path) -> None:
        (root / "app").mkdir()
        (root / "app/accounts.json").write_text('{"account":1}\n', encoding="utf-8")
        (root / "caddy/data").mkdir(parents=True)
        (root / "caddy/Caddyfile").write_text("example.test {}\n", encoding="utf-8")
        (root / "postgres/base").mkdir(parents=True)
        (root / "postgres/base/123").write_bytes(b"physical-pgdata")
        (root / "redis").mkdir()
        (root / "redis/dump.rdb").write_bytes(b"redis-state")
        (root / "pgdump").mkdir()
        (root / ".env").write_text("JWT_SECRET=not-for-output\n", encoding="utf-8")
        (root / ".env.secret").write_text("DATABASE_PASSWORD=never-log-this\n", encoding="utf-8")
        (root / "docker-compose.yml").write_text("services: {}\n", encoding="utf-8")
        os.symlink("accounts.json", root / "app/accounts-current.json")
        os.chmod(root / ".env.secret", 0o600)

    def test_inventory_is_stable_redacted_and_classifies_data_planes(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            self._tree(root)

            first = build_inventory(root)
            second = build_inventory(root)

            self.assertEqual(first, second)
            entries = first["entries"]
            self.assertEqual([item["path"] for item in entries], sorted(item["path"] for item in entries))
            by_path = {item["path"]: item for item in entries}
            self.assertEqual(by_path[".env.secret"]["mode"], "0600")
            self.assertEqual(by_path[".env.secret"]["classification"], "secret")
            self.assertEqual(by_path["app/accounts.json"]["type"], "file")
            self.assertEqual(by_path["app/accounts.json"]["size"], len(b'{"account":1}\n'))
            self.assertEqual(len(by_path["app/accounts.json"]["sha256"]), 64)
            self.assertEqual(by_path["app/accounts-current.json"]["type"], "symlink")
            self.assertEqual(by_path["app/accounts-current.json"]["link_target"], "accounts.json")
            self.assertEqual(by_path["postgres/base/123"]["classification"], "postgresql_physical")
            self.assertFalse(by_path["postgres/base/123"]["transferable"])
            self.assertEqual(by_path["redis/dump.rdb"]["classification"], "redis")
            rendered = json.dumps(first, sort_keys=True)
            self.assertNotIn("not-for-output", rendered)
            self.assertNotIn("never-log-this", rendered)

    def test_inventory_rejects_unknown_top_level_path(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            self._tree(root)
            (root / "unclassified").mkdir()
            (root / "unclassified/value").write_text("precious", encoding="utf-8")

            with self.assertRaisesRegex(InventoryError, "unclassified"):
                build_inventory(root)

    def test_inventory_ignores_private_migration_workspace(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            self._tree(root)
            (root / "migration/run-123").mkdir(parents=True)
            (root / "migration/run-123/bundle.tar.gz").write_bytes(b"temporary")

            inventory = build_inventory(root)

            self.assertFalse(any(item["path"].startswith("migration/") for item in inventory["entries"]))

    def test_inventory_ignores_empty_ext4_lost_found_but_rejects_recovered_content(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            self._tree(root)
            (root / "lost+found").mkdir()

            inventory = build_inventory(root)

            self.assertFalse(any(item["path"].startswith("lost+found") for item in inventory["entries"]))
            (root / "lost+found/recovered").write_bytes(b"must-not-be-silently-dropped")
            with self.assertRaisesRegex(InventoryError, r"lost\+found"):
                build_inventory(root)

    def test_inventory_classifies_stage0_backup_files(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            self._tree(root)
            (root / ".env.before-1.8.149").write_text("JWT_SECRET=old\n", encoding="utf-8")
            (root / "docker-compose.yml.compose-before-1.8.149").write_text("services: {}\n", encoding="utf-8")

            inventory = build_inventory(root)
            by_path = {item["path"]: item for item in inventory["entries"]}

            self.assertEqual(by_path[".env.before-1.8.149"]["classification"], "secret")
            self.assertEqual(by_path["docker-compose.yml.compose-before-1.8.149"]["classification"], "config_backup")

    @unittest.skipUnless(hasattr(os, "mkfifo"), "FIFO is unavailable on this platform")
    def test_inventory_rejects_special_files(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            self._tree(root)
            os.mkfifo(root / "app/queue")
            self.assertTrue(stat.S_ISFIFO((root / "app/queue").lstat().st_mode))

            with self.assertRaisesRegex(InventoryError, "unsupported file type"):
                build_inventory(root)

    def test_inventory_rejects_symlinks_that_escape_the_persistent_root(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            self._tree(root)
            os.symlink("../../etc/shadow", root / "app/outside-relative")

            with self.assertRaisesRegex(InventoryError, "symlink escapes persistent root"):
                build_inventory(root)

            (root / "app/outside-relative").unlink()
            os.symlink("/etc/shadow", root / "app/outside-absolute")
            with self.assertRaisesRegex(InventoryError, "symlink escapes persistent root"):
                build_inventory(root)

    def test_compare_reports_transferable_drift_but_ignores_pgdata_bytes(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            self._tree(root)
            expected = build_inventory(root)
            (root / "app/accounts.json").write_text('{"account":2}\n', encoding="utf-8")
            (root / "postgres/base/123").write_bytes(b"restored-layout-is-different")
            actual = build_inventory(root)

            differences = compare_inventories(expected, actual)

            self.assertEqual(differences, ["app/accounts.json: sha256 mismatch"])

    def test_receipt_replaces_atomically_with_private_permissions(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            path = pathlib.Path(raw) / "state/receipt.json"
            write_receipt_atomic(path, {"step": "old", "secret": "digest-only"})
            write_receipt_atomic(path, {"step": "new", "ok": True})

            self.assertEqual(json.loads(path.read_text(encoding="utf-8")), {"ok": True, "step": "new"})
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(list(path.parent.glob(f".{path.name}.*")), [])


class EdgeEC2RemotePlanTest(unittest.TestCase):
    def _plan(self, action: str, *, target_eip: str = "203.0.113.8") -> list[str]:
        return build_remote_plan(
            action,
            root=pathlib.Path("/var/lib/tokenkey"),
            work_dir=pathlib.Path("/var/lib/tokenkey/migration/run-123"),
            transfer_url="https://transfer.invalid/object?redacted=1",
            target_eip=target_eip,
            expected_bundle_sha256="a" * 64,
        )

    def test_freeze_source_uses_complete_logical_dump_and_redis_persistence(self) -> None:
        steps = self._plan("freeze-source")
        plan = "\n".join(steps)
        self.assertIn("systemctl disable tokenkey.service tokenkey-pgdump.timer", plan)
        self.assertIn("SIGUSR1", plan)
        self.assertIn("/health/inflight", plan)
        self.assertIn("pg_dump --format=custom", plan)
        self.assertIn("pg_dumpall --globals-only", plan)
        self.assertNotIn("--no-owner", plan)
        self.assertNotIn("--no-acl", plan)
        self.assertNotIn("--no-role-passwords", plan)
        self.assertNotIn("--exclude-table", plan)
        self.assertIn("redis-cli SAVE", plan)
        self.assertIn("files.tar", plan)
        self.assertIn("--exclude=postgres", plan)
        self.assertIn("--exclude=redis", plan)
        self.assertIn("--exclude=lost+found", plan)
        caddy_snapshot = next(
            i for i, item in enumerate(steps)
            if "Caddyfile.source.next" in item and "cp -a" in item
        )
        self.assertIn("rm -f", steps[caddy_snapshot])
        self.assertIn("Caddyfile.source.next", steps[caddy_snapshot])
        self.assertIn("sudo mv", steps[caddy_snapshot])
        stop_caddy = next(i for i, item in enumerate(steps) if " stop " in item and "caddy" in item)
        self.assertLess(caddy_snapshot, stop_caddy)
        database_report = next(i for i, item in enumerate(steps) if "database-report" in item)
        inventory = next(i for i, item in enumerate(steps) if " inventory " in f" {item} ")
        self.assertLess(stop_caddy, database_report)
        self.assertLess(stop_caddy, inventory)

    def test_prepare_source_uploads_a_rehearsal_bundle_without_stopping_services(self) -> None:
        plan = "\n".join(self._plan("prepare-source"))
        self.assertIn("pg_dump --format=custom", plan)
        self.assertIn("bundle.tar.gz", plan)
        self.assertIn("$TK_MIGRATION_TRANSFER_URL", plan)
        self.assertIn("bundle_sha256=", plan)
        self.assertIn("manifest_digest=", plan)
        self.assertNotIn("stop tokenkey", plan)
        self.assertNotIn("stop redis postgres", plan)

    def test_restore_target_keeps_app_stopped_until_restore_and_verification(self) -> None:
        plan = self._plan("restore-target")
        joined = "\n".join(plan)
        self.assertIn("touch /var/lib/tokenkey/migration/.write-owner-locked", joined)
        self.assertIn("globals.sql", joined)
        self.assertIn("DROP DATABASE IF EXISTS", joined)
        self.assertIn("restore-globals", joined)
        self.assertIn("pg_restore", joined)
        self.assertNotIn("--no-owner", joined)
        self.assertNotIn("--no-acl", joined)
        self.assertIn("compare-json", joined)
        self.assertIn("compare-inventory", joined)
        self.assertIn("stop caddy tokenkey redis postgres", joined)
        pg_reset_index = next(i for i, item in enumerate(plan) if "find" in item and "/postgres" in item)
        pg_start_index = next(i for i, item in enumerate(plan) if "up -d --no-deps postgres redis" in item)
        self.assertLess(pg_reset_index, pg_start_index)
        self.assertIn("DROP DATABASE IF EXISTS", joined)
        self.assertNotIn("up -d --no-deps tokenkey", joined)

    def test_restore_validates_archive_members_before_extracting(self) -> None:
        plan = self._plan("restore-target")
        joined = "\n".join(plan)

        validate_index = next(i for i, item in enumerate(plan) if "validate-bundle" in item)
        extract_index = next(i for i, item in enumerate(plan) if "-xzf" in item)

        self.assertLess(validate_index, extract_index)
        self.assertIn("validate-tar --archive", joined)

    def test_restore_replaces_roles_left_by_an_earlier_rehearsal(self) -> None:
        plan = self._plan("restore-target")
        drop_database = next(i for i, item in enumerate(plan) if "DROP DATABASE IF EXISTS" in item)
        drop_roles = next(i for i, item in enumerate(plan) if "DROP ROLE" in item)
        restore_globals = next(i for i, item in enumerate(plan) if "restore-globals-ok" in item)

        self.assertLess(drop_database, drop_roles)
        self.assertLess(drop_roles, restore_globals)
        self.assertIn("rolname !~ '^pg_'", plan[drop_roles])
        self.assertIn("rolname <> current_user", plan[drop_roles])

    def test_online_rehearsal_restores_bundle_without_cross_time_comparison(self) -> None:
        formal = "\n".join(self._plan("restore-target"))
        rehearsal = "\n".join(
            build_remote_plan(
                "restore-target",
                root=pathlib.Path("/var/lib/tokenkey"),
                work_dir=pathlib.Path("/var/lib/tokenkey/migration/run-123"),
                transfer_url="https://transfer.invalid/object?redacted=1",
                expected_bundle_sha256="a" * 64,
                rehearsal=True,
            )
        )

        self.assertIn("pg_restore", rehearsal)
        self.assertIn("sha256sum -c artifacts.sha256", rehearsal)
        self.assertIn("database-report.actual.json", rehearsal)
        self.assertIn("redis-report.actual.json", rehearsal)
        self.assertIn("inventory.actual.json", rehearsal)
        self.assertNotIn("compare-json", rehearsal)
        self.assertNotIn("compare-inventory", rehearsal)
        self.assertNotIn(".write-owner-locked", rehearsal)
        self.assertIn("compare-json", formal)
        self.assertIn("compare-inventory", formal)

    def test_enable_target_starts_app_and_caddy_then_verifies_https(self) -> None:
        plan = "\n".join(self._plan("enable-target"))
        self.assertIn(".target-write-owner-active", plan)
        self.assertIn("rm -f /var/lib/tokenkey/migration/.write-owner-locked", plan)
        self.assertIn("systemctl enable tokenkey.service tokenkey-pgdump.timer", plan)
        self.assertIn("up -d --no-deps tokenkey caddy", plan)
        self.assertIn("docker exec tokenkey wget", plan)
        self.assertNotIn("http://127.0.0.1:8080", plan)
        self.assertIn("--resolve", plan)
        self.assertIn("https://$API_DOMAIN/health/live", plan)

    def test_source_proxy_starts_only_caddy(self) -> None:
        steps = self._plan("proxy-source")
        plan = "\n".join(steps)
        self.assertIn("test -f", plan)
        self.assertNotIn("|| sudo cp -a", plan)
        self.assertIn("Caddyfile.source", plan)
        self.assertIn("reverse_proxy https://203.0.113.8", plan)
        self.assertIn("tls_server_name", plan)
        self.assertIn("caddy validate", plan)
        self.assertIn("up -d --no-deps --force-recreate caddy", plan)
        self.assertIn("systemctl disable tokenkey.service tokenkey-pgdump.timer", plan)
        self.assertNotIn("docker compose up -d tokenkey", plan)
        self.assertNotIn("docker compose up -d postgres", plan)
        self.assertNotIn("docker compose up -d redis", plan)
        self.assertIn('--resolve "$API_DOMAIN:443:127.0.0.1"', plan)
        self.assertIn('https://$API_DOMAIN/health/live', plan)
        render_step = next(item for item in steps if "Caddyfile.proxy" in item and "printf" in item)
        self.assertIn("API_DOMAIN=", render_step)

    def test_stable_gate_verifies_target_and_old_ip_proxy_https(self) -> None:
        target = "\n".join(self._plan("verify-target"))
        source = "\n".join(self._plan("verify-source-proxy", target_eip="35.81.204.18"))

        self.assertIn("docker exec tokenkey wget", target)
        self.assertIn("--resolve \"$API_DOMAIN:443:127.0.0.1\"", target)
        self.assertIn("https://$API_DOMAIN/health/live", target)
        self.assertIn("--resolve \"$API_DOMAIN:443:35.81.204.18\"", source)
        self.assertNotIn("127.0.0.1", source)
        self.assertIn("https://$API_DOMAIN/health/live", source)
        self.assertNotIn("docker exec tokenkey wget", source)

    def test_stable_gate_requires_the_old_public_ip(self) -> None:
        with self.assertRaisesRegex(ValueError, "verify-source-proxy requires target_eip"):
            self._plan("verify-source-proxy", target_eip="")

    def test_online_rehearsal_validates_the_app_container_without_starting_it(self) -> None:
        plan = "\n".join(
            build_remote_plan(
                "restore-target",
                root=pathlib.Path("/var/lib/tokenkey"),
                work_dir=pathlib.Path("/var/lib/tokenkey/migration/run-123"),
                transfer_url="https://transfer.invalid/object?redacted=1",
                expected_bundle_sha256="a" * 64,
                rehearsal=True,
            )
        )
        self.assertIn("docker compose", plan)
        self.assertIn("config --quiet", plan)
        self.assertIn("pull tokenkey", plan)
        self.assertIn("create --no-deps tokenkey", plan)
        self.assertIn("State.Running", plan)
        self.assertNotIn("up -d --no-deps tokenkey", plan)
        self.assertNotIn("docker exec tokenkey wget", plan)
        self.assertNotIn("up -d --no-deps caddy", plan)

    def test_resume_source_restores_original_caddy_before_services(self) -> None:
        plan = self._plan("resume-source")
        joined = "\n".join(plan)
        self.assertIn("if [ -f", joined)
        self.assertIn("Caddyfile.source", joined)
        self.assertIn("caddy/Caddyfile", joined)
        self.assertIn("systemctl enable tokenkey.service tokenkey-pgdump.timer", joined)
        self.assertIn("docker exec tokenkey wget", joined)
        self.assertIn('--resolve "$API_DOMAIN:443:127.0.0.1"', joined)
        self.assertIn("rm -f /var/lib/tokenkey/migration/.write-owner-locked", joined)
        restore_index = next(i for i, item in enumerate(plan) if "Caddyfile.source" in item)
        start_index = next(i for i, item in enumerate(plan) if "up -d tokenkey caddy" in item)
        unlock_index = next(i for i, item in enumerate(plan) if ".write-owner-locked" in item and "rm -f" in item)
        self.assertLess(restore_index, start_index)
        self.assertGreater(unlock_index, start_index)

    def test_release_target_returns_to_candidate_mode(self) -> None:
        plan = "\n".join(self._plan("release-target-candidate"))
        self.assertIn("stop tokenkey caddy", plan)
        self.assertIn(".target-write-owner-active", plan)
        self.assertIn(".write-owner-locked", plan)
        self.assertLess(plan.index("stop tokenkey caddy"), plan.index("rm -f"))

    def test_freeze_target_creates_verified_reverse_bundle(self) -> None:
        steps = self._plan("freeze-target")
        plan = "\n".join(steps)
        self.assertIn("systemctl disable tokenkey.service tokenkey-pgdump.timer", plan)
        self.assertIn("touch /var/lib/tokenkey/migration/.write-owner-locked", plan)
        self.assertIn("docker inspect", plan)
        self.assertIn('if [ "$TARGET_RUNNING" = true ]', plan)
        self.assertIn("up -d --no-deps postgres redis", plan)
        recover_index = next(i for i, item in enumerate(steps) if "up -d --no-deps postgres redis" in item)
        report_index = next(i for i, item in enumerate(steps) if "database-report" in item)
        stop_caddy = next(i for i, item in enumerate(steps) if "stop caddy" in item)
        self.assertLess(recover_index, report_index)
        self.assertLess(stop_caddy, report_index)
        self.assertIn("pg_dump --format=custom", plan)
        self.assertIn("redis-cli SAVE", plan)
        self.assertIn("bundle.tar.gz", plan)
        self.assertIn("$TK_MIGRATION_TRANSFER_URL", plan)
        self.assertIn("manifest_digest=", plan)

    def test_freeze_only_skips_pgdump_timer_stop_when_the_unit_is_absent(self) -> None:
        for action in ("freeze-source", "freeze-target"):
            with self.subTest(action=action):
                plan = "\n".join(self._plan(action))
                self.assertIn("systemctl list-unit-files tokenkey-pgdump.timer", plan)
                self.assertIn(
                    "systemctl stop tokenkey-pgdump.timer tokenkey-pgdump.service",
                    plan,
                )
                self.assertNotIn("systemctl stop tokenkey-pgdump.timer 2>/dev/null || true", plan)

    def test_target_proxy_drains_old_ec2_ip_back_to_restored_lightsail(self) -> None:
        plan = "\n".join(self._plan("proxy-target"))
        self.assertIn("reverse_proxy https://203.0.113.8", plan)
        self.assertIn("tls_server_name", plan)
        self.assertIn("up -d --no-deps --force-recreate caddy", plan)
        self.assertIn("systemctl disable tokenkey.service tokenkey-pgdump.timer", plan)
        self.assertIn(".target-proxy-retained", plan)
        self.assertIn("rm -f /var/lib/tokenkey/migration/.target-write-owner-active", plan)
        self.assertIn(".write-owner-locked", plan)
        self.assertNotIn("up -d tokenkey", plan)

    def test_unknown_remote_action_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "unsupported remote action"):
            self._plan("delete-source")

    def test_remote_actions_reject_destructive_paths_outside_tokenkey_root(self) -> None:
        for root, work_dir in (
            (pathlib.Path("/"), pathlib.Path("/var/lib/tokenkey/migration/us4")),
            (pathlib.Path("/var/lib/tokenkey"), pathlib.Path("/tmp/us4")),
            (pathlib.Path("/var/lib/tokenkey"), pathlib.Path("/var/lib/tokenkey/migration/../postgres")),
        ):
            with self.subTest(root=root, work_dir=work_dir):
                with self.assertRaisesRegex(ValueError, "fixed tokenkey migration paths"):
                    build_remote_plan(
                        "resume-source",
                        root=root,
                        work_dir=work_dir,
                    )

    def test_database_report_plan_includes_sequences_and_redacted_credentials(self) -> None:
        plan = "\n".join(self._plan("freeze-source"))
        self.assertIn("database-report", plan)
        self.assertNotIn("credentials=", plan)

    def test_database_report_excludes_soft_deleted_account_credentials(self) -> None:
        responses = [
            mock.Mock(stdout=""),
            mock.Mock(stdout=""),
            mock.Mock(stdout='{"id":7,"credentials":{"token":"secret"}}\n'),
        ]
        with tempfile.TemporaryDirectory() as raw, mock.patch(
            "ops.migration.edge_ec2_remote.subprocess.run", side_effect=responses
        ) as run:
            output = pathlib.Path(raw) / "database-report.json"
            _database_report(output)
            report = json.loads(output.read_text(encoding="utf-8"))

        credential_query = run.call_args_list[2].args[0][-1]
        self.assertIn("FROM accounts WHERE deleted_at IS NULL", credential_query)
        self.assertEqual(report["account_credentials"][0]["account_id"], 7)
        self.assertNotIn("secret", json.dumps(report))

    def test_redis_report_binds_absolute_expiry_time(self) -> None:
        binary_key = b"session\nkey\x00\xff"
        binary_value = b"serialized\x00value\xff"
        raw_report = json.dumps(
            ["0", [[binary_key.hex(), binary_value.hex(), 1893456000123]]]
        )
        responses = [mock.Mock(stdout=raw_report)]
        with tempfile.TemporaryDirectory() as raw, mock.patch(
            "ops.migration.edge_ec2_remote.subprocess.run", side_effect=responses
        ) as run:
            output = pathlib.Path(raw) / "redis-report.json"
            _redis_report(output)
            report = json.loads(output.read_text(encoding="utf-8"))

        self.assertEqual(report["keys"][0]["expire_at_ms"], 1893456000123)
        self.assertEqual(
            report["keys"][0]["key_sha256"], hashlib.sha256(binary_key).hexdigest()
        )
        self.assertEqual(
            report["keys"][0]["value_sha256"], hashlib.sha256(binary_value).hexdigest()
        )
        self.assertNotIn("session", json.dumps(report))
        command = run.call_args.args[0]
        self.assertIn("--json", command)
        self.assertIn("EVAL", command)

    def test_bundle_validation_rejects_path_traversal_and_unexpected_members(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            traversal = root / "traversal.tar.gz"
            with tarfile.open(traversal, "w:gz") as archive:
                info = tarfile.TarInfo("../outside")
                payload = b"escape"
                info.size = len(payload)
                archive.addfile(info, io.BytesIO(payload))
            with self.assertRaisesRegex(InventoryError, "unsafe bundle member"):
                validate_bundle_archive(traversal)

            unexpected = root / "unexpected.tar.gz"
            with tarfile.open(unexpected, "w:gz") as archive:
                info = tarfile.TarInfo("extra.txt")
                payload = b"not-part-of-contract"
                info.size = len(payload)
                archive.addfile(info, io.BytesIO(payload))
            with self.assertRaisesRegex(InventoryError, "unexpected bundle members"):
                validate_bundle_archive(unexpected)

    def test_archive_validation_accepts_the_exact_bundle_and_internal_links(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            bundle = root / "bundle.tar.gz"
            expected = (
                "database.dump",
                "globals.sql",
                "files.tar",
                "redis.tar",
                "inventory.json",
                "database-report.json",
                "redis-report.json",
                "artifacts.sha256",
            )
            with tarfile.open(bundle, "w:gz") as archive:
                for name in expected:
                    info = tarfile.TarInfo(name)
                    archive.addfile(info, io.BytesIO())
            validate_bundle_archive(bundle)

            files = root / "files.tar"
            with tarfile.open(files, "w") as archive:
                directory = tarfile.TarInfo("./app")
                directory.type = tarfile.DIRTYPE
                archive.addfile(directory)
                account = tarfile.TarInfo("./app/accounts.json")
                payload = b"{}\n"
                account.size = len(payload)
                archive.addfile(account, io.BytesIO(payload))
                link = tarfile.TarInfo("./app/accounts-current.json")
                link.type = tarfile.SYMTYPE
                link.linkname = "accounts.json"
                archive.addfile(link)
            validate_data_archive(files, "files")

    def test_nested_tar_validation_rejects_escaping_links_and_special_files(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            archive_path = root / "files.tar"
            with tarfile.open(archive_path, "w") as archive:
                link = tarfile.TarInfo("app/escape")
                link.type = tarfile.SYMTYPE
                link.linkname = "../../etc/shadow"
                archive.addfile(link)
            with self.assertRaisesRegex(InventoryError, "unsafe archive link"):
                validate_data_archive(archive_path, "files")

            with tarfile.open(archive_path, "w") as archive:
                link = tarfile.TarInfo("app/database-current")
                link.type = tarfile.SYMTYPE
                link.linkname = "../postgres/base/123"
                archive.addfile(link)
            with self.assertRaisesRegex(InventoryError, "excluded data plane"):
                validate_data_archive(archive_path, "files")

            fifo_path = root / "redis.tar"
            with tarfile.open(fifo_path, "w") as archive:
                fifo = tarfile.TarInfo("redis/queue")
                fifo.type = tarfile.FIFOTYPE
                archive.addfile(fifo)
            with self.assertRaisesRegex(InventoryError, "unsupported archive member type"):
                validate_data_archive(fifo_path, "redis")

    def test_redis_compare_allows_only_keys_that_naturally_expired(self) -> None:
        expected = {
            "report_version": 1,
            "kind": "redis_logical",
            "keys": [
                {"key_sha256": "expired", "value_sha256": "old", "expire_at_ms": 1000},
                {"key_sha256": "live", "value_sha256": "same", "expire_at_ms": 5000},
                {"key_sha256": "permanent", "value_sha256": "same", "expire_at_ms": -1},
            ],
        }
        actual = {
            "report_version": 1,
            "kind": "redis_logical",
            "keys": [
                {"key_sha256": "live", "value_sha256": "same", "expire_at_ms": 5000},
                {"key_sha256": "permanent", "value_sha256": "same", "expire_at_ms": -1},
            ],
        }

        self.assertEqual(compare_redis_reports(expected, actual, now_ms=2000), [])
        self.assertEqual(
            compare_redis_reports(expected, actual, now_ms=500),
            ["expired: missing before absolute expiry"],
        )
        changed = copy.deepcopy(actual)
        changed["keys"][0]["expire_at_ms"] = 6000
        self.assertEqual(
            compare_redis_reports(expected, changed, now_ms=2000),
            ["live: value or expiry mismatch"],
        )


if __name__ == "__main__":
    unittest.main()
