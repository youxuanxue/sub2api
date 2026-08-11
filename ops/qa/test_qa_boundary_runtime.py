#!/usr/bin/env python3
"""Behavior tests for the QA hourly boundary host runner."""

from __future__ import annotations

import datetime as dt
import json
import os
import shlex
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
RUNNER = ROOT / "deploy/aws/stage0/tokenkey-qa-boundary.sh"


class QABoundaryRunnerTest(unittest.TestCase):
    def _sandbox(self, root: Path, *, image: str = "sha256:image-v2"):
        fake_bin = root / "bin"
        runtime = root / "run"
        data_root = root / "app"
        receipt = root / "qa-boundary-last-run.json"
        systemd = root / "systemd"
        for path in (fake_bin, runtime, data_root, systemd):
            path.mkdir(parents=True, exist_ok=True)
        for name in ("qa_blobs", "qa_dlq", "qa_exports_tmp"):
            (data_root / name).mkdir()

        resolver = root / "resolve-app-container.sh"
        resolver.write_text(
            "tk_resolve_app_container() { "
            "[ \"${TEST_RESOLVER_FAIL:-0}\" = 0 ] || return 1; "
            "printf '%s\\n' tokenkey-green; }\n",
            encoding="utf-8",
        )
        docker_log = root / "docker.log"
        fake_docker = fake_bin / "docker"
        fake_docker.write_text(
            """#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_DOCKER_LOG"
if [ "${1:-}" = inspect ]; then
  case "$*" in
    *'.Image'*) printf '%s\n' "$TEST_IMAGE" ;;
    *'.Mounts'*) printf 'bind|%s|true\n' "$TEST_DATA_ROOT" ;;
    *'.Config.Env'*) printf '%s\n' 'DATABASE_URL=postgres://local-only' 'DATA_DIR=/app/data' ;;
    *) printf '[]\n' ;;
  esac
  exit 0
fi
if [ "${1:-}" = run ]; then
  shift
  image_index=-1
  run_id=""
  trigger=""
  index=0
  for argument in "$@"; do
    case "$argument" in
      --env=QA_MAINTENANCE_RUN_ID=*)
        run_id="${argument#--env=QA_MAINTENANCE_RUN_ID=}"
        [ "$image_index" -lt 0 ] || exit 93
        ;;
      --env=QA_MAINTENANCE_TRIGGER=*)
        trigger="${argument#--env=QA_MAINTENANCE_TRIGGER=}"
        [ "$image_index" -lt 0 ] || exit 93
        ;;
      sha256:*) image_index=$index ;;
    esac
    index=$((index + 1))
  done
  [ "$image_index" -ge 0 ] || exit 94
  [ -n "$run_id" ] && [ -n "$trigger" ] || exit 95
  for mount in qa_blobs qa_dlq qa_exports_tmp; do
    expected="$TEST_DATA_ROOT/$mount:/app/data/$mount:rw"
    [[ " $* " == *" --volume=$expected "* ]] || exit 96
  done
  child="${TEST_CHILD_JSON//__RUN_ID__/$run_id}"
  child="${child//__TRIGGER__/$trigger}"
  printf '%s\n' "$child"
  exit "$TEST_CHILD_RC"
fi
exit 8
""",
            encoding="utf-8",
        )
        fake_docker.chmod(0o755)

        orphan = root / "qa-export-orphan.py"
        orphan.write_text(
            """#!/usr/bin/env python3
import json
import os
import sys
if "resolve-runtime" in sys.argv:
    print(json.dumps({
        "host_dir": os.environ["TEST_DATA_ROOT"] + "/qa_exports_tmp",
        "container_dir": "/app/data/qa_exports_tmp",
    }))
    raise SystemExit(0)
if "action" in sys.argv:
    mode = sys.argv[sys.argv.index("--mode") + 1]
    if mode not in {"plan", "apply", "apply-activate"}:
        raise SystemExit(19)
    if int(os.environ.get("TEST_ORPHAN_RC", "0")) != 0:
        raise SystemExit(int(os.environ["TEST_ORPHAN_RC"]))
    count = int(os.environ.get("TEST_ORPHAN_COUNT", "0"))
    if mode == "plan":
        print(json.dumps({"plan_hash": "a" * 64, "count": count, "total_bytes": count}))
    else:
        print(json.dumps({"plan_hash": "a" * 64, "deleted_count": count, "deleted_bytes": count}))
    raise SystemExit(0)
raise SystemExit(9)
""",
            encoding="utf-8",
        )

        activation_marker = root / "qa-export-orphan-cleanup-activated.json"
        activation_marker.write_text(
            json.dumps(
                {
                    "schema_version": "qa-export-orphan-activation-v1",
                    "activated_plan_hash": "b" * 64,
                    "activated_at": "2026-08-11T00:00:00Z",
                }
            ),
            encoding="utf-8",
        )
        archive_receipt = root / "qa-maintenance-last-run.json"
        archive_receipt.write_text(
            json.dumps(
                {
                    "schema_version": "qa-maintenance-runner-v1",
                    "run_id": "archive-1",
                    "trigger": "timer",
                    "finished_at": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                    "active_container": "tokenkey-green",
                    "image": image,
                    "child_exit_code": 0,
                    "runner_exit_code": 0,
                    "error_code": "",
                    "deletion_authorized": False,
                    "normal": {"state": "committed", "restore_verified": True},
                }
            ),
            encoding="utf-8",
        )

        child = {
            "receipt_version": 1,
            "mode": "qa_maintenance_boundary",
            "ok": True,
            "job_name": "qa-boundary",
            "run_id": "__RUN_ID__",
            "trigger": "__TRIGGER__",
            "boundary": {"deletion_authorized": True},
            "deletion_authorized": True,
        }
        env = {
            "PATH": f"{fake_bin}:/opt/homebrew/bin:/usr/bin:/bin",
            "QA_BOUNDARY_DOCKER": "docker",
            "QA_BOUNDARY_RESOLVER": str(resolver),
            "QA_BOUNDARY_RUNTIME_DIR": str(runtime),
            "QA_BOUNDARY_HOST_DATA_ROOT": str(data_root),
            "QA_BOUNDARY_RECEIPT": str(receipt),
            "QA_BOUNDARY_SYSTEMD_DIR": str(systemd),
            "QA_EXPORT_ORPHAN_HELPER": str(orphan),
            "QA_EXPORT_ACTIVATION_MARKER": str(activation_marker),
            "QA_MAINTENANCE_RECEIPT": str(archive_receipt),
            "TEST_DOCKER_LOG": str(docker_log),
            "TEST_DATA_ROOT": str(data_root),
            "TEST_IMAGE": image,
            "TEST_CHILD_RC": "0",
            "TEST_CHILD_JSON": json.dumps(child, separators=(",", ":")),
            "TEST_ORPHAN_RC": "0",
            "TEST_ORPHAN_COUNT": "0",
        }
        return env, receipt, docker_log

    def test_success_uses_pre_image_env_and_exact_writable_hot_mounts(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            env, receipt, docker_log = self._sandbox(root)
            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=operator"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            payload = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertEqual(payload["schema_version"], "qa-boundary-runner-v1")
            self.assertEqual(payload["active_container"], "tokenkey-green")
            self.assertEqual(payload["image"], "sha256:image-v2")
            self.assertEqual(payload["runner_uid"], 1000)
            self.assertEqual(payload["runner_gid"], 1000)
            self.assertEqual(payload["error_code"], "")
            self.assertEqual(payload["runner_exit_code"], 0)
            self.assertEqual(stat.S_IMODE(receipt.stat().st_mode), 0o600)
            calls = docker_log.read_text(encoding="utf-8")
            self.assertIn(f"{root / 'app'}:/app/data:ro", calls)

    def test_success_runs_export_orphan_plan_then_exact_apply(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, _, _ = self._sandbox(Path(temp_dir))
            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=timer"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            self.assertNotIn("scheduled", proc.stdout + proc.stderr)

    def test_pre_app_failure_still_writes_atomic_identity_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, receipt, _ = self._sandbox(Path(temp_dir), image="")
            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=timer"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            payload = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertEqual(payload["error_code"], "image_unavailable")
            self.assertEqual(payload["child_exit_code"], -1)
            self.assertEqual(payload["runner_exit_code"], proc.returncode)
            self.assertEqual(payload["active_container"], "tokenkey-green")
            self.assertEqual(payload["image"], "")

    def test_export_orphan_failure_is_not_swallowed(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, receipt, _ = self._sandbox(Path(temp_dir))
            env["TEST_ORPHAN_RC"] = "17"
            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=timer"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            payload = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertEqual(payload["error_code"], "export_orphan_plan_failed")
            self.assertEqual(payload["child_exit_code"], 0)
            self.assertEqual(payload["runner_exit_code"], proc.returncode)

    def test_cutover_operator_commands_use_the_same_hardened_runtime(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            env, receipt, docker_log = self._sandbox(root)
            proc = subprocess.run(
                [
                    "bash",
                    str(RUNNER),
                    "--qa-cutover-finalize-plan",
                    "--t0=2026-08-11T12:00:00Z",
                ],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            call = docker_log.read_text(encoding="utf-8")
            self.assertIn("--env=QA_MAINTENANCE_TRIGGER=operator", call)
            self.assertIn("sha256:image-v2 /app/sub2api --qa-cutover-finalize-plan", call)
            self.assertIn("--t0=2026-08-11T12:00:00Z", call)
            self.assertFalse(receipt.exists(), "operator cutover must not overwrite scheduled boundary receipt")

    def test_boundary_runner_applies_database_timeouts_to_every_child(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, _, docker_log = self._sandbox(Path(temp_dir))
            proc = subprocess.run(
                ["bash", str(RUNNER), "--qa-cutover-inventory"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            self.assertIn(
                "--env=PGOPTIONS=-c lock_timeout=100ms -c statement_timeout=120s",
                docker_log.read_text(encoding="utf-8"),
            )

    def test_finalize_operator_rejects_pending_export_orphans(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, receipt, docker_log = self._sandbox(Path(temp_dir))
            env["TEST_ORPHAN_COUNT"] = "1"
            proc = subprocess.run(
                [
                    "bash",
                    str(RUNNER),
                    "--qa-cutover-finalize-plan",
                    "--t0=2026-08-11T12:00:00Z",
                ],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            self.assertIn("export orphans remain", proc.stderr)
            self.assertNotIn(" run ", f" {docker_log.read_text(encoding='utf-8')} ")
            self.assertFalse(receipt.exists())

    def test_finalize_operator_requires_fresh_successful_archive_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            env, receipt, docker_log = self._sandbox(root)
            env["QA_MAINTENANCE_RECEIPT"] = str(root / "missing-archive-receipt.json")
            proc = subprocess.run(
                [
                    "bash",
                    str(RUNNER),
                    "--qa-cutover-finalize-plan",
                    "--t0=2026-08-11T12:00:00Z",
                ],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            self.assertIn("archive health receipt", proc.stderr)
            self.assertNotIn(" run ", f" {docker_log.read_text(encoding='utf-8')} ")
            self.assertFalse(receipt.exists())


class QABoundaryDeploymentTest(unittest.TestCase):
    def test_ssm_sync_installs_runtime_and_switches_cleanup_owner_atomically(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            fake_bin = root / "bin"
            output = root / "output"
            fake_bin.mkdir()
            fake_aws = fake_bin / "aws"
            fake_aws.write_text(
                """#!/usr/bin/env bash
case " $* " in
  *" ssm send-command "*) printf '%s\n' command-123 ;;
  *" --query Status "*) printf '%s\n' Success ;;
  *" --query ResponseCode "*) printf '%s\n' 0 ;;
  *) printf '\n' ;;
esac
""",
                encoding="utf-8",
            )
            fake_aws.chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fake_bin}:/opt/homebrew/bin:/usr/bin:/bin",
                "STAGE0_SSM_OUTPUT_DIR": str(output),
                "QA_BOUNDARY_TIMER_STATE": "enabled",
            }
            proc = subprocess.run(
                [
                    "bash",
                    str(ROOT / "ops/stage0/sync-qa-boundary-timer-via-ssm.sh"),
                    "i-0123456789abcdef0",
                ],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            payload = json.loads((output / "ssm-params.json").read_text(encoding="utf-8"))
            commands = payload["commands"]
            joined = "\n".join(commands)
            self.assertIn("/usr/local/bin/tokenkey-qa-boundary.sh", joined)
            self.assertIn("/usr/local/lib/tokenkey/qa-export-orphan.py", joined)
            self.assertIn("/usr/local/lib/tokenkey/resolve-app-container.sh", joined)
            switch = next(
                command
                for command in commands
                if "enable --now tokenkey-qa-boundary.timer" in command
            )
            self.assertIn("disable --now tokenkey-qa-stale-cleanup.timer", switch)
            self.assertNotIn("enable --now tokenkey-qa-stale-cleanup.timer", switch)
            self.assertNotIn("disable --now tokenkey-qa-boundary.timer", switch)
            self.assertIn("qa_lifecycle_receipts", switch)
            self.assertIn("phase=concat(chr(102)", switch)
            self.assertIn("JOIN qa_lifecycle_receipts a ON a.t0_utc=f.t0_utc", switch)
            self.assertIn("a.phase=concat(chr(97)", switch)
            self.assertLess(
                switch.index("qa_lifecycle_receipts"),
                switch.rindex("disable --now tokenkey-qa-stale-cleanup.timer"),
            )

            switch_log = root / "switch.log"
            systemctl = fake_bin / "systemctl"
            systemctl.write_text(
                """#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >> "$TEST_SWITCH_LOG"
case "$*" in
  "is-enabled tokenkey-qa-boundary.timer") printf 'enabled\n' ;;
  "is-active tokenkey-qa-boundary.timer") printf 'failed\n' ;;
  "is-enabled tokenkey-qa-stale-cleanup.timer") printf 'disabled\n' ;;
  "is-active tokenkey-qa-stale-cleanup.timer") printf 'inactive\n' ;;
esac
""",
                encoding="utf-8",
            )
            systemctl.chmod(0o755)
            docker = fake_bin / "docker"
            docker.write_text("#!/usr/bin/env bash\nprintf '1\\n'\n", encoding="utf-8")
            docker.chmod(0o755)
            parsed = shlex.split(switch)
            self.assertEqual(parsed[:3], ["sudo", "bash", "-c"])
            failed_switch = subprocess.run(
                ["bash", "-c", parsed[3]],
                env={
                    **os.environ,
                    "PATH": f"{fake_bin}:/usr/bin:/bin",
                    "TEST_SWITCH_LOG": str(switch_log),
                },
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(failed_switch.returncode, 0)
            calls = switch_log.read_text(encoding="utf-8")
            self.assertNotIn("enable --now tokenkey-qa-stale-cleanup.timer", calls)
            self.assertNotIn("disable --now tokenkey-qa-boundary.timer", calls)
            self.assertEqual(
                calls.count("enable --now tokenkey-qa-boundary.timer"),
                2,
                calls,
            )


if __name__ == "__main__":
    unittest.main()
