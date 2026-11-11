#!/usr/bin/env python3
"""Behavior tests for the local-lint command consumed by preflight."""

from __future__ import annotations

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


REPO_ROOT = Path(__file__).resolve().parents[1]
LINT_CONFIG = REPO_ROOT / ".preflight" / "local-lint.conf"


def configured_command() -> str:
    commands = [
        line.strip()
        for line in LINT_CONFIG.read_text(encoding="utf-8").splitlines()
        if line.strip() and not line.lstrip().startswith("#")
    ]
    if len(commands) != 1:
        raise AssertionError(f"expected exactly one local lint command, got {commands!r}")
    return commands[0]


def run_configured_command(*, github_actions: bool, backend_lint: str = "1", deferred: str = "") -> str:
    with tempfile.TemporaryDirectory() as temp_dir:
        root = Path(temp_dir)
        (root / "backend").mkdir()
        fake_bin = root / "bin"
        fake_bin.mkdir()
        marker = root / "make-called"
        fake_make = fake_bin / "make"
        fake_make.write_text(
            "#!/usr/bin/env bash\n"
            "set -euo pipefail\n"
            f"printf '%s\\n' \"$*\" > {marker!s}\n",
            encoding="utf-8",
        )
        fake_make.chmod(0o755)
        env = os.environ.copy()
        env["PREFLIGHT_BACKEND_LINT"] = backend_lint
        if deferred:
            env["PREFLIGHT_LINT_DEFERRED"] = deferred
        else:
            env.pop("PREFLIGHT_LINT_DEFERRED", None)
        env["PATH"] = f"{fake_bin}:{env['PATH']}"
        if github_actions:
            env["GITHUB_ACTIONS"] = "true"
        else:
            env.pop("GITHUB_ACTIONS", None)
        result = subprocess.run(
            ["bash", "-c", configured_command()],
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        self_message = result.stdout + result.stderr
        if result.returncode != 0:
            raise AssertionError(self_message)
        if not marker.exists():
            return ""
        return marker.read_text(encoding="utf-8").strip()


class PreflightCILintSkipTest(unittest.TestCase):
    def test_github_actions_does_not_run_duplicate_lint(self) -> None:
        self.assertEqual(run_configured_command(github_actions=True), "")

    def test_local_script_change_does_not_run_backend_lint(self) -> None:
        self.assertEqual(run_configured_command(github_actions=False, backend_lint="0"), "")

    def test_local_preflight_lints_changed_packages(self) -> None:
        self.assertEqual(run_configured_command(github_actions=False), "lint-changed")

    def test_deferred_lint_is_not_started_again_by_the_template(self) -> None:
        self.assertEqual(run_configured_command(github_actions=False, deferred="1"), "")


if __name__ == "__main__":
    unittest.main()
