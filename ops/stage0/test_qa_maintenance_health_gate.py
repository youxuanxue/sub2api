#!/usr/bin/env python3
"""Behavior tests for the real systemd QA maintenance deployment gate."""

from __future__ import annotations

import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
WRAPPER = ROOT / "ops/stage0/run-qa-maintenance-health-gate-via-ssm.sh"


class QAMaintenanceHealthGateTest(unittest.TestCase):
    def _emitted_gate_command(self, root: pathlib.Path) -> str:
        fake_bin = root / "emit-bin"
        output = root / "emit-output"
        fake_bin.mkdir()
        output.mkdir()
        aws = fake_bin / "aws"
        aws.write_text(
            textwrap.dedent(
                """\
                #!/usr/bin/env bash
                case " $* " in
                  *" ssm send-command "*) printf '%s\n' command-gate ;;
                  *" --query Status "*) printf '%s\n' Success ;;
                  *" --query ResponseCode "*) printf '%s\n' 0 ;;
                  *) printf '\n' ;;
                esac
                """
            ),
            encoding="utf-8",
        )
        aws.chmod(0o755)
        sleep = fake_bin / "sleep"
        sleep.write_text(
            "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$SLEEP_CALLS\"\n",
            encoding="utf-8",
        )
        sleep.chmod(0o755)
        sleep_calls = root / "sleep-calls"
        proc = subprocess.run(
            ["bash", str(WRAPPER), "i-0123456789abcdef0"],
            env={
                **os.environ,
                "PATH": f"{fake_bin}:/opt/homebrew/bin:/usr/bin:/bin",
                "SLEEP_CALLS": str(sleep_calls),
                "STAGE0_SSM_OUTPUT_DIR": str(output),
                "STAGE0_SSM_TIMEOUT_SECONDS": "10",
            },
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
        self.assertEqual(sleep_calls.read_text(encoding="utf-8"), "3\n")
        commands = json.loads(
            (output / "ssm-params.json").read_text(encoding="utf-8")
        )["commands"]
        self.assertEqual(commands[0], "set -euo pipefail")
        return commands[1]

    def _run_emitted_gate(
        self,
        *,
        service_start_rc: int = 0,
        update_receipt: bool = True,
        activation_count: int = 0,
        boundary_enabled: str = "enabled",
        boundary_active: str = "active",
        heartbeat_run_id: str = "gate-run",
    ) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = pathlib.Path(temp_dir)
            command = self._emitted_gate_command(root)
            fake_bin = root / "run-bin"
            fake_bin.mkdir()
            receipt = root / "receipt.json"
            receipt.write_text(
                json.dumps({"run_id": "previous-run"}) + "\n", encoding="utf-8"
            )
            hook = root / "write-receipt.sh"
            hook.write_text(
                textwrap.dedent(
                    f"""\
                    #!/usr/bin/env bash
                    {'true' if update_receipt else 'exit 0'}
                    printf '%s\n' '{{"schema_version":"qa-maintenance-runner-v1","run_id":"gate-run","trigger":"timer","started_at":"2026-08-17T15:00:00Z","finished_at":"2026-08-17T15:00:05Z","active_container":"tokenkey-blue","image":"sha256:image","runner_uid":1000,"runner_gid":1000,"normal":{{"state":"committed","restore_verified":true,"cleanup_eligible":false}},"child_exit_code":0,"runner_exit_code":0,"error_code":"","deletion_authorized":false}}' > "$QA_MAINTENANCE_RECEIPT"
                    """
                ),
                encoding="utf-8",
            )
            hook.chmod(0o755)
            systemctl = fake_bin / "systemctl"
            systemctl.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    case "$*" in
                      "show tokenkey-qa-maintenance.service -p InvocationID --value")
                        if [ -f "$INVOCATION_MARKER" ]; then printf '%s\n' invocation-new; else printf '%s\n' invocation-old; fi ;;
                      "start tokenkey-qa-maintenance.service")
                        [ "$SERVICE_START_RC" = 0 ] || exit "$SERVICE_START_RC"
                        "$SYSTEMCTL_START_HOOK"
                        : > "$INVOCATION_MARKER" ;;
                      "show tokenkey-qa-maintenance.service -p Result --value") printf '%s\n' success ;;
                      "show tokenkey-qa-maintenance.service -p ExecMainStatus --value") printf '%s\n' 0 ;;
                      "show tokenkey-qa-maintenance.service -p ExecMainStartTimestamp --value") printf '%s\n' 'Mon 2026-08-17 15:00:00 UTC' ;;
                      "show tokenkey-qa-maintenance.service -p ExecMainExitTimestamp --value") printf '%s\n' 'Mon 2026-08-17 15:00:05 UTC' ;;
                      "is-enabled tokenkey-qa-boundary.timer") printf '%s\n' "$BOUNDARY_ENABLED"; [ "$BOUNDARY_ENABLED" = enabled ] ;;
                      "is-active tokenkey-qa-boundary.timer") printf '%s\n' "$BOUNDARY_ACTIVE"; [ "$BOUNDARY_ACTIVE" = active ] ;;
                      *) exit 92 ;;
                    esac
                    """
                ),
                encoding="utf-8",
            )
            systemctl.chmod(0o755)
            docker = fake_bin / "docker"
            docker.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    case "$*" in
                      *ops_job_heartbeats*) printf '{"last_run_at":"2026-08-17T15:00:00Z","last_success_at":"2026-08-17T15:00:05Z","last_result":"status=committed run_id=%s trigger=timer deletion_authorized=false"}\n' "$HEARTBEAT_RUN_ID" ;;
                      *single_owner_activate*) printf '%s\n' "$ACTIVATION_COUNT" ;;
                      *) exit 93 ;;
                    esac
                    """
                ),
                encoding="utf-8",
            )
            docker.chmod(0o755)
            sudo = fake_bin / "sudo"
            sudo.write_text("#!/usr/bin/env bash\nexec \"$@\"\n", encoding="utf-8")
            sudo.chmod(0o755)
            date = fake_bin / "date"
            date.write_text(
                "#!/usr/bin/env bash\nprintf '%s\\n' 2026-08-17T14:59:59Z\n",
                encoding="utf-8",
            )
            date.chmod(0o755)

            return subprocess.run(
                ["bash", "-c", command],
                env={
                    **os.environ,
                    "PATH": f"{fake_bin}:/usr/bin:/bin",
                    "QA_MAINTENANCE_RECEIPT": str(receipt),
                    "SYSTEMCTL_START_HOOK": str(hook),
                    "INVOCATION_MARKER": str(root / "invocation-new"),
                    "SERVICE_START_RC": str(service_start_rc),
                    "ACTIVATION_COUNT": str(activation_count),
                    "BOUNDARY_ENABLED": boundary_enabled,
                    "BOUNDARY_ACTIVE": boundary_active,
                    "HEARTBEAT_RUN_ID": heartbeat_run_id,
                },
                capture_output=True,
                text=True,
                check=False,
            )

    def test_emitted_gate_runs_real_unit_and_accepts_correlated_execution(self) -> None:
        proc = self._run_emitted_gate()
        self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
        self.assertIn('"run_id":"gate-run"', proc.stdout)

    def test_emitted_gate_rejects_service_failure_and_unchanged_receipt(self) -> None:
        failed_service = self._run_emitted_gate(service_start_rc=23)
        self.assertNotEqual(failed_service.returncode, 0)

        stale_receipt = self._run_emitted_gate(update_receipt=False)
        self.assertNotEqual(stale_receipt.returncode, 0)

        stale_heartbeat = self._run_emitted_gate(heartbeat_run_id="previous-run")
        self.assertNotEqual(stale_heartbeat.returncode, 0)

    def test_emitted_gate_enforces_receipt_dependent_boundary_owner(self) -> None:
        activated_ok = self._run_emitted_gate(
            activation_count=1,
            boundary_enabled="disabled",
            boundary_active="inactive",
        )
        self.assertEqual(
            activated_ok.returncode, 0, (activated_ok.stdout, activated_ok.stderr)
        )

        activated_but_boundary_running = self._run_emitted_gate(activation_count=1)
        self.assertNotEqual(activated_but_boundary_running.returncode, 0)

        pre_activation_but_boundary_stopped = self._run_emitted_gate(
            activation_count=0,
            boundary_enabled="disabled",
            boundary_active="inactive",
        )
        self.assertNotEqual(pre_activation_but_boundary_stopped.returncode, 0)


if __name__ == "__main__":
    raise SystemExit(unittest.main())
