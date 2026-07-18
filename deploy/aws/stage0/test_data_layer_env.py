#!/usr/bin/env python3
"""Behavior tests for fail-closed data-layer overlay loading."""

from __future__ import annotations

import os
import pathlib
import subprocess
import tempfile
import unittest


REPO = pathlib.Path(__file__).resolve().parents[3]
HELPER = REPO / "deploy/aws/stage0/tokenkey-data-layer-env.sh"


class DataLayerEnvTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.temp = pathlib.Path(self.temp_dir.name)
        self.env_file = self.temp / ".env"
        self.env_file.write_text(
            "DATABASE_HOST=postgres\nCOMPOSE_PROFILES=localpg,localredis\n"
        )
        self.marker = self.temp / ".rds-cutover-started"
        self.bin_dir = self.temp / "bin"
        self.bin_dir.mkdir()
        fake_aws = self.bin_dir / "aws"
        fake_aws.write_text(
            """#!/usr/bin/env bash
set -eu
case "${FAKE_AWS_MODE}" in
  success) printf '%s\\n' "${FAKE_OVERLAY}" ;;
  missing) echo 'ParameterNotFound: missing parameter' >&2; exit 254 ;;
  denied) echo 'AccessDeniedException: denied' >&2; exit 254 ;;
esac
"""
        )
        fake_aws.chmod(0o755)

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def run_fetch(
        self, mode: str, overlay: str = "", *, mark_external: bool = False
    ) -> subprocess.CompletedProcess:
        env = os.environ.copy()
        env.update(
            {
                "PATH": f"{self.bin_dir}:{env['PATH']}",
                "FAKE_AWS_MODE": mode,
                "FAKE_OVERLAY": overlay,
            }
        )
        command = [
            "bash",
            str(HELPER),
            "fetch-apply",
            "/tokenkey/test/stage0/data-layer-env",
            "us-east-1",
            str(self.env_file),
            str(self.marker),
        ]
        if mark_external:
            command.append("mark-external")
        return subprocess.run(
            command,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_explicit_missing_parameter_keeps_never_cut_over_host_local(self) -> None:
        proc = self.run_fetch("missing")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("DATABASE_HOST=postgres", self.env_file.read_text())

    def test_transient_ssm_error_refuses_local_fallback(self) -> None:
        proc = self.run_fetch("denied")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("refusing to guess local mode", proc.stderr)
        self.assertIn("DATABASE_HOST=postgres", self.env_file.read_text())

    def test_missing_parameter_after_rds_start_refuses_local_fallback(self) -> None:
        self.marker.touch()
        proc = self.run_fetch("missing")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("refusing stale-local fallback", proc.stderr)

    def test_valid_external_overlay_is_applied(self) -> None:
        proc = self.run_fetch(
            "success",
            "DATABASE_HOST=db.test.internal\nCOMPOSE_PROFILES=localredis",
            mark_external=True,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        rendered = self.env_file.read_text()
        self.assertIn("DATABASE_HOST=db.test.internal", rendered)
        self.assertIn("COMPOSE_PROFILES=localredis", rendered)
        self.assertTrue(self.marker.exists())

    def test_invalid_overlay_is_rejected_before_any_line_is_applied(self) -> None:
        proc = self.run_fetch(
            "success",
            "DATABASE_HOST=db.test.internal\nPOSTGRES_PASSWORD=bad value",
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertEqual(
            self.env_file.read_text(),
            "DATABASE_HOST=postgres\nCOMPOSE_PROFILES=localpg,localredis\n",
        )


if __name__ == "__main__":
    unittest.main()
