#!/usr/bin/env python3
"""Behavior tests for the QA Phase 2 host runner and correlated health."""

from __future__ import annotations

import copy
import datetime as dt
import importlib.util
import json
import os
import stat
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RUNNER = ROOT / "deploy/aws/stage0/tokenkey-qa-maintenance.sh"


def _load_module(name: str, relative: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / relative)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {relative}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class QAPhase2RunnerTest(unittest.TestCase):
    def _sandbox(self, root: Path, *, image: str = "sha256:image-v2", child_rc: int = 0):
        fake_bin = root / "bin"
        runtime = root / "run"
        data_root = root / "app"
        scratch = data_root / "qa_archive_tmp"
        blob_root = data_root / "qa_blobs"
        dlq_root = data_root / "qa_dlq"
        ledger_root = data_root / "qa_capture_ledger"
        receipt = root / "qa-maintenance-last-run.json"
        systemd = root / "systemd"
        for path in (fake_bin, runtime, data_root, scratch, blob_root, dlq_root, ledger_root, systemd):
            path.mkdir(parents=True, exist_ok=True)
        scratch.chmod(0o700)

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
    *State.Running*) printf '%s\n' true ;;
    *'.Image'*) printf '%s\n' "$TEST_IMAGE" ;;
    *'.Mounts'*) printf '%s\n' "$TEST_MOUNT_FACT" ;;
    *'.Config.Env'*)
      if [ "${TEST_ENV_CAPTURE_FAIL:-0}" = 1 ]; then exit 10; fi
      printf '%s\n' 'DATABASE_URL=postgres://local-only' 'DATA_DIR=/app/data'
      ;;
    *) exit 7 ;;
  esac
  exit 0
fi
if [ "${1:-}" = run ]; then
  if [[ "$*" == *qa-maintenance-selftest-create* ]]; then
    printf '%s' 'qa-maintenance-selftest-ok' > "$TEST_SCRATCH/.qa-maintenance-selftest"
    chmod 0600 "$TEST_SCRATCH/.qa-maintenance-selftest"
    exit 0
  fi
	  if [[ "$*" == *qa-maintenance-selftest-remove* ]]; then
	    rm -f -- "$TEST_SCRATCH/.qa-maintenance-selftest"
	    exit 0
	  fi
	  if [[ "$*" == *'--qa-single-owner-activate'* ]]; then
	    run_id=""
	    plan_hash=""
	    for argument in "$@"; do
	      case "$argument" in
	        --env=QA_SINGLE_OWNER_ACTIVATION_RUN_ID=*)
	          run_id="${argument#--env=QA_SINGLE_OWNER_ACTIVATION_RUN_ID=}"
	          ;;
	        --plan-hash=*) plan_hash="${argument#--plan-hash=}" ;;
	      esac
	    done
	    ready="$TEST_ACTIVATION_DIR/$run_id.ready.json"
	    ack="$TEST_ACTIVATION_DIR/$run_id.ack.json"
	    printf '{"schema_version":"qa-single-owner-db-lock-ready-v1","run_id":"%s","nonce":"nonce-1","plan_hash":"%s","database_lock_acquired":true,"ready_at":"2026-08-15T10:00:00Z"}\n' "$run_id" "$plan_hash" > "$ready"
	    deadline=$(( $(date +%s) + 3 ))
	    while [ ! -f "$ack" ]; do
	      [ "$(date +%s)" -lt "$deadline" ] || exit 61
	      sleep 0.01
	    done
	    printf '{"ok":true,"phase":"single_owner_activate","run_id":"%s","plan_hash":"%s","t0_utc":"2026-08-15T10:00:00Z","activated_at":"2026-08-15T10:00:01Z"}\n' "$run_id" "$plan_hash"
	    exit 0
	  fi
	  if [[ "$*" == *'/app/sub2api'* ]]; then
	    if [ -n "${TEST_CHILD_STDERR:-}" ]; then
	      printf '%s\n' "$TEST_CHILD_STDERR" >&2
	    fi
	    if [ -n "${TEST_CHILD_JSON:-}" ]; then
	      run_id=""
	      trigger=""
	      for argument in "$@"; do
	        case "$argument" in
	          --env=QA_MAINTENANCE_RUN_ID=*)
	            run_id="${argument#--env=QA_MAINTENANCE_RUN_ID=}"
	            ;;
	          --env=QA_MAINTENANCE_TRIGGER=*)
	            trigger="${argument#--env=QA_MAINTENANCE_TRIGGER=}"
	            ;;
	        esac
	      done
	      child_json="${TEST_CHILD_JSON//__RUN_ID__/$run_id}"
	      child_json="${child_json//__TRIGGER__/$trigger}"
	      printf '%s\n' "$child_json"
	    fi
	    exit "$TEST_CHILD_RC"
  fi
  if [[ "$*" == *'/app/qa-archive'* ]]; then
    printf '%s\n' "$TEST_ARCHIVE_JSON"
    exit 0
  fi
fi
exit 8
""",
            encoding="utf-8",
        )
        fake_docker.chmod(0o755)
        fake_stat = fake_bin / "stat"
        fake_stat.write_text(
            """#!/usr/bin/env bash
target="${@: -1}"
case "$target" in
  */.qa-maintenance-selftest) printf '%s\n' '1000:1000:600' ;;
  */qa_archive_restore) printf '%s\n' "${TEST_RESTORE_FACT:-1000:1000:700}" ;;
  *) printf '%s\n' "${TEST_SCRATCH_FACT:-1000:1000:700}" ;;
esac
""",
            encoding="utf-8",
        )
        fake_stat.chmod(0o755)
        fake_flock = fake_bin / "flock"
        fake_flock.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
        fake_flock.chmod(0o755)
        child = {
            "receipt_version": 2,
            "mode": "qa_maintenance_archive",
            "ok": True,
            "job_name": "qa-maintenance",
            "run_id": "__RUN_ID__",
            "trigger": "__TRIGGER__",
            "plan": {
                "window_start": "2026-08-08T20:00:00Z",
                "window_end": "2026-08-08T21:00:00Z",
                "state": "committed",
                "commit_etag": "normal-etag",
                "restore_verified": True,
                "cleanup_eligible": False,
            },
            "compensation": {
                "window_start": "2026-08-07T22:00:00Z",
                "window_end": "2026-08-07T23:00:00Z",
                "state": "committed",
                "commit_etag": "catchup-etag",
                "restore_verified": True,
                "cleanup_eligible": False,
            },
            "deletion_authorized": False,
            "upload_authorized": True,
        }
        env = {
            "PATH": f"{fake_bin}:/opt/homebrew/bin:/usr/bin:/bin",
            "QA_MAINTENANCE_DOCKER": "docker",
            "QA_MAINTENANCE_RESOLVER": str(resolver),
            "QA_MAINTENANCE_RUNTIME_DIR": str(runtime),
            "QA_MAINTENANCE_HOST_DATA_ROOT": str(data_root),
            "QA_MAINTENANCE_HOST_SCRATCH": str(scratch),
            "QA_MAINTENANCE_RECEIPT": str(receipt),
            "QA_MAINTENANCE_SYSTEMD_DIR": str(systemd),
            "QA_LIFECYCLE_LOCK_FILE": str(root / "qa-lifecycle.lock"),
            "TEST_DOCKER_LOG": str(docker_log),
            "TEST_DATA_ROOT": str(data_root),
            "TEST_MOUNT_FACT": f"bind|{data_root}|true",
            "TEST_SCRATCH": str(scratch),
            "TEST_SCRATCH_FACT": "1000:1000:700",
            "TEST_RESTORE_FACT": "1000:1000:700",
            "TEST_IMAGE": image,
            "TEST_CHILD_RC": str(child_rc),
            "TEST_CHILD_JSON": json.dumps(child, separators=(",", ":")),
            "TEST_ARCHIVE_JSON": json.dumps(
                {
                    "ok": True,
                    "command": "inspect",
                    "window_start": "2026-08-08T20:00:00Z",
                    "cleanup_eligible": False,
                    "deletion_authorized": False,
                },
                separators=(",", ":"),
            ),
        }
        return env, receipt, docker_log, scratch, systemd

    def _install_activation_host_fakes(self, root: Path, env: dict[str, str]) -> Path:
        fake_bin = root / "bin"
        systemctl_log = root / "systemctl.log"
        systemctl = fake_bin / "systemctl"
        systemctl.write_text(
            """#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_SYSTEMCTL_LOG"
