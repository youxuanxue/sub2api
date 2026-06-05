#!/usr/bin/env python3
"""Two-mode gates for the Stage0 compose data-layer split (stdlib-only).

Local-container mode (COMPOSE_PROFILES=localpg,localredis) must stay
bit-identical to historical behavior: postgres+redis active, tokenkey
depends_on both. External-RDS mode (COMPOSE_PROFILES=localredis +
docker-compose.external-db.yml via COMPOSE_FILE) must drop postgres and
reset tokenkey's depends_on to redis only — `!override` is the only
compose-native way to do that, because an always-enabled service may not
depends_on a profile-gated one (asserted by the negative test below).

Requires docker compose >= v2.24 (prod measured v2.29.7). Skips cleanly when
docker compose is unavailable (CI runners without docker must not go red on
infra absence — see feedback_docker_exec_not_host_psql discipline).
"""
from __future__ import annotations

import json
import pathlib
import shutil
import subprocess
import unittest

_REPO = pathlib.Path(__file__).resolve().parents[3]
STAGE0 = _REPO / "deploy/aws/stage0"
COMPOSE = STAGE0 / "docker-compose.yml"
COMPOSE_EXT = STAGE0 / "docker-compose.external-db.yml"

_DUMMY_ENV = {
    "TOKENKEY_IMAGE": "ghcr.io/example/sub2api:test",
    "POSTGRES_PASSWORD": "dummy-password",
    "JWT_SECRET": "dummy-jwt-secret",
    "TOTP_ENCRYPTION_KEY": "dummy-totp-key",
    "PATH": "/usr/local/bin:/usr/bin:/bin",
}


def _compose_available() -> bool:
    if shutil.which("docker") is None:
        return False
    proc = subprocess.run(
        ["docker", "compose", "version"], capture_output=True, text=True
    )
    return proc.returncode == 0


def _config(profiles: str, files: list[pathlib.Path], extra_env: dict | None = None):
    env = dict(_DUMMY_ENV)
    env["COMPOSE_PROFILES"] = profiles
    if extra_env:
        env.update(extra_env)
    cmd = ["docker", "compose"]
    for f in files:
        cmd += ["-f", str(f)]
    cmd += ["config", "--format", "json"]
    return subprocess.run(cmd, capture_output=True, text=True, env=env, cwd=STAGE0)


class ComposeDataLayerModesTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        if not _compose_available():
            raise unittest.SkipTest("docker compose unavailable — skipping compose mode gates")

    def test_local_mode_is_full_stack(self) -> None:
        proc = _config("localpg,localredis", [COMPOSE])
        self.assertEqual(proc.returncode, 0, proc.stderr)
        cfg = json.loads(proc.stdout)
        services = cfg["services"]
        self.assertIn("postgres", services)
        self.assertIn("redis", services)
        self.assertEqual(
            sorted(services["tokenkey"].get("depends_on", {})), ["postgres", "redis"]
        )
        self.assertEqual(sorted(services["caddy"].get("depends_on", {})), ["tokenkey"])
        # 历史默认值守恒：不设 DATABASE_* 时仍指向本机容器
        env = services["tokenkey"]["environment"]
        self.assertEqual(env.get("DATABASE_HOST"), "postgres")
        self.assertEqual(env.get("DATABASE_SSLMODE"), "disable")
        self.assertEqual(env.get("REDIS_HOST"), "redis")

    def test_external_mode_drops_postgres_keeps_redis(self) -> None:
        proc = _config("localredis", [COMPOSE, COMPOSE_EXT])
        self.assertEqual(proc.returncode, 0, proc.stderr)
        cfg = json.loads(proc.stdout)
        services = cfg["services"]
        self.assertNotIn("postgres", services)
        self.assertIn("redis", services)
        self.assertEqual(sorted(services["tokenkey"].get("depends_on", {})), ["redis"])
        self.assertEqual(sorted(services["caddy"].get("depends_on", {})), ["tokenkey"])

    def test_external_mode_env_overrides_flow_through(self) -> None:
        proc = _config(
            "localredis",
            [COMPOSE, COMPOSE_EXT],
            extra_env={
                "DATABASE_HOST": "ledger.example.rds.amazonaws.com",
                "DATABASE_SSLMODE": "require",
            },
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        env = json.loads(proc.stdout)["services"]["tokenkey"]["environment"]
        self.assertEqual(env.get("DATABASE_HOST"), "ledger.example.rds.amazonaws.com")
        self.assertEqual(env.get("DATABASE_SSLMODE"), "require")

    def test_compose_file_env_var_selects_override(self) -> None:
        # systemd 路径：EnvironmentFile=.env 把 COMPOSE_FILE 注入进程环境，
        # 不带 -f 也必须叠加 override。这是 cutover 后下一次 reboot 的生效机制。
        proc = subprocess.run(
            ["docker", "compose", "config", "--format", "json"],
            capture_output=True,
            text=True,
            env={
                **_DUMMY_ENV,
                "COMPOSE_PROFILES": "localredis",
                "COMPOSE_FILE": f"{COMPOSE}:{COMPOSE_EXT}",
            },
            cwd=STAGE0,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        services = json.loads(proc.stdout)["services"]
        self.assertNotIn("postgres", services)
        self.assertEqual(sorted(services["tokenkey"].get("depends_on", {})), ["redis"])

    def test_external_profiles_without_override_is_rejected(self) -> None:
        # 防回归：谁要是删了 override 文件/depends_on !override，外部模式必须
        # 在 config 阶段就炸（depends on undefined service），而不是上线后才发现。
        proc = _config("localredis", [COMPOSE])
        self.assertNotEqual(
            proc.returncode,
            0,
            "external profiles WITHOUT the override must fail compose config — "
            "if this passes, the localpg profile gating was removed",
        )
        self.assertIn("postgres", proc.stderr)


if __name__ == "__main__":
    unittest.main()
