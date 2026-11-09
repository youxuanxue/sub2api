#!/usr/bin/env python3
"""Behavior tests for the host-side data-layer safety probe."""

from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest


_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-data-layer-safety.sh"


class ProbeDataLayerSafetyTest(unittest.TestCase):
    def run_probe(
        self,
        *,
        states: dict[str, str],
        partition_sql: str | None = None,
        partition_failure: bool = False,
        postgres_container: str = "tokenkey-postgres",
        postgres_states: dict[str, str] | None = None,
        expect_exit: int = 0,
    ) -> tuple[dict[str, object], str]:
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            fakebin = root / "bin"
            fakebin.mkdir()
            calls = root / "docker-calls.log"
            partition_stdin = root / "partition-stdin.sql"

            docker = fakebin / "docker"
            docker.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    set -u
                    printf '%s\n' "$*" >> "$FAKE_DOCKER_CALLS"
                    if [ "$1" = inspect ]; then
                      name="${@: -1}"
                      key="$(printf '%s' "$name" | tr '-' '_' | tr '[:lower:]' '[:upper:]')"
                      state_var="STATE_${key}"
                      state="${!state_var:-missing}"
                      case "$*" in
                        *State.Running*)
                          case "$state" in
                            running) printf '%s\n' true ;;
                            stopped) printf '%s\n' false ;;
                            *) exit 1 ;;
                          esac
                          exit 0
                          ;;
                      esac
                      exit 2
                    fi
                    if [ "$1" = exec ]; then
                      has_i=false
                      for arg in "$@"; do
                        if [ "$arg" = "-i" ]; then
                          has_i=true
                          break
                        fi
                      done
                      [ "$has_i" = true ] || exit 2
                      query="$(cat)"
                      printf '%s' "$query" > "$FAKE_PARTITION_STDIN"
                      [ "${PARTITION_FAILURE:-false}" = true ] && exit 1
                      printf '%s\n' 'PARTITIONSTATS {"probe_ok":true}'
                      exit 0
                    fi
                    exit 2
                    """
                ),
                encoding="utf-8",
            )
            docker.chmod(0o755)

            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "FAKE_DOCKER_CALLS": str(calls),
                "FAKE_PARTITION_STDIN": str(partition_stdin),
                "PARTITION_FAILURE": "true" if partition_failure else "false",
            }
            if partition_sql is not None:
                env["PARTITION_COVERAGE_SQL"] = partition_sql
            for name in ("tokenkey", "tokenkey-blue", "tokenkey-green"):
                key = name.replace("-", "_").upper()
                env[f"STATE_{key}"] = states.get(name, "missing")
            # Stage0 vs Tencent layout-exception PG name; exactly one runs unless
            # postgres_states spells the boundary under test.
            if postgres_states is None:
                env["STATE_" + postgres_container.replace("-", "_").upper()] = "running"
            else:
                for name, state in postgres_states.items():
                    env["STATE_" + name.replace("-", "_").upper()] = state

            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, expect_exit, msg=proc.stderr + proc.stdout)
            if expect_exit != 0:
                return {}, proc.stderr + proc.stdout
            self.assertNotIn("TELEMETRYSTATS", proc.stdout)
            partition_line = next(
                (line for line in proc.stdout.splitlines() if line.startswith("PARTITIONSTATS ")),
                None,
            )
            self.assertIsNotNone(partition_line, msg=proc.stdout)
            assert partition_line is not None
            calls_text = calls.read_text(encoding="utf-8")
            if partition_stdin.exists():
                calls_text += "\nPARTITION_SQL:\n" + partition_stdin.read_text(encoding="utf-8")
            return (
                json.loads(partition_line.removeprefix("PARTITIONSTATS ")),
                calls_text,
            )

    def test_executes_shared_partition_sql_through_container_stdin(self) -> None:
        partition, calls = self.run_probe(
            states={"tokenkey": "running"},
        )
        self.assertEqual(partition, {"probe_ok": True})
        self.assertIn("exec -i -e", calls)
        self.assertIn("PARTITION_SQL:", calls)
        self.assertIn("pg_get_expr(child.relpartbound, child.oid, true)", calls)

    def test_layout_exception_postgres_name_is_resolved(self) -> None:
        partition, calls = self.run_probe(
            states={"tokenkey": "running"},
            postgres_container="tk-archive-standby",
        )
        self.assertEqual(partition, {"probe_ok": True})
        self.assertIn("tk-archive-standby psql", calls)
        self.assertNotIn("tokenkey-postgres psql", calls)

    def test_stopped_stage0_postgres_does_not_mask_layout_exception(self) -> None:
        partition, calls = self.run_probe(
            states={"tokenkey": "running"},
            postgres_states={"tokenkey-postgres": "stopped", "tk-archive-standby": "running"},
        )
        self.assertEqual(partition, {"probe_ok": True})
        self.assertIn("tk-archive-standby psql", calls)
        self.assertNotIn("tokenkey-postgres psql", calls)

    def test_two_running_postgres_candidates_fail_closed(self) -> None:
        _, text = self.run_probe(
            states={"tokenkey": "running"},
            postgres_states={"tokenkey-postgres": "running", "tk-archive-standby": "running"},
            expect_exit=1,
        )
        self.assertIn("unresolved", text)

    def test_no_running_postgres_fails_closed(self) -> None:
        _, text = self.run_probe(
            states={"tokenkey": "running"},
            postgres_states={"tokenkey-postgres": "missing", "tk-archive-standby": "stopped"},
            expect_exit=1,
        )
        self.assertIn("unresolved", text)

    def test_explicit_missing_partition_sql_fails_closed(self) -> None:
        partition, _ = self.run_probe(
            states={"tokenkey": "running"},
            partition_sql="/definitely/missing/partition-coverage.sql",
        )
        self.assertEqual(partition, {"probe_ok": False})

    def test_partition_sql_failure_fails_closed(self) -> None:
        partition, _ = self.run_probe(
            states={"tokenkey": "running"},
            partition_failure=True,
        )
        self.assertEqual(partition, {"probe_ok": False})


if __name__ == "__main__":
    unittest.main()
