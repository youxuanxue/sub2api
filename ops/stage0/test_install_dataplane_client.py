#!/usr/bin/env python3
"""install-dataplane-client.sh refuses before the cutover and needs a confirm to write."""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops" / "stage0" / "install-dataplane-client.sh"


def fake_docker(directory: pathlib.Path, running: set[str], env: dict[str, str]) -> pathlib.Path:
    """`docker inspect` answers running-ness, app env and images; `run`/`rm` are logged."""
    env_lines = "\\n".join(f"{k}={v}" for k, v in env.items())
    docker = directory / "docker"
    docker.write_text(textwrap.dedent(f"""\
        #!/bin/bash
        echo "$*" >> "{directory}/calls"
        [ "$1" = inspect ] || exit 0
        name="${{@: -1}}"
        case "$3" in
          *State.Running*) for r in {" ".join(sorted(running))}; do [ "$r" = "$name" ] && echo true && exit 0; done; exit 1 ;;
          *Config.Env*) [ "$name" = tokenkey-blue ] && printf '{env_lines}\\n' && exit 0; exit 1 ;;
          *Config.Image*) case "$name" in tokenkey-postgres) echo postgres:18-alpine ;; tokenkey-redis) echo redis:8-alpine ;; *) exit 1 ;; esac ;;
          *) case "$name" in tokenkey-postgres|tokenkey-redis|tokenkey-blue) exit 0 ;; esac; exit 1 ;;
        esac
        """))
    docker.chmod(0o755)
    # The reachability probe needs a listener; make `timeout` succeed without one.
    stub = directory / "timeout"
    stub.write_text("#!/bin/sh\nexit 0\n")
    stub.chmod(0o755)
    return docker


CUTOVER_ENV = {
    "DATABASE_HOST": "10.4.0.3", "DATABASE_PORT": "5432", "DATABASE_PASSWORD": "pw",
    "REDIS_HOST": "10.4.0.3", "REDIS_PORT": "6379", "REDIS_PASSWORD": "rpw",
}


class InstallDataplaneClientTest(unittest.TestCase):
    def run_script(self, running: set[str], env: dict[str, str], **extra: str):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        directory = pathlib.Path(tmp.name)
        docker = fake_docker(directory, running, env)
        proc = subprocess.run(
            ["bash", str(SCRIPT)],
            env={**os.environ, "PATH": f"{directory}:{os.environ['PATH']}", "TK_DOCKER": str(docker),
                 "ACTIVE_COLOR_FILE": str(directory / "none"), "TK_DATAPLANE_CLIENT_DIR": str(directory / "secrets"),
                 "TK_LIB_DIR": str(ROOT / "ops" / "lib"), **extra},
            capture_output=True, text=True, check=False,
        )
        calls = (directory / "calls").read_text() if (directory / "calls").exists() else ""
        return proc, calls, directory

    def test_refuses_while_app_still_uses_local_database(self) -> None:
        for host in ("postgres", "tokenkey-postgres", "tk-archive-standby", ""):
            with self.subTest(host=host):
                proc, calls, _ = self.run_script({"tokenkey-blue"}, {**CUTOVER_ENV, "DATABASE_HOST": host})
                self.assertEqual(proc.returncode, 2, proc.stderr)
                self.assertIn("still uses local Postgres", proc.stderr)
                self.assertNotIn("run -d", calls)

    def test_refuses_while_old_server_is_running(self) -> None:
        proc, calls, _ = self.run_script({"tokenkey-blue", "tokenkey-postgres"}, CUTOVER_ENV)
        self.assertEqual(proc.returncode, 2, proc.stderr)
        self.assertIn("local Postgres server tokenkey-postgres is still running", proc.stderr)
        self.assertNotIn("run -d", calls)

    def test_plan_is_read_only_and_apply_needs_exact_confirm(self) -> None:
        proc, calls, directory = self.run_script({"tokenkey-blue"}, CUTOVER_ENV)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("TK_DATAPLANE_CLIENT_CONFIRM=install-dataplane-client-10.4.0.3", proc.stdout)
        self.assertNotIn("run -d", calls)
        self.assertFalse((directory / "secrets").exists())
        proc, calls, directory = self.run_script({"tokenkey-blue"}, CUTOVER_ENV, MODE="apply",
                                                 TK_DATAPLANE_CLIENT_CONFIRM="install-dataplane-client-10.4.0.9")
        self.assertEqual(proc.returncode, 64, proc.stderr)
        self.assertNotIn("run -d", calls)
        self.assertFalse((directory / "secrets").exists())


if __name__ == "__main__":
    unittest.main()
