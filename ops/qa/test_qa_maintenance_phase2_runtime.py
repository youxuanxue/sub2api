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
        receipt = root / "qa-maintenance-last-run.json"
        systemd = root / "systemd"
        for path in (fake_bin, runtime, data_root, scratch, systemd):
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
	  if [[ "$*" == *'/app/sub2api'* ]]; then
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
                f"{scratch}:/app/data/qa_archive_tmp:rw",
                "QA_MAINTENANCE_RUN_ID=",
                "QA_MAINTENANCE_TRIGGER=operator",
                "/app/sub2api --qa-maintenance-once",
            ):
                self.assertIn(expected, calls)
            self.assertNotIn("--volumes-from", calls)

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
                    "status=committed run_id=run-045 normal_window="
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
        }
        return snapshot, now

    def test_us045_correlated_health_accepts_only_matching_fresh_success(self) -> None:
        health = _load_module("qa_phase2_health", "ops/qa/qa_phase2_health.py")
        snapshot, now = self._healthy_snapshot()
        verdict = health.evaluate(snapshot, now=now)
        self.assertIs(verdict["healthy"], True)
        self.assertEqual(verdict["status"], "healthy")
        self.assertEqual(verdict["reasons"], [])

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

        for name, mutation in (
            ("missing receipt", mutate_missing_receipt),
            ("stale receipt", mutate_stale_receipt),
            ("systemd failure", mutate_systemd_failure),
            ("run mismatch", mutate_run_mismatch),
            ("operator trigger", mutate_operator_trigger),
            ("missing runner identity", mutate_missing_runner_identity),
            ("run time mismatch", mutate_run_time_mismatch),
            ("window mismatch", mutate_window_mismatch),
            ("etag mismatch", mutate_etag_mismatch),
            ("newer host failure", mutate_newer_host_failure),
            ("terminal failure", mutate_terminal_failure),
        ):
            with self.subTest(name=name):
                snapshot = copy.deepcopy(baseline)
                mutation(snapshot)
                verdict = health.evaluate(snapshot, now=now)
                self.assertIs(verdict["healthy"], False, verdict)
                self.assertIn(verdict["status"], {"degraded", "failed"})
                self.assertTrue(verdict["reasons"], verdict)


if __name__ == "__main__":
    raise SystemExit(unittest.main())
