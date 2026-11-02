import json
import sys
from pathlib import Path
from unittest import TestCase
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent))
import tencent_pgbackrest as runner


class _Lock:
    def close(self):
        pass


class TencentPgBackrestTests(TestCase):
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
             patch.object(runner, "repo2_enabled", return_value=True), \
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

    def test_single_repository_backup_keeps_repo_one_command(self):
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

        with patch.object(runner, "exclusive", return_value=_Lock()), \
             patch.object(runner, "repo2_enabled", return_value=False), \
             patch.object(runner, "backup_kind", return_value="full"), \
             patch.object(runner, "backrest", side_effect=fake_backrest), \
             patch.object(runner, "atomic_json"), \
             patch.object(runner.time, "time", return_value=100):
            runner.cmd_backup()

        self.assertIn(("--repo=1", "--type=full", "backup"), calls)
        self.assertNotIn(("--repo=2", "--type=full", "backup"), calls)

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
             patch.object(runner, "repo2_enabled", return_value=True), \
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
             patch.object(runner, "repo2_enabled", return_value=True), \
             patch.object(runner, "backrest", side_effect=fake_backrest), \
             patch.object(runner, "run", return_value='{"last_archived_wal": "wal", "last_archived_time": null, "failed_count": 0}'), \
             patch.object(runner, "atomic_json"):
            runner.cmd_check()

        self.assertIn(("check",), calls)
        self.assertNotIn(("--repo=1", "check"), calls)
        self.assertNotIn(("--repo=2", "check"), calls)

    def _run_check(self, repo_infos, failed_count=0):
        """Drive cmd_check against per-repository info, returning the receipt."""
        written = {}

        def fake_backrest(*args, **kwargs):
            if args[-1] == "info":
                repo = int(args[0].split("=", 1)[1])
                return json.dumps([repo_infos[repo]])
            if args == ("check",):
                return ""
            raise AssertionError(args)

        archiver = ('{"last_archived_wal": "wal", "last_archived_time": null,'
                    f' "failed_count": {failed_count}}}')
        with patch.object(runner, "exclusive", return_value=_Lock()), \
             patch.object(runner, "repo2_enabled", return_value=True), \
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

    def test_repository_with_backups_reporting_invalid_still_fails(self):
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

    def test_primary_repository_never_gets_the_bootstrap_exemption(self):
        # repo1 is the primary recovery chain. "No valid backups" there is a
        # disaster, never an initialisation.
        receipt, code = self._run_check({
            1: {"name": "tokenkey", "status": {"code": 2, "message": "no valid backups"},
                "backup": []},
            2: {"name": "tokenkey", "status": {"code": 0}, "backup": []},
        })

        self.assertEqual(code, 2)
        self.assertNotIn("reasons", receipt)
        self.assertEqual(receipt["repositories"][0], {"repo": 1, "status": "failed"})

    def test_bootstrap_does_not_excuse_wal_archive_failures(self):
        receipt, code = self._run_check({
            1: {"name": "tokenkey", "status": {"code": 0}, "backup": []},
            2: {"name": "tokenkey", "status": {"code": 2}, "backup": []},
        }, failed_count=3)

        self.assertEqual(code, 2)
        self.assertEqual(receipt["status"], "degraded")
        self.assertNotIn("reasons", receipt)
