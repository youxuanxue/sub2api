#!/usr/bin/env python3
from __future__ import annotations

import argparse
import contextlib
import gzip
import importlib.util
import io
import json
import pathlib
import stat
import subprocess
import tempfile
import unittest
from unittest import mock


SCRIPT = pathlib.Path(__file__).with_name("migrate-edge-accounts.py")
SPEC = importlib.util.spec_from_file_location("migrate_edge_accounts", SCRIPT)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"cannot load {SCRIPT}")
MIGRATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MIGRATE)


class TargetParserTest(unittest.TestCase):
    def test_explicit_lightsail_target(self) -> None:
        self.assertEqual(
            MIGRATE.parse_target("edge:us4@lightsail"),
            ("edge", "us4", "lightsail"),
        )

    def test_explicit_ec2_target(self) -> None:
        self.assertEqual(
            MIGRATE.parse_target("edge:us4@ec2"),
            ("edge", "us4", "ec2"),
        )

    def test_legacy_edge_and_prod_keep_existing_routing(self) -> None:
        self.assertEqual(MIGRATE.parse_target("edge:us4"), ("edge", "us4", "auto"))
        self.assertEqual(MIGRATE.parse_target("prod"), ("prod", "", "ec2"))

    def test_unknown_platform_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "unknown edge platform"):
            MIGRATE.parse_target("edge:us4@lambda")

    def test_missing_edge_id_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "edge id"):
            MIGRATE.parse_target("edge:@ec2")

    def test_same_edge_platforms_resolve_distinct_instances(self) -> None:
        calls: list[list[str]] = []

        def fake_run(cmd: list[str], **_kwargs) -> subprocess.CompletedProcess:
            calls.append(cmd)
            platform = cmd[cmd.index("--platform") + 1]
            instance_id = "mi-lightsail" if platform == "lightsail" else "i-ec2"
            return subprocess.CompletedProcess(
                cmd,
                0,
                stdout=json.dumps({"region": "us-west-2", "instance_id": instance_id}),
                stderr="",
            )

        with mock.patch.object(MIGRATE, "run", side_effect=fake_run):
            lightsail = MIGRATE.resolve_edge("edge:us4@lightsail")
            ec2 = MIGRATE.resolve_edge("edge:us4@ec2")

        self.assertEqual(lightsail, ("us-west-2", "mi-lightsail"))
        self.assertEqual(ec2, ("us-west-2", "i-ec2"))
        self.assertEqual(
            [cmd[cmd.index("--platform") + 1] for cmd in calls],
            ["lightsail", "ec2"],
        )


