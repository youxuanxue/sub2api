#!/usr/bin/env python3
"""Unit tests for scripts/stage0/rollout-edges.sh.

Uses a fake repo root with fake gh and dispatch scripts. No GitHub/network.
"""
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import tempfile
import textwrap
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "rollout-edges.sh"


class RolloutEdgesTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.repo = pathlib.Path(self._tmp.name) / "repo"
        self.repo.mkdir()
        (self.repo / "scripts/stage0").mkdir(parents=True)
        (self.repo / "deploy/aws/stage0").mkdir(parents=True)
        shutil.copy(_SCRIPT, self.repo / "scripts/stage0/rollout-edges.sh")
        (self.repo / "scripts/stage0/rollout-edges.sh").chmod(0o755)
        self.fakebin = self.repo / "fakebin"
        self.fakebin.mkdir()
        self.events = self.repo / "events.log"

        (self.repo / "scripts/stage0/dispatch-edge-deploy.sh").write_text(
            textwrap.dedent(
                """\
                #!/usr/bin/env bash
                edge=""
                while [ "$#" -gt 0 ]; do
                  case "$1" in
                    --edge-id) edge="$2"; shift 2 ;;
                    *) shift ;;
                  esac
                done
                echo "dispatch $edge" >> events.log
                case "$edge" in
                  a) run=101 ;;
                  b) run=102 ;;
                  c) run=103 ;;
                  *) run=199 ;;
                esac
                echo "dispatch-edge-deploy: platform=lightsail workflow=deploy-edge-lightsail-stage0.yml edge=$edge op=upgrade tag=1.2.3 smoke_phase=infra"
                echo "https://github.com/o/r/actions/runs/$run"
                """
            ),
        )
        (self.repo / "scripts/stage0/dispatch-edge-deploy.sh").chmod(0o755)
        (self.repo / "deploy/aws/stage0/resolve-edge-target.py").write_text(
            "#!/usr/bin/env python3\nprint('a')\nprint('b')\nprint('c')\n",
        )
        (self.repo / "deploy/aws/stage0/resolve-edge-target.py").chmod(0o755)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _write_fake_gh(self, *, fail_run: str | None = None) -> None:
        fail_case = f'[ "$run" = "{fail_run}" ] && exit 1' if fail_run else ""
        (self.fakebin / "gh").write_text(
            textwrap.dedent(
                f"""\
                #!/usr/bin/env bash
                cmd="$1 $2"
                if [ "$cmd" = "run watch" ]; then
                  run="$3"
                  echo "watch $run" >> events.log
                  {fail_case}
                  exit 0
                fi
                if [ "$cmd" = "run view" ]; then
                  run="$3"
                  if [ "$4" = "--log" ]; then
                    echo "log $run" >> events.log
                    echo "tk_edge_post_deploy_smoke: OK phase=infra"
                    exit 0
                  fi
                  if [ "$run" = "{fail_run}" ]; then
                    printf 'completed/failure'
                  else
                    printf 'completed/success'
                  fi
                  exit 0
                fi
                exit 2
                """
            ),
        )
        (self.fakebin / "gh").chmod(0o755)

    def _run(self, *args: str) -> subprocess.CompletedProcess:
        env = {**os.environ, "PATH": f"{self.fakebin}:{os.environ.get('PATH', '')}"}
        return subprocess.run(
            ["bash", "scripts/stage0/rollout-edges.sh", *args],
            cwd=self.repo,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_parallel_dispatches_batch_before_watching(self) -> None:
        self._write_fake_gh()
        proc = self._run("--tag", "1.2.3", "--edges", "a b c", "--parallel", "2")
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("rollout-edges: ALL_OK n=3", proc.stdout)
        events = self.events.read_text().splitlines()
        self.assertEqual(events[:2], ["dispatch a", "dispatch b"])
        self.assertIn("watch 101", events[2])
        self.assertIn("watch 102", events[4])
        self.assertEqual(events[6], "dispatch c")

    def test_parallel_failure_stops_next_batch(self) -> None:
        self._write_fake_gh(fail_run="102")
        proc = self._run("--tag", "1.2.3", "--edges", "a b c", "--parallel", "2")
        self.assertEqual(proc.returncode, 1, msg=proc.stderr + proc.stdout)
        events = self.events.read_text().splitlines()
        self.assertIn("dispatch a", events)
        self.assertIn("dispatch b", events)
        self.assertNotIn("dispatch c", events)
        self.assertIn("b:102", proc.stderr)

    def test_rejects_bad_parallel(self) -> None:
        self._write_fake_gh()
        proc = self._run("--tag", "1.2.3", "--edges", "a", "--parallel", "0")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("--parallel must be >= 1", proc.stderr)


if __name__ == "__main__":
    unittest.main()