case "$*" in
  "disable --now tokenkey-qa-boundary.timer") exit 0 ;;
  "is-enabled tokenkey-qa-boundary.timer") printf 'disabled\n'; exit 1 ;;
  "is-active tokenkey-qa-boundary.timer") printf 'inactive\n'; exit 3 ;;
  "is-active tokenkey-qa-boundary.service")
    count=0
    [ ! -f "$TEST_SERVICE_POLLS" ] || count="$(cat "$TEST_SERVICE_POLLS")"
    count=$((count + 1))
    printf '%s\n' "$count" > "$TEST_SERVICE_POLLS"
    if [ "$count" -le "${TEST_SERVICE_ACTIVE_POLLS:-0}" ]; then
      printf 'active\n'; exit 0
    fi
    printf 'inactive\n'; exit 3 ;;
esac
exit 9
""",
            encoding="utf-8",
        )
        systemctl.chmod(0o755)
        flock = fake_bin / "flock"
        flock.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
        flock.chmod(0o755)
        activation_dir = root / "activation"
        activation_dir.mkdir(mode=0o700)
        env.update(
            {
                "QA_SINGLE_OWNER_ACTIVATION_DIR": str(activation_dir),
                "QA_SINGLE_OWNER_DRAIN_TIMEOUT_SECONDS": "2",
                "QA_LIFECYCLE_LOCK_FILE": str(root / "qa-lifecycle.lock"),
                "TEST_ACTIVATION_DIR": str(activation_dir),
                "TEST_SYSTEMCTL_LOG": str(systemctl_log),
                "TEST_SERVICE_POLLS": str(root / "service-polls"),
                "TEST_SERVICE_ACTIVE_POLLS": "1",
            }
        )
        return systemctl_log

    def test_phase3_activation_drains_boundary_and_commits_without_manual_maintenance(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            env, _, docker_log, _, _ = self._sandbox(root)
            systemctl_log = self._install_activation_host_fakes(root, env)
            plan_hash = "a" * 64

            proc = subprocess.run(
                [
                    "bash",
                    str(RUNNER),
                    "--activate-single-owner",
                    f"--plan-hash={plan_hash}",
                    f"--confirm=tokenkey-prod-qa-single-owner-activate-v1:{plan_hash}",
                ],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            receipt = json.loads(proc.stdout.strip().splitlines()[-1])
            self.assertTrue(receipt["ok"])
            self.assertEqual(receipt["plan_hash"], plan_hash)
            calls = systemctl_log.read_text(encoding="utf-8").splitlines()
            self.assertEqual(calls[0], "disable --now tokenkey-qa-boundary.timer")
            self.assertIn("is-active tokenkey-qa-boundary.service", calls)
            self.assertFalse(any(call.startswith(("stop ", "kill ", "enable ")) for call in calls))
            docker_calls = docker_log.read_text(encoding="utf-8")
            self.assertIn("--qa-single-owner-activate", docker_calls)
            self.assertNotIn("--qa-maintenance-once", docker_calls)
            ack_files = list(Path(env["TEST_ACTIVATION_DIR"]).glob("*.ack.json"))
            self.assertEqual(len(ack_files), 1)
            ack = json.loads(ack_files[0].read_text(encoding="utf-8"))
            self.assertEqual(ack["nonce"], "nonce-1")
            self.assertFalse(ack["boundary_service_active"])

    def test_phase3_activation_drain_timeout_never_forces_or_reenables_boundary(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            env, _, docker_log, _, _ = self._sandbox(root)
            systemctl_log = self._install_activation_host_fakes(root, env)
            env["QA_SINGLE_OWNER_DRAIN_TIMEOUT_SECONDS"] = "1"
            env["TEST_SERVICE_ACTIVE_POLLS"] = "999"
            plan_hash = "b" * 64

            proc = subprocess.run(
                [
                    "bash",
                    str(RUNNER),
                    "--activate-single-owner",
                    f"--plan-hash={plan_hash}",
                    f"--confirm=tokenkey-prod-qa-single-owner-activate-v1:{plan_hash}",
                ],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertNotEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            self.assertTrue(systemctl_log.exists(), (proc.stdout, proc.stderr))
            calls = systemctl_log.read_text(encoding="utf-8").splitlines()
            self.assertEqual(calls[0], "disable --now tokenkey-qa-boundary.timer")
            self.assertFalse(any(call.startswith(("stop ", "kill ", "enable ")) for call in calls))
            docker_calls = docker_log.read_text(encoding="utf-8") if docker_log.exists() else ""
            self.assertNotIn("--qa-single-owner-activate", docker_calls)

    def test_bundle_canary_does_not_block_or_depend_on_the_lifecycle_lock(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            env, _, docker_log, _, _ = self._sandbox(root)
            flock = root / "bin" / "flock"
            flock.write_text("#!/usr/bin/env bash\nexit 75\n", encoding="utf-8")
            flock.chmod(0o755)

            result = subprocess.run(
                ["bash", str(RUNNER), "--qa-bundle-canary"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("--qa-bundle-canary", docker_log.read_text(encoding="utf-8"))

    def test_us045_selftest_uses_real_image_user_and_mount_for_create_read_remove(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, _, docker_log, scratch, _ = self._sandbox(Path(temp_dir))
            proc = subprocess.run(
                ["bash", str(RUNNER), "--selftest"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            calls = docker_log.read_text(encoding="utf-8")
            self.assertIn("sha256:image-v2", calls)
            self.assertIn("--user=1000:1000", calls)
            self.assertIn(f"{scratch}:/app/data/qa_archive_tmp:rw", calls)
            self.assertIn("qa-maintenance-selftest-create", calls)
            self.assertIn("qa-maintenance-selftest-remove", calls)
            self.assertFalse((scratch / ".qa-maintenance-selftest").exists())

    def test_us045_runner_success_writes_atomic_correlated_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            env, receipt, docker_log, scratch, _ = self._sandbox(root)
            receipt.write_text('{"old":true}\n', encoding="utf-8")
            receipt.chmod(0o600)
            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=operator"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            payload = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertEqual(payload["schema_version"], "qa-maintenance-runner-v1")
            self.assertEqual(payload["trigger"], "operator")
            self.assertTrue(payload["run_id"])
            self.assertEqual(payload["active_container"], "tokenkey-green")
            self.assertEqual(payload["image"], "sha256:image-v2")
            self.assertEqual(payload["runner_uid"], 1000)
            self.assertEqual(payload["runner_gid"], 1000)
            self.assertEqual(payload["normal"]["commit_etag"], "normal-etag")
            self.assertEqual(payload["compensation"]["commit_etag"], "catchup-etag")
            self.assertEqual(payload["child_exit_code"], 0)
            self.assertEqual(payload["runner_exit_code"], 0)
            self.assertIs(payload["deletion_authorized"], False)
            self.assertEqual(stat.S_IMODE(receipt.stat().st_mode), 0o600)
            self.assertEqual(list(root.glob(".qa-maintenance-last-run.json.*")), [])
            calls = docker_log.read_text(encoding="utf-8")
            for expected in (
                "--user=1000:1000",
                "--read-only",
                "--cap-drop=ALL",
                "--security-opt=no-new-privileges",
                "--memory=1g",
                "--memory-swap=1g",
                "--cpus=0.20",
                "--pids-limit=128",
                "--network=container:tokenkey-green",
                f"{env['TEST_DATA_ROOT']}:/app/data:ro",
                f"{env['TEST_DATA_ROOT']}/qa_blobs:/app/data/qa_blobs:rw",
                f"{env['TEST_DATA_ROOT']}/qa_dlq:/app/data/qa_dlq:rw",
                f"{env['TEST_DATA_ROOT']}/qa_capture_ledger:/app/data/qa_capture_ledger:ro",
                f"{scratch}:/app/data/qa_archive_tmp:rw",
                "QA_MAINTENANCE_RUN_ID=",
                "QA_MAINTENANCE_TRIGGER=operator",
                "/app/sub2api --qa-maintenance-once",
            ):
                self.assertIn(expected, calls)
            self.assertNotIn("--volumes-from", calls)

    def test_phase3_runner_preserves_archive_gated_deletion_authorization(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, receipt, _, _, _ = self._sandbox(Path(temp_dir))
            child = json.loads(env["TEST_CHILD_JSON"])
            child["deletion_authorized"] = True
            child["normal_drop"] = {
                "partition_name": "qa_records_20260808_20",
                "source_dropped_at": "2026-08-08T21:16:00Z",
                "hot_files_cleaned": True,
            }
            env["TEST_CHILD_JSON"] = json.dumps(child, separators=(",", ":"))

            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=operator"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            payload = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertIs(payload["deletion_authorized"], True)
            self.assertEqual(
                payload["normal_drop"]["partition_name"],
                "qa_records_20260808_20",
            )

    def test_us045_runner_records_child_and_pre_app_failures(self) -> None:
        for name, image, child_rc, expected_code, expected_child in (
            ("child", "sha256:image-v2", 23, "child_failed", 23),
            ("image", "", 0, "image_unavailable", -1),
        ):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp_dir:
                env, receipt, _, _, _ = self._sandbox(
                    Path(temp_dir), image=image, child_rc=child_rc
                )
                proc = subprocess.run(
                    ["bash", str(RUNNER), "--trigger=timer"],
                    env=env,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertNotEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
                payload = json.loads(receipt.read_text(encoding="utf-8"))
                self.assertEqual(payload["error_code"], expected_code)
                self.assertEqual(payload["child_exit_code"], expected_child)
                self.assertEqual(payload["runner_exit_code"], proc.returncode)
                self.assertIs(payload["deletion_authorized"], False)
                self.assertEqual(stat.S_IMODE(receipt.stat().st_mode), 0o600)

    def test_runner_returns_child_stderr_when_archive_fails(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, receipt, _, _, _ = self._sandbox(Path(temp_dir), child_rc=23)
            env["TEST_CHILD_STDERR"] = "normal reconcile: source unavailable after retention"

            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=operator"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(proc.returncode, 23, (proc.stdout, proc.stderr))
            self.assertIn("normal reconcile: source unavailable after retention", proc.stderr)
            payload = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertEqual(payload["error_code"], "child_failed")
            self.assertNotIn("source unavailable", receipt.read_text(encoding="utf-8"))

    def test_runner_preserves_structured_child_facts_when_compensation_fails(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, receipt, _, _, _ = self._sandbox(Path(temp_dir), child_rc=1)
            child = json.loads(env["TEST_CHILD_JSON"])
            child.update(
                ok=False,
                failure_stage="compensation_terminal",
                failure_code="source_unavailable_after_retention",
            )
            child["compensation"].update(
                state="failed",
                commit_etag="",
                restore_verified=False,
                verification_error_code="source_unavailable_after_retention",
            )
            env["TEST_CHILD_JSON"] = json.dumps(child, separators=(",", ":"))

            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=timer"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(proc.returncode, 1, (proc.stdout, proc.stderr))
            payload = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertEqual(payload["normal"]["commit_etag"], "normal-etag")
            self.assertEqual(
                payload["compensation"]["verification_error_code"],
                "source_unavailable_after_retention",
            )
            self.assertEqual(payload["failure_stage"], "compensation_terminal")
            self.assertEqual(payload["failure_code"], "source_unavailable_after_retention")
            self.assertEqual(payload["child_exit_code"], 1)
            self.assertEqual(payload["runner_exit_code"], 1)

    def test_runner_rejects_uncorrelated_failure_json_as_host_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, receipt, _, _, _ = self._sandbox(Path(temp_dir), child_rc=1)
            child = json.loads(env["TEST_CHILD_JSON"])
            child.update(
                ok=False,
                run_id="different-run",
                failure_stage="compensation_terminal",
                failure_code="source_unavailable_after_retention",
            )
            env["TEST_CHILD_JSON"] = json.dumps(child, separators=(",", ":"))

            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=timer"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(proc.returncode, 1, (proc.stdout, proc.stderr))
            payload = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertIsNone(payload["normal"])
            self.assertIsNone(payload["compensation"])
            self.assertIsNone(payload["failure_stage"])
            self.assertIsNone(payload["failure_code"])

    def test_us045_runner_rejects_zero_exit_without_correlated_child_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, receipt, _, _, _ = self._sandbox(Path(temp_dir))
            env["TEST_CHILD_JSON"] = '{"ok":true}'
            proc = subprocess.run(
                ["bash", str(RUNNER), "--trigger=timer"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            payload = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertEqual(payload["error_code"], "child_receipt_invalid")
            self.assertEqual(payload["child_exit_code"], 0)
            self.assertEqual(payload["runner_exit_code"], proc.returncode)
            self.assertIsNone(payload["normal"])
            self.assertIs(payload["deletion_authorized"], False)

    def test_us045_runner_records_every_pre_app_failure(self) -> None:
        for name, expected_code, mutate in (
            (
                "runtime directory",
                "runtime_dir_unavailable",
                lambda env, root: env.update(
                    {"QA_MAINTENANCE_RUNTIME_DIR": str(root / "not-a-directory")}
                ),
            ),
            (
                "resolver unavailable",
                "resolver_unavailable",
                lambda env, root: env.update(
                    {"QA_MAINTENANCE_RESOLVER": str(root / "missing-resolver")}
                ),
            ),
            (
                "container unresolved",
                "container_unresolved",
                lambda env, root: env.update({"TEST_RESOLVER_FAIL": "1"}),
            ),
            (
                "image unavailable",
                "image_unavailable",
                lambda env, root: env.update({"TEST_IMAGE": ""}),
            ),
            (
                "data mount invalid",
                "data_mount_invalid",
                lambda env, root: env.update(
                    {"TEST_MOUNT_FACT": f"volume|{root / 'app'}|true"}
                ),
            ),
            (
                "scratch invalid",
                "scratch_invalid",
                lambda env, root: env.update({"TEST_SCRATCH_FACT": "1000:1000:755"}),
            ),
            (
                "environment capture",
                "env_capture_failed",
                lambda env, root: env.update({"TEST_ENV_CAPTURE_FAIL": "1"}),
            ),
        ):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp_dir:
                root = Path(temp_dir)
                env, receipt, _, _, _ = self._sandbox(root)
                if name == "runtime directory":
                    (root / "not-a-directory").write_text("occupied", encoding="utf-8")
                mutate(env, root)
                proc = subprocess.run(
                    ["bash", str(RUNNER), "--trigger=timer"],
                    env=env,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertNotEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
                payload = json.loads(receipt.read_text(encoding="utf-8"))
                self.assertEqual(payload["error_code"], expected_code)
                self.assertEqual(payload["child_exit_code"], -1)
                self.assertEqual(payload["runner_exit_code"], proc.returncode)
                self.assertIs(payload["deletion_authorized"], False)
                self.assertEqual(stat.S_IMODE(receipt.stat().st_mode), 0o600)

    def test_us045_installed_unit_uses_the_single_runner_with_approved_controls(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, _, _, _, systemd = self._sandbox(Path(temp_dir))
            proc = subprocess.run(
                ["bash", str(RUNNER), "--install-units"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, (proc.stdout, proc.stderr))
            service = (systemd / "tokenkey-qa-maintenance.service").read_text(
                encoding="utf-8"
            )
            for line in (
                "ExecStart=/usr/local/bin/tokenkey-qa-maintenance.sh --trigger=timer",
                "Nice=15",
                "IOSchedulingClass=idle",
                "CPUQuota=20%",
                "MemoryMax=1G",
                "PrivateTmp=true",
            ):
                self.assertIn(line, service)

    def test_phase3_unit_install_is_content_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            env, _, _, _, systemd = self._sandbox(Path(temp_dir))
            command = ["bash", str(RUNNER), "--install-units"]
            first = subprocess.run(command, env=env, capture_output=True, text=True, check=False)
            self.assertEqual(first.returncode, 0, (first.stdout, first.stderr))
            service = systemd / "tokenkey-qa-maintenance.service"
            timer = systemd / "tokenkey-qa-maintenance.timer"
            before = (service.stat().st_mtime_ns, timer.stat().st_mtime_ns)
            time.sleep(0.02)

            second = subprocess.run(command, env=env, capture_output=True, text=True, check=False)

            self.assertEqual(second.returncode, 0, (second.stdout, second.stderr))
            self.assertEqual(before, (service.stat().st_mtime_ns, timer.stat().st_mtime_ns))

    def test_us045_archive_restore_accepts_only_the_isolated_owned_root(self) -> None:
        for name, prepare, expected_code in (
            (
                "valid",
                lambda root, env: (root / "app/qa_archive_restore").mkdir(mode=0o700),
                0,
            ),
            (
                "symlink",
                lambda root, env: (root / "app/qa_archive_restore").symlink_to(
                    root / "outside", target_is_directory=True
                ),
                51,
            ),
            (
                "wrong mode",
                lambda root, env: (
                    (root / "app/qa_archive_restore").mkdir(mode=0o700),
                    env.update({"TEST_RESTORE_FACT": "1000:1000:755"}),
                ),
                51,
            ),
        ):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp_dir:
                root = Path(temp_dir)
                env, _, docker_log, _, _ = self._sandbox(root)
                (root / "outside").mkdir()
                prepare(root, env)
                proc = subprocess.run(
                    [
                        "bash",
                        str(RUNNER),
                        "--qa-archive",
                        "restore",
                        "--window-start",
                        "2026-08-08T20:00:00Z",
                        "--output=/app/data/qa_archive_restore/run-045",
                        "--confirm",
                        "privacy-confirmation",
                    ],
                    env=env,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(proc.returncode, expected_code, (proc.stdout, proc.stderr))
                calls = docker_log.read_text(encoding="utf-8")
                if expected_code == 0:
                    self.assertIn(
                        f"{root / 'app/qa_archive_restore'}:/app/data/qa_archive_restore:rw",
                        calls,
                    )
                    self.assertIn("/app/qa-archive restore", calls)
                else:
                    self.assertNotIn("/app/qa-archive restore", calls)

    def test_us045_archive_restore_rejects_dot_segments_and_empty_output_reuse(self) -> None:
        invalid_arguments = (
            ("dot", "--output=/app/data/qa_archive_restore/."),
            ("dotdot", "--output=/app/data/qa_archive_restore/.."),
            ("empty equals", "--output="),
            (
                "duplicate after empty",
                "--output=",
                "--output=/app/data/qa_archive_restore/run-045",
            ),
        )
        for arguments in invalid_arguments:
            with self.subTest(name=arguments[0]), tempfile.TemporaryDirectory() as temp_dir:
                root = Path(temp_dir)
                env, _, docker_log, _, _ = self._sandbox(root)
                (root / "app/qa_archive_restore").mkdir(mode=0o700)
                proc = subprocess.run(
                    [
                        "bash",
                        str(RUNNER),
                        "--qa-archive",
                        "restore",
                        "--window-start",
                        "2026-08-08T20:00:00Z",
                        *arguments[1:],
                        "--confirm",
                        "privacy-confirmation",
                    ],
                    env=env,
                    capture_output=True,
                    text=True,
                    check=False,
                )
                self.assertEqual(proc.returncode, 51, (proc.stdout, proc.stderr))
                self.assertNotIn(
                    "/app/qa-archive restore",
                    docker_log.read_text(encoding="utf-8"),
                )


class QAPhase2OperatorAndHealthTest(unittest.TestCase):
    def test_us045_timer_and_operators_invoke_the_single_host_runner(self) -> None:
        maintenance = _load_module("prod_qa_maintenance", "ops/qa/prod_qa_maintenance.py")
        archive_closeout = _load_module(
            "prod_qa_archive_closeout_runtime", "ops/qa/prod_qa_archive_closeout.py"
        )
        maintenance_remote = maintenance._remote_command()
        archive_remote = archive_closeout._remote_command(
            "inspect",
            dt.datetime(2026, 8, 8, 20, tzinfo=dt.timezone.utc),
            "",
            "",
        )
        runner = "/usr/local/bin/tokenkey-qa-maintenance.sh"
        self.assertIn(f"sudo {runner} --trigger=operator", maintenance_remote)
        self.assertIn(f"sudo {runner} --qa-archive inspect", archive_remote)
        for remote in (maintenance_remote, archive_remote):
            self.assertNotIn("docker exec", remote)
            self.assertNotIn("docker run", remote)

    def _healthy_snapshot(self) -> tuple[dict, dt.datetime]:
        now = dt.datetime(2026, 8, 8, 22, 30, tzinfo=dt.timezone.utc)
        window = "2026-08-08T20:00:00Z"
        snapshot = {
            "systemd": {
                "timer_enabled": True,
                "timer_active": True,
                "service_result": "success",
                "last_trigger_at": "2026-08-08T22:15:00Z",
                "finished_at": "2026-08-08T22:16:05Z",
            },
            "host_receipt": {
                "schema_version": "qa-maintenance-runner-v1",
                "run_id": "run-045",
                "trigger": "timer",
                "started_at": "2026-08-08T22:15:01Z",
                "finished_at": "2026-08-08T22:16:04Z",
                "active_container": "tokenkey-green",
                "image": "sha256:image-v2",
                "runner_uid": 1000,
                "runner_gid": 1000,
                "child_exit_code": 0,
                "runner_exit_code": 0,
                "error_code": "",
                "normal": {
                    "window_start": window,
                    "state": "committed",
                    "commit_etag": "etag-045",
                    "restore_verified": True,
                    "cleanup_eligible": False,
                },
                "compensation": None,
                "deletion_authorized": False,
            },
            "database_heartbeat": {
                "last_run_at": "2026-08-08T22:15:00Z",
                "last_success_at": "2026-08-08T22:16:03Z",
                "last_error_at": None,
                "last_result": (
                    "status=committed run_id=run-045 trigger=timer normal_window="
                    + window
                    + " normal_state=committed normal_commit_etag=etag-045 "
                    "normal_restore_verified=true deletion_authorized=false"
                ),
            },
            "archive_control": {
                "normal": {
                    "window_start": window,
                    "state": "committed",
                    "commit_etag": "etag-045",
                    "restore_verified": True,
                    "cleanup_eligible": False,
                },
                "compensation": None,
                "terminal_failures_after_cutover": [],
            },
            "boundary_systemd": {
                "timer_enabled": True,
                "timer_active": True,
                "service_result": "success",
                "last_trigger_at": "2026-08-08T22:00:00Z",
                "finished_at": "2026-08-08T22:00:05Z",
            },
            "boundary_host_receipt": {
                "schema_version": "qa-boundary-runner-v1",
                "run_id": "boundary-045",
                "trigger": "timer",
                "started_at": "2026-08-08T22:00:01Z",
                "finished_at": "2026-08-08T22:00:04Z",
                "active_container": "tokenkey-green",
                "image": "sha256:image-v2",
                "runner_uid": 1000,
                "runner_gid": 1000,
                "child_exit_code": 0,
                "runner_exit_code": 0,
                "error_code": "",
                "boundary": {
                    "provision": {
                        "db_anchor_utc": "2026-08-08T22:00:00Z",
                        "hours_ahead": 72,
                        "ranges_required": 72,
                        "ranges_covered": 72,
                        "attempts": 1,
                        "lock_retries": 0,
                    },
                    "deletion_authorized": False,
                },
                "deletion_authorized": False,
            },
            "boundary_database_heartbeat": {
                "last_run_at": "2026-08-08T22:00:00Z",
                "last_success_at": "2026-08-08T22:00:03Z",
                "last_error_at": None,
                "last_result": (
                    "status=ok phase=boundary run_id=boundary-045 trigger=timer "
                    "provision_covered=72/72 provision_attempts=1 "
                    "provision_lock_retries=0 deletion_authorized=false"
                ),
            },
            "qa_records": {
                "partition_owner": "partitioned",
                "hourly_cutover_active": True,
                "hourly_cutover_finalize_receipt_present": True,
                "hourly_cutover_finalized": True,
                "activate_t0_utc": "2026-08-07T20:00:00Z",
                "activate_plan_hash": "a" * 64,
                "activate_applied_at": "2026-08-07T19:30:00Z",
                "finalize_t0_utc": "2026-08-07T20:00:00Z",
                "finalize_plan_hash": "f" * 64,
                "finalize_applied_at": "2026-08-08T21:30:00Z",
                "default_present": False,
                "default_rows_after_t0": 0,
                "future_coverage_start_utc": "2026-08-08T22:00:00Z",
                "future_coverage_end_utc": "2026-08-11T22:00:00Z",
                "future_coverage_required_hours": 72,
                "future_coverage_canonical_hours": 72,
                "future_coverage_gap_hours": 0,
                "current_hour_partition_missing": False,
                "expired_partitions_attached": 0,
                "noncanonical_partitions_attached": 0,
                "hot_cleanup_backlog": 0,
                "hot_files_cleanup_pending": False,
            },
        }
        return snapshot, now

    def test_us045_correlated_health_accepts_only_matching_fresh_success(self) -> None:
        health = _load_module("qa_phase2_health", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        verdict = health.evaluate(snapshot, now=now)
        self.assertIs(verdict["healthy"], True)
        self.assertEqual(verdict["status"], "healthy")
        self.assertEqual(verdict["reasons"], [])

    def test_daemon_reload_allows_missing_service_finish_with_timer_receipt_db_chain(self) -> None:
        health = _load_module("qa_phase2_health_reload", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["systemd"]["finished_at"] = None
        snapshot["boundary_systemd"]["finished_at"] = None

        verdict = health.evaluate(snapshot, now=now)

        self.assertEqual(verdict["status"], "healthy", verdict)
        self.assertNotIn("systemd_finished_at_invalid", verdict["forward_reasons"], verdict)
        self.assertNotIn(
            "boundary_systemd_finished_at_invalid", verdict["forward_reasons"], verdict
        )

    def test_timer_receipt_start_correlation_fails_closed_for_both_lifecycles(self) -> None:
        health = _load_module("qa_phase2_health_timer", "ops/qa/qa_phase2_health.py")
        baseline, now = self._healthy_snapshot()
        for owner, reason in (
            ("systemd", "systemd_receipt_start_time_mismatch"),
            ("boundary_systemd", "boundary_systemd_receipt_start_time_mismatch"),
        ):
            with self.subTest(owner=owner):
                snapshot = copy.deepcopy(baseline)
                snapshot[owner]["last_trigger_at"] = "2026-08-08T21:00:00Z"

                verdict = health.evaluate(snapshot, now=now)

                self.assertEqual(verdict["status"], "failed", verdict)
                self.assertIn(reason, verdict["forward_reasons"], verdict)

    def test_missing_or_invalid_timer_trigger_fails_closed_for_both_lifecycles(self) -> None:
        health = _load_module(
            "qa_phase2_health_timer_missing", "ops/qa/qa_phase2_health.py"
        )
        baseline, now = self._healthy_snapshot()
        for owner, value, reason in (
            ("systemd", None, "systemd_last_trigger_at_invalid"),
            ("systemd", "invalid", "systemd_last_trigger_at_invalid"),
            (
                "boundary_systemd",
                None,
                "boundary_systemd_last_trigger_at_invalid",
            ),
            (
                "boundary_systemd",
                "invalid",
                "boundary_systemd_last_trigger_at_invalid",
            ),
        ):
            with self.subTest(owner=owner, value=value):
                snapshot = copy.deepcopy(baseline)
                snapshot[owner]["last_trigger_at"] = value

                verdict = health.evaluate(snapshot, now=now)

                self.assertEqual(verdict["status"], "failed", verdict)
                self.assertIn(reason, verdict["forward_reasons"], verdict)

    def test_service_finish_still_fails_when_present_and_mismatched(self) -> None:
        health = _load_module("qa_phase2_health_finish", "ops/qa/qa_phase2_health.py")
        baseline, now = self._healthy_snapshot()
        for owner, reason in (
            ("systemd", "systemd_receipt_time_mismatch"),
            ("boundary_systemd", "boundary_systemd_receipt_time_mismatch"),
        ):
            with self.subTest(owner=owner):
                snapshot = copy.deepcopy(baseline)
                snapshot[owner]["finished_at"] = "2026-08-08T21:00:00Z"

                verdict = health.evaluate(snapshot, now=now)

                self.assertEqual(verdict["status"], "failed", verdict)
                self.assertIn(reason, verdict["forward_reasons"], verdict)

    def test_boundary_provision_retry_facts_must_correlate(self) -> None:
        health = _load_module("qa_phase2_health_retry", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["boundary_host_receipt"]["boundary"]["provision"]["attempts"] = 3
        snapshot["boundary_host_receipt"]["boundary"]["provision"]["lock_retries"] = 2

        verdict = health.evaluate(snapshot, now=now)

        self.assertIs(verdict["healthy"], False)
        self.assertIn("boundary_provision_attempts_heartbeat_mismatch", verdict["reasons"])
        self.assertIn("boundary_provision_lock_retries_heartbeat_mismatch", verdict["reasons"])

    def test_boundary_correlated_nonzero_retry_facts_remain_healthy(self) -> None:
        health = _load_module("qa_phase2_health_retry_ok", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["boundary_host_receipt"]["boundary"]["provision"]["attempts"] = 3
        snapshot["boundary_host_receipt"]["boundary"]["provision"]["lock_retries"] = 2
        heartbeat = snapshot["boundary_database_heartbeat"]
        heartbeat["last_result"] = heartbeat["last_result"].replace(
            "provision_attempts=1 provision_lock_retries=0",
            "provision_attempts=3 provision_lock_retries=2",
        )

        verdict = health.evaluate(snapshot, now=now)

        self.assertIs(verdict["healthy"], True)
        self.assertEqual(verdict["reasons"], [])

    def test_boundary_legacy_retry_shape_is_accepted_during_rollout(self) -> None:
        health = _load_module("qa_phase2_health_retry_legacy", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        provision = snapshot["boundary_host_receipt"]["boundary"]["provision"]
        provision.pop("attempts")
        provision.pop("lock_retries")
        heartbeat = snapshot["boundary_database_heartbeat"]
        heartbeat["last_result"] = heartbeat["last_result"].replace(
            " provision_attempts=1 provision_lock_retries=0",
            "",
        )

        verdict = health.evaluate(snapshot, now=now)

        self.assertIs(verdict["healthy"], True)
        self.assertEqual(verdict["reasons"], [])

    def test_finalized_requires_complete_catalog_and_cleanup_facts(self) -> None:
        health = _load_module("qa_phase2_health_complete_facts", "ops/qa/qa_phase2_health.py")
        required = {
            "default_present": "qa_records_default_presence_unknown",
            "expired_partitions_attached": "qa_records_expired_partitions_fact_missing",
            "noncanonical_partitions_attached": "qa_records_noncanonical_partitions_fact_missing",
            "hot_cleanup_backlog": "qa_records_hot_cleanup_fact_missing",
            "hot_files_cleanup_pending": "qa_records_hot_cleanup_fact_missing",
        }

        for field, reason in required.items():
            with self.subTest(field=field):
                snapshot, now = self._healthy_snapshot()
                del snapshot["qa_records"][field]

                verdict = health.evaluate(snapshot, now=now)

                self.assertEqual(verdict["status"], "failed", verdict)
                self.assertIn(reason, verdict["forward_reasons"], verdict)

    def test_scheduled_activation_accepts_default_and_disabled_boundary_before_t0(self) -> None:
        health = _load_module("qa_phase2_health_scheduled", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["boundary_systemd"].update(
            timer_enabled=False,
            timer_active=False,
            service_result="unknown",
            finished_at=None,
        )
        snapshot["boundary_host_receipt"] = None
        snapshot["boundary_database_heartbeat"] = None
        snapshot["qa_records"].update(
            partition_owner="default_only",
            hourly_cutover_finalize_receipt_present=False,
            hourly_cutover_finalized=False,
            activate_t0_utc="2026-08-08T23:00:00Z",
            activate_applied_at="2026-08-08T21:30:00Z",
            finalize_t0_utc=None,
            finalize_plan_hash=None,
            finalize_applied_at=None,
            default_present=True,
            default_rows_after_t0=0,
            future_coverage_start_utc="2026-08-08T23:00:00Z",
            future_coverage_end_utc="2026-08-11T23:00:00Z",
            current_hour_partition_missing=True,
        )

        verdict = health.evaluate(snapshot, now=now)

        self.assertEqual(verdict["status"], "healthy", verdict)
        self.assertEqual(verdict["lifecycle_phase"], "scheduled_activation", verdict)

    def test_draining_accepts_decaying_horizon_but_rejects_default_growth_after_t0(self) -> None:
        health = _load_module("qa_phase2_health_draining", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["boundary_systemd"].update(
            timer_enabled=False,
            timer_active=False,
            service_result="unknown",
            finished_at=None,
        )
        snapshot["boundary_host_receipt"] = None
        snapshot["boundary_database_heartbeat"] = None
        snapshot["qa_records"].update(
            partition_owner="mixed",
            hourly_cutover_finalize_receipt_present=False,
            hourly_cutover_finalized=False,
            activate_t0_utc="2026-08-07T20:00:00Z",
            finalize_t0_utc=None,
            finalize_plan_hash=None,
            finalize_applied_at=None,
            default_present=True,
            default_rows_after_t0=0,
            future_coverage_start_utc="2026-08-08T22:00:00Z",
            future_coverage_end_utc="2026-08-11T22:00:00Z",
            future_coverage_canonical_hours=46,
            future_coverage_gap_hours=26,
            expired_partitions_attached=1,
        )

        verdict = health.evaluate(snapshot, now=now)
        self.assertEqual(verdict["status"], "healthy", verdict)
        self.assertEqual(verdict["lifecycle_phase"], "draining", verdict)

        snapshot["qa_records"]["default_rows_after_t0"] = 1
        failed = health.evaluate(snapshot, now=now)
        self.assertEqual(failed["status"], "failed", failed)
        self.assertIn("qa_records_default_growth_after_t0", failed["forward_reasons"], failed)

    def test_draining_rejects_missing_current_partition_fact(self) -> None:
        health = _load_module("qa_phase2_health_draining_missing", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["boundary_systemd"].update(timer_enabled=False, timer_active=False)
        snapshot["boundary_host_receipt"] = None
        snapshot["boundary_database_heartbeat"] = None
        snapshot["qa_records"].update(
            hourly_cutover_finalize_receipt_present=False,
            hourly_cutover_finalized=False,
            activate_t0_utc="2026-08-07T20:00:00Z",
            finalize_t0_utc=None,
            finalize_plan_hash=None,
            finalize_applied_at=None,
            default_present=True,
            default_rows_after_t0=0,
        )
        del snapshot["qa_records"]["current_hour_partition_missing"]

        verdict = health.evaluate(snapshot, now=now)

        self.assertEqual(verdict["status"], "failed", verdict)
        self.assertIn("qa_records_current_partition_missing", verdict["forward_reasons"], verdict)

    def test_pre_finalize_rejects_enabled_boundary_timer(self) -> None:
        health = _load_module("qa_phase2_health_early_boundary", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["qa_records"].update(
            hourly_cutover_finalize_receipt_present=False,
            hourly_cutover_finalized=False,
            finalize_t0_utc=None,
            finalize_plan_hash=None,
            finalize_applied_at=None,
            default_present=True,
        )

        verdict = health.evaluate(snapshot, now=now)

        self.assertEqual(verdict["status"], "failed", verdict)
        self.assertIn("boundary_timer_enabled_before_finalize", verdict["forward_reasons"], verdict)

    def test_finalized_rejects_mismatched_receipt_t0(self) -> None:
        health = _load_module("qa_phase2_health_receipt_t0", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["qa_records"]["finalize_t0_utc"] = "2026-08-07T21:00:00Z"

        verdict = health.evaluate(snapshot, now=now)

        self.assertEqual(verdict["status"], "failed", verdict)
        self.assertIn("qa_records_cutover_receipts_inconsistent", verdict["forward_reasons"], verdict)

    def test_finalized_rejects_receipt_application_time_reversal(self) -> None:
        health = _load_module("qa_phase2_health_receipt_order", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["qa_records"]["finalize_applied_at"] = "2026-08-07T19:00:00Z"

        verdict = health.evaluate(snapshot, now=now)

        self.assertEqual(verdict["status"], "failed", verdict)
        self.assertIn("qa_records_cutover_receipts_inconsistent", verdict["forward_reasons"], verdict)

    def test_us045_correlated_health_rejects_missing_stale_and_contradictory_facts(self) -> None:
        health = _load_module("qa_phase2_health_failures", "ops/qa/qa_phase2_health.py")
        baseline, now = self._healthy_snapshot()

        def mutate_missing_receipt(value: dict) -> None:
            value["host_receipt"] = None

        def mutate_stale_receipt(value: dict) -> None:
            value["host_receipt"]["finished_at"] = "2026-08-08T19:00:00Z"

        def mutate_systemd_failure(value: dict) -> None:
            value["systemd"]["service_result"] = "failed"

        def mutate_run_mismatch(value: dict) -> None:
            value["database_heartbeat"]["last_result"] = value["database_heartbeat"][
                "last_result"
            ].replace("run-045", "run-other")

        def mutate_heartbeat_trigger(value: dict) -> None:
            value["database_heartbeat"]["last_result"] = value["database_heartbeat"][
                "last_result"
            ].replace("trigger=timer", "trigger=operator")

        def mutate_operator_trigger(value: dict) -> None:
            value["host_receipt"]["trigger"] = "operator"

        def mutate_missing_runner_identity(value: dict) -> None:
            value["host_receipt"].pop("image")

        def mutate_run_time_mismatch(value: dict) -> None:
            value["database_heartbeat"]["last_run_at"] = "2026-08-08T21:00:00Z"

        def mutate_window_mismatch(value: dict) -> None:
            value["archive_control"]["normal"]["window_start"] = "2026-08-08T19:00:00Z"

        def mutate_etag_mismatch(value: dict) -> None:
            value["archive_control"]["normal"]["commit_etag"] = "etag-other"

        def mutate_newer_host_failure(value: dict) -> None:
            value["host_receipt"]["finished_at"] = "2026-08-08T22:20:00Z"
            value["host_receipt"]["runner_exit_code"] = 1
            value["host_receipt"]["error_code"] = "child_failed"

        def mutate_terminal_failure(value: dict) -> None:
            value["archive_control"]["terminal_failures_after_cutover"] = [
                {
                    "window_start": "2026-08-07T22:00:00Z",
                    "verification_error_code": "source_unavailable_after_retention",
                }
            ]

        def mutate_phantom_control_compensation(value: dict) -> None:
            value["archive_control"]["compensation"] = {
                "window_start": "2026-08-07T22:00:00Z",
                "state": "committed",
                "commit_etag": "etag-phantom",
                "restore_verified": True,
                "cleanup_eligible": False,
            }

        def mutate_phantom_heartbeat_compensation(value: dict) -> None:
            value["database_heartbeat"]["last_result"] += (
                " compensation_window=2026-08-07T22:00:00Z"
            )

        for name, mutation, policy, want_healthy, want_status in (
            ("terminal failure strict", mutate_terminal_failure, "strict", False, "failed"),
            ("terminal failure accepted", mutate_terminal_failure, "accepted_terminal", False, "degraded"),
        ):
            with self.subTest(name=name):
                snapshot = copy.deepcopy(baseline)
                mutation(snapshot)
                verdict = health.evaluate(snapshot, now=now, catchup_gap_policy=policy)
                self.assertIs(verdict["healthy"], want_healthy, verdict)
                self.assertEqual(verdict["status"], want_status, verdict)
                if policy == "accepted_terminal":
                    self.assertIn("catchup_terminal_gaps_present", verdict["catchup_reasons"], verdict)
                else:
                    self.assertIn("terminal_failures_after_cutover", verdict["catchup_reasons"], verdict)

        for name, mutation in (
            ("missing receipt", mutate_missing_receipt),
            ("stale receipt", mutate_stale_receipt),
            ("systemd failure", mutate_systemd_failure),
            ("run mismatch", mutate_run_mismatch),
            ("heartbeat trigger mismatch", mutate_heartbeat_trigger),
            ("operator trigger", mutate_operator_trigger),
            ("missing runner identity", mutate_missing_runner_identity),
            ("run time mismatch", mutate_run_time_mismatch),
            ("window mismatch", mutate_window_mismatch),
            ("etag mismatch", mutate_etag_mismatch),
            ("newer host failure", mutate_newer_host_failure),
            ("phantom control compensation", mutate_phantom_control_compensation),
            ("phantom heartbeat compensation", mutate_phantom_heartbeat_compensation),
        ):
            with self.subTest(name=name):
                snapshot = copy.deepcopy(baseline)
                mutation(snapshot)
                verdict = health.evaluate(snapshot, now=now)
                self.assertIs(verdict["healthy"], False, verdict)
                self.assertEqual(verdict["status"], "failed", verdict)
                self.assertTrue(verdict["reasons"], verdict)

    def test_terminal_compensation_failure_keeps_forward_health_and_degrades_catchup(self) -> None:
        health = _load_module("qa_phase2_health_terminal_run", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        terminal_window = "2026-08-07T22:00:00Z"
        snapshot["systemd"]["service_result"] = "exit-code"
        snapshot["host_receipt"].update(
            child_exit_code=1,
            runner_exit_code=1,
            error_code="child_failed",
            failure_stage="compensation_terminal",
            failure_code="source_unavailable_after_retention",
            compensation={
                "window_start": terminal_window,
                "state": "failed",
                "commit_etag": "",
                "restore_verified": False,
                "verification_error_code": "source_unavailable_after_retention",
                "cleanup_eligible": False,
            },
        )
        snapshot["database_heartbeat"].update(
            last_success_at="2026-08-08T22:16:03Z",
            last_error_at="2026-08-08T22:16:03Z",
            last_result=(
                "status=failed run_id=run-045 trigger=timer "
                "stage=compensation_terminal error_code=source_unavailable_after_retention "
                "normal_window=2026-08-08T20:00:00Z normal_state=committed "
                "normal_commit_etag=etag-045 normal_restore_verified=true "
                f"compensation_window={terminal_window} compensation_state=failed "
                "compensation_error_code=source_unavailable_after_retention "
                "deletion_authorized=false"
            ),
        )
        snapshot["archive_control"].update(
            compensation={
                "window_start": terminal_window,
                "state": "failed",
                "commit_etag": None,
                "restore_verified": False,
                "verification_error_code": "source_unavailable_after_retention",
                "cleanup_eligible": False,
            },
            terminal_failures_after_cutover=[
                {
                    "window_start": terminal_window,
                    "verification_error_code": "source_unavailable_after_retention",
                }
            ],
        )

        verdict = health.evaluate(snapshot, now=now, catchup_gap_policy="accepted_terminal")

        self.assertEqual(verdict["status"], "degraded", verdict)
        self.assertEqual(verdict["forward_reasons"], [], verdict)
        self.assertEqual(verdict["catchup_reasons"], ["catchup_terminal_gaps_present"], verdict)

        snapshot["host_receipt"]["failure_code"] = "archive_failed"
        failed = health.evaluate(snapshot, now=now, catchup_gap_policy="accepted_terminal")
        self.assertEqual(failed["status"], "failed", failed)
        self.assertTrue(failed["forward_reasons"], failed)

    def test_us045_boundary_health_requires_matching_three_source_success(self) -> None:
        health = _load_module("qa_phase2_health_boundary", "ops/qa/qa_phase2_health.py")
        baseline, now = self._healthy_snapshot()

        mutations = {
            "boundary systemd missing": lambda value: value.pop("boundary_systemd"),
            "boundary receipt missing": lambda value: value.pop("boundary_host_receipt"),
            "boundary heartbeat missing": lambda value: value.pop("boundary_database_heartbeat"),
            "boundary run mismatch": lambda value: value["boundary_database_heartbeat"].update(
                last_result=value["boundary_database_heartbeat"]["last_result"].replace(
                    "boundary-045", "boundary-other"
                )
            ),
            "boundary failure": lambda value: value["boundary_host_receipt"].update(
                runner_exit_code=1, error_code="child_failed"
            ),
            "boundary stale": lambda value: value["boundary_host_receipt"].update(
                finished_at="2026-08-08T19:00:00Z"
            ),
        }
        for name, mutation in mutations.items():
            with self.subTest(name=name):
                snapshot = copy.deepcopy(baseline)
                mutation(snapshot)
                verdict = health.evaluate(snapshot, now=now)
                self.assertEqual(verdict["status"], "failed", verdict)
                self.assertTrue(
                    any(reason.startswith("boundary_") for reason in verdict["forward_reasons"]),
                    verdict,
                )

    def test_us045_catalog_health_requires_exact_canonical_current_plus_72h(self) -> None:
        health = _load_module("qa_phase2_health_catalog", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["qa_records"]["future_coverage_canonical_hours"] = 71
        snapshot["qa_records"]["future_coverage_gap_hours"] = 0
        verdict = health.evaluate(snapshot, now=now)
        self.assertEqual(verdict["status"], "failed", verdict)
        self.assertIn("qa_records_future_partition_gap", verdict["forward_reasons"], verdict)

    def test_us045_catalog_health_rejects_finalize_without_same_t0_activation(self) -> None:
        health = _load_module("qa_phase2_health_cutover_receipts", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["qa_records"]["hourly_cutover_finalized"] = False
        verdict = health.evaluate(snapshot, now=now)
        self.assertEqual(verdict["status"], "failed", verdict)
        self.assertIn("qa_records_cutover_receipts_inconsistent", verdict["forward_reasons"], verdict)

    def test_us045_catalog_health_rejects_finalize_without_activation(self) -> None:
        health = _load_module("qa_phase2_health_orphan_finalize", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        snapshot["qa_records"].update(
            hourly_cutover_active=False,
            hourly_cutover_finalize_receipt_present=True,
            hourly_cutover_finalized=False,
        )
        verdict = health.evaluate(snapshot, now=now)
        self.assertEqual(verdict["status"], "failed", verdict)
        self.assertIn("qa_records_cutover_receipts_inconsistent", verdict["forward_reasons"], verdict)

    def test_us045_archive_failed_health_exits_failed(self) -> None:
        health = _load_module("qa_phase2_health_archive_failed", "ops/qa/qa_phase2_health.py")
        baseline, now = self._healthy_snapshot()
        baseline["qa_records"] = {"hourly_cutover_active": False}
        baseline["archive_control"]["archive_failed_windows"] = [
            {"window_start": "2026-08-07T22:00:00Z", "verification_error_code": "archive_failed"}
        ]
        verdict = health.evaluate(baseline, now=now)
        self.assertEqual(verdict["status"], "failed", verdict)
        self.assertIn("archive_failed", verdict["reasons"], verdict)

    def test_us045_terminal_inventory_only_degrades_for_source_retention_gap(self) -> None:
        health = _load_module("qa_phase2_health_terminal_codes", "ops/qa/qa_phase2_health.py")
        baseline, now = self._healthy_snapshot()
        for code, reason in (
            ("archive_failed", "archive_failed"),
            ("restore_failed", "archive_verification_failure"),
        ):
            with self.subTest(code=code):
                snapshot = copy.deepcopy(baseline)
                snapshot["archive_control"]["terminal_failures_after_cutover"] = [
                    {
                        "window_start": "2026-08-07T22:00:00Z",
                        "verification_error_code": code,
                    }
                ]
                verdict = health.evaluate(
                    snapshot,
                    now=now,
                    catchup_gap_policy="accepted_terminal",
                )
                self.assertEqual(verdict["status"], "failed", verdict)
                self.assertIn(reason, verdict["catchup_reasons"], verdict)


if __name__ == "__main__":
    raise SystemExit(unittest.main())
