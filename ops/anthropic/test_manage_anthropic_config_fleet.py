"""Release checks must query the live fleet and fail on incomplete evidence."""
from __future__ import annotations

import argparse
import contextlib
import copy
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

SPEC = importlib.util.spec_from_file_location(
    "anthropic_fleet", pathlib.Path(__file__).with_name("manage-anthropic-config.py"))
mgr = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(mgr)


class FleetChecksTest(unittest.TestCase):
    def setUp(self):
        self.ids = mgr._EDGE_ROUTING.live_deployable_edge_ids(mgr.REPO_ROOT)
        self.edge = {"deployable": True, "oauth_accounts": [], "tiers": [],
                     "operator_user_exists": True, "operator_user_balance": 9999999}
        self.prod = {"anthropic_stubs": [], "tiers": []}
        self.snapshot = {"version": mgr.SNAPSHOT_VERSION,
                         "edges": {eid: copy.deepcopy(self.edge) for eid in self.ids},
                         "prod": self.prod}

    def snapshot_run(self, capture=None):
        with tempfile.TemporaryDirectory() as td, contextlib.ExitStack() as stack:
            path = pathlib.Path(td) / "snapshot.json"
            stack.enter_context(mock.patch.object(mgr._EDGE_ROUTING, "load_lightsail_targets",
                                                  side_effect=AssertionError("standby enumeration")))
            resolver = stack.enter_context(mock.patch.object(
                mgr._EDGE_SSM, "resolve_edge_execution_identity",
                side_effect=lambda root, eid: SimpleNamespace(
                    region="eu-west-2", instance_id=f"mi-{eid}", routing="hetzner")))
            stack.enter_context(mock.patch.object(mgr, "_capture_edge_bundle",
                                                  side_effect=capture or (lambda *a, **k: self.edge)))
            stack.enter_context(mock.patch.object(mgr, "_resolve_prod_target",
                                                  return_value=("eu-west-2", "mi-prod", {})))
            stack.enter_context(mock.patch.object(mgr, "_capture_prod_bundle", return_value=self.prod))
            rc = mgr.cmd_snapshot(argparse.Namespace(out=str(path), skip_prod=False,
                                                     allow_planned=False, parallel_edges=2))
            return rc, json.loads(path.read_text()), resolver.call_args_list

    def test_snapshot_queries_every_live_edge_without_reading_standby(self):
        rc, snap, calls = self.snapshot_run()
        self.assertEqual(rc, 0)
        self.assertEqual(sorted(c.args[1] for c in calls), self.ids)
        self.assertEqual(sorted(snap["edges"]), self.ids)
        self.assertEqual(snap["coverage"]["checked"], [f"edge:{e}" for e in self.ids] + ["prod"])
        self.assertEqual(snap["coverage"]["failed"], [])

    def test_snapshot_records_failed_node_and_collects_remaining_nodes(self):
        def capture(eid, *args, **kwargs):
            if eid == self.ids[0]:
                raise ValueError("malformed response")
            return self.edge
        rc, snap, calls = self.snapshot_run(capture)
        self.assertEqual(rc, 2)
        self.assertEqual(len(calls), len(self.ids))
        self.assertEqual(snap["coverage"]["failed"], [f"edge:{self.ids[0]}"])
        self.assertEqual(snap["coverage"]["checked"], [f"edge:{e}" for e in self.ids[1:]] + ["prod"])

    def check_run(self, snapshot, *, guard_failure=None, live_errors=None):
        with tempfile.TemporaryDirectory() as td, contextlib.ExitStack() as stack:
            path = pathlib.Path(td) / "snapshot.json"
            if snapshot is not None:
                path.write_text(json.dumps(snapshot))
            def guards(ids, *a, **kw):
                return {"guards": [{"exit_code": 2 if e == guard_failure else 0} for e in ids],
                        "edges_in_scope": ids, "any_violation": False}
            guard = stack.enter_context(mock.patch.object(mgr, "_run_check_guards", side_effect=guards))
            for name in ("_balance_violations_from_snapshot", "_tier_table_drift_from_snapshot",
                         "_run_balance_checks_live", "_run_tier_table_checks_live"):
                stack.enter_context(mock.patch.object(mgr, name, return_value=[]))
            stack.enter_context(mock.patch.object(mgr, "_run_live_node_checks",
                                                  return_value=(live_errors or [], [])))
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                rc = mgr.cmd_check(argparse.Namespace(snapshot=str(path) if snapshot is not None else None,
                    allow_planned=False, parallel_edges=1, legacy_guard=False, json=True))
            return rc, json.loads(out.getvalue()), guard.call_args.args[0]

    def test_complete_snapshot_and_live_check_cover_same_nodes(self):
        for snap in (self.snapshot, None):
            with self.subTest(snapshot=snap is not None):
                rc, report, queried = self.check_run(snap)
                self.assertEqual(rc, 0)
                self.assertEqual(queried, self.ids)
                self.assertEqual(report["coverage"]["checked"], [f"edge:{e}" for e in self.ids] + ["prod"])

    def test_missing_skipped_and_errored_snapshot_nodes_never_succeed(self):
        for bad in (None, {"deployable": False, "skipped_reason": "planned"},
                    {"deployable": True, "error": "SSM failed"}, {}):
            with self.subTest(bad=bad):
                snap = copy.deepcopy(self.snapshot)
                if bad is None:
                    del snap["edges"][self.ids[0]]
                else:
                    snap["edges"][self.ids[0]] = bad
                # An old/misleading receipt cannot certify its own completeness.
                snap["coverage"] = {"expected": [], "checked": [], "missing": [], "failed": []}
                rc, report, queried = self.check_run(snap)
                self.assertEqual(rc, 2)
                self.assertTrue(report["any_violation"])
                self.assertEqual(queried, self.ids[1:])
                field = "missing" if bad is None else "failed"
                self.assertEqual(report["coverage"][field], [f"edge:{self.ids[0]}"])

    def test_all_skipped_edges_reproduces_release_false_green(self):
        snap = copy.deepcopy(self.snapshot)
        snap["edges"] = {e: {"deployable": False, "skipped_reason": "planned"} for e in self.ids}
        rc, report, queried = self.check_run(snap)
        self.assertEqual(rc, 2)
        self.assertEqual(queried, [])
        self.assertEqual(report["coverage"]["failed"], [f"edge:{e}" for e in self.ids])

    def test_snapshot_standby_node_is_never_queried(self):
        self.snapshot["edges"]["standby-only"] = self.edge
        rc, _, queried = self.check_run(self.snapshot)
        self.assertEqual(rc, 0)
        self.assertEqual(queried, self.ids)

    def test_guard_or_live_read_failure_removes_node_from_checked(self):
        rc, report, _ = self.check_run(None, guard_failure=self.ids[0],
                                      live_errors=[{"node": "prod", "status": "error"}])
        self.assertEqual(rc, 2)
        self.assertEqual(report["coverage"]["failed"], [f"edge:{self.ids[0]}", "prod"])
        self.assertEqual(report["coverage"]["checked"], [f"edge:{e}" for e in self.ids[1:]])

    def test_missing_prod_snapshot_is_not_a_complete_fleet_check(self):
        del self.snapshot["prod"]
        rc, report, _ = self.check_run(self.snapshot)
        self.assertEqual(rc, 2)
        self.assertEqual(report["coverage"]["missing"], ["prod"])

    def test_live_balance_and_tier_reads_continue_after_ssm_failure(self):
        identity = SimpleNamespace(region="eu-west-2", instance_id="mi-test")
        with mock.patch.object(mgr._EDGE_SSM, "resolve_edge_execution_identity", return_value=identity), \
             mock.patch.object(mgr, "_read_operator_balance", side_effect=[
                 SystemExit(2), {"operator_user_exists": True, "operator_user_balance": 9999999}]) as read:
            items = mgr._run_balance_checks_live(self.ids[:2], mgr._load_operator_balance_policy())
        self.assertEqual(read.call_count, 2)
        self.assertEqual([(i["edge_id"], i["status"]) for i in items], [(self.ids[0], "error")])
        with mock.patch.object(mgr._EDGE_SSM, "resolve_edge_execution_identity", return_value=identity), \
             mock.patch.object(mgr, "_resolve_prod_target", return_value=("eu-west-2", "mi-prod", {})), \
             mock.patch.object(mgr, "_read_tiers", side_effect=[SystemExit(2), [], []]) as read:
            items = mgr._run_tier_table_checks_live(self.ids[:2], {})
        self.assertEqual(read.call_count, 3)
        self.assertEqual([(i["node"], i["status"]) for i in items], [(f"edge:{self.ids[0]}", "error")])

    def test_live_readers_reject_malformed_json(self):
        for raw in ("", "{truncated", "null", "{}"):
            for reader in (mgr._read_tiers, mgr._read_operator_balance):
                with self.subTest(raw=raw, reader=reader.__name__), mock.patch.object(
                        mgr, "ssm_run_sql", return_value=(raw, "cid")), self.assertRaises(ValueError):
                    reader("eu-west-2", "mi-test", "test")

    def test_capture_rejects_invalid_json_or_incomplete_bundle(self):
        for raw in ("", "{truncated", "[]", "{}", '{"tiers": null}'):
            for edge in (True, False):
                with self.subTest(raw=raw, edge=edge), self.assertRaises(ValueError):
                    mgr._parse_capture_bundle(raw, edge=edge)

    def test_guard_distinguishes_empty_accounts_from_invalid_output(self):
        identity = SimpleNamespace(region="eu-west-2", instance_id="mi-test")
        for raw in ("[]", "", "{truncated", "{}", "[null]", "[{}]"):
            with self.subTest(raw=raw), mock.patch.object(mgr._EDGE_SSM,
                    "resolve_edge_execution_identity", return_value=identity), mock.patch.object(
                    mgr, "ssm_run_sql", return_value=(raw, "command-test")):
                result = mgr._run_guard_batch_for_edge(self.ids[0], False)
            if raw == "[]":
                self.assertEqual(result["exit_code"], 0)
                self.assertEqual(result["report"]["items"][0]["status"], "not_applicable")
                self.assertEqual(result["report"]["summary"]["ok_count"], 0)
            else:
                self.assertEqual(result["exit_code"], 2)
                self.assertIsNone(result["report"])


if __name__ == "__main__":
    unittest.main()
