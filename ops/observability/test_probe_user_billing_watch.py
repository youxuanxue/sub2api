#!/usr/bin/env python3
"""Behavior tests for probe-user-billing-watch.sh user discovery."""
from __future__ import annotations

import os
import pathlib
import stat
import subprocess
import tempfile
import textwrap
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops" / "observability" / "probe-user-billing-watch.sh"

_FAKE_DOCKER = textwrap.dedent(
    r"""
    #!/usr/bin/env bash
    sql=""
    while [ $# -gt 0 ]; do
      if [ "$1" = "-c" ]; then
        sql="$2"
        break
      fi
      shift
    done
    if [ -n "${FAKE_SQL_LOG:-}" ]; then
      printf '%s\n' "$sql" >> "$FAKE_SQL_LOG"
    fi
    if printf '%s' "$sql" | grep -q 'string_agg'; then
      if [ "${FAKE_DISCOVERY_FAIL:-}" = "1" ]; then
        echo "docker: connection refused" >&2
        exit 1
      fi
      printf '%s\n' "${FAKE_DISCOVERY_IDS-}"
      exit 0
    fi
    echo '{}'
    exit 0
    """
).lstrip()


class ProbeUserBillingWatchTest(unittest.TestCase):
    def run_probe(self, **env_overrides: str) -> tuple[subprocess.CompletedProcess[str], str]:
        with tempfile.TemporaryDirectory() as tmp:
            fake_bin = pathlib.Path(tmp) / "bin"
            fake_bin.mkdir()
            docker = fake_bin / "docker"
            docker.write_text(_FAKE_DOCKER, encoding="utf-8")
            docker.chmod(docker.stat().st_mode | stat.S_IXUSR)
            sql_log = pathlib.Path(tmp) / "sql.log"
            env = os.environ.copy()
            env.pop("USER_IDS", None)
            env.update(env_overrides)
            env["PATH"] = f"{fake_bin}:{env['PATH']}"
            env["FAKE_SQL_LOG"] = str(sql_log)
            proc = subprocess.run(
                ["bash", str(SCRIPT)],
                cwd=ROOT,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            logged = sql_log.read_text(encoding="utf-8") if sql_log.exists() else ""
            return proc, logged

    def test_discovers_active_users_when_user_ids_unset(self) -> None:
        proc, logged = self.run_probe(FAKE_DISCOVERY_IDS="1,6,16")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertIn("string_agg", logged)
        self.assertIn("id IN (1,6,16)", logged)
        self.assertIn("'1,6,16'::text", logged)

    def test_user_ids_override_skips_discovery_query(self) -> None:
        proc, logged = self.run_probe(USER_IDS="1,16", FAKE_DISCOVERY_IDS="99")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertNotIn("string_agg", logged)
        self.assertIn("id IN (1,16)", logged)
        self.assertNotIn("id IN (99)", logged)

    def test_rejects_invalid_user_ids(self) -> None:
        proc, logged = self.run_probe(USER_IDS="1,abc")
        self.assertEqual(proc.returncode, 2, proc.stderr + proc.stdout)
        self.assertIn("bad USER_IDS", proc.stderr)
        self.assertEqual(logged, "")

    def test_empty_discovery_exits_empty_window(self) -> None:
        proc, _logged = self.run_probe(FAKE_DISCOVERY_IDS="")
        self.assertEqual(proc.returncode, 3, proc.stderr + proc.stdout)
        self.assertIn("no active users found", proc.stderr)

    def test_discovery_failure_is_not_reported_as_empty_set(self) -> None:
        proc, _logged = self.run_probe(FAKE_DISCOVERY_FAIL="1")
        self.assertEqual(proc.returncode, 1, proc.stderr + proc.stdout)
        self.assertIn("active-user discovery failed", proc.stderr)
        self.assertIn("connection refused", proc.stderr)
        self.assertNotIn("no active users found", proc.stderr)

    def test_rejects_invalid_window_minutes(self) -> None:
        proc, logged = self.run_probe(USER_IDS="1", WINDOW_MINUTES="30;drop")
        self.assertEqual(proc.returncode, 2, proc.stderr + proc.stdout)
        self.assertIn("bad WINDOW_MINUTES", proc.stderr)
        self.assertEqual(logged, "")

    def test_wow_usage_emits_delta_pct_from_single_two_window_scan(self) -> None:
        proc, logged = self.run_probe(USER_IDS="1,16")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertIn("AS delta_reqs_pct", logged)
        self.assertIn("AS delta_cost_pct", logged)
        self.assertIn("created_at >= now() - 2*", logged)
        self.assertIn("(created_at >= now() - interval '30 minutes') AS is_cur", logged)
        self.assertNotIn("previous window, for 环比", proc.stdout)

    def test_wow_errors_are_per_user_not_independent_top40(self) -> None:
        proc, logged = self.run_probe(USER_IDS="1,16")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertIn("AS delta_n_pct", logged)
        error_wow = [q for q in logged.split(";\n") if "AS delta_n_pct" in q]
        self.assertEqual(len(error_wow), 1, logged)
        self.assertNotIn("ORDER BY n DESC LIMIT 40", error_wow[0])

    def test_trailing_24h_baseline_excludes_current_window(self) -> None:
        proc, logged = self.run_probe(USER_IDS="1,16", WINDOW_MINUTES="15")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertIn("interval '24 hours'", logged)
        self.assertIn("created_at < now() - interval '15 minutes'", logged)
        self.assertIn("AS avg_reqs_per_window_24h", logged)
        self.assertIn("AS max_per_window_24h", logged)
        self.assertIn(" / (15*60)", logged)


if __name__ == "__main__":
    unittest.main()
