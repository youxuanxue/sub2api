#!/usr/bin/env python3
"""Tests for scripts/release-bump-and-tag.sh dry-run paths.

Focus: tag-only decision must not abort when suggested_next_version is absent
(the field()/grep + set -e bug fixed in 2026-07).
"""
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

_BUMP_SCRIPT = pathlib.Path(__file__).resolve().parent / "release-bump-and-tag.sh"
_DECIDE_SCRIPT = pathlib.Path(__file__).resolve().parent / "release-decide-version.sh"
_ROUTE_SCRIPT = pathlib.Path(__file__).resolve().parent / "release-main-push-route.sh"


def _clean_env() -> dict[str, str]:
    return {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}


def _git(cwd: pathlib.Path, *args: str) -> None:
    subprocess.run(
        ["git", *args], cwd=cwd, env=_clean_env(), capture_output=True, check=True,
    )


class ReleaseBumpAndTagTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        tmp = pathlib.Path(self._tmp.name)
        self.origin = tmp / "origin.git"
        self.repo = tmp / "repo"
        subprocess.run(
            ["git", "init", "--bare", "-q", "-b", "main", str(self.origin)],
            env=_clean_env(), check=True,
        )
        self.repo.mkdir()
        _git(self.repo, "init", "-q", "-b", "main")
        _git(self.repo, "config", "user.email", "test@example.com")
        _git(self.repo, "config", "user.name", "Test")
        _git(self.repo, "remote", "add", "origin", str(self.origin))
        (self.repo / "backend/cmd/server").mkdir(parents=True)
        (self.repo / "backend/cmd/server/VERSION").write_text("1.0.1\n")
        scripts = self.repo / "scripts"
        scripts.mkdir(parents=True)
        for name in (
            "release-bump-and-tag.sh",
            "release-decide-version.sh",
            "release-main-push-route.sh",
            "release_main_push_route.py",
            "release-tag.sh",
        ):
            src = pathlib.Path(__file__).resolve().parent / name
            if src.exists():
                shutil.copy(src, scripts / name)
                (scripts / name).chmod(0o755)
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-q", "-m", "base")
        _git(self.repo, "push", "-q", "origin", "main")

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def test_dry_run_tag_only_does_not_abort(self) -> None:
        """tag-only: VERSION on main, tag missing — dry-run must exit 0 with plan."""
        proc = subprocess.run(
            ["bash", "scripts/release-bump-and-tag.sh", "--dry-run"],
            cwd=self.repo,
            env=_clean_env(),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(
            proc.returncode, 0,
            f"stdout:{proc.stdout}\nstderr:{proc.stderr}",
        )
        self.assertIn("dry-run: action=tag-only", proc.stdout)
        self.assertIn("target_tag=v1.0.1", proc.stdout)

    def test_bump_and_tag_fails_closed_when_route_script_fails(self) -> None:
        """bump-and-tag must not silently default to direct-push when route lookup fails."""
        _git(self.repo, "tag", "-a", "v1.0.1", "-m", "v1.0.1")
        _git(self.repo, "push", "-q", "origin", "v1.0.1")
        (self.repo / "touch.txt").write_text("ahead\n")
        _git(self.repo, "add", "touch.txt")
        _git(self.repo, "commit", "-q", "-m", "ahead of tag")
        _git(self.repo, "push", "-q", "origin", "main")

        route = self.repo / "scripts/release-main-push-route.sh"
        route.write_text("#!/usr/bin/env bash\nexit 2\n")
        route.chmod(0o755)
        proc = subprocess.run(
            ["bash", "scripts/release-bump-and-tag.sh", "--dry-run"],
            cwd=self.repo,
            env=_clean_env(),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 2, f"stdout:{proc.stdout}\nstderr:{proc.stderr}")
        self.assertIn("refusing to guess bump path", proc.stderr)

    def test_docs_ops_baseline_not_gitignored(self) -> None:
        """release bump must stage endpoint-compat baseline without git add -f."""
        repo_root = pathlib.Path(__file__).resolve().parent.parent
        baseline = repo_root / "docs/ops/endpoint-compat-baseline.md"
        self.assertTrue(baseline.is_file(), f"missing {baseline}")
        proc = subprocess.run(
            ["git", "check-ignore", "-q", str(baseline.relative_to(repo_root))],
            cwd=repo_root,
            env=_clean_env(),
            capture_output=True,
        )
        self.assertNotEqual(
            proc.returncode,
            0,
            "docs/ops/endpoint-compat-baseline.md must not match docs/* gitignore",
        )


if __name__ == "__main__":
    unittest.main()
