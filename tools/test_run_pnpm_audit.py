from __future__ import annotations

import json
import io
import subprocess
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path

from tools.run_pnpm_audit import run_audit


VALID_REPORT = {
    "advisories": {},
    "metadata": {"vulnerabilities": {"high": 0, "critical": 0}},
}


def completed(stdout: object, returncode: int = 0) -> subprocess.CompletedProcess[str]:
    return subprocess.CompletedProcess(
        args=["pnpm", "audit"],
        returncode=returncode,
        stdout=json.dumps(stdout),
        stderr="",
    )


class RunPnpmAuditTest(unittest.TestCase):
    def test_accepts_valid_vulnerability_report_with_nonzero_exit(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "audit.json"

            result = run_audit(
                output,
                attempts=3,
                retry_delay_seconds=0,
                timeout_seconds=1,
                runner=lambda *args, **kwargs: completed(VALID_REPORT, returncode=1),
            )

            self.assertEqual(result, 0)
            self.assertEqual(json.loads(output.read_text(encoding="utf-8")), VALID_REPORT)

    def test_retries_invalid_registry_response_then_writes_valid_report(self) -> None:
        responses = iter(
            [
                completed({"message": "Bad Gateway"}, returncode=1),
                completed(VALID_REPORT),
            ]
        )
        sleeps: list[float] = []
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "audit.json"

            with redirect_stderr(io.StringIO()):
                result = run_audit(
                    output,
                    attempts=3,
                    retry_delay_seconds=2,
                    timeout_seconds=1,
                    runner=lambda *args, **kwargs: next(responses),
                    sleeper=sleeps.append,
                )

            self.assertEqual(result, 0)
            self.assertEqual(sleeps, [2])
            self.assertEqual(json.loads(output.read_text(encoding="utf-8")), VALID_REPORT)

    def test_fails_closed_without_reusing_stale_report(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "audit.json"
            output.write_text(json.dumps(VALID_REPORT), encoding="utf-8")

            with redirect_stderr(io.StringIO()):
                result = run_audit(
                    output,
                    attempts=2,
                    retry_delay_seconds=0,
                    timeout_seconds=1,
                    runner=lambda *args, **kwargs: completed(
                        {"error": "registry unavailable"}, 1
                    ),
                    sleeper=lambda _: None,
                )

            self.assertEqual(result, 1)
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
