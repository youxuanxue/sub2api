#!/usr/bin/env python3
"""Contracts for the cn-prod pgBackRest runner and its unit installer.

The point of these is the freshness coupling: daily diagnostics classifies
physical backup health through a receipt that must be stamped within 900s, so an
installer that schedules only the 2-hourly backup produces a receipt that is
always stale. That is how cn-prod ended up with healthy pgBackRest backups and no
gate able to see them.
"""

from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest import mock


_HERE = pathlib.Path(__file__).resolve().parent


def _load(name: str, filename: str):
    spec = importlib.util.spec_from_file_location(name, _HERE / filename)
    module = importlib.util.module_from_spec(spec)
    assert spec and spec.loader
    spec.loader.exec_module(module)
    return module


runner = _load("cn_pgbackrest", "cn_pgbackrest.py")
installer = _load("install_cn_postgres_backup", "install_cn_postgres_backup.py")


class CnBackupRunnerTest(unittest.TestCase):
    def test_repo2_backup_is_explicit_and_receipt_is_per_repository(self) -> None:
        before = {
            1: {"name": "tokenkey", "backup": []},
            2: {"name": "tokenkey", "backup": []},
        }
        after = {
            1: {"name": "tokenkey", "backup": [{"label": "one", "type": "full"}]},
            2: {"name": "tokenkey", "backup": [{"label": "two", "type": "full"}]},
        }
        with tempfile.TemporaryDirectory() as root, \
                mock.patch.object(runner, "STATE", pathlib.Path(root)), \
                mock.patch.object(runner, "repo2_enabled", return_value=True), \
                mock.patch.object(runner, "repo_info", side_effect=[before[1], before[2], after[1], after[2]]), \
                mock.patch.object(runner, "backrest") as backrest:
            runner.cmd_backup()
            receipt = json.loads((pathlib.Path(root) / "backup.json").read_text())
        self.assertEqual([call.args[:3] for call in backrest.call_args_list], [
            ("--repo=1", "--type=full", "backup"),
            ("--repo=2", "--type=full", "backup"),
        ])
        self.assertTrue(receipt["cos_repo2"])
        self.assertEqual({item["repo"] for item in receipt["repositories"]}, {1, 2})

    def test_receipt_paths_match_what_the_verdict_reads(self) -> None:
        # host_https_watch.physical_backup_fact accepts health.json or
        # archive_check.json under this directory; writing anywhere else means a
        # healthy host reports "missing/invalid".
        self.assertEqual(runner.STATE, pathlib.Path("/var/lib/tokenkey/postgres-backup"))

    def test_weekly_full_otherwise_incremental(self) -> None:
        # Empty repo must take a full, never an incremental against nothing.
        self.assertEqual(runner.backup_kind([], 1_792_687_000), "full")
        recent_full = [{"type": "full", "timestamp": {"stop": 1_792_686_000}}]
        self.assertEqual(runner.backup_kind(recent_full, 1_792_687_000), "incr")
        stale_full = [{"type": "full", "timestamp": {"stop": 1_792_687_000 - 14 * 86400}}]
        self.assertEqual(runner.backup_kind(stale_full, 1_792_687_000), "full")

    def test_container_is_resolved_not_hard_coded(self) -> None:
        body = (_HERE / "cn_pgbackrest.py").read_text(encoding="utf-8")
        # The Tencent USW layout exception renamed exactly this container, so a
        # literal exec target fails with "No such container" while looking like a
        # healthy probe. Every docker call must go through _container().
        for line in body.splitlines():
            stripped = line.strip()
            if stripped.startswith("#") or "docker" not in stripped:
                continue
            if "tokenkey-postgres" in stripped:
                self.assertIn(
                    "candidates",
                    stripped,
                    msg=f"literal container in a docker call: {stripped}",
                )

    def test_rejects_unknown_subcommand(self) -> None:
        import io
        import contextlib

        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            import sys

            saved = sys.argv
            try:
                sys.argv = ["cn_pgbackrest.py", "destroy"]
                self.assertEqual(runner.main(), 2)
            finally:
                sys.argv = saved
        self.assertIn("usage", stderr.getvalue())

    def test_repo2_disabled_keeps_single_repository(self) -> None:
        with mock.patch.object(runner, "repo2_enabled", return_value=False):
            self.assertEqual(runner.repository_numbers(), (1,))

    def _run_check(self, repo_infos: dict, failed_count: int = 0) -> tuple[dict, int]:
        """Drive cmd_check against per-repository info, returning the receipt."""
        written: dict = {}

        def fake_backrest(*args, **kwargs):
            if args[-1] == "info":
                repo = int(args[0].split("=", 1)[1])
                return json.dumps([repo_infos[repo]])
            if args == ("check",):
                return ""
            raise AssertionError(args)

        archiver = ('{"last_archived_wal": "wal", "last_archived_time": null,'
                    f' "failed_count": {failed_count}}}')
        code = 0
        with tempfile.TemporaryDirectory() as root, \
                mock.patch.object(runner, "STATE", pathlib.Path(root)), \
                mock.patch.object(runner, "repo2_enabled", return_value=True), \
                mock.patch.object(runner, "_container", return_value="tokenkey-postgres"), \
                mock.patch.object(runner, "backrest", side_effect=fake_backrest), \
                mock.patch.object(runner, "run", return_value=archiver):
            try:
                runner.cmd_check()
            except SystemExit as exit_code:
                code = exit_code.code
            written = json.loads((pathlib.Path(root) / "archive_check.json").read_text())
        return written, code

    def test_new_repository_without_backups_is_bootstrapping_not_failed(self) -> None:
        # cn-prod refreshes its identity every 15 minutes and backs up every two
        # hours, so the gap between "repo2 appears in the config" and "repo2 has
        # a full" is the widest of the two hosts. Exiting non-zero across it
        # turned a guaranteed initialisation state into a red workflow.
        receipt, code = self._run_check({
            1: {"name": "tokenkey", "status": {"code": 0},
                "backup": [{"label": "old", "type": "full", "timestamp": {"stop": 1}}]},
            2: {"name": "tokenkey", "status": {"code": 2, "message": "no valid backups"},
                "backup": []},
        })

        self.assertEqual(code, 0)
        self.assertEqual(receipt["status"], "degraded")
        self.assertEqual(receipt["reasons"], ["repository_bootstrapping"])
        self.assertEqual(receipt["repository_bootstrapping"]["repositories"], [2])
        self.assertEqual(receipt["repository_bootstrapping"]["status"], "ok")
        self.assertEqual(
            receipt["repositories"],
            [{"repo": 1, "status": "ok"}, {"repo": 2, "status": "bootstrapping"}],
        )

    def test_repository_with_backups_reporting_invalid_still_fails(self) -> None:
        # Code 2 with backups present is a repository that LOST its chain, which
        # is the opposite of a bootstrap and must stay a failure.
        receipt, code = self._run_check({
            1: {"name": "tokenkey", "status": {"code": 0}, "backup": []},
            2: {"name": "tokenkey", "status": {"code": 2, "message": "no valid backups"},
                "backup": [{"label": "gone", "type": "full", "timestamp": {"stop": 1}}]},
        })

        self.assertEqual(code, 2)
        self.assertEqual(receipt["status"], "degraded")
        self.assertNotIn("reasons", receipt)
        self.assertEqual(receipt["repositories"][1], {"repo": 2, "status": "failed"})

    def test_primary_repository_never_gets_the_bootstrap_exemption(self) -> None:
        receipt, code = self._run_check({
            1: {"name": "tokenkey", "status": {"code": 2, "message": "no valid backups"},
                "backup": []},
            2: {"name": "tokenkey", "status": {"code": 0}, "backup": []},
        })

        self.assertEqual(code, 2)
        self.assertNotIn("reasons", receipt)
        self.assertEqual(receipt["repositories"][0], {"repo": 1, "status": "failed"})

    def test_bootstrap_does_not_excuse_wal_archive_failures(self) -> None:
        receipt, code = self._run_check({
            1: {"name": "tokenkey", "status": {"code": 0}, "backup": []},
            2: {"name": "tokenkey", "status": {"code": 2}, "backup": []},
        }, failed_count=3)

        self.assertEqual(code, 2)
        self.assertEqual(receipt["status"], "degraded")
        self.assertNotIn("reasons", receipt)

    def test_check_failure_still_writes_a_diagnosable_receipt(self) -> None:
        # A missing receipt is indistinguishable from a dead timer to the
        # freshness gate, so an exploding check must leave evidence behind.
        # This is the exact shape of the real 2026-11-02 cn-prod failure, where
        # repo2 had no stanza and the old code exited before writing anything.
        error = (
            "docker exit 55: P00 INFO: check command begin 2.58.0: --repo2-s3-key=<redacted>\n"
            "P00  ERROR: [055]: unable to load info file "
            "'/cn-prod/postgres/archive/tokenkey/archive.info'\n"
            "      HINT: has a stanza-create been performed?"
        )

        def fake_backrest(*args, **kwargs):
            if args == ("check",):
                raise runner.Err(error)
            raise AssertionError(args)

        with tempfile.TemporaryDirectory() as root, \
                mock.patch.object(runner, "STATE", pathlib.Path(root)), \
                mock.patch.object(runner, "repo2_enabled", return_value=True), \
                mock.patch.object(runner, "_container", return_value="tokenkey-postgres"), \
                mock.patch.object(runner, "backrest", side_effect=fake_backrest):
            with self.assertRaises(runner.Err):
                runner.cmd_check()
            receipt = json.loads((pathlib.Path(root) / "archive_check.json").read_text())

        self.assertEqual(receipt["status"], "degraded")
        self.assertEqual(receipt["reasons"], ["check_command_failed"])
        detail = receipt["check_command_failed"]["detail"]
        self.assertIn("[055]", detail)
        self.assertIn("stanza-create", detail)
        self.assertNotIn("--repo2-s3-key", detail)
        self.assertTrue(receipt["cos_repo2"])

    def test_check_failure_detail_drops_the_command_line_noise(self) -> None:
        detail = runner.check_failure_detail(
            "INFO: check command begin --repo1-s3-key=x --repo2-s3-key-secret=y\n"
            "ERROR: [082]: WAL segment 000000010000001600000032 was not archived\n"
            "HINT: check the archive_command to ensure that all options are correct."
        )
        self.assertIn("[082]", detail)
        self.assertIn("HINT", detail)
        self.assertNotIn("s3-key", detail)
        self.assertLessEqual(len(detail), 400)


