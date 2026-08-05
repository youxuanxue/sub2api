#!/usr/bin/env python3
from __future__ import annotations

import argparse
import contextlib
import importlib.util
import io
import json
import pathlib
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
                ],
            },
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
            "groups": [{"id": 21, "name": "oauth-group", "platform": "anthropic"}],
            "bindings": [{"account_id": 11, "group_id": 21, "priority": 3}],
        }

    def test_build_preserves_credentials_and_resets_host_local_state(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            state_dir = pathlib.Path(tmp)
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
        self.assertIn("VALUES ('full_rebuild', NULL, now())", sql)

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
