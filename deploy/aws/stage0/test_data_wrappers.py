#!/usr/bin/env python3
"""Behavior tests for data wrappers and secret-safe Docker invocation."""

from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest


REPO = pathlib.Path(__file__).resolve().parents[3]
INSTALLER = REPO / "deploy/aws/stage0/tokenkey-data-wrappers.sh"


class DataWrappersTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.temp = pathlib.Path(self.temp_dir.name)
        self.bin_dir = self.temp / "bin"
        self.bin_dir.mkdir()
        self.docker_log = self.temp / "docker.log"
        self.env_file = self.temp / "tokenkey.env"
        self.env_file.write_text(
            """POSTGRES_PASSWORD=pg-super-secret
DATABASE_HOST=db.internal
DATABASE_PORT=5432
DATABASE_SSLMODE=require
POSTGRES_USER=tokenkey
POSTGRES_DB=tokenkey
REDIS_PASSWORD=redis-super-secret
REDIS_HOST=redis.internal
REDIS_PORT=6379
"""
        )
        fake_docker = self.bin_dir / "docker"
        fake_docker.write_text(
            """#!/usr/bin/env bash
set -eu
if [ "${1:-}" = network ]; then
  printf '%s\\n' tokenkey_tokenkey-network
  exit 0
fi
printf 'ARGS=%s\\n' "$*" >> "$FAKE_DOCKER_LOG"
printf 'PGPASSWORD=%s REDISCLI_AUTH=%s\\n' "${PGPASSWORD:-}" "${REDISCLI_AUTH:-}" >> "$FAKE_DOCKER_LOG"
"""
        )
        fake_docker.chmod(0o755)
        install_env = os.environ.copy()
        install_env["TOKENKEY_WRAPPER_INSTALL_DIR"] = str(self.bin_dir)
        subprocess.run(["bash", str(INSTALLER)], env=install_env, check=True, capture_output=True)

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def wrapper_env(self) -> dict[str, str]:
        env = os.environ.copy()
        env.update(
            {
                "PATH": f"{self.bin_dir}:{env['PATH']}",
                "TOKENKEY_ENV_FILE": str(self.env_file),
                "FAKE_DOCKER_LOG": str(self.docker_log),
            }
        )
        return env

    def test_psql_forwards_password_by_environment_name_not_argv(self) -> None:
        subprocess.run(
            [str(self.bin_dir / "tokenkey-psql"), "-c", "select 1"],
            env=self.wrapper_env(),
            check=True,
        )
        lines = self.docker_log.read_text().splitlines()
        self.assertIn("-e PGPASSWORD -e PGSSLMODE", lines[0])
        self.assertNotIn("pg-super-secret", lines[0])
        self.assertIn("PGPASSWORD=pg-super-secret", lines[1])

    def test_redis_cli_forwards_password_by_environment_name_not_argv(self) -> None:
        subprocess.run(
            [str(self.bin_dir / "tokenkey-redis-cli"), "ping"],
            env=self.wrapper_env(),
            check=True,
        )
        lines = self.docker_log.read_text().splitlines()
        self.assertIn("-e REDISCLI_AUTH", lines[0])
        self.assertNotIn("redis-super-secret", lines[0])
        self.assertIn("REDISCLI_AUTH=redis-super-secret", lines[1])


if __name__ == "__main__":
    unittest.main()
