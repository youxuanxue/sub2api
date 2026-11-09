import json
import sys
from pathlib import Path
from unittest import TestCase
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent))
import tencent_pgbackrest as runner


# Archiving succeeded after the last failure -- the cumulative counter stays
# non-zero, exactly like cn-prod after its 2026-11-02 incident.
_ARCHIVER_OK = json.dumps({
    "last_archived_wal": "000000010000001600000063",
    "last_archived_time": "2026-11-02T16:40:29+08:00",
    "last_failed_wal": "000000010000001600000031",
    "last_failed_time": "2026-11-02T14:32:51+08:00",
    "archived_count": 5856, "failed_count": 15,
    "stats_reset": "2026-10-22T21:47:26+08:00",
    "archiving_now": True,
})
# The archive_command is failing right now.
_ARCHIVER_FAILING = json.dumps({
    "last_archived_wal": "000000010000001600000030",
    "last_archived_time": "2026-11-02T14:25:51+08:00",
    "last_failed_wal": "000000010000001600000031",
    "last_failed_time": "2026-11-02T14:32:51+08:00",
    "archived_count": 5841, "failed_count": 15,
    "stats_reset": "2026-10-22T21:47:26+08:00",
    "archiving_now": False,
})


class _Lock:
    def close(self):
        pass


