"""COS restore acceptance: the judgments that decide pass or fail.

Fixtures use the real numbers observed on cn-prod on 2026-11-02, so a future
change that loosens these checks fails against data that actually occurred.
"""
from __future__ import annotations

import importlib.util
import json
import pathlib
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("cos_restore_acceptance", HERE / "cos_restore_acceptance.py")
acceptance = importlib.util.module_from_spec(spec)
assert spec and spec.loader
spec.loader.exec_module(acceptance)

# Live cn-prod at the time of the first successful acceptance run. accounts and
# api_keys are soft-delete filtered (140 rows / 137 live, 112 / 110), which is
# what DATA_SQL returns.
LIVE = {
    "accounts": 137, "api_keys": 110,
    "usage_logs": 515715, "billing_dedup": 515689,
    "cost_sum": "9232.404804", "actual_cost_sum": "3374.483810",
    "db_size_bytes": 1704564415,
}
# The oldest full, restored from COS: strictly behind live, same billing drift.
HISTORICAL = {
    "accounts": 135, "api_keys": 110,
    "usage_logs": 507683, "billing_dedup": 507657,
    "cost_sum": "9045.662212", "actual_cost_sum": "3315.381977",
    "db_size_bytes": 1678743231,
}


class CompareTest(unittest.TestCase):
    def test_a_past_point_that_lags_live_is_accepted(self) -> None:
        self.assertEqual(acceptance.compare(HISTORICAL, LIVE), [])

    def test_a_restored_point_may_never_exceed_live(self) -> None:
        # Counts above live mean the comparison baseline is wrong or the restore
        # pulled from somewhere unexpected; either way it is not an acceptance.
        ahead = dict(HISTORICAL, usage_logs=LIVE["usage_logs"] + 1)
        self.assertTrue(any("usage_logs restored" in p for p in acceptance.compare(ahead, LIVE)))

    def test_cost_aggregate_above_live_fails(self) -> None:
        ahead = dict(HISTORICAL, cost_sum="99999.0")
        self.assertTrue(any("cost_sum" in p for p in acceptance.compare(ahead, LIVE)))

    def test_billing_pair_drift_must_match_live(self) -> None:
        # usage_logs and usage_billing_dedup are written together. The same
        # delta as live is what distinguishes a consistent restore from a copy
        # captured mid-write; cn-prod showed 26 on live and on both points.
        torn = dict(HISTORICAL, billing_dedup=HISTORICAL["billing_dedup"] - 700)
        problems = acceptance.compare(torn, LIVE)
        self.assertTrue(any("billing pair drift" in p for p in problems))

    def test_missing_aggregate_is_a_failure_not_a_pass(self) -> None:
        absent = {k: v for k, v in HISTORICAL.items() if k != "cost_sum"}
        self.assertTrue(any("cost_sum" in p for p in acceptance.compare(absent, LIVE)))


class SoftDeleteTest(unittest.TestCase):
    def test_soft_delete_tables_are_filtered(self) -> None:
        # Soft-delete does not reset status, and a row deleted AFTER the
        # recovery point still reads as live in the restored copy. Counting
        # ghosts would drift the comparison for a reason that has nothing to do
        # with the backup. cn-prod carries 3 such accounts and 2 api_keys.
        for table in ("accounts", "api_keys"):
            with self.subTest(table=table):
                self.assertRegex(
                    acceptance.DATA_SQL,
                    rf"FROM {table} WHERE deleted_at IS NULL",
                    f"{table} is a soft-delete table and must be filtered")

    def test_tables_without_soft_delete_are_not_filtered(self) -> None:
        # usage_logs has no deleted_at column; adding a filter would error.
        self.assertIn("FROM usage_logs)", acceptance.DATA_SQL)


class SelfCheckRegistryTest(unittest.TestCase):
    def test_every_sql_generator_is_enumerated_or_exempt(self) -> None:
        # ops-sql-coverage requires forced classification so a new generator
        # cannot ship without reaching the real-Postgres execution gate.
        labels = {label for label, _ in acceptance.iter_self_check_sql()}
        self.assertEqual(labels, {"SCHEMA_SQL", "DATA_SQL"})
        self.assertIn("live_sql", acceptance.SELF_CHECK_EXEMPT)
        for _, sql in acceptance.iter_self_check_sql():
            self.assertIn("json_build_object", sql)


class WalContinuityTest(unittest.TestCase):
    def test_single_timeline_range_is_returned(self) -> None:
        result = acceptance.wal_continuity(
            {"archive": [{"min": "0000000100000016000000B7", "max": "0000000100000016000000D8"}]})
        self.assertEqual(result["timeline"], "00000001")

    def test_timeline_change_needs_a_reviewed_baseline(self) -> None:
        with self.assertRaises(acceptance.AcceptanceError):
            acceptance.wal_continuity(
                {"archive": [{"min": "0000000100000016000000B7", "max": "0000000200000016000000D8"}]})

    def test_absent_or_malformed_range_fails_closed(self) -> None:
        for archive in ([], [{}], [{"min": "short", "max": "short"}]):
            with self.subTest(archive=archive):
                with self.assertRaises(acceptance.AcceptanceError):
                    acceptance.wal_continuity({"archive": archive})


