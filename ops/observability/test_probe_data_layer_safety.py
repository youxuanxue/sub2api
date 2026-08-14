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
        telemetry: dict[str, str],
        active_color: str | None = None,
        app_container: str = "auto",
        partition_sql: str | None = None,
        partition_failure: bool = False,
    ) -> tuple[dict[str, object], dict[str, object], str]:
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            fakebin = root / "bin"
            fakebin.mkdir()
            calls = root / "docker-calls.log"
            partition_stdin = root / "partition-stdin.sql"
            active_file = root / "active-color"
            if active_color is not None:
                active_file.write_text(active_color, encoding="utf-8")

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
                        *Config.Env*)
                          [ "$state" = running ] || exit 1
                          telemetry_var="TELEMETRY_${key}"
                          enabled="${!telemetry_var:-false}"
                          printf 'TELEMETRY_ARCHIVE_ENABLED=%s\n' "$enabled"
                          exit 0
                          ;;
                      esac
                      exit 2
                    fi
                    if [ "$1" = exec ]; then
                      if [[ "$*" == *telemetry_archive_shadow* ]]; then
                        printf '%s\n' 'TELEMETRYSTATS {"probe_ok":true,"enabled":true,"last_result":{"dropped":0,"failed":0}}'
                      else
                        query="$(cat)"
                        printf '%s' "$query" > "$FAKE_PARTITION_STDIN"
                        [ "${PARTITION_FAILURE:-false}" = true ] && exit 1
                        printf '%s\n' 'PARTITIONSTATS {"probe_ok":true}'
                      fi
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
                "ACTIVE_COLOR_FILE": str(active_file),
                "APP_CONTAINER": app_container,
                "FAKE_DOCKER_CALLS": str(calls),
                "FAKE_PARTITION_STDIN": str(partition_stdin),
                "PARTITION_FAILURE": "true" if partition_failure else "false",
            }
            if partition_sql is not None:
                env["PARTITION_COVERAGE_SQL"] = partition_sql
            for name in ("tokenkey", "tokenkey-blue", "tokenkey-green"):
                key = name.replace("-", "_").upper()
                env[f"STATE_{key}"] = states.get(name, "missing")
                env[f"TELEMETRY_{key}"] = telemetry.get(name, "false")

            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
            telemetry_line = next(
                (line for line in proc.stdout.splitlines() if line.startswith("TELEMETRYSTATS ")),
                None,
            )
            partition_line = next(
                (line for line in proc.stdout.splitlines() if line.startswith("PARTITIONSTATS ")),
                None,
            )
            self.assertIsNotNone(telemetry_line, msg=proc.stdout)
            self.assertIsNotNone(partition_line, msg=proc.stdout)
            assert telemetry_line is not None and partition_line is not None
            calls_text = calls.read_text(encoding="utf-8")
            if partition_stdin.exists():
                calls_text += "\nPARTITION_SQL:\n" + partition_stdin.read_text(encoding="utf-8")
            return (
                json.loads(telemetry_line.removeprefix("TELEMETRYSTATS ")),
                json.loads(partition_line.removeprefix("PARTITIONSTATS ")),
                calls_text,
            )

    def test_active_color_running_container_wins(self) -> None:
        signal, _, calls = self.run_probe(
            active_color="green\n",
            states={"tokenkey": "running", "tokenkey-blue": "running", "tokenkey-green": "running"},
            telemetry={"tokenkey-green": "true"},
        )
        self.assertIs(signal["enabled"], True)
        self.assertIn("tokenkey-green", calls)

    def test_stopped_active_target_does_not_win_ambiguous_fallback(self) -> None:
        signal, _, _ = self.run_probe(
            active_color="green\n",
            states={"tokenkey": "running", "tokenkey-blue": "running", "tokenkey-green": "stopped"},
            telemetry={"tokenkey": "true", "tokenkey-blue": "true", "tokenkey-green": "true"},
        )
        self.assertEqual(signal, {"probe_ok": False, "enabled": None})

    def test_unique_running_fallback_is_selected(self) -> None:
        signal, _, calls = self.run_probe(
            states={"tokenkey-blue": "running"},
            telemetry={"tokenkey-blue": "true"},
        )
        self.assertIs(signal["enabled"], True)
        self.assertIn("tokenkey-blue", calls)

    def test_zero_running_candidates_fails_closed(self) -> None:
        signal, _, _ = self.run_probe(states={}, telemetry={})
        self.assertEqual(signal, {"probe_ok": False, "enabled": None})

    def test_multiple_running_candidates_fail_closed(self) -> None:
        signal, _, _ = self.run_probe(
            states={"tokenkey-blue": "running", "tokenkey-green": "running"},
            telemetry={"tokenkey-blue": "true", "tokenkey-green": "true"},
        )
        self.assertEqual(signal, {"probe_ok": False, "enabled": None})

    def test_explicit_stopped_container_fails_closed(self) -> None:
        signal, _, calls = self.run_probe(
            app_container="tokenkey-green",
            states={"tokenkey": "running", "tokenkey-green": "stopped"},
            telemetry={"tokenkey": "true", "tokenkey-green": "true"},
        )
        self.assertEqual(signal, {"probe_ok": False, "enabled": None})
        self.assertNotIn("Config.Env tokenkey\n", calls)

    def test_executes_shared_partition_sql_through_container_stdin(self) -> None:
        _, partition, calls = self.run_probe(
            states={"tokenkey": "running"},
            telemetry={"tokenkey": "false"},
        )
        self.assertEqual(partition, {"probe_ok": True})
        self.assertIn("PARTITION_SQL:", calls)
        self.assertIn("pg_get_expr(child.relpartbound, child.oid, true)", calls)

    def test_explicit_missing_partition_sql_fails_closed(self) -> None:
        _, partition, _ = self.run_probe(
            states={"tokenkey": "running"},
            telemetry={"tokenkey": "false"},
            partition_sql="/definitely/missing/partition-coverage.sql",
        )
        self.assertEqual(partition, {"probe_ok": False})

    def test_partition_sql_failure_fails_closed(self) -> None:
        _, partition, _ = self.run_probe(
            states={"tokenkey": "running"},
            telemetry={"tokenkey": "false"},
            partition_failure=True,
        )
        self.assertEqual(partition, {"probe_ok": False})


if __name__ == "__main__":
    unittest.main()