class CnBackupInstallerTest(unittest.TestCase):
    def setUp(self) -> None:
        self.bodies = installer.unit_bodies()

    def test_installs_both_backup_and_archive_check(self) -> None:
        self.assertEqual(
            sorted(self.bodies),
            [
                "tokenkey-cn-postgres-archive-check.service",
                "tokenkey-cn-postgres-archive-check.timer",
                "tokenkey-cn-postgres-backup.service",
                "tokenkey-cn-postgres-backup.timer",
            ],
        )

    def test_archive_check_runs_inside_the_receipt_freshness_window(self) -> None:
        """The 5-minute cadence is a contract, not a preference.

        physical_backup_fact rejects a receipt older than 900s. Backups run every
        2 hours, so without this timer the receipt is stale ~97% of the time.
        """
        timer = self.bodies["tokenkey-cn-postgres-archive-check.timer"]
        self.assertIn("OnUnitActiveSec=5min", timer)
        self.assertIn("OnBootSec=2min", timer)

    def test_backup_cadence_matches_prod(self) -> None:
        timer = self.bodies["tokenkey-cn-postgres-backup.timer"]
        self.assertIn("OnCalendar=*-*-* 00/2:30:00 UTC", timer)
        self.assertIn("Persistent=true", timer)

    def test_units_order_after_credential_refresh(self) -> None:
        """S3 repo credentials are short-lived and refreshed every 15 minutes.

        Dropping this ordering lets a backup start against expired credentials and
        fail for a reason unrelated to backup health. The pre-existing host unit
        carried it.
        """
        for name, body in self.bodies.items():
            if name.endswith(".service"):
                with self.subTest(name):
                    self.assertIn("tokenkey-cn-backup-identity.service", body)

    def test_plan_includes_identity_refresh_source(self) -> None:
        plan = installer.plan()
        self.assertEqual(plan["identity"]["dst"], "/opt/tokenkey/cn-backup-identity.py")
        self.assertEqual(plan["identity"]["src"], str(_HERE / "cn_backup_identity.py"))
        self.assertEqual(plan["target"]["dst"], "/opt/tokenkey/prod-target.json")

    def test_apply_requires_root(self) -> None:
        import os

        if os.geteuid() == 0:
            self.skipTest("test must not run as root")
        with self.assertRaises(installer.InstallError):
            installer.apply()

    def test_plan_is_read_only(self) -> None:
        body = (_HERE / "install_cn_postgres_backup.py").read_text(encoding="utf-8")
        plan_src = body[body.index("def plan()"):body.index("def apply()")]
        for forbidden in ("write_text", "systemctl", "copyfile", "chmod", "mkdir"):
            self.assertNotIn(forbidden, plan_src, msg=f"plan() must not {forbidden}")


if __name__ == "__main__":
    unittest.main()
