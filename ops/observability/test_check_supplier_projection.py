#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parent / "check-supplier-projection.sh"


class CheckSupplierProjectionWrapperTest(unittest.TestCase):
    def run_case(self, verdict: str | None, remote_rc: int) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as directory:
            fake = Path(directory) / "run-probe.sh"
            payload = "" if verdict is None else json.dumps({"verdict": verdict})
            fake.write_text(
                "#!/usr/bin/env bash\n"
                + (f"printf '%s\\n' '{payload}'\n" if payload else "")
                + f"exit {remote_rc}\n",
                encoding="utf-8",
            )
            fake.chmod(fake.stat().st_mode | stat.S_IXUSR)
            env = os.environ.copy()
            env["SUPPLIER_PROJECTION_RUN_PROBE"] = str(fake)
            return subprocess.run(
                ["bash", str(SCRIPT)], capture_output=True, text=True, check=False, env=env
            )

    def test_aligned_is_success(self) -> None:
        proc = self.run_case("aligned", 0)
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_remote_drift_is_checker_failure(self) -> None:
        proc = self.run_case("drift", 3)
        self.assertEqual(proc.returncode, 1, proc.stderr)

    def test_setup_or_transport_failure_is_distinct(self) -> None:
        self.assertEqual(self.run_case("setup_error", 3).returncode, 2)
        self.assertEqual(self.run_case(None, 2).returncode, 2)


if __name__ == "__main__":
    unittest.main()
