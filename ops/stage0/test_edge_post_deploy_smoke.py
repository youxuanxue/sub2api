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
    def test_infra_smoke_uses_active_bluegreen_container(self) -> None:
        with tempfile.TemporaryDirectory(prefix="edge-infra-smoke-test-") as td:
            temp_root = pathlib.Path(td)
            fake_bin = temp_root / "fake-bin"
            fake_bin.mkdir()
            active_color = temp_root / "active-color"
            active_color.write_text("green\n")
            docker_calls = temp_root / "docker-calls.txt"

            fake_run_probe = temp_root / "run-probe.sh"
            fake_run_probe.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    set -euo pipefail
                    script=""
                    while [ "$#" -gt 0 ]; do
                      case "$1" in
                        --script) script="$2"; shift 2 ;;
                        --target|--comment|--timeout-seconds|--expected-instance-id|--env|--with|--remote-path) shift 2 ;;
                        *) echo "unexpected run-probe arg: $1" >&2; exit 1 ;;
                      esac
                    done
                    test -n "$script"
                    bash "$script"
                    """
                )
            )
            fake_run_probe.chmod(0o755)

            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    echo "unexpected direct aws invocation: $*" >&2
                    exit 99
                    """
                )
            )
            fake_aws.chmod(0o755)

            fake_curl = fake_bin / "curl"
            fake_curl.write_text("#!/usr/bin/env bash\nprintf '403'\n")
            fake_curl.chmod(0o755)

            fake_docker = fake_bin / "docker"
            fake_docker.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    set -euo pipefail
                    printf '%s\\n' "$*" >> "${DOCKER_CALLS_LOG}"
                    case "${1:-}" in
                      inspect)
                        name="${!#}"
                        if [ "$name" = tokenkey-green ]; then
                          printf 'true\\n'
                          exit 0
                        fi
                        exit 1
                        ;;
                      compose)
                        exit 0
                        ;;
                      exec)
                        container="${2:-}"
                        [ "$container" = tokenkey-green ] || exit 1
                        case "${3:-}" in
                          printenv) printf 'false\\n' ;;
                          wget) printf '{"status":"ok"}\\n' ;;
                          *) exit 1 ;;
                        esac
                        ;;
                      *) exit 1 ;;
                    esac
                    """
                )
            )
            fake_docker.chmod(0o755)

            env = {
                **os.environ,
                "PATH": f"{fake_bin}{os.pathsep}{os.environ.get('PATH', '')}",
                "AWS_REGION": "us-east-1",
                "EDGE_API_URL": "https://us5.example.test",
                "EDGE_ID": "us5",
                "EDGE_INSTANCE_ID": "mi-test",
                "EDGE_SMOKE_PHASE": "infra",
                "EDGE_RUN_PROBE": str(fake_run_probe),
                "SKIP_EXTERNAL_HEALTH": "1",
                "ACTIVE_COLOR_FILE": str(active_color),
                "TOKENKEY_ROOT": str(temp_root),
                "TK_DOCKER": "docker",
                "DOCKER_CALLS_LOG": str(docker_calls),
            }
            proc = subprocess.run(
                ["bash", str(_SCRIPT)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
                timeout=5,
            )
            calls = docker_calls.read_text().splitlines() if docker_calls.exists() else []

        self.assertEqual(proc.returncode, 0, msg=f"stdout:\n{proc.stdout}\nstderr:\n{proc.stderr}")
        self.assertIn("tk_edge_post_deploy_smoke: container=tokenkey-green QA_CAPTURE_ENABLED=false", proc.stdout)
        self.assertIn("exec tokenkey-green printenv QA_CAPTURE_ENABLED", calls)
        self.assertIn("exec tokenkey-green wget -qO- http://localhost:8080/health", calls)

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
