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
        psql_lines: list[str] | None = None,
    ) -> tuple[str, str]:
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            fakebin = root / "bin"
            fakebin.mkdir()
            receipt_path = root / "qa-maintenance-last-run.json"
            if receipt is not None:
                receipt_path.write_text(json.dumps(receipt), encoding="utf-8")
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


if __name__ == "__main__":
    raise SystemExit(unittest.main())
