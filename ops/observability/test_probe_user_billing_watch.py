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
        self.assertIn("COALESCE(e.status_code,0) >= 400", q)
        # 499 is the caller hanging up, not a gateway failure — see
        # docs/approved/client-closed-499-ssot.md
        self.assertIn("COALESCE(e.status_code,0) <> 499", q)
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
        """These ops_error_logs columns exist in migration 033 but have no writer in
        backend/internal, so reading them only advertises empty values as if they
        were signal. account_status is verified empty in prod (0 non-null of 1.07M
        rows over 7d), so the account's status must come from accounts.status and be
        named for what it is — current state, not state at failure time."""
        proc, logged = self.run_probe(USER_IDS="1,16")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertNotIn("provider_error_code", logged)
        self.assertNotIn("network_error_type", logged)
        self.assertNotIn("e.account_status", logged)

    def test_user_facing_failure_predicate_tracks_its_registered_owner(self) -> None:
        """"User-visible failure" already has an owner:
        backend/internal/repository/ops_repo_user_visible_failure_tk.go, whose
        predicate feeds the alert/SLA numerator. This probe must not drift into a
        second definition, so assert the owner still spells each clause the way this
        probe copies it — if the owner changes, this fails and the probe follows
        rather than silently disagreeing about what counts as hitting a user."""
        owner = (
            ROOT / "backend" / "internal" / "repository"
            / "ops_repo_user_visible_failure_tk.go"
        )
        self.assertTrue(owner.exists(), f"owner moved: {owner}")
        owner_src = owner.read_text(encoding="utf-8")
        for clause in (
            'COALESCE(status_code, 0) >= 400',
            'COALESCE(status_code, 0) <> 499',
            'context canceled',
            'COALESCE(user_id, deleted_key_owner_user_id)',
        ):
            self.assertIn(clause, owner_src, f"owner no longer defines: {clause}")

        proc, logged = self.run_probe(USER_IDS="1,16")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        failures = [q for q in logged.split("\nSELECT row_to_json") if "AS root_cause_sample" in q]
        self.assertEqual(len(failures), 1, logged)
        q = failures[0]
        self.assertIn("COALESCE(e.status_code,0) >= 400", q)
        self.assertIn("COALESCE(e.status_code,0) <> 499", q)
        # the text form of a caller disconnect is the same event as a labelled 499
        self.assertIn("NOT LIKE '%context cancel%'", q)
        # a failure on a since-deleted key still belongs to its owner
        self.assertIn("COALESCE(e.user_id, e.deleted_key_owner_user_id)", q)

    def test_account_status_is_named_as_current_not_historical(self) -> None:
        """The joined account status is the account's state right now, not its state
        when the failure happened. The alias must say so, or a report will narrate a
        live status as the cause of a past failure."""
        proc, logged = self.run_probe(USER_IDS="1,16")
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        failures = [q for q in logged.split("\nSELECT row_to_json") if "AS root_cause_sample" in q]
        self.assertEqual(len(failures), 1, logged)
        q = failures[0]
        self.assertIn("AS account_status_now", q)
        # a bare `AS account_status` alias would read as status-at-failure-time
        self.assertNotRegex(q, r"AS account_status\b")

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
