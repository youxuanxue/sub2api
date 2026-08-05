#!/usr/bin/env python3
"""Behavior tests for the container entrypoint's data ownership gate."""
from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
ENTRYPOINT = REPO_ROOT / "deploy" / "docker-entrypoint.sh"


class DockerEntrypointTest(unittest.TestCase):
    def _run_as_fake_root(self, *, skip_chown: bool, data_owner: str = "0:0") -> list[str]:
        with tempfile.TemporaryDirectory(prefix="entrypoint-test-") as raw:
            root = pathlib.Path(raw)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            calls = root / "calls.log"
            scripts = {
                "id": "#!/bin/sh\necho 0\n",
                "mkdir": f"#!/bin/sh\necho mkdir \"$@\" >> {calls!s}\n",
                "chown": f"#!/bin/sh\necho chown \"$@\" >> {calls!s}\n",
                "stat": f"#!/bin/sh\necho {data_owner}\n",
                "su-exec": f"#!/bin/sh\necho su-exec \"$@\" >> {calls!s}\nexit 0\n",
            }
            for name, body in scripts.items():
                path = bin_dir / name
                path.write_text(body, encoding="utf-8")
                path.chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ.get('PATH', '')}",
            }
            if skip_chown:
                env["SKIP_DATA_CHOWN"] = "1"
            proc = subprocess.run(
                ["sh", str(ENTRYPOINT), "/app/sub2api"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr)
            return calls.read_text(encoding="utf-8").splitlines()

    def test_default_root_start_fixes_data_ownership(self) -> None:
        calls = self._run_as_fake_root(skip_chown=False)
        self.assertIn("mkdir -p /app/data", calls)
        self.assertIn("chown -R sub2api:sub2api /app/data", calls)
        self.assertTrue(any(line.startswith("su-exec sub2api ") for line in calls))

    def test_skip_data_chown_avoids_recursive_walk_but_still_drops_privileges(self) -> None:
        calls = self._run_as_fake_root(skip_chown=True)
        self.assertIn("mkdir -p /app/data", calls)
        self.assertFalse(any(line.startswith("chown ") for line in calls), calls)
        self.assertTrue(any(line.startswith("su-exec sub2api ") for line in calls))

    def test_owned_bind_mount_auto_skips_recursive_walk_for_existing_edges(self) -> None:
        calls = self._run_as_fake_root(skip_chown=False, data_owner="1000:1000")
        self.assertFalse(any(line.startswith("chown ") for line in calls), calls)
        self.assertTrue(any(line.startswith("su-exec sub2api ") for line in calls))


if __name__ == "__main__":
    unittest.main()