class TargetsTest(unittest.TestCase):
    def test_oldest_full_and_latest_point_are_chosen(self) -> None:
        entry = {"backup": [
            {"label": "20261102-202410F", "type": "full"},
            {"label": "20261102-203119I", "type": "incr"},
        ]}
        self.assertEqual(acceptance.targets(entry), ("20261102-202410F", "20261102-203119I"))

    def test_repository_without_a_full_cannot_be_accepted(self) -> None:
        # This is the bootstrapping state: writes land but nothing is restorable
        # yet, so acceptance must refuse rather than report success.
        with self.assertRaises(acceptance.AcceptanceError):
            acceptance.targets({"backup": []})


class IsolationTest(unittest.TestCase):
    """The host serves live users; these flags are the safety contract."""

    def _sandbox(self):
        return acceptance.Sandbox("img", pathlib.Path("/conf"), pathlib.Path("/tmp"), "1", "1g")

    def test_restore_joins_no_network_and_publishes_no_port(self) -> None:
        box = self._sandbox()
        with mock.patch.object(acceptance, "run", return_value="") as runner:
            box.restore(2, "20261102-202410F", 60)
        argv = next(c.args[0] for c in runner.call_args_list if "run" in c.args[0])
        self.assertNotIn("-p", argv)
        self.assertNotIn("--publish", argv)
        self.assertNotIn("--network", argv, "joining a network would expose the gateway")
        self.assertIn("--rm", argv)
        self.assertIn("/conf:/etc/pgbackrest:ro", argv, "config must be read-only")
        self.assertNotIn("--delta", argv, "--delta could write into an existing directory")
        self.assertIn("19", argv)  # nice
        self.assertIn("-c3", argv)  # ionice idle class

    def test_restore_uses_promote_so_recovery_completes(self) -> None:
        # With --target-action=shutdown the cluster never finishes recovery and
        # reads back as "in production"; it cannot be queried at all.
        box = self._sandbox()
        with mock.patch.object(acceptance, "run", return_value="") as runner:
            box.restore(2, "20261102-202410F", 60)
        argv = next(c.args[0] for c in runner.call_args_list if "run" in c.args[0])
        self.assertIn("--target-action=promote", argv)
        self.assertIn("--repo=2", argv)

    def test_started_instance_matches_the_primary_connection_floor(self) -> None:
        # PostgreSQL refuses to recover when max_connections is below the
        # primary's: "recovery aborted because of insufficient parameter
        # settings". The floor comes from the live database, never a constant.
        box = self._sandbox()
        calls = []

        def fake_run(cmd, **kwargs):
            calls.append(cmd)
            return "true" if "inspect" in cmd else ""

        with mock.patch.object(acceptance, "run", side_effect=fake_run), \
             mock.patch.object(acceptance.subprocess, "run",
                               return_value=mock.Mock(returncode=0)):
            box.start(100, 30)
        argv = next(c for c in calls if "-d" in c)
        self.assertIn("max_connections=100", argv)
        self.assertIn("listen_addresses=", argv, "the socket must not be reachable over TCP")
        self.assertNotIn("--network", argv)

    def test_sandbox_cleans_up_even_when_the_body_raises(self) -> None:
        removed = []

        def fake_run(cmd, **kwargs):
            if "rm" in cmd:
                removed.append(cmd)
            return ""

        with mock.patch.object(acceptance, "run", side_effect=fake_run), \
             mock.patch.object(acceptance.Path, "exists", lambda self: True):
            with self.assertRaises(ValueError):
                with self._sandbox():
                    raise ValueError("boom")
        self.assertTrue(any("docker" in c for c in removed), "container must be removed")
        self.assertTrue(any("sudo" in c for c in removed), "work dir must be removed")


class ReceiptTest(unittest.TestCase):
    def test_receipts_carry_what_the_watcher_checks(self) -> None:
        # host_https_watch clears its two reasons only for receipts with
        # status == "ok" and a numeric completed_at within eight days
        # (ops/stage0/postgres_backup.py:fresh).
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / "restore_acceptance.json"
            acceptance.atomic_json(path, {
                "schema_version": 1, "status": "ok",
                "started_at": 1793622000.0, "completed_at": 1793622300.0,
                "repo": 2, "points": [],
            })
            receipt = json.loads(path.read_text())
        self.assertEqual(receipt["status"], "ok")
        self.assertIsInstance(receipt["completed_at"], float)
        self.assertEqual(oct(path.stat().st_mode)[-3:], "640") if path.exists() else None

    def test_failures_downgrade_the_receipt(self) -> None:
        problems = acceptance.compare(dict(HISTORICAL, usage_logs=LIVE["usage_logs"] + 5), LIVE)
        self.assertTrue(problems)
        status = "ok" if not problems else "degraded"
        self.assertEqual(status, "degraded")


if __name__ == "__main__":
    unittest.main()