class MigrationSafetyTest(unittest.TestCase):
    SECRET = "refresh-secret-must-not-be-logged"

    def _payload(self) -> dict:
        account_columns = [
            ("id", "bigint"),
            ("name", "character varying"),
            ("platform", "character varying"),
            ("type", "character varying"),
            ("channel_type", "integer"),
            ("credentials", "jsonb"),
            ("status", "character varying"),
            ("schedulable", "boolean"),
            ("error_message", "text"),
            ("last_used_at", "timestamp with time zone"),
            ("rate_limited_at", "timestamp with time zone"),
            ("rate_limit_reset_at", "timestamp with time zone"),
            ("overload_until", "timestamp with time zone"),
            ("session_window_start", "timestamp with time zone"),
            ("session_window_end", "timestamp with time zone"),
            ("session_window_status", "character varying"),
            ("temp_unschedulable_until", "timestamp with time zone"),
            ("temp_unschedulable_reason", "text"),
            ("proxy_id", "bigint"),
            ("proxy_fallback_origin_id", "bigint"),
            ("tier_id", "bigint"),
        ]
        return {
            "schema": {
                "accounts": [
                    {"column_name": name, "data_type": dtype}
                    for name, dtype in account_columns
                ],
                "groups": [
                    {"column_name": "id", "data_type": "bigint"},
                    {"column_name": "name", "data_type": "character varying"},
                    {"column_name": "platform", "data_type": "character varying"},
                    {"column_name": "fallback_group_id", "data_type": "bigint"},
                    {
                        "column_name": "fallback_group_id_on_invalid_request",
                        "data_type": "bigint",
                    },
                ],
            },
            "requested_account_ids": [11],
            "accounts": [
                {
                    "id": 11,
                    "name": "oauth-source",
                    "platform": "anthropic",
                    "type": "oauth",
                    "channel_type": 0,
                    "credentials": {"refresh_token": self.SECRET},
                    "status": "error",
                    "schedulable": True,
                    "error_message": "host error",
                    "last_used_at": "2026-08-01T00:00:00Z",
                    "rate_limited_at": "2026-08-01T00:00:00Z",
                    "rate_limit_reset_at": "2026-08-02T00:00:00Z",
                    "overload_until": "2026-08-02T00:00:00Z",
                    "session_window_start": "2026-08-01T00:00:00Z",
                    "session_window_end": "2026-08-02T00:00:00Z",
                    "session_window_status": "rejected",
                    "temp_unschedulable_until": "2026-08-02T00:00:00Z",
                    "temp_unschedulable_reason": "host cooldown",
                    "proxy_id": 8,
                    "proxy_fallback_origin_id": 7,
                    "tier_id": 5,
                }
            ],
            "groups": [
                {
                    "id": 21,
                    "name": "oauth-group",
                    "platform": "anthropic",
                    "fallback_group_id": None,
                    "fallback_group_id_on_invalid_request": None,
                }
            ],
            "bindings": [{"account_id": 11, "group_id": 21, "priority": 3}],
        }

    def test_extract_rejects_a_missing_requested_account(self) -> None:
        payload = self._payload()
        payload["requested_account_ids"] = [11, 12]
        payload["accounts"] = [payload["accounts"][0]]
        with self.assertRaisesRegex(ValueError, "requested account ids"):
            MIGRATE.validate_extract_payload(payload, [11, 12])

    def test_account_ids_reject_duplicates_and_non_positive_values(self) -> None:
        for raw in ("11,11", "0", "-1", "11,,12", "abc"):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                MIGRATE.parse_account_ids(raw)

    def test_extract_rejects_duplicate_accounts(self) -> None:
        payload = self._payload()
        payload["accounts"].append(dict(payload["accounts"][0]))
        with self.assertRaisesRegex(ValueError, "duplicate account"):
            MIGRATE.validate_extract_payload(payload, [11])

    def test_extract_rejects_binding_for_an_unrequested_account(self) -> None:
        payload = self._payload()
        payload["bindings"].append(
            {"account_id": 12, "group_id": 21, "priority": 4},
        )
        with self.assertRaisesRegex(ValueError, "binding account"):
            MIGRATE.validate_extract_payload(payload, [11])

    def test_extract_rejects_binding_to_a_missing_group(self) -> None:
        payload = self._payload()
        payload["bindings"][0]["group_id"] = 99
        with self.assertRaisesRegex(ValueError, "binding group"):
            MIGRATE.validate_extract_payload(payload, [11])

    def test_extract_rejects_a_dangling_fallback_group(self) -> None:
        payload = self._payload()
        payload["groups"][0]["fallback_group_id"] = 99
        with self.assertRaisesRegex(ValueError, "fallback group"):
            MIGRATE.validate_extract_payload(payload, [11])

    def test_extract_rejects_groups_outside_the_binding_fallback_closure(self) -> None:
        payload = self._payload()
        payload["groups"].append(
            {
                "id": 22,
                "name": "unrelated-group",
                "platform": "anthropic",
                "fallback_group_id": None,
                "fallback_group_id_on_invalid_request": None,
            },
        )
        with self.assertRaisesRegex(ValueError, "fallback closure"):
            MIGRATE.validate_extract_payload(payload, [11])

    def test_expected_manifest_contains_credential_keys_but_no_values(self) -> None:
        manifest = MIGRATE.build_expected_manifest(self._payload(), {}, {})
        encoded = json.dumps(manifest, sort_keys=True)
        self.assertIn("refresh_token", encoded)
        self.assertNotIn(self.SECRET, encoded)
        self.assertEqual(
            manifest["bindings"],
            [{"account": "oauth-source", "group": "oauth-group", "priority": 3}],
        )

    def test_expected_manifest_maps_both_fallback_names_after_renaming(self) -> None:
        payload = self._payload()
        payload["groups"].extend(
            [
                {
                    "id": 22,
                    "name": "fallback-default",
                    "platform": "anthropic",
                    "fallback_group_id": None,
                    "fallback_group_id_on_invalid_request": None,
                },
                {
                    "id": 23,
                    "name": "fallback-invalid",
                    "platform": "anthropic",
                    "fallback_group_id": None,
                    "fallback_group_id_on_invalid_request": None,
                },
            ],
        )
        payload["groups"][0]["fallback_group_id"] = 22
        payload["groups"][0]["fallback_group_id_on_invalid_request"] = 23

        manifest = MIGRATE.build_expected_manifest(
            payload,
            {"oauth-source": "oauth-target"},
            {
                "oauth-group": "oauth-target-group",
                "fallback-default": "fallback-default-target",
                "fallback-invalid": "fallback-invalid-target",
            },
        )

        self.assertEqual(manifest["accounts"][0]["name"], "oauth-target")
        primary = next(
            group for group in manifest["groups"]
            if group["name"] == "oauth-target-group"
        )
        self.assertEqual(primary["fallback_group"], "fallback-default-target")
        self.assertEqual(
            primary["invalid_request_fallback_group"],
            "fallback-invalid-target",
        )

    def test_build_preserves_credentials_and_resets_host_local_state(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            state_dir = pathlib.Path(tmp)
            state_dir.chmod(0o755)
            (state_dir / "payload.json").write_text(
                json.dumps(self._payload()),
                encoding="utf-8",
            )
            stdout = io.StringIO()
            stderr = io.StringIO()
            args = argparse.Namespace(rename=[], rename_group=[])
            with mock.patch.object(MIGRATE, "STATE_DIR", state_dir):
                with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                    MIGRATE.cmd_build(args)
            sql = (state_dir / "migrate.sql").read_text(encoding="utf-8")
            manifest_text = (state_dir / "expected-manifest.json").read_text(
                encoding="utf-8",
            )
            state_mode = stat.S_IMODE(state_dir.stat().st_mode)
            sql_mode = stat.S_IMODE((state_dir / "migrate.sql").stat().st_mode)
            manifest_mode = stat.S_IMODE(
                (state_dir / "expected-manifest.json").stat().st_mode,
            )

        self.assertEqual(state_mode, 0o700)
        self.assertEqual(sql_mode, 0o600)
        self.assertEqual(manifest_mode, 0o600)
        self.assertNotIn(self.SECRET, manifest_text)
        self.assertIn(self.SECRET, sql)
        self.assertNotIn(self.SECRET, stdout.getvalue())
        self.assertNotIn(self.SECRET, stderr.getvalue())
        self.assertIn("'active', false", sql)
        reset_columns = [
            "error_message", "last_used_at", "rate_limited_at", "rate_limit_reset_at",
            "overload_until", "session_window_start", "session_window_end",
            "session_window_status", "temp_unschedulable_until",
            "temp_unschedulable_reason", "proxy_id", "proxy_fallback_origin_id", "tier_id",
        ]
        sql_lines = sql.splitlines()
        account_insert_index = next(
            index
            for index, line in enumerate(sql_lines)
            if line.startswith("  INSERT INTO accounts (")
        )
        insert_columns = sql_lines[account_insert_index]
        insert_values = sql_lines[account_insert_index + 1]
        columns = [item.strip() for item in insert_columns.removeprefix("  INSERT INTO accounts (").removesuffix(")").split(",")]
        values = [item.strip() for item in insert_values.removeprefix("    VALUES (").removesuffix(") RETURNING id INTO new_aid;").split(",")]
        mapped = dict(zip(columns, values, strict=True))
        for column in reset_columns:
            self.assertEqual(mapped[column], "NULL", column)
        self.assertEqual(mapped["status"], "'active'")
        self.assertEqual(mapped["schedulable"], "false")
        self.assertEqual(mapped["credentials"], "(v->'credentials')")
        self.assertIn("INSERT INTO account_groups", sql)
        self.assertIn("migration manifest mismatch", sql)
        self.assertNotIn("created_at > now() - interval '5 minutes'", sql)
        self.assertIn("VALUES ('full_rebuild', NULL, now())", sql)

    def test_extract_uses_private_remote_and_local_storage(self) -> None:
        captured_commands: list[str] = []

        def fake_run(cmd: list[str], **_kwargs) -> subprocess.CompletedProcess:
            if cmd[:3] == ["date", "-u", "+%Y%m%dT%H%M%SZ"]:
                return subprocess.CompletedProcess(cmd, 0, stdout="20260805T000000Z\n", stderr="")
            if cmd[:3] == ["aws", "s3", "cp"]:
                destination = pathlib.Path(cmd[-1])
                with gzip.open(destination, "wt", encoding="utf-8") as handle:
                    json.dump(self._payload(), handle)
                return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
            if cmd[:3] == ["aws", "s3", "rm"]:
                return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
            raise AssertionError(f"unexpected command: {cmd}")

        def fake_ssm_run(
            _region: str,
            _instance_id: str,
            commands: list[str],
            _comment: str,
        ) -> str:
            captured_commands.extend(commands)
            return "EXTRACT_OK\n"

        with tempfile.TemporaryDirectory() as tmp:
            state_dir = pathlib.Path(tmp) / "cache"
            args = argparse.Namespace(
                from_target="edge:us4@lightsail",
                account_ids="11",
            )
            with mock.patch.object(MIGRATE, "STATE_DIR", state_dir), \
                 mock.patch.object(MIGRATE, "resolve_edge", return_value=("us-west-2", "mi-test")), \
                 mock.patch.object(MIGRATE, "presign", return_value="https://example.test/upload"), \
                 mock.patch.object(MIGRATE, "run", side_effect=fake_run), \
                 mock.patch.object(MIGRATE, "ssm_run", side_effect=fake_ssm_run), \
                 contextlib.redirect_stdout(io.StringIO()), \
                 contextlib.redirect_stderr(io.StringIO()):
                MIGRATE.cmd_extract(args)
            state_mode = stat.S_IMODE(state_dir.stat().st_mode)
            payload_mode = stat.S_IMODE((state_dir / "payload.json").stat().st_mode)

        self.assertEqual(state_mode, 0o700)
        self.assertEqual(payload_mode, 0o600)
        self.assertIn("umask 077", captured_commands)
        self.assertTrue(
            any(command.startswith("trap '") and command.endswith("' EXIT") for command in captured_commands),
            captured_commands,
        )
        extract_command = next(
            command for command in captured_commands if "json_build_object" in command
        )
        self.assertIn("WITH RECURSIVE selected_accounts", extract_command)
        self.assertIn("seed_group_ids", extract_command)
        self.assertIn("fallback_group_id", extract_command)
        self.assertIn("fallback_group_id_on_invalid_request", extract_command)
        self.assertIn("requested_account_ids", extract_command)

    def test_extract_deletes_s3_object_when_local_download_fails(self) -> None:
        removed = False

        def fake_run(cmd: list[str], **_kwargs) -> subprocess.CompletedProcess:
            nonlocal removed
            if cmd[:3] == ["date", "-u", "+%Y%m%dT%H%M%SZ"]:
                return subprocess.CompletedProcess(cmd, 0, stdout="20260805T000000Z\n", stderr="")
            if cmd[:3] == ["aws", "s3", "cp"]:
                raise subprocess.CalledProcessError(1, cmd, stderr="download failed")
            if cmd[:3] == ["aws", "s3", "rm"]:
                removed = True
                return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
            raise AssertionError(f"unexpected command: {cmd}")

        with tempfile.TemporaryDirectory() as tmp:
            args = argparse.Namespace(from_target="edge:us4@lightsail", account_ids="11")
            with mock.patch.object(MIGRATE, "STATE_DIR", pathlib.Path(tmp) / "cache"), \
                 mock.patch.object(MIGRATE, "resolve_edge", return_value=("us-west-2", "mi-test")), \
                 mock.patch.object(MIGRATE, "presign", return_value="https://example.test/upload"), \
                 mock.patch.object(MIGRATE, "run", side_effect=fake_run), \
                 mock.patch.object(MIGRATE, "ssm_run", return_value="EXTRACT_OK\n"), \
                 contextlib.redirect_stdout(io.StringIO()), \
                 contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(subprocess.CalledProcessError):
                    MIGRATE.cmd_extract(args)

        self.assertTrue(removed, "the credential-bearing S3 object must be deleted on failure")

    def test_load_deletes_s3_object_when_remote_execution_fails(self) -> None:
        removed = False

        def fake_run(cmd: list[str], **_kwargs) -> subprocess.CompletedProcess:
            nonlocal removed
            if cmd[:3] == ["date", "-u", "+%Y%m%dT%H%M%SZ"]:
                return subprocess.CompletedProcess(cmd, 0, stdout="20260805T000000Z\n", stderr="")
            if cmd[:3] == ["aws", "s3", "cp"]:
                return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
            if cmd[:3] == ["aws", "s3", "rm"]:
                removed = True
                return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
            raise AssertionError(f"unexpected command: {cmd}")

        with tempfile.TemporaryDirectory() as tmp:
            state_dir = pathlib.Path(tmp) / "cache"
            state_dir.mkdir()
            (state_dir / "migrate.sql").write_text("SELECT 1;\n", encoding="utf-8")
            args = argparse.Namespace(to_target="edge:us4@ec2", execute=True)
            with mock.patch.object(MIGRATE, "STATE_DIR", state_dir), \
                 mock.patch.object(MIGRATE, "resolve_edge", return_value=("us-west-2", "i-test")), \
                 mock.patch.object(MIGRATE, "presign", return_value="https://example.test/download"), \
                 mock.patch.object(MIGRATE, "run", side_effect=fake_run), \
                 mock.patch.object(MIGRATE, "ssm_run", side_effect=RuntimeError("remote failed")), \
                 contextlib.redirect_stdout(io.StringIO()), \
                 contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaisesRegex(RuntimeError, "remote failed"):
                    MIGRATE.cmd_load(args)

        self.assertTrue(removed, "the credential-bearing S3 object must be deleted on failure")

    def test_load_dry_run_never_prints_migrate_sql_secrets(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            state_dir = pathlib.Path(tmp)
            (state_dir / "migrate.sql").write_text(
                f"SELECT '{self.SECRET}';\n",
                encoding="utf-8",
            )
            stdout = io.StringIO()
            args = argparse.Namespace(to_target="edge:us4@ec2", execute=False)
            with mock.patch.object(MIGRATE, "STATE_DIR", state_dir):
                with contextlib.redirect_stdout(stdout):
                    MIGRATE.cmd_load(args)
        self.assertNotIn(self.SECRET, stdout.getvalue())
        self.assertIn("NOT applied", stdout.getvalue())

    def test_load_rejects_missing_manifest_verification_marker(self) -> None:
        def fake_run(cmd: list[str], **_kwargs) -> subprocess.CompletedProcess:
            if cmd[:3] == ["date", "-u", "+%Y%m%dT%H%M%SZ"]:
                return subprocess.CompletedProcess(
                    cmd,
                    0,
                    stdout="20260805T000000Z\n",
                    stderr="",
                )
            if cmd[:3] in (["aws", "s3", "cp"], ["aws", "s3", "rm"]):
                return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
            raise AssertionError(f"unexpected command: {cmd}")

        with tempfile.TemporaryDirectory() as tmp:
            state_dir = pathlib.Path(tmp)
            (state_dir / "migrate.sql").write_text("SELECT 1;\n", encoding="utf-8")
            args = argparse.Namespace(to_target="edge:us4@ec2", execute=True)
            with mock.patch.object(MIGRATE, "STATE_DIR", state_dir), \
                 mock.patch.object(MIGRATE, "resolve_edge", return_value=("us-west-2", "i-test")), \
                 mock.patch.object(MIGRATE, "presign", return_value="https://example.test/download"), \
                 mock.patch.object(MIGRATE, "run", side_effect=fake_run), \
                 mock.patch.object(MIGRATE, "ssm_run", return_value="LOAD_OK\n"), \
                 contextlib.redirect_stdout(io.StringIO()), \
                 contextlib.redirect_stderr(io.StringIO()), \
                 self.assertRaises(SystemExit):
                MIGRATE.cmd_load(args)

    def test_set_schedulable_requires_exactly_one_row(self) -> None:
        sql = MIGRATE.build_set_schedulable_sql("kiro-us4-real", True)
        self.assertIn("GET DIAGNOSTICS affected = ROW_COUNT", sql)
        self.assertIn("affected <> 1", sql)
        self.assertIn("schedulable IS DISTINCT FROM true", sql)
        self.assertLess(
            sql.index("affected <> 1"),
            sql.index("INSERT INTO scheduler_outbox"),
        )

    def test_set_schedulable_rejects_missing_success_marker(self) -> None:
        args = argparse.Namespace(
            to_target="edge:us4@ec2",
            account_name="kiro-us4-real",
            value="true",
            execute=True,
        )
        with mock.patch.object(
            MIGRATE,
            "resolve_edge",
            return_value=("us-west-2", "i-test"),
        ), mock.patch.object(
            MIGRATE,
            "ssm_run",
            return_value="UPDATE 0\n",
        ), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(
            io.StringIO(),
        ), self.assertRaises(SystemExit):
            MIGRATE.cmd_set_schedulable(args)

    def test_extract_empty_account_ids_fails_before_target_resolution(self) -> None:
        args = argparse.Namespace(from_target="edge:us5@lightsail", account_ids="")
        with mock.patch.object(
            MIGRATE,
            "resolve_edge",
            side_effect=AssertionError("target must not resolve"),
        ):
            with contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    MIGRATE.cmd_extract(args)

    def test_soft_delete_empty_account_ids_fails_before_target_resolution(self) -> None:
        args = argparse.Namespace(
            from_target="edge:us5@lightsail",
            account_ids="",
            execute=False,
        )
        with mock.patch.object(
            MIGRATE,
            "resolve_edge",
            side_effect=AssertionError("target must not resolve"),
        ):
            with contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    MIGRATE.cmd_soft_delete(args)


if __name__ == "__main__":
    unittest.main()
