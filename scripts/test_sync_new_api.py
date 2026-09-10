"""Exercise dependency pin checks against real Git clones and worktrees."""
import pathlib
import shutil
import subprocess
import tempfile
import unittest


SOURCE = pathlib.Path(__file__).parent / "upstream" / "sync-new-api.sh"


class SyncNewAPITest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = pathlib.Path(self.temp.name)
        self.repo = root / "tokenkey"
        self.dependency = root / "new-api"
        self.source_repo = root / "source"
        script_dir = self.repo / "scripts" / "upstream"
        script_dir.mkdir(parents=True)
        shutil.copy2(SOURCE, script_dir / SOURCE.name)
        self.git("init", "-q", str(self.source_repo))
        for index in range(2):
            (self.source_repo / "version").write_text(str(index))
            self.git("-C", str(self.source_repo), "add", "version")
            self.git("-C", str(self.source_repo), "-c", "user.name=Test", "-c",
                     "user.email=test@example.invalid", "commit", "-qm", str(index))
        self.sha = self.git("-C", str(self.source_repo), "rev-parse", "HEAD").strip()
        self.previous = self.git("-C", str(self.source_repo), "rev-parse", "HEAD^").strip()
        self.pin = self.repo / ".new-api-ref"
        self.pin.write_text(self.sha + "\n")

    def git(self, *args):
        return subprocess.check_output(["git", *args], text=True, stderr=subprocess.PIPE)

    def sync(self, *args):
        return subprocess.run(["bash", str(self.repo / "scripts/upstream/sync-new-api.sh"),
                               *args], text=True, capture_output=True)

    def test_matching_worktree_accepts_check_and_bump(self):
        self.git("-C", str(self.source_repo), "worktree", "add", "--detach",
                 str(self.dependency), self.sha)
        self.assertEqual(self.sync("--check").returncode, 0)
        result = self.sync("--bump", self.sha)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.pin.read_text().strip(), self.sha)

    def test_worktree_revision_change_refused_without_mutating_pin(self):
        self.git("-C", str(self.source_repo), "worktree", "add", "--detach",
                 str(self.dependency), self.sha)
        result = self.sync("--bump", self.previous)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("worktree", result.stderr)
        self.assertEqual(self.pin.read_text().strip(), self.sha)
        self.assertEqual(self.git("-C", str(self.dependency), "rev-parse", "HEAD").strip(), self.sha)

    def test_dirty_clone_refused_without_losing_edits_or_pin(self):
        self.git("clone", "-q", str(self.source_repo), str(self.dependency))
        (self.dependency / "version").write_text("operator edit")
        result = self.sync("--bump", self.previous)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("local changes", result.stderr)
        self.assertEqual(self.pin.read_text().strip(), self.sha)
        self.assertEqual((self.dependency / "version").read_text(), "operator edit")


if __name__ == "__main__":
    unittest.main()
