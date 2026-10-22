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
import pathlib
import unittest


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
