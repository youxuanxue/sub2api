#!/usr/bin/env python3
"""Validation tests for probe-post-release-tick.sh.

The script normally runs on the prod host via SSM and reads Docker logs. These
tests fake only the docker CLI and active-color file so the blue/green container
resolution contract is pinned without AWS or Docker.

The contract is running-ness, not existence: a probe that reads a STOPPED
container reports its stale state as live runtime, which is worse than
reporting unknown because it looks like a healthy answer.
"""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-post-release-tick.sh"

# Fake docker honouring `inspect --format {{.State.Running}}`. STATE_<NAME> env
# vars (dashes -> underscores, upper-cased) drive per-container state so a test
# can express "exists but stopped" — the case the old existence-only resolver
# silently accepted.
_FAKE_DOCKER = """\
#!/usr/bin/env bash
set -u
if [ "$1" = inspect ]; then
  name="${@: -1}"
  key="$(printf '%s' "$name" | tr '-' '_' | tr '[:lower:]' '[:upper:]')"
  var="STATE_${key}"
  state="${!var:-missing}"
  case "$state" in
    running) echo true; exit 0 ;;
    stopped) echo false; exit 0 ;;
    *) exit 1 ;;
  esac
fi
if [ "$1" = logs ]; then
  cat <<'LOGS'
2026-06-24T05:00:00Z INFO http request completed {"request_id":"r1","path":"/v1/messages","status_code":200}
2026-06-24T05:00:00Z INFO http request completed {"request_id":"r2","path":"/admin/users/42","status_code":200}
2026-06-24T05:00:01Z INFO anthropic_downstream_kiro_oauth_403_skip_penalty
LOGS
  exit 0
fi
exit 2
"""


class ProbePostReleaseTickTest(unittest.TestCase):
    def run_probe(
        self,
        *,
        states: dict[str, str],
        active_color: str | None,
        traffic_paths: str = "/v1/messages",
    ) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            fakebin = tmp / "bin"
            fakebin.mkdir()
            docker = fakebin / "docker"
            docker.write_text(textwrap.dedent(_FAKE_DOCKER), encoding="utf-8")
            docker.chmod(0o755)

            active_file = tmp / "active-color"
            if active_color is not None:
                active_file.write_text(active_color, encoding="utf-8")

            env = {
                **os.environ,
                "PATH": f"{fakebin}:{os.environ.get('PATH', '')}",
                "ACTIVE_COLOR_FILE": str(active_file),
                "HOOK_PATTERNS": "anthropic_downstream_kiro_oauth_403_skip_penalty",
                "TRAFFIC_PATHS": traffic_paths,
            }
            for name, state in states.items():
                env[f"STATE_{name.replace('-', '_').upper()}"] = state

            return subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

    @staticmethod
    def meta_of(stdout: str) -> dict | None:
        for line in stdout.splitlines():
            if line.startswith("{") and '"container_resolution"' in line:
                return json.loads(line)
        return None

    def test_syntax_clean(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SCRIPT)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_auto_container_resolves_active_color(self) -> None:
        proc = self.run_probe(states={"tokenkey-green": "running"}, active_color="green\n")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn('"container": "tokenkey-green"', proc.stdout)
        self.assertIn('"pattern": "anthropic_downstream_kiro_oauth_403_skip_penalty", "count": 1', proc.stdout)

        meta = self.meta_of(proc.stdout)
        self.assertIsNotNone(meta)
        assert meta is not None
        self.assertIn("active-color=green", meta["container_resolution"])
        self.assertIn("tokenkey-green is running", meta["container_resolution"])

        traffic = next(
            json.loads(line)
            for line in proc.stdout.splitlines()
            if line.startswith("{") and '"completed_total"' in line
        )
        self.assertEqual(traffic["completed_total"], 2)
        self.assertEqual(traffic["path_counts"], {"/v1/messages": 1})
        self.assertNotIn("/admin/users/42", traffic["path_counts"])

    def test_auto_container_accepts_unique_running_candidate(self) -> None:
        proc = self.run_probe(states={"tokenkey": "running"}, active_color=None)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn('"container": "tokenkey"', proc.stdout)

        meta = self.meta_of(proc.stdout)
        assert meta is not None
        self.assertIn("unique running candidate tokenkey", meta["container_resolution"])

    def test_active_color_target_stopped_falls_back_to_unique_running(self) -> None:
        proc = self.run_probe(
            states={"tokenkey-green": "stopped", "tokenkey-blue": "running"},
            active_color="green\n",
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn('"container": "tokenkey-blue"', proc.stdout)

        meta = self.meta_of(proc.stdout)
        assert meta is not None
        self.assertIn("tokenkey-green is not running", meta["container_resolution"])

    def test_stopped_container_is_never_selected(self) -> None:
        # The pre-consolidation resolver only asked whether a container existed,
        # so this shape selected tokenkey-green and reported its stale env as
        # live runtime. Unknown is the correct answer instead.
        proc = self.run_probe(states={"tokenkey-green": "stopped"}, active_color="green\n")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("unresolved", proc.stdout)
        self.assertNotIn('"container": "tokenkey-green"', proc.stdout)

    def test_several_running_candidates_are_ambiguous(self) -> None:
        # Mid blue/green both colours run. A positional first-match would pick
        # one arbitrarily; ambiguity must fail closed.
        proc = self.run_probe(
            states={"tokenkey-blue": "running", "tokenkey-green": "running"},
            active_color=None,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("unresolved", proc.stdout)

    def test_no_running_candidate_is_unknown(self) -> None:
        proc = self.run_probe(states={}, active_color=None)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("unresolved", proc.stdout)


if __name__ == "__main__":
    unittest.main()
