#!/usr/bin/env python3
"""Behavior tests for preflight background-output replay."""

from __future__ import annotations

import pathlib
import subprocess
import tempfile
import unittest


PREFLIGHT = pathlib.Path(__file__).resolve().parent / "preflight.sh"


def _shell_function(name: str) -> str:
    lines = PREFLIGHT.read_text(encoding="utf-8").splitlines()
    start = lines.index(next(line for line in lines if line.startswith(f"{name}() {{")))
    for end in range(start + 1, len(lines)):
        if lines[end] == "}":
            return "\n".join(lines[start : end + 1])
    raise AssertionError(f"unterminated shell function: {name}")


def _optional_shell_function(name: str) -> str:
    try:
        return _shell_function(name)
    except (StopIteration, ValueError, AssertionError):
        return ""


def _run_spawned_join(return_code: int, output_mode: str) -> subprocess.CompletedProcess[str]:
    with tempfile.TemporaryDirectory() as tmp:
        script = f"""
set -u
_preflight_bg_dir={tmp!s}
{_shell_function("_bg_spawn")}
{_shell_function("_bg_join")}
_bg_spawn job bash -c "printf 'captured job detail\\n'; exit {return_code}"
_bg_join job {output_mode}
printf 'joined-rc=%s\\n' "$_bg_rc"
"""
        return subprocess.run(
            ["bash", "-c", script],
            check=False,
            capture_output=True,
            text=True,
        )


def _anthropic_gate() -> str:
    text = PREFLIGHT.read_text(encoding="utf-8")
    start = text.index('echo "=== sub2api: ops/anthropic orchestrators unittest ==="')
    end = text.index("# Servable-model allowlist generator", start)
    return text[start:end]


def _run_anthropic_failure_gate() -> subprocess.CompletedProcess[str]:
    script = f"""
set -u
errors=0
_bg_spawned() {{ return 0; }}
_bg_join() {{
    if [ "${{2:-silent}}" = "replay-on-failure" ]; then
        printf 'captured anthropic traceback\n'
    fi
    _bg_rc=1
}}
{_anthropic_gate()}
printf 'errors=%s\n' "$errors"
"""
    return subprocess.run(
        ["bash", "-c", script],
        check=False,
        capture_output=True,
        text=True,
    )


def _archive_gate() -> str:
    text = PREFLIGHT.read_text(encoding="utf-8")
    start = text.index('echo "=== sub2api: nonprod archive/restore rehearsal ==="')
    end = text.index("# ---- sub2api: runtime resource config verdict selftest", start)
    return text[start:end]


def _run_archive_spawn(skip_slow_ops: int) -> subprocess.CompletedProcess[str]:
    script = f"""
set -u
_preflight_skip_slow_ops={skip_slow_ops}
_bg_spawn() {{ printf 'spawned=%s:%s\n' "$1" "$2"; }}
{_optional_shell_function("_archive_rehearsal_spawn_if_needed")}
_archive_rehearsal_spawn_if_needed
"""
    return subprocess.run(
        ["bash", "-c", script],
        check=False,
        capture_output=True,
        text=True,
    )


def _run_archive_failure_gate() -> subprocess.CompletedProcess[str]:
    script = f"""
set -u
errors=0
_preflight_skip_slow_ops=0
python3() {{ return 0; }}
_bg_spawned() {{ return 0; }}
_bg_join() {{
    if [ "${{2:-silent}}" = "replay-on-failure" ]; then
        printf 'captured archive traceback\n'
        printf 'failed command: python3 ops/archive/test_data_layer_archive_rehearsal.py\n'
    fi
    _bg_rc=1
}}
{_archive_gate()}
printf 'errors=%s\n' "$errors"
"""
    return subprocess.run(
        ["bash", "-c", script],
        check=False,
        capture_output=True,
        text=True,
    )


class PreflightBackgroundOutputTest(unittest.TestCase):
    def test_failure_replays_captured_output(self) -> None:
        result = _run_spawned_join(1, "replay-on-failure")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("captured job detail", result.stdout)
        self.assertIn("joined-rc=1", result.stdout)

    def test_success_stays_quiet(self) -> None:
        result = _run_spawned_join(0, "replay-on-failure")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("captured job detail", result.stdout)
        self.assertIn("joined-rc=0", result.stdout)

    def test_default_mode_keeps_failure_output_captured(self) -> None:
        result = _run_spawned_join(1, "silent")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("captured job detail", result.stdout)
        self.assertIn("joined-rc=1", result.stdout)

    def test_anthropic_gate_replays_failure_detail(self) -> None:
        result = _run_anthropic_failure_gate()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("captured anthropic traceback", result.stdout)
        self.assertIn("FAIL: ops/anthropic unittest failed", result.stdout)
        self.assertIn("errors=1", result.stdout)

    def test_archive_gate_spawns_one_composite_background_job(self) -> None:
        result = _run_archive_spawn(skip_slow_ops=0)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(
            "spawned=archive_rehearsal:_archive_rehearsal_gate_run",
            result.stdout,
        )

    def test_archive_gate_does_not_spawn_when_slow_ops_are_skipped(self) -> None:
        result = _run_archive_spawn(skip_slow_ops=1)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("spawned=", result.stdout)

    def test_archive_gate_replays_background_failure_detail(self) -> None:
        result = _run_archive_failure_gate()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("captured archive traceback", result.stdout)
        self.assertIn(
            "failed command: python3 ops/archive/test_data_layer_archive_rehearsal.py",
            result.stdout,
        )
        self.assertIn("FAIL: nonprod archive/restore rehearsal contracts", result.stdout)
        self.assertIn("errors=1", result.stdout)


if __name__ == "__main__":
    unittest.main()
