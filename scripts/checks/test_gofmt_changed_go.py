#!/usr/bin/env python3
"""Tests for scripts/checks/gofmt-changed-go.py."""
from __future__ import annotations

import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MODULE = ROOT / "scripts/checks/gofmt-changed-go.py"


def _load_module():
    spec = importlib.util.spec_from_file_location("gofmt_changed_go", MODULE)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


class GofmtChangedGoTest(unittest.TestCase):
    def test_collect_includes_untracked_go_file(self) -> None:
        mod = _load_module()
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            subprocess.run(["git", "init"], cwd=repo, check=True, capture_output=True)
            subprocess.run(
                ["git", "config", "user.email", "test@example.com"],
                cwd=repo,
                check=True,
                capture_output=True,
            )
            subprocess.run(
                ["git", "config", "user.name", "test"],
                cwd=repo,
                check=True,
                capture_output=True,
            )
            (repo / "sample.go").write_text("package sample\n", encoding="utf-8")
            original_root = mod.ROOT
            mod.ROOT = repo
            try:
                files = mod.collect_changed_go_files("HEAD")
            finally:
                mod.ROOT = original_root
            self.assertIn("sample.go", files)

    def test_gofmt_dirty_detects_bad_format(self) -> None:
        mod = _load_module()
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            (repo / "bad.go").write_text("package bad\n\nfunc X(){}\n", encoding="utf-8")
            original_root = mod.ROOT
            mod.ROOT = repo
            try:
                dirty = mod.gofmt_dirty(["bad.go"])
            finally:
                mod.ROOT = original_root
            self.assertEqual(dirty, ["bad.go"])


if __name__ == "__main__":
    unittest.main()
