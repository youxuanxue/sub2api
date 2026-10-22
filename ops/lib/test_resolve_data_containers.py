#!/usr/bin/env python3
"""PG/Redis container resolution: Stage0 names, Tencent USW names, fail closed."""
from __future__ import annotations

import os
import pathlib
import subprocess
import sys
import tempfile
import textwrap
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import resolve_app_container as owner  # noqa: E402

SHELL_OWNER = HERE / "resolve-app-container.sh"


def fake_docker(directory: pathlib.Path, running: set[str]) -> pathlib.Path:
    docker = directory / "docker"
    names = " ".join(sorted(running))
    docker.write_text(textwrap.dedent(f"""\
        #!/bin/sh
        [ "$1" = inspect ] || exit 9
        for a in "$@"; do n="$a"; done
        for r in {names}; do [ "$r" = "$n" ] && echo true && exit 0; done
        exit 1
        """))
    docker.chmod(0o755)
    return docker


class ShellOwnerTest(unittest.TestCase):
    def resolve(self, function: str, running: set[str], requested: str = "auto") -> subprocess.CompletedProcess:
        with tempfile.TemporaryDirectory() as tmp:
            docker = fake_docker(pathlib.Path(tmp), running)
            return subprocess.run(
                ["bash", "-c", f'. "$1"; {function} "$2"', "_", str(SHELL_OWNER), requested],
                env={**os.environ, "TK_DOCKER": str(docker)},
                capture_output=True, text=True, check=False,
            )

    def test_stage0_and_tencent_names_resolve(self) -> None:
        cases = [
            ("tk_resolve_postgres_container", {"tokenkey-postgres"}, "tokenkey-postgres"),
            ("tk_resolve_postgres_container", {"tk-archive-standby"}, "tk-archive-standby"),
            ("tk_resolve_redis_container", {"tokenkey-redis"}, "tokenkey-redis"),
            ("tk_resolve_redis_container", {"tk-cutover-prod-redis"}, "tk-cutover-prod-redis"),
        ]
        for function, running, expected in cases:
            with self.subTest(function=function, running=running):
                proc = self.resolve(function, running | {"tokenkey-blue"})
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertEqual(proc.stdout.strip(), expected)

    def test_zero_or_several_running_fails_closed(self) -> None:
        for running in (set(), {"tokenkey-postgres", "tk-archive-standby"}):
            with self.subTest(running=running):
                proc = self.resolve("tk_resolve_postgres_container", running)
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(proc.stdout, "")
        proc = self.resolve("tk_require_redis_container", set())
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("redis container unresolved", proc.stderr)
        self.assertIn("tk-cutover-prod-redis", proc.stderr)

    def test_explicit_name_must_be_running(self) -> None:
        self.assertEqual(self.resolve("tk_resolve_postgres_container", {"pg-x"}, "pg-x").stdout.strip(), "pg-x")
        self.assertNotEqual(self.resolve("tk_resolve_postgres_container", set(), "pg-x").returncode, 0)


class PythonOwnerTest(unittest.TestCase):
    def patched(self, running: set[str]):
        def run(args, **_kwargs):
            ok = args[-1] in running
            return subprocess.CompletedProcess(args, 0 if ok else 1, stdout="true\n" if ok else "", stderr="")
        return mock.patch.multiple(owner, subprocess=mock.MagicMock(run=run, SubprocessError=subprocess.SubprocessError),
                                   shutil=mock.MagicMock(which=lambda _: "/usr/bin/docker"))

    def test_candidate_lists_match_shell_owner(self) -> None:
        text = SHELL_OWNER.read_text()
        self.assertIn(f'TK_POSTGRES_CONTAINER_CANDIDATES="{" ".join(owner.POSTGRES_CANDIDATES)}"', text)
        self.assertIn(f'TK_REDIS_CONTAINER_CANDIDATES="{" ".join(owner.REDIS_CANDIDATES)}"', text)

    def test_tencent_names_and_ambiguity(self) -> None:
        with self.patched({"tk-archive-standby", "tk-cutover-prod-redis"}):
            self.assertEqual(owner.resolve_postgres(), "tk-archive-standby")
            self.assertEqual(owner.resolve_redis(), "tk-cutover-prod-redis")
        with self.patched({"tokenkey-postgres", "tk-archive-standby"}):
            self.assertIsNone(owner.resolve_postgres())
        with self.patched(set()):
            self.assertIsNone(owner.resolve_redis())

    def test_shell_snippet_resolves_and_fails_closed(self) -> None:
        for running, expected_rc, expected in (({"tk-archive-standby"}, 0, "tk-archive-standby"),
                                               (set(), 1, ""),
                                               ({"tokenkey-postgres", "tk-archive-standby"}, 1, "")):
            with self.subTest(running=running), tempfile.TemporaryDirectory() as tmp:
                docker = fake_docker(pathlib.Path(tmp), running)
                snippet = owner.data_container_shell_snippet("postgres", docker=str(docker))
                proc = subprocess.run(["bash", "-c", snippet + '; printf %s "$TK_PG"'],
                                      capture_output=True, text=True, check=False)
                self.assertEqual(proc.returncode, expected_rc, proc.stderr)
                self.assertEqual(proc.stdout, expected)

    def test_preamble_exports_for_child_script(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            docker = fake_docker(pathlib.Path(tmp), {"tokenkey-postgres", "tk-cutover-prod-redis"})
            preamble = owner.data_container_preamble(docker=str(docker), redis=True)
            proc = subprocess.run(["bash", "-c", preamble + 'bash -c \'printf "%s %s" "$TK_PG" "$TK_REDIS"\''],
                                  capture_output=True, text=True, check=False)
        self.assertEqual(proc.stdout, "tokenkey-postgres tk-cutover-prod-redis", proc.stderr)

    def test_snippet_cli(self) -> None:
        out = subprocess.run([sys.executable, str(HERE / "resolve_app_container.py"), "snippet", "redis", "--var", "R"],
                             capture_output=True, text=True, check=True).stdout
        self.assertIn("tk-cutover-prod-redis", out)
        self.assertIn("R=''", out)


if __name__ == "__main__":
    unittest.main()
