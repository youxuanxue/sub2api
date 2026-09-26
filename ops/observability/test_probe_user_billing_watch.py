#!/usr/bin/env python3
"""Behavior tests for probe-user-billing-watch.sh user discovery."""
from __future__ import annotations

import importlib.util
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
    if printf '%s' "$sql" | grep -Fq 'string_agg(id::text'; then
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
    def run_probe(self, *, extra_sql: str = "", **env_overrides: str) -> tuple[subprocess.CompletedProcess[str], str]:
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
            script = SCRIPT
            if extra_sql:
                script = pathlib.Path(tmp) / "probe.sh"
                script.write_text(SCRIPT.read_text() + '\n$PSQL -c "' + extra_sql + '"\n')
            proc = subprocess.run(
                ["bash", str(script)],
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
        self.assertIn("string_agg(id::text", logged)
        self.assertIn("id IN (1,6,16)", logged)
        self.assertIn("'1,6,16'::text", logged)

    def test_user_ids_override_skips_discovery_query(self) -> None:
        proc, logged = self.run_probe(USER_IDS="1,16", FAKE_DISCOVERY_IDS="99")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertNotIn("string_agg(id::text", logged)
        self.assertIn("id IN (1,16)", logged)
        self.assertNotIn("id IN (99)", logged)

    def test_other_string_aggregation_does_not_trigger_discovery_mock(self) -> None:
        proc, logged = self.run_probe(
            USER_IDS="1,16", FAKE_DISCOVERY_FAIL="1",
            extra_sql="SELECT string_agg(g.name, ', ') FROM groups g WHERE g.deleted_at IS NULL;",
        )
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertIn("string_agg(g.name", logged)
        self.assertNotIn("string_agg(id::text", logged)

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

    def test_user_facing_failures_carry_model_account_and_root_cause(self) -> None:
        proc, logged = self.run_probe(USER_IDS="1,16")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        # Split on the logged-query boundary, not on ";\n" — SQL comments inside a
        # statement may themselves contain ";\n" and would truncate the match.
        failures = [q for q in logged.split("\nSELECT row_to_json") if "AS root_cause_sample" in q]
        self.assertEqual(len(failures), 1, logged)
        q = failures[0]
        # recovered-200 must not show up as a user-facing failure
        self.assertIn("status_code IS DISTINCT FROM 200", q)
        # 499 is the caller hanging up, not a gateway failure — see
        # docs/approved/client-closed-499-ssot.md
        self.assertIn("status_code IS DISTINCT FROM 499", q)
        # terminal-impact basics resolved in-query, not left to a manual join
        self.assertIn("LEFT JOIN accounts a ON a.id = e.account_id", q)
        self.assertIn("AS account_name", q)
        self.assertIn("AS account_platform", q)
        self.assertIn("AS group_name", q)
        self.assertIn("e.model", q)
        # soft-deleted accounts keep status=active, so the ghost must be visible
        # rather than silently read as a live account
        self.assertIn("AS account_soft_deleted", q)

    def test_accounts_join_carries_soft_delete_marker_where_the_gate_reads_it(self) -> None:
        """The soft-delete gate scans FORWARD from the FROM/JOIN line to the first
        ';', so a marker on the preceding line is invisible to it and the join
        reads as an unfiltered `accounts` query. Assert placement the way the gate
        actually parses it, not mere presence of the marker string anywhere."""
        gate_path = ROOT / "scripts" / "checks" / "ops-sql-soft-delete.py"
        spec = importlib.util.spec_from_file_location("ops_sql_soft_delete", gate_path)
        gate = importlib.util.module_from_spec(spec)
        assert spec.loader is not None
        spec.loader.exec_module(gate)

        lines = SCRIPT.read_text(encoding="utf-8").splitlines()
        seen = 0
        for idx, line in enumerate(lines):
            for match in gate.FROM_RE.finditer(line):
                if match.group(1) != "accounts":
                    continue
                seen += 1
                window = gate._statement_window(lines, idx)
                self.assertIn(
                    gate.MARKER, window,
                    f"accounts join at line {idx + 1} has no soft-delete marker in the "
                    f"statement window the gate reads",
                )
        self.assertEqual(seen, 1, "expected exactly one accounts join in this probe")

    def test_user_facing_failures_omit_never_written_columns(self) -> None:
        """provider_error_code / network_error_type exist in migration 033 but have
        no writer in backend/internal, so selecting them only advertises empty
        strings as if they were root-cause signal."""
        proc, logged = self.run_probe(USER_IDS="1,16")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertNotIn("provider_error_code", logged)
        self.assertNotIn("network_error_type", logged)

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
