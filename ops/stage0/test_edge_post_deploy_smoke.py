#!/usr/bin/env python3
"""Behavioral tests for edge_post_deploy_smoke.sh orchestration."""
from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
import tempfile
import textwrap
import unittest

_STAGE0_DIR = pathlib.Path(__file__).resolve().parent
_SCRIPT = _STAGE0_DIR / "edge_post_deploy_smoke.sh"


class EdgePostDeploySmokeTest(unittest.TestCase):
    def test_native_oauth_smoke_delivers_reserved_resources_companion(self) -> None:
        with tempfile.TemporaryDirectory(prefix="edge-post-deploy-smoke-test-") as td:
            repo_root = pathlib.Path(td) / "repo"
            stage0_dir = repo_root / "ops" / "stage0"
            observability_dir = repo_root / "ops" / "observability"
            fake_bin = repo_root / "fake-bin"
            stage0_dir.mkdir(parents=True)
            observability_dir.mkdir(parents=True)
            fake_bin.mkdir()

            for name in (
                "edge_post_deploy_smoke.sh",
                "smoke_env.sh",
                "ssm_resolve_invocation_mi.inc.sh",
            ):
                shutil.copy2(_STAGE0_DIR / name, stage0_dir / name)

            args_log = repo_root / "run-probe-args.txt"
            run_probe = observability_dir / "run-probe.sh"
            run_probe.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    printf '%s\n' "$@" > "${RUN_PROBE_ARGS_LOG}"
                    """
                )
            )

            for command in ("aws", "curl", "jq"):
                fake_command = fake_bin / command
                fake_command.write_text("#!/usr/bin/env bash\nexit 0\n")
                fake_command.chmod(0o755)

            env = {
                **os.environ,
                "PATH": f"{fake_bin}{os.pathsep}{os.environ.get('PATH', '')}",
                "AWS_REGION": "us-east-1",
                "EDGE_API_URL": "https://us5.example.test",
                "EDGE_ID": "us5",
                "EDGE_INSTANCE_ID": "mi-test",
                "EDGE_SMOKE_PHASE": "edge-native-oauth",
                "RUN_PROBE_ARGS_LOG": str(args_log),
            }
            proc = subprocess.run(
                ["bash", str(stage0_dir / _SCRIPT.name)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

            delivered_args = args_log.read_text().splitlines() if args_log.exists() else []

        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn("tk_edge_post_deploy_smoke: OK phase=edge-native-oauth", proc.stdout)
        companion = str(repo_root / "ops" / "pricing" / "probe_reserved_resources.sh")
        self.assertIn(companion, delivered_args)
        companion_at = delivered_args.index(companion)
        self.assertGreater(companion_at, 0)
        self.assertEqual(delivered_args[companion_at - 1], "--with")


if __name__ == "__main__":
    unittest.main()