class TencentPgBackrestTests(TestCase):
    def setUp(self):
        # Most tests stub run(); pin the resolved container so they never shell out.
        runner._CONTAINER = "tk-archive-standby"
        self.addCleanup(setattr, runner, "_CONTAINER", None)

    def _ps(self, names):
        return patch.object(runner.subprocess, "run", return_value=type(
            "R", (), {"stdout": "\n".join(names), "returncode": 0})())

    def test_container_resolves_the_layout_exception_name_on_the_app_host(self):
        runner._CONTAINER = None
        with self._ps(["tokenkey-blue", "tk-archive-standby", "tokenkey-caddy"]):
            self.assertEqual(runner.container(), "tk-archive-standby")

    def test_container_resolves_tokenkey_postgres_on_the_data_machine(self):
        runner._CONTAINER = None
        with self._ps(["tokenkey-postgres", "tokenkey-redis"]):
            self.assertEqual(runner.container(), "tokenkey-postgres")

    def test_container_fails_closed_when_none_or_both_run(self):
        for names in ([], ["tokenkey-postgres", "tk-archive-standby"]):
            runner._CONTAINER = None
            with self.subTest(names=names), self._ps(names):
                with self.assertRaises(runner.Err):
                    runner.container()

    def test_candidates_match_the_shared_resolver_owner(self):
        # The host copy runs without the repo tree, so it carries its own list;
        # this keeps it from drifting from ops/lib/resolve_app_container.py.
        import importlib.util
        owner = Path(__file__).resolve().parents[2] / "ops" / "lib" / "resolve_app_container.py"
        spec = importlib.util.spec_from_file_location("tk_resolve_owner", owner)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(runner.POSTGRES_CANDIDATES, module.POSTGRES_CANDIDATES)

    def test_dual_repository_backup_runs_each_repo_and_records_both_labels(self):
        info = {
            "name": "tokenkey",
            "status": {"code": 0},
            "backup": [{"label": "old", "type": "full", "timestamp": {"stop": 1}}],
        }
        fresh = {
            "label": "new",
            "type": "incr",
            "timestamp": {"stop": 2},
            "archive": {"start": "000000010000000000000001"},
        }
        calls = []
        backed_up = False

        def fake_backrest(*args, **kwargs):
            nonlocal backed_up
            calls.append(args)
            if args[-1] == "info":
                repo = args[0].split("=", 1)[1]
                value = dict(info, backup=[dict(item) for item in info["backup"]])
                if backed_up:
                    value["backup"].append(dict(fresh))
                return json.dumps([value])
            if "backup" in args:
                backed_up = True
                return ""
            raise AssertionError(args)

        captured = {}
        with patch.object(runner, "exclusive", return_value=_Lock()), \
             patch.object(runner, "configured_repositories", return_value=(1, 2)), \
             patch.object(runner, "backup_kind", return_value="incr"), \
             patch.object(runner, "backrest", side_effect=fake_backrest), \
             patch.object(runner, "atomic_json", side_effect=lambda path, value: captured.update(value)), \
             patch.object(runner.time, "time", return_value=100):
            runner.cmd_backup()

        self.assertIn(("--repo=1", "--type=incr", "backup"), calls)
        self.assertIn(("--repo=2", "--type=incr", "backup"), calls)
        self.assertTrue(captured["cos_repo2"])
        self.assertEqual(captured["repositories"], [
            {"repo": 1, "label": "new", "kind": "incr", "status": "ok"},
            {"repo": 2, "label": "new", "kind": "incr", "status": "ok"},
        ])

    def test_cos_only_backup_runs_repo2_alone(self):
        # 2026-11-08: AWS repo1 retired. The config carries repo2 only, and the
        # backup must neither invent a --repo=1 call nor need one for the receipt.
        info = {"name": "tokenkey", "status": {"code": 0}, "backup": []}
        calls = []
        backed_up = False

        def fake_backrest(*args, **kwargs):
            nonlocal backed_up
            calls.append(args)
            if args[-1] == "info":
                backups = []
                if backed_up:
                    backups.append({"label": "new", "type": "full", "timestamp": {"stop": 2}})
                return json.dumps([dict(info, backup=backups)])
            if "backup" in args:
                backed_up = True
            return ""

        captured = {}
        with patch.object(runner, "exclusive", return_value=_Lock()), \
             patch.object(runner, "configured_repositories", return_value=(2,)), \
             patch.object(runner, "backup_kind", return_value="full"), \
             patch.object(runner, "backrest", side_effect=fake_backrest), \
             patch.object(runner, "atomic_json", side_effect=lambda path, value: captured.update(value)), \
             patch.object(runner.time, "time", return_value=100):
            runner.cmd_backup()

        self.assertEqual([c for c in calls if "backup" in c], [("--repo=2", "--type=full", "backup")])
        self.assertFalse(any(c and c[0] == "--repo=1" for c in calls))
        self.assertEqual(captured["label"], "new")
        self.assertEqual(captured["aws_repo1"], "retired")
        self.assertEqual(captured["live_repo"], "prod/postgres")
        self.assertEqual(captured["repositories"],
                         [{"repo": 2, "label": "new", "kind": "full", "status": "ok"}])

    def test_config_without_cos_never_falls_back_to_aws(self):
        # An AWS-only (or empty) config must not be backed up "because it is
        # what is there": COS is the only sanctioned repository.
        with patch.object(runner, "configured_repositories", return_value=(1,)):
            with self.assertRaises(runner.Err):
                runner.repository_numbers()

    def test_new_repository_gets_full_before_existing_repo_incr(self):
        repo_infos = {
            1: {"name": "tokenkey", "status": {"code": 0},
                "backup": [{"label": "old", "type": "full", "timestamp": {"stop": 1}}]},
            2: {"name": "tokenkey", "status": {"code": 0}, "backup": []},
        }
        calls = []
        backed_up = set()

        def fake_backrest(*args, **kwargs):
            calls.append(args)
            if args[-1] == "info":
                repo = int(args[0].split("=", 1)[1])
                backups = list(repo_infos[repo]["backup"])
                if repo in backed_up:
                    backups.append({"label": f"new-{repo}",
                                    "type": "incr" if repo == 1 else "full",
                                    "timestamp": {"stop": 2}})
                return json.dumps([dict(repo_infos[repo], backup=backups)])
            if "backup" in args:
                backed_up.add(int(args[0].split("=", 1)[1]))
                return ""
            raise AssertionError(args)

        with patch.object(runner, "exclusive", return_value=_Lock()), \
             patch.object(runner, "configured_repositories", return_value=(1, 2)), \
             patch.object(runner, "backrest", side_effect=fake_backrest), \
             patch.object(runner, "atomic_json"), \
             patch.object(runner.time, "time", return_value=100):
            runner.cmd_backup()

        self.assertIn(("--repo=1", "--type=incr", "backup"), calls)
        self.assertIn(("--repo=2", "--type=full", "backup"), calls)

    def test_check_validates_all_configured_repositories_with_one_command(self):
        info = {
            "name": "tokenkey",
            "status": {"code": 0},
            "backup": [],
        }
        calls = []

        def fake_backrest(*args, **kwargs):
            calls.append(args)
            if args[-1] == "info":
                return json.dumps([info])
            if args == ("check",):
                return ""
            raise AssertionError(args)

        with patch.object(runner, "exclusive", return_value=_Lock()), \
             patch.object(runner, "configured_repositories", return_value=(1, 2)), \
             patch.object(runner, "backrest", side_effect=fake_backrest), \
             patch.object(runner, "run", return_value=_ARCHIVER_OK), \
             patch.object(runner, "atomic_json"):
            runner.cmd_check()

        self.assertIn(("check",), calls)
        self.assertNotIn(("--repo=1", "check"), calls)
        self.assertNotIn(("--repo=2", "check"), calls)

    def _run_check(self, repo_infos, archiving_now=True):
        """Drive cmd_check against per-repository info, returning the receipt."""
        written = {}

        def fake_backrest(*args, **kwargs):
            if args[-1] == "info":
                repo = int(args[0].split("=", 1)[1])
                return json.dumps([repo_infos[repo]])
            if args == ("check",):
                return ""
            raise AssertionError(args)

        archiver = _ARCHIVER_OK if archiving_now else _ARCHIVER_FAILING
        with patch.object(runner, "exclusive", return_value=_Lock()), \
             patch.object(runner, "configured_repositories", return_value=tuple(sorted(repo_infos))), \
             patch.object(runner, "backrest", side_effect=fake_backrest), \
             patch.object(runner, "run", return_value=archiver), \
             patch.object(runner, "atomic_json",
                          side_effect=lambda path, value: written.update(value)):
            try:
                runner.cmd_check()
            except SystemExit as exit_code:
                return written, exit_code.code
        return written, 0

    def test_new_repository_without_backups_is_bootstrapping_not_failed(self):
        # The window this covers: credentials land, the config grows a repo2,
        # and the next check runs before the first full can exist. pgBackRest
        # necessarily reports code 2 there, and exiting non-zero for up to two
        # hours made a guaranteed state look like an outage.
        receipt, code = self._run_check({
            1: {"name": "tokenkey", "status": {"code": 2, "message": "no valid backups"},
                "backup": []},
            2: {"name": "tokenkey", "status": {"code": 0},
                "backup": [{"label": "old", "type": "full", "timestamp": {"stop": 1}}]},
        })

        self.assertEqual(code, 0)
        self.assertEqual(receipt["status"], "degraded")
        self.assertEqual(receipt["reasons"], ["repository_bootstrapping"])
        self.assertEqual(receipt["repository_bootstrapping"]["repositories"], [1])
        self.assertEqual(receipt["repository_bootstrapping"]["status"], "ok")
        self.assertEqual(
            receipt["repositories"],
            [{"repo": 1, "status": "bootstrapping"}, {"repo": 2, "status": "ok"}],
        )

    def test_repository_with_backups_reporting_invalid_still_fails(self):
        # Code 2 with backups present is a repository that LOST its chain, which
        # is the opposite of a bootstrap and must stay a failure.
        receipt, code = self._run_check({
            1: {"name": "tokenkey", "status": {"code": 2, "message": "no valid backups"},
                "backup": [{"label": "gone", "type": "full", "timestamp": {"stop": 1}}]},
            2: {"name": "tokenkey", "status": {"code": 0}, "backup": []},
        })

        self.assertEqual(code, 2)
        self.assertEqual(receipt["status"], "degraded")
        self.assertEqual(receipt["reasons"], ["repository_failed"])
        self.assertNotIn("repository_bootstrapping", receipt["reasons"])
        self.assertEqual(receipt["repository_failed"]["repositories"], [1])
        self.assertEqual(receipt["repositories"][0], {"repo": 1, "status": "failed"})

    def test_primary_repository_never_gets_the_bootstrap_exemption(self):
        # repo1 is the primary recovery chain. "No valid backups" there is a
        # disaster, never an initialisation.
        receipt, code = self._run_check({
            2: {"name": "tokenkey", "status": {"code": 2, "message": "no valid backups"},
                "backup": []},
        })

        self.assertEqual(code, 2)
        self.assertEqual(receipt["reasons"], ["repository_failed"])
        self.assertNotIn("repository_bootstrapping", receipt["reasons"])
        self.assertEqual(receipt["repository_failed"]["repositories"], [2])
        self.assertEqual(receipt["repositories"], [{"repo": 2, "status": "failed"}])

    def test_bootstrap_does_not_excuse_wal_archive_failures(self):
        receipt, code = self._run_check({
            1: {"name": "tokenkey", "status": {"code": 2}, "backup": []},
            2: {"name": "tokenkey", "status": {"code": 0}, "backup": []},
        }, archiving_now=False)

        self.assertEqual(code, 2)
        self.assertEqual(receipt["status"], "degraded")
        # Bootstrap never excuses a failing archive_command.
        self.assertEqual(receipt["reasons"], ["wal_archive_failing"])
        self.assertNotIn("repository_bootstrapping", receipt["reasons"])

    def test_cos_only_check_is_ok_with_a_single_repository(self):
        receipt, code = self._run_check({
            2: {"name": "tokenkey", "status": {"code": 0},
                "backup": [{"label": "a", "type": "full", "timestamp": {"stop": 1}}]},
        })
        self.assertEqual(code, 0)
        self.assertEqual(receipt["status"], "ok")
        self.assertNotIn("reasons", receipt)
        self.assertEqual(receipt["repositories"], [{"repo": 2, "status": "ok"}])
        self.assertEqual(list(receipt["stanza_status"]), ["2"])
        self.assertEqual(receipt["backup_count"], 1)
        self.assertEqual(receipt["aws_repo1"], "retired")

    def test_cos_only_check_with_no_backups_is_a_fault(self):
        # The only repository losing (or never having) its backups is a
        # disaster, never a bootstrap.
        receipt, code = self._run_check({
            2: {"name": "tokenkey", "status": {"code": 2, "message": "no valid backups"},
                "backup": []},
        })
        self.assertEqual(code, 2)
        self.assertEqual(receipt["status"], "degraded")
        self.assertEqual(receipt["reasons"], ["repository_failed"])
        self.assertEqual(receipt["repository_failed"]["repositories"], [2])

    def test_check_without_cos_configured_writes_a_hard_fault_receipt(self):
        written = {}
        with patch.object(runner, "exclusive", return_value=_Lock()), \
             patch.object(runner, "configured_repositories", return_value=(1,)), \
             patch.object(runner, "backrest", side_effect=AssertionError("must not run")), \
             patch.object(runner, "atomic_json",
                          side_effect=lambda path, value: written.update(value)):
            with self.assertRaises(runner.Err):
                runner.cmd_check()
        self.assertEqual(written["status"], "degraded")
        self.assertEqual(written["reasons"], ["primary_repository_missing"])
        self.assertIn("primary_repository_missing", written)

    def test_check_failure_still_writes_a_diagnosable_receipt(self):
        # A missing receipt is indistinguishable from a dead timer to the
        # freshness gate, so an exploding check must leave evidence behind.
        # This is the shape of the real 2026-11-02 cn-prod failure.
        written = {}
        error = (
            "docker exit 55: P00 INFO: check command begin 2.58.0: --repo1-s3-key=<redacted>\n"
            "P00  ERROR: [055]: unable to load info file "
            "'/prod/postgres/archive/tokenkey/archive.info'\n"
            "      HINT: has a stanza-create been performed?"
        )

        def fake_backrest(*args, **kwargs):
            if args == ("check",):
                raise runner.Err(error)
            raise AssertionError(args)

        with patch.object(runner, "exclusive", return_value=_Lock()), \
             patch.object(runner, "configured_repositories", return_value=(1, 2)), \
             patch.object(runner, "backrest", side_effect=fake_backrest), \
             patch.object(runner, "atomic_json",
                          side_effect=lambda path, value: written.update(value)):
            with self.assertRaises(runner.Err):
                runner.cmd_check()

        self.assertEqual(written["status"], "degraded")
        self.assertEqual(written["reasons"], ["check_command_failed"])
        detail = written["check_command_failed"]["detail"]
        self.assertIn("[055]", detail)
        self.assertIn("stanza-create", detail)
        self.assertNotIn("--repo1-s3-key", detail)
        self.assertTrue(written["cos_repo2"])
        self.assertEqual([r["status"] for r in written["repositories"]], ["unknown", "unknown"])

    def test_check_failure_receipt_is_not_advisory(self):
        # check_command_failed must NOT be in host_https_watch's evidence map:
        # a broken check is a hard fault, unlike a bootstrapping repository.
        import pathlib as _p
        source = (_p.Path(runner.__file__).resolve().parents[2]
                  / "ops" / "observability" / "host_https_watch.py").read_text()
        self.assertIn('"repository_bootstrapping": "repository_bootstrapping",', source)
        self.assertNotIn('"check_command_failed":', source)

    def test_recovered_host_is_ok_despite_a_nonzero_cumulative_failed_count(self):
        # The regression this fixes: pg_stat_archiver counters are cumulative
        # since stats_reset, so `failed_count == 0` asks "has this host ever
        # failed to archive". cn-prod answered no forever after a 3-minute
        # incident left failed_count at 15 -- every repository read code 0,
        # archiving had recovered, and the receipt still said degraded on every
        # run, with no reason attached and no way to clear short of resetting
        # Postgres statistics.
        receipt, code = self._run_check({
            2: {"name": "tokenkey", "status": {"code": 0},
                "backup": [{"label": "a", "type": "full", "timestamp": {"stop": 1}}]},
        })

        self.assertEqual(code, 0)
        self.assertEqual(receipt["status"], "ok")
        self.assertNotIn("reasons", receipt)
        self.assertEqual(receipt["archiver"]["failed_count"], 15,
                         "the cumulative counter stays in the receipt as evidence")

    def test_a_degraded_receipt_always_names_a_reason(self):
        # A bare "degraded" tells the watcher something is wrong but not what,
        # which is barely better than a missing receipt. Guard the invariant for
        # every non-ok path rather than trusting each new branch to remember.
        for label, infos, archiving in (
            ("repository failed", {2: {"name": "tokenkey", "status": {"code": 1}, "backup": []}}, True),
            ("archive failing", {2: {"name": "tokenkey", "status": {"code": 0},
                                     "backup": [{"label": "a", "type": "full", "timestamp": {"stop": 1}}]}}, False),
        ):
            with self.subTest(label):
                receipt, code = self._run_check(infos, archiving_now=archiving)
                self.assertEqual(receipt["status"], "degraded")
                self.assertTrue(receipt.get("reasons"), "degraded must name a reason")
                self.assertEqual(code, 2)
                for reason in receipt["reasons"]:
                    self.assertIn(reason, receipt, "each reason needs its evidence object")

    def test_archiver_healthy_compares_instants_not_strings(self):
        # Equal-looking timestamps can carry different UTC offsets, where
        # lexical order inverts the real one.
        self.assertFalse(runner.archiver_healthy({
            "last_archived_time": "2026-11-02T00:30:00+08:00",   # 2026-11-01T16:30Z
            "last_failed_time": "2026-11-01T20:00:00+00:00",     # later in reality
        }))
        self.assertTrue(runner.archiver_healthy({
            "last_archived_time": "2026-11-02 16:40:29+08",      # psql spacing
            "last_failed_time": "2026-11-02 14:32:51+08",
        }))
        # A host that has never failed is healthy once it has archived.
        self.assertTrue(runner.archiver_healthy(
            {"last_archived_time": "2026-11-02T16:00:00+08:00", "last_failed_time": None}))
        self.assertFalse(runner.archiver_healthy(
            {"last_archived_time": None, "last_failed_time": None}))
        # Unparseable input must not pass as healthy.
        self.assertFalse(runner.archiver_healthy(
            {"last_archived_time": "garbage", "last_failed_time": "2026-11-02T14:32:51+08:00"}))
