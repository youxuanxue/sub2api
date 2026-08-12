#!/usr/bin/env python3
"""Behavior tests for the prod QA Phase 2 live health probe shell."""

from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest


_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-qa-phase2-live-health.sh"


class ProbeQAPhase2LiveHealthTest(unittest.TestCase):
    def run_probe(
        self,
        *,
        receipt: dict | None = None,
        boundary_receipt: dict | None = None,
        psql_lines: list[str] | None = None,
    ) -> tuple[str, str]:
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            fakebin = root / "bin"
            fakebin.mkdir()
            receipt_path = root / "qa-maintenance-last-run.json"
            if receipt is not None:
                receipt_path.write_text(json.dumps(receipt), encoding="utf-8")
            boundary_receipt_path = root / "qa-boundary-last-run.json"
            if boundary_receipt is not None:
                boundary_receipt_path.write_text(json.dumps(boundary_receipt), encoding="utf-8")
            psql_log = root / "psql.log"
            psql_output = root / "psql.out"
            psql_output.write_text("\n".join(psql_lines or []) + "\n", encoding="utf-8")
            docker = fakebin / "docker"
            docker.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    set -u
                    if [ "$1" = exec ] && [[ "$*" == *" psql "* ]] && [[ "$*" == *" -f -"* ]]; then
                      has_i=false
                      for arg in "$@"; do
                        if [ "$arg" = "-i" ]; then
                          has_i=true
                          break
                        fi
                      done
                      if [ "$has_i" != true ]; then
                        exit 2
                      fi
                      cat >> "$PSQL_LOG"
                      cat "$PSQL_OUTPUT"
                      exit 0
                    fi
                    exit 2
                    """
                ),
                encoding="utf-8",
            )
            docker.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{fakebin}:{env['PATH']}"
            env["QA_MAINTENANCE_RECEIPT"] = str(receipt_path)
            env["QA_BOUNDARY_RECEIPT"] = str(boundary_receipt_path)
            env["PSQL_LOG"] = str(psql_log)
            env["PSQL_OUTPUT"] = str(psql_output)
            completed = subprocess.run(
                ["bash", str(_SCRIPT)],
                capture_output=True,
                text=True,
                check=False,
                env=env,
            )
            log = psql_log.read_text(encoding="utf-8") if psql_log.exists() else ""
            return completed.stdout + completed.stderr, log

    def test_probe_emits_sql_without_psql_variable_syntax(self) -> None:
        receipt = {
            "compensation": {"window_start": "2026-08-07T22:00:00Z"},
        }
        psql_lines = [
            'PHASE2HEARTBEAT {"last_result":"status=committed"}',
            'PHASE2ARCHIVE {"normal":null,"compensation":null,"terminal_failures_after_cutover":[]}',
            'PHASE2QARECORDS {"partition_owner":"default_only","default_rows":0,"non_default_rows":0}',
        ]
        output, sql_log = self.run_probe(receipt=receipt, psql_lines=psql_lines)
        self.assertIn("PHASE2SYSTEMD", output)
        self.assertIn("PHASE2RECEIPT", output)
        self.assertIn("PHASE2HEARTBEAT", output)
        self.assertIn("PHASE2BOUNDARYSYSTEMD", output)
        self.assertIn("PHASE2BOUNDARYRECEIPT null", output)
        self.assertNotIn(":'receipt_comp_window'", sql_log)
        self.assertIn("2026-08-07T22:00:00Z", sql_log)
        self.assertIn("comp_target AS", sql_log)

    def test_probe_without_receipt_uses_null_comp_target(self) -> None:
        psql_lines = [
            'PHASE2HEARTBEAT null',
            'PHASE2ARCHIVE {"normal":null,"compensation":null,"terminal_failures_after_cutover":[]}',
            'PHASE2QARECORDS {"partition_owner":"default_only","default_rows":0,"non_default_rows":0}',
        ]
        _, sql_log = self.run_probe(psql_lines=psql_lines)
        self.assertIn("SELECT NULL::timestamptz AS window_start", sql_log)

    def test_probe_emits_boundary_receipt_and_exact_catalog_coverage_query(self) -> None:
        boundary_receipt = {
            "schema_version": "qa-boundary-runner-v1",
            "run_id": "boundary-1",
            "trigger": "timer",
        }
        psql_lines = [
            'PHASE2HEARTBEAT null',
            'PHASE2ARCHIVE {"normal":null,"compensation":null,"terminal_failures_after_cutover":[]}',
            'PHASE2BOUNDARYHEARTBEAT {"last_result":"status=ok phase=boundary"}',
            'PHASE2QARECORDS {"future_coverage_canonical_hours":72}',
        ]
        output, sql_log = self.run_probe(
            boundary_receipt=boundary_receipt,
            psql_lines=psql_lines,
        )
        self.assertIn("PHASE2BOUNDARYRECEIPT", output)
        self.assertIn('"run_id": "boundary-1"', output)
        self.assertIn("generate_series(0, 71)", sql_log)
        self.assertIn("'qa_records_' || to_char", sql_log)
        self.assertIn("'YYYYMMDD_HH24'", sql_log)
        self.assertIn("cs.activate_t0_utc = cs.finalize_t0_utc", sql_log)
        self.assertIn("finalize_receipt_present", sql_log)
        self.assertIn("activate_t0_utc", sql_log)
        self.assertIn("activate_plan_hash", sql_log)
        self.assertIn("activate_applied_at", sql_log)
        self.assertIn("finalize_t0_utc", sql_log)
        self.assertIn("default_rows_after_t0", sql_log)
        self.assertIn("last_error_at, last_error, last_result", sql_log)
        self.assertIn("PHASE2BOUNDARYHEARTBEAT", output)
        lifecycle_sql = sql_log[
            sql_log.index("), lifecycle AS (") : sql_log.index("), boundary_heartbeat AS (")
        ]
        self.assertIn("c.relname = 'qa_records_' || to_char", lifecycle_sql)
        self.assertIn("+ interval '1 hour'", lifecycle_sql)

    def test_probe_uses_non_reserved_generate_series_alias(self) -> None:
        _, sql_log = self.run_probe()

        self.assertIn("AS g(hour_offset)", sql_log)
        self.assertIn("g.hour_offset", sql_log)
        self.assertNotIn("AS g(offset)", sql_log)

    def test_probe_without_docker_exec_i_skips_psql(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            fakebin = root / "bin"
            fakebin.mkdir()
            docker = fakebin / "docker"
            docker.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    set -u
                    if [ "$1" = exec ] && [[ "$*" == *" psql "* ]] && [[ "$*" == *" -f -"* ]]; then
                      exit 2
                    fi
                    exit 2
                    """
                ),
                encoding="utf-8",
            )
            docker.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{fakebin}:{env['PATH']}"
            completed = subprocess.run(
                ["bash", str(_SCRIPT)],
                capture_output=True,
                text=True,
                check=False,
                env=env,
            )
        output = completed.stdout + completed.stderr
        self.assertIn("PHASE2SYSTEMD", output)
        self.assertIn("PHASE2RECEIPT null", output)
        self.assertIn("PHASE2BOUNDARYSYSTEMD", output)
        self.assertIn("PHASE2BOUNDARYRECEIPT null", output)
        self.assertNotIn("PHASE2HEARTBEAT", output)
        self.assertNotIn("PHASE2ARCHIVE", output)
        self.assertNotIn("PHASE2QARECORDS", output)


if __name__ == "__main__":
    raise SystemExit(unittest.main())
