#!/usr/bin/env python3
"""Window scripts refuse without the exact confirm and never write in check mode."""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
APP = ROOT / "ops" / "stage0" / "dataplane-cutover-app.sh"
DATA = ROOT / "ops" / "stage0" / "dataplane-cutover-data.sh"

# Commands that would change the site. Check mode must issue none of them.
WRITES = ("kill -s USR1", "stop ", "update --restart", "compose", "REPLICAOF", "pg_promote",
          "pg_switch_wal", "systemctl stop", "systemctl start", "systemctl disable", "alter system")


def stub_bin(directory: pathlib.Path, *, app_db_host: str = "tokenkey-postgres", caddy_try_s: int = 30) -> None:
    """docker / systemctl / ip stubs: every call is logged, reads answer a healthy prod."""
    log = directory / "calls"
    docker = directory / "docker"
    docker.write_text(textwrap.dedent(f"""\
        #!/bin/bash
        echo "docker $*" >> "{log}"
        all="$*"
        case "$1" in
          inspect)
            name="${{@: -1}}"
            case "$all" in
              *State.Running*) case "$name" in tokenkey-blue|tokenkey-postgres|tokenkey-redis) echo true ;; *) exit 1 ;; esac ;;
              *State.Health.Status*) echo healthy ;;
              *Mounts*) echo bind:true ;;
              *Config.Env*) printf 'DATABASE_HOST={app_db_host}\\nDATABASE_PASSWORD=pw\\nREDIS_PASSWORD=rpw\\n' ;;
              *) exit 0 ;;
            esac ;;
          exec)
            case "$all" in
              *pg_is_in_recovery*-h\\ 10.*|*-h\\ 10.*pg_is_in_recovery*) echo t ;;
              *pg_is_in_recovery*) case "$all" in *-h\\ 127.0.0.1*) echo t ;; *) echo f ;; esac ;;
              *last_failed_time*) echo t ;;
              *extract\\(epoch*) echo 30 ;;
              *pg_stat_activity*) echo 0 ;;
              *"show restore_command"*) echo "pgbackrest --repo=2 archive-get" ;;
              *show\\ *) echo x ;;
              *"INFO server"*) echo config_file:/data/tokenkey-redis.conf ;;
              *PING*) echo PONG ;;
              *"INFO replication"*) printf 'role:slave\\nmaster_link_status:up\\nslave_repl_offset:5\\n' ;;
              *"CONFIG GET maxmemory-policy"*) printf 'maxmemory-policy\\nnoeviction\\n' ;;
              *"CONFIG GET appendonly"*) printf 'appendonly\\nyes\\n' ;;
              *"cat /etc/caddy/Caddyfile"*) printf 'reverse_proxy tokenkey-blue:8080 {{\\n lb_try_duration {caddy_try_s}s\\n}}\\n' ;;
              *) : ;;
            esac ;;
        esac
        exit 0
        """))
    systemctl = directory / "systemctl"
    systemctl.write_text(f'#!/bin/sh\necho "systemctl $*" >> "{log}"\n'
                         '[ "$1" = list-timers ] && echo "x tokenkey-postgres-backup.timer y"\n'
                         '[ "$1" = is-active ] && exit 3\nexit 0\n')
    ip = directory / "ip"
    ip.write_text("#!/bin/sh\necho '2: eth0    inet 10.4.0.3/22 brd 10.4.3.255 scope global eth0'\n")
    for path in (docker, systemctl, ip):
        path.chmod(0o755)


def run(script: pathlib.Path, directory: pathlib.Path, **env: str) -> tuple[subprocess.CompletedProcess, str]:
    proc = subprocess.run(
        ["bash", str(script)],
        env={**os.environ, "PATH": f"{directory}:{os.environ['PATH']}", "TK_DOCKER": str(directory / "docker"),
             "ACTIVE_COLOR_FILE": str(directory / "none"), "TK_LIB_DIR": str(ROOT / "ops" / "lib"), **env},
        capture_output=True, text=True, check=False,
    )
    log = directory / "calls"
    return proc, log.read_text() if log.exists() else ""


class AppWindowTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = pathlib.Path(tmp.name)
        stub_bin(self.dir)
        self.root = self.dir / "root"
        self.root.mkdir()
        (self.root / "docker-compose.bluegreen.yml").write_text(
            "- DATABASE_HOST=${DATABASE_HOST:-tokenkey-postgres}\n- REDIS_HOST=${REDIS_HOST:-tokenkey-redis}\n")
        secret = self.root / "dataplane-redis-password"
        secret.write_text("new")
        secret.chmod(0o600)
        self.env = {"SITE": "prod", "DATA_IP": "10.4.0.3", "NEW_REDIS_PASSWORD_FILE": str(secret)}

    def test_apply_refuses_without_exact_confirm(self) -> None:
        for confirm in ("", "cutover-prod-to-10.4.0.9", "cutover-cn-prod-to-10.4.0.3"):
            with self.subTest(confirm=confirm):
                (self.dir / "calls").unlink(missing_ok=True)
                proc, calls = run(APP, self.dir, **self.env, PHASE="apply", TK_DATAPLANE_CONFIRM=confirm)
                self.assertEqual(proc.returncode, 64, proc.stderr)
                self.assertIn("TK_DATAPLANE_CONFIRM must be cutover-prod-to-10.4.0.3", proc.stderr)
                for write in WRITES:
                    self.assertNotIn(write, calls)

    def test_check_never_writes(self) -> None:
        proc, calls = run(APP, self.dir, **self.env)
        self.assertIn("== check site=prod", proc.stdout)
        for write in WRITES:
            self.assertNotIn(write, calls)

    def test_missing_target_redis_password_is_a_check_failure(self) -> None:
        env = {**self.env, "NEW_REDIS_PASSWORD_FILE": str(self.root / "absent")}
        proc, _ = run(APP, self.dir, **env)
        self.assertEqual(proc.returncode, 2)
        self.assertIn("FAIL missing", proc.stdout)
        self.assertIn("skip data redis checks", proc.stdout)

    def test_window_pauses_https_watch_and_bounds_the_wait_before_promote(self) -> None:
        # https-watch probes the database; it must pause with the other writers.
        # The pre-promote waits share one budget below Caddy's lb_try_duration
        # (30s), so a slow archive aborts and restores instead of returning 502s.
        source = APP.read_text()
        self.assertIn("qa-archive|https-watch)", source)
        self.assertIn('PRE_PROMOTE_BUDGET_S="${PRE_PROMOTE_BUDGET_S:-20}"', source)
        self.assertEqual(source.count("while within_budget; do"), 3)

    def test_abort_reports_an_incomplete_restore(self) -> None:
        source = APP.read_text()
        body = source[source.index("abort() {"):source.index("phase_apply() {")]
        self.assertNotIn("|| true", body)
        self.assertIn("exit 4", body)

    def test_check_rejects_a_caddy_wait_below_the_promote_budget(self) -> None:
        stub_bin(self.dir, caddy_try_s=15)
        proc, calls = run(APP, self.dir, **self.env, PRE_PROMOTE_BUDGET_S="20")
        self.assertEqual(proc.returncode, 2, proc.stdout)
        self.assertIn("does not cover budget 20s", proc.stdout)
        for write in WRITES:
            self.assertNotIn(write, calls)

    def test_rejects_public_data_ip(self) -> None:
        proc, calls = run(APP, self.dir, **{**self.env, "DATA_IP": "43.153.73.120"})
        self.assertEqual(proc.returncode, 2)
        self.assertIn("private 10.x", proc.stderr)
        self.assertEqual(calls, "")


class DataMachineTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = pathlib.Path(tmp.name)
        stub_bin(self.dir)

    def test_finish_refuses_without_confirm_bound_to_site_and_host(self) -> None:
        for confirm in ("", "finish-cn-prod-10.4.0.3", "finish-prod-10.4.0.9"):
            with self.subTest(confirm=confirm):
                (self.dir / "calls").unlink(missing_ok=True)
                proc, calls = run(DATA, self.dir, SITE="prod", PHASE="finish", TK_DATAPLANE_CONFIRM=confirm)
                self.assertEqual(proc.returncode, 64, proc.stderr)
                self.assertIn("finish-prod-10.4.0.3", proc.stderr)
                for write in WRITES:
                    self.assertNotIn(write, calls)

    def test_check_is_read_only(self) -> None:
        _, calls = run(DATA, self.dir, SITE="prod")
        for write in WRITES:
            self.assertNotIn(write, calls)

    def test_prepare_redis_refuses_an_existing_server(self) -> None:
        proc, calls = run(DATA, self.dir, SITE="prod", PHASE="prepare-redis",
                          TK_DATAPLANE_CONFIRM="prepare-redis-prod-10.4.0.3")
        self.assertEqual(proc.returncode, 2)
        self.assertIn("only for a fresh replica", proc.stderr)
        for write in WRITES:
            self.assertNotIn(write, calls)

    def test_promote_phase_no_longer_exists(self) -> None:
        # Promotion lives only in the app-side window, one remote invocation.
        proc, _ = run(DATA, self.dir, SITE="prod", PHASE="promote")
        self.assertEqual(proc.returncode, 2)
        self.assertIn("unknown PHASE=promote", proc.stderr)

    def test_prod_default_profile_keeps_live_2c8g_expectations(self) -> None:
        source = DATA.read_text()
        self.assertIn('TOKENKEY_DATA_HOST_PROFILE', source)
        self.assertIn('prod-4c16g', source)
        # Default (unset profile) must still match live 2C8G / PG 6G until resize.
        self.assertIn('sb=2GB ecs=5GB mwm=256MB max_conn=200', source)
        self.assertIn('sb=4GB ecs=10GB mwm=512MB max_conn=200', source)

    def test_prod_4c16g_profile_is_opt_in_and_check_stays_read_only(self) -> None:
        source = DATA.read_text()
        # Both branches must coexist: live default until resize, target after.
        default_idx = source.index("sb=2GB ecs=5GB mwm=256MB max_conn=200")
        target_idx = source.index("sb=4GB ecs=10GB mwm=512MB max_conn=200")
        self.assertLess(target_idx, default_idx)  # prod-4c16g case before default *
        self.assertIn("max_parallel_workers=3", source)
        proc, calls = run(DATA, self.dir, SITE="prod", TOKENKEY_DATA_HOST_PROFILE="prod-4c16g")
        for write in WRITES:
            self.assertNotIn(write, calls)
        # Standby-oriented check may fail on stubs; it must never refuse as confirm-gated write.
        self.assertNotEqual(proc.returncode, 64)


if __name__ == "__main__":
    unittest.main()
