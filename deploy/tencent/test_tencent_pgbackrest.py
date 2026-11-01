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
            {"repo": 1, "label": "new", "status": "ok"},
            {"repo": 2, "label": "new", "status": "ok"},
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
