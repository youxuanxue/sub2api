#!/usr/bin/env python3
"""Exercise the real edge smoke phase dispatcher with offline transport stubs."""
from __future__ import annotations

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class EdgeSmokePhaseContractTest(unittest.TestCase):
    def _run(self, phase: str, self_mode: str = "infra") -> tuple[subprocess.CompletedProcess[str], str]:
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            stage = repo / "ops/stage0"
            stage.mkdir(parents=True)
            for name in ("edge_post_deploy_smoke.sh", "smoke_env.sh", "ssm_resolve_invocation_mi.inc.sh"):
                shutil.copy2(ROOT / "ops/stage0" / name, stage / name)
            probe = repo / "ops/observability/run-probe.sh"
            probe.parent.mkdir()
            probe.write_text('#!/bin/bash\nprintf "%s\\n" "$*" >> "$CALL_LOG"\n')
            (stage / "post_deploy_smoke.sh").write_text(
                '#!/bin/bash\nprintf "gateway:%s:%s\\n" "$GATEWAY_SMOKE_SUITE" "$TK_SMOKE_SKIP_FRONTEND" >> "$CALL_LOG"\n'
            )
            fake_bin = repo / "bin"
            fake_bin.mkdir()
            for name, body in {
                "curl": "printf 403",
                "aws": "printf '/v1/messages\\n'",
                "sleep": "exit 0",
            }.items():
                path = fake_bin / name
                path.write_text("#!/bin/bash\n" + body + "\n")
                path.chmod(0o755)
            calls = repo / "calls.log"
            proc = subprocess.run(
                ["bash", str(stage / "edge_post_deploy_smoke.sh")], cwd=repo,
                env={
                    "PATH": str(fake_bin) + os.pathsep + os.environ["PATH"],
                    "AWS_REGION": "us-east-1", "EDGE_ID": "fixture",
                    "EDGE_API_URL": "https://edge.example.test", "EDGE_INSTANCE_ID": "i-fixture",
                    "EDGE_SMOKE_PHASE": phase, "EDGE_SELF_SMOKE_MODE": self_mode,
                    "TK_SMOKE_API_KEY": "fixture-only", "SKIP_EXTERNAL_HEALTH": "1",
                    "CALL_LOG": str(calls),
                }, capture_output=True, text=True, check=False,
            )
            return proc, calls.read_text() if calls.exists() else ""

    def test_phases_dispatch_actual_runners(self) -> None:
        for phase, mode, infra, native, gateway in (
            ("infra", "infra", True, False, False),
            ("infra", "api", True, True, False),
            ("edge-native-oauth", "infra", False, True, False),
            ("main-via-edge", "infra", False, False, True),
            ("full", "infra", True, True, False),
        ):
            with self.subTest(phase=phase, mode=mode):
                proc, calls = self._run(phase, mode)
                self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
                self.assertEqual("edge_infra_smoke.sh" in calls, infra, calls)
                self.assertEqual("edge_native_anthropic_smoke.sh" in calls, native, calls)
                self.assertEqual("gateway:main-via-edge:1" in calls, gateway, calls)

    def test_unknown_phase_fails_before_any_probe(self) -> None:
        proc, calls = self._run("unknown")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("EDGE_SMOKE_PHASE must be", proc.stderr)
        self.assertEqual(calls, "")


if __name__ == "__main__":
    unittest.main()
