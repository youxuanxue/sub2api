#!/usr/bin/env python3
"""Behavior tests for changed-package lint and the local fixture-gateway skip."""
from __future__ import annotations

import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
PREFLIGHT = ROOT / "scripts" / "preflight.sh"


def load_lint():
    spec = importlib.util.spec_from_file_location(
        "lint_changed_go", ROOT / "scripts" / "checks" / "lint-changed-go.py"
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load_gateway():
    spec = importlib.util.spec_from_file_location(
        "check_gateway_tk", ROOT / "scripts" / "sentinels" / "check-gateway-tk.py"
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


lint = load_lint()
gateway = load_gateway()


def _speed_defaults_snippet() -> str:
    lines = PREFLIGHT.read_text(encoding="utf-8").splitlines()
    start = next(i for i, line in enumerate(lines) if line.startswith("_preflight_apply_local_speed_defaults() {"))
    seen_close = False
    for index in range(start, len(lines)):
        if lines[index] == "}":
            seen_close = True
            continue
        if seen_close and lines[index].startswith("_preflight_apply_local_speed_defaults "):
            return "\n".join(lines[start:index + 1])
    raise AssertionError("local speed defaults are not applied")


class LintSelectionTest(unittest.TestCase):
    def test_changed_go_file_selects_its_package(self) -> None:
        decision = lint.classify(["backend/internal/service/foo.go", "docs/readme.md"])
        self.assertEqual(decision, "./internal/service")

    def test_two_packages_are_deduped_and_sorted(self) -> None:
        decision = lint.classify([
            "backend/internal/handler/b.go",
            "backend/internal/service/a.go",
            "backend/internal/handler/a.go",
        ])
        self.assertEqual(decision, "./internal/handler ./internal/service")

    def test_toolchain_file_forces_the_whole_module(self) -> None:
        decision = lint.classify(["backend/internal/service/foo.go", "backend/go.mod"])
        self.assertEqual(decision, "FULL")

    def test_golangci_config_forces_the_whole_module(self) -> None:
        self.assertEqual(lint.classify(["backend/.golangci.yml"]), "FULL")

    def test_non_go_change_skips_lint(self) -> None:
        self.assertEqual(lint.classify(["frontend/src/App.vue", "backend/README.md"]), "SKIP")

    def test_force_full_ignores_an_empty_diff(self) -> None:
        self.assertEqual(lint.classify([], force_full=True), "FULL")

    def test_deleted_package_directory_is_not_linted(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "backend" / "internal" / "service").mkdir(parents=True)
            decision = lint.classify(
                ["backend/internal/service/live.go", "backend/internal/missing/gone.go"],
                root=root,
            )
        self.assertEqual(decision, "./internal/service")

    def test_pending_and_untracked_go_files_are_collected(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.email", "fixture@example.invalid"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.name", "Fixture"], cwd=root, check=True)
            subprocess.run(["git", "config", "commit.gpgsign", "false"], cwd=root, check=True)
            tracked = root / "backend" / "internal" / "service"
            tracked.mkdir(parents=True)
            (tracked / "old.go").write_text("package service\n", encoding="utf-8")
            subprocess.run(["git", "add", "."], cwd=root, check=True)
            subprocess.run(["git", "commit", "-qm", "base"], cwd=root, check=True)
            (tracked / "edited.go").write_text("package service\n", encoding="utf-8")
            untracked = root / "backend" / "internal" / "handler"
            untracked.mkdir()
            (untracked / "new.go").write_text("package handler\n", encoding="utf-8")
            paths = lint.collect_paths(root, "HEAD")
            decision = lint.classify(paths, force_full=False, root=root)
        self.assertEqual(decision, "./internal/handler ./internal/service")


class SpeedDefaultTest(unittest.TestCase):
    def _run(self, scope: str, preset: str | None) -> str:
        env = os.environ.copy()
        env.pop("PREFLIGHT_SKIP_PROMPT_FIXTURE_GATEWAY", None)
        if preset is not None:
            env["PREFLIGHT_SKIP_PROMPT_FIXTURE_GATEWAY"] = preset
        script = f"""
set -u
_scope={scope}
{_speed_defaults_snippet()}
printf '%s\\n' "${{PREFLIGHT_SKIP_PROMPT_FIXTURE_GATEWAY-unset}}"
"""
        result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def test_local_scope_skips_the_service_compile(self) -> None:
        self.assertEqual(self._run("worktree", None), "1")

    def test_full_scope_keeps_the_fixture(self) -> None:
        self.assertEqual(self._run("full", None), "unset")

    def test_explicit_zero_keeps_the_fixture(self) -> None:
        self.assertEqual(self._run("auto", "0"), "0")


class FailoverScanTest(unittest.TestCase):
    def test_generated_ent_violation_is_ignored_and_adapter_violation_is_reported(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            backend = Path(tmp) / "backend"
            generated = backend / "ent"
            adapter = backend / "internal" / "service"
            generated.mkdir(parents=True)
            adapter.mkdir(parents=True)
            violation = "package p\n\ntype Row struct {\n\tShouldFailover bool\n}\n"
            (generated / "adapter.go").write_text(violation, encoding="utf-8")
            (adapter / "adapter.go").write_text(violation, encoding="utf-8")
            ok, failures = gateway._check_failover_ssot(backend)
        self.assertFalse(ok)
        rendered = "\n".join(failures)
        self.assertIn("internal/service/adapter.go", rendered)
        self.assertNotIn("ent/adapter.go", rendered)


if __name__ == "__main__":
    unittest.main()
