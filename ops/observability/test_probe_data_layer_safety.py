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
    ) -> tuple[dict[str, object], str]:
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            fakebin = root / "bin"
            fakebin.mkdir()
            calls = root / "docker-calls.log"
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
            }
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
            line = next(
                (line for line in proc.stdout.splitlines() if line.startswith("TELEMETRYSTATS ")),
                None,
            )
            self.assertIsNotNone(line, msg=proc.stdout)
            assert line is not None
            return json.loads(line.removeprefix("TELEMETRYSTATS ")), calls.read_text(encoding="utf-8")

    def test_active_color_running_container_wins(self) -> None:
        signal, calls = self.run_probe(
            active_color="green\n",
            states={"tokenkey": "running", "tokenkey-blue": "running", "tokenkey-green": "running"},
            telemetry={"tokenkey-green": "true"},
        )
        self.assertIs(signal["enabled"], True)
        self.assertIn("tokenkey-green", calls)

    def test_stopped_active_target_does_not_win_ambiguous_fallback(self) -> None:
        signal, _ = self.run_probe(
            active_color="green\n",
            states={"tokenkey": "running", "tokenkey-blue": "running", "tokenkey-green": "stopped"},
            telemetry={"tokenkey": "true", "tokenkey-blue": "true", "tokenkey-green": "true"},
        )
        self.assertEqual(signal, {"probe_ok": False, "enabled": None})

    def test_unique_running_fallback_is_selected(self) -> None:
        signal, calls = self.run_probe(
            states={"tokenkey-blue": "running"},
            telemetry={"tokenkey-blue": "true"},
        )
        self.assertIs(signal["enabled"], True)
        self.assertIn("tokenkey-blue", calls)

    def test_zero_running_candidates_fails_closed(self) -> None:
        signal, _ = self.run_probe(states={}, telemetry={})
        self.assertEqual(signal, {"probe_ok": False, "enabled": None})

    def test_multiple_running_candidates_fail_closed(self) -> None:
        signal, _ = self.run_probe(
            states={"tokenkey-blue": "running", "tokenkey-green": "running"},
            telemetry={"tokenkey-blue": "true", "tokenkey-green": "true"},
        )
        self.assertEqual(signal, {"probe_ok": False, "enabled": None})

    def test_explicit_stopped_container_fails_closed(self) -> None:
        signal, calls = self.run_probe(
            app_container="tokenkey-green",
            states={"tokenkey": "running", "tokenkey-green": "stopped"},
            telemetry={"tokenkey": "true", "tokenkey-green": "true"},
        )
        self.assertEqual(signal, {"probe_ok": False, "enabled": None})
        self.assertNotIn("Config.Env tokenkey\n", calls)


if __name__ == "__main__":
    unittest.main()
