#!/usr/bin/env python3
from __future__ import annotations

import datetime as dt
import hashlib
import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("prod_qa_stale_cleanup", HERE / "prod_qa_stale_cleanup.py")
assert SPEC is not None and SPEC.loader is not None
control = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(control)

INSTANCE = "i-0123456789abcdef0"
CUTOFF = "2026-08-06T12:00:00.000000Z"
CONFIRM = "tokenkey-prod-qa-retention-apply-v1:" + CUTOFF
MARKER_SHA = "a" * 64
EXPORT_CONFIRM_PREFIX = "tokenkey-prod-qa-export-orphan-apply-v1:"


def export_plan(*, files: list[dict] | None = None) -> dict:
    entries = files or []
    value = {
        "schema_version": "qa-export-orphan-plan-v1",
        "container_dir": "/app/data/qa_exports_tmp",
        "host_dir": "/var/lib/tokenkey/app/qa_exports_tmp",
        "cutoff": CUTOFF,
        "directory_present": True,
        "files": entries,
        "count": len(entries),
        "total_bytes": sum(item["size_bytes"] for item in entries),
        "deletion_authorized": False,
    }
    digest = hashlib.sha256(
        json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest()
    return {
        **value,
        "plan_hash": digest,
        "required_confirmation": EXPORT_CONFIRM_PREFIX + digest,
    }


def plan(clock: str | None = None) -> dict:
    return {
        "mode": "prod_data_retention_activation_plan",
        "environment": "prod",
        "instance_id": INSTANCE,
        "activation_ready": True,
        "deletion_authorized": False,
        "ops": {"server_clock": clock or dt.datetime.now(dt.timezone.utc).isoformat()},
        "qa": {
            "mode": "prod_qa_age_retention_plan",
            "cutoff": CUTOFF,
            "active_image": "ghcr.io/youxuanxue/sub2api:1.8.140",
            "candidate_rows": 42,
            "candidate_blob_files": 3,
            "candidate_dlq_files": 1,
            "required_confirmation": CONFIRM,
            "export_tmp": export_plan(),
            "export_jobs": {
                "total_rows": 4,
                "expired_rows": 1,
                "status_counts": {"done": 2, "failed": 2},
                "done_without_storage_key": 0,
                "non_done_with_storage_key": 0,
            },
            "deletion_authorized": False,
        },
    }


class ProdQAStaleCleanupTest(unittest.TestCase):
    def _host_sandbox(
        self,
        root: pathlib.Path,
        *,
        export_container_dir: str = "/app/data/qa_exports_tmp",
        export_host_dir: pathlib.Path | None = None,
        configured_export_dir: str = "",
    ) -> tuple[dict[str, str], pathlib.Path, pathlib.Path, pathlib.Path]:
        fake_bin = root / "bin"
        fake_bin.mkdir()
        calls = root / "calls.log"
        proc_root = root / "proc"
        proc_root.mkdir()
        app_root = root / "app"
        app_root.mkdir()
        (app_root / "qa_blobs").mkdir()
        (app_root / "qa_dlq").mkdir()
        host_export = export_host_dir or (app_root / "qa_exports_tmp")
        host_export.mkdir(parents=True)
        (root / "active-color").write_text("blue\n", encoding="utf-8")
        env_value = f"QA_EXPORT_TMP_DIR={configured_export_dir}" if configured_export_dir else ""
        inspect_payload = [
            {
                "Config": {
                    "Image": "ghcr.io/youxuanxue/sub2api:1.8.140",
                    "Env": [value for value in (env_value, "QA_CAPTURE_ENABLED=true") if value],
                },
                "Mounts": [
                    {
                        "Type": "bind",
                        "Source": str(
                            host_export if configured_export_dir else app_root
                        ),
                        "Destination": (
                            export_container_dir if configured_export_dir else "/app/data"
                        ),
                        "RW": True,
                    }
                ],
            }
        ]
        inspect_json = root / "inspect.json"
        inspect_json.write_text(json.dumps(inspect_payload), encoding="utf-8")
        db_json = root / "db.json"
        db_json.write_text(
            json.dumps(
                {
                    "server_clock": "2026-08-07T12:00:00.000000Z",
                    "cutoff": CUTOFF,
                    "candidate_rows": 0,
                    "oldest_created_at": None,
                    "newest_created_at": None,
                    "export_jobs": {
                        "total_rows": 4,
                        "expired_rows": 1,
                        "status_counts": {"done": 2, "failed": 2},
                        "done_without_storage_key": 0,
                        "non_done_with_storage_key": 0,
                    },
                }
            ),
            encoding="utf-8",
        )
        (fake_bin / "docker").write_text(
            """#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "$CALLS"
case "$1" in
  ps) echo tokenkey-postgres ;;
  inspect)
    if [[ "$*" == *--format* ]]; then
      echo ghcr.io/youxuanxue/sub2api:1.8.140
    else
      cat "$TEST_INSPECT_JSON"
    fi
    ;;
  exec)
    case "$*" in
      *"WITH bounds AS"*) cat "$TEST_DB_JSON" ;;
      *"clock_timestamp()-interval '24 hours'"*) echo '2026-08-06T12:00:00.000000Z' ;;
      *"'ready'"*) echo '{"ready":true,"fresh":true,"candidate_rows":0}' ;;
      *"DELETE FROM qa_records"*) echo 0 ;;
      *"SELECT count(*) FROM qa_records"*) echo 0 ;;
      *"'applied_at'"*) echo '{"applied_at":"2026-08-07T12:01:00.000000Z","authorization_expires_at":"2026-08-07T12:11:00.000000Z"}' ;;
      *) echo 0 ;;
    esac
    ;;
  *) exit 9 ;;
esac
""",
            encoding="utf-8",
        )
        (fake_bin / "systemctl").write_text(
            """#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >> "$CALLS"
case "$*" in
  *tokenkey-qa-maintenance.timer*)
    [[ "$1" == is-enabled ]] && echo disabled || echo inactive ;;
  *tokenkey-qa-stale-cleanup.timer*)
    [[ "$1" == is-enabled ]] && echo disabled || echo inactive ;;
  *) exit 9 ;;
esac
""",
            encoding="utf-8",
        )
        (fake_bin / "flock").write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
        (fake_bin / "logger").write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
        (fake_bin / "find").write_text(
            "#!/usr/bin/env bash\nprintf 'find %s\\n' \"$*\" >> \"$CALLS\"\nexit 0\n",
            encoding="utf-8",
        )
        (fake_bin / "sha256sum").write_text(
            f"#!/usr/bin/env bash\necho '{MARKER_SHA}  $1'\n", encoding="utf-8"
        )
        for name in ("docker", "systemctl", "flock", "logger", "find", "sha256sum"):
            (fake_bin / name).chmod(0o755)
        env = {
            "PATH": f"{fake_bin}:{os.environ.get('PATH', '/usr/bin:/bin')}",
            "CALLS": str(calls),
            "TOKENKEY_ROOT": str(root),
            "QA_STALE_PROC_ROOT": str(proc_root),
            "EXPORT_ORPHAN_HELPER": str(
                HERE.parent.parent / "deploy/aws/stage0/tokenkey-qa-export-orphan.py"
            ),
            "TEST_INSPECT_JSON": str(inspect_json),
            "TEST_DB_JSON": str(db_json),
        }
        return env, host_export, proc_root, calls

    def _run_host(self, env: dict[str, str], *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["bash", str(HERE.parent.parent / "deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh"), *args],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_us045_export_orphan_plan_is_exact_and_revalidated(self) -> None:
        cutoff = dt.datetime.strptime(CUTOFF, "%Y-%m-%dT%H:%M:%S.%fZ").replace(
            tzinfo=dt.timezone.utc
        )
        cutoff_ns = int(cutoff.timestamp() * 1_000_000_000)
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            env, export_root, proc_root, calls = self._host_sandbox(root)
            known = export_root / "traj-export-4288971549.zip"
            with known.open("wb") as handle:
                handle.truncate(1_041_960_960)
            os.utime(known, ns=(cutoff_ns - 1_000_000_000, cutoff_ns - 1_000_000_000))
            boundary = export_root / "boundary.zip"
            boundary.write_bytes(b"boundary")
            os.utime(boundary, ns=(cutoff_ns, cutoff_ns))
            fresh = export_root / "fresh.zip"
            fresh.write_bytes(b"fresh")
            os.utime(fresh, ns=(cutoff_ns + 1_000_000_000, cutoff_ns + 1_000_000_000))
            (export_root / "nested").mkdir()
            (export_root / "symlink.zip").symlink_to(known)
            opened = export_root / "opened.zip"
            opened.write_bytes(b"open")
            os.utime(opened, ns=(cutoff_ns - 1_000_000_000, cutoff_ns - 1_000_000_000))
            fd_dir = proc_root / "123/fd"
            fd_dir.mkdir(parents=True)
            (fd_dir / "7").symlink_to(opened)

            first = self._run_host(env, "--plan")
            second = self._run_host(env, "--plan")
            self.assertEqual(first.returncode, 0, first.stderr)
            self.assertEqual(second.returncode, 0, second.stderr)
            payload = json.loads(first.stdout)
            same = json.loads(second.stdout)
            export = payload["export_tmp"]
            self.assertEqual(export["plan_hash"], same["export_tmp"]["plan_hash"])
            self.assertEqual(export["count"], 1)
            self.assertEqual(export["total_bytes"], 1_041_960_960)
            self.assertEqual(
                export["files"],
                [
                    {
                        "basename": "traj-export-4288971549.zip",
                        "size_bytes": 1_041_960_960,
                        "mtime": cutoff_ns - 1_000_000_000,
                    }
                ],
            )
            self.assertEqual(export["host_dir"], str(export_root))
            self.assertNotEqual(export["host_dir"], str(root / "qa_exports_tmp"))
            self.assertIs(export["deletion_authorized"], False)
            self.assertEqual(payload["export_jobs"]["total_rows"], 4)

            applied = self._run_host(
                env,
                "--apply-export-orphans",
                "--cutoff",
                CUTOFF,
                "--expected-active-image",
                payload["active_image"],
                "--expected-plan-hash",
                export["plan_hash"],
                "--confirm",
                export["required_confirmation"],
            )
            self.assertEqual(applied.returncode, 0, applied.stderr)
            receipt = json.loads(applied.stdout)
            self.assertEqual(receipt["files"], export["files"])
            self.assertEqual(receipt["deleted_count"], 1)
            self.assertEqual(receipt["deleted_bytes"], 1_041_960_960)
            self.assertFalse(known.exists())
            self.assertTrue(boundary.exists())
            self.assertTrue(fresh.exists())
            self.assertTrue(opened.exists())
            self.assertTrue((root / "qa-export-orphan-cleanup-activated.json").is_file())
            self.assertNotIn("DELETE FROM qa_export_jobs", calls.read_text(encoding="utf-8"))

    def test_export_orphan_apply_rejects_plan_drift_before_removal(self) -> None:
        cutoff = dt.datetime.strptime(CUTOFF, "%Y-%m-%dT%H:%M:%S.%fZ").replace(
            tzinfo=dt.timezone.utc
        )
        old_ns = int((cutoff - dt.timedelta(seconds=1)).timestamp() * 1_000_000_000)
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            env, export_root, _, _ = self._host_sandbox(root)
            candidate = export_root / "candidate.zip"
            candidate.write_bytes(b"planned")
            os.utime(candidate, ns=(old_ns, old_ns))
            payload = json.loads(self._run_host(env, "--plan").stdout)
            candidate.write_bytes(b"changed-size")
            os.utime(candidate, ns=(old_ns, old_ns))
            export = payload["export_tmp"]
            applied = self._run_host(
                env,
                "--apply-export-orphans",
                "--cutoff",
                CUTOFF,
                "--expected-active-image",
                payload["active_image"],
                "--expected-plan-hash",
                export["plan_hash"],
                "--confirm",
                export["required_confirmation"],
            )
            self.assertNotEqual(applied.returncode, 0)
            self.assertTrue(candidate.exists())
            self.assertFalse((root / "qa-export-orphan-cleanup-activated.json").exists())

    def test_scheduled_export_cleanup_waits_for_exact_first_activation(self) -> None:
        cutoff = dt.datetime.strptime(CUTOFF, "%Y-%m-%dT%H:%M:%S.%fZ").replace(
            tzinfo=dt.timezone.utc
        )
        old_ns = int((cutoff - dt.timedelta(seconds=1)).timestamp() * 1_000_000_000)
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            env, export_root, _, _ = self._host_sandbox(root)
            first = export_root / "first.zip"
            first.write_bytes(b"first")
            os.utime(first, ns=(old_ns, old_ns))

            before_activation = self._run_host(env)
            self.assertEqual(before_activation.returncode, 0, before_activation.stderr)
            before_receipt = json.loads(before_activation.stdout)
            self.assertTrue(before_receipt["export_tmp"]["activation_required"])
            self.assertEqual(before_receipt["export_tmp"]["deleted_count"], 0)
            self.assertTrue(first.exists())

            planned = json.loads(self._run_host(env, "--plan").stdout)
            export = planned["export_tmp"]
            activated = self._run_host(
                env,
                "--apply-export-orphans",
                "--cutoff",
                CUTOFF,
                "--expected-active-image",
                planned["active_image"],
                "--expected-plan-hash",
                export["plan_hash"],
                "--confirm",
                export["required_confirmation"],
            )
            self.assertEqual(activated.returncode, 0, activated.stderr)
            second = export_root / "second.zip"
            second.write_bytes(b"second")
            os.utime(second, ns=(old_ns, old_ns))

            after_activation = self._run_host(env)
            self.assertEqual(after_activation.returncode, 0, after_activation.stderr)
            after_receipt = json.loads(after_activation.stdout)
            self.assertEqual(after_receipt["export_tmp"]["deleted_count"], 1)
            self.assertFalse(second.exists())

    def test_export_tmp_override_resolves_its_effective_bind(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            override = root / "override"
            env, _, _, _ = self._host_sandbox(
                root,
                export_container_dir="/mnt/exports",
                export_host_dir=override,
                configured_export_dir="/mnt/exports",
            )
            proc = self._run_host(env, "--plan")
            self.assertEqual(proc.returncode, 0, proc.stderr)
            export = json.loads(proc.stdout)["export_tmp"]
            self.assertEqual(export["container_dir"], "/mnt/exports")
            self.assertEqual(export["host_dir"], str(override))

    def test_age_retention_first_apply_does_not_depend_on_maintenance_timer(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            env, _, _, calls = self._host_sandbox(root)
            proc = self._run_host(
                env,
                "--apply-first",
                "--cutoff",
                CUTOFF,
                "--expected-rows",
                "0",
                "--expected-blob-files",
                "0",
                "--expected-dlq-files",
                "0",
                "--expected-active-image",
                "ghcr.io/youxuanxue/sub2api:1.8.140",
                "--confirm",
                CONFIRM,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertNotIn(
                "systemctl is-enabled tokenkey-qa-maintenance.timer",
                calls.read_text(encoding="utf-8"),
            )

    def test_wrong_confirmation_is_rejected_before_ssm(self) -> None:
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(control, "_run_remote") as remote:
            path = pathlib.Path(temp) / "plan.json"
            path.write_text(json.dumps(plan()), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "confirmation"):
                control.apply_first(path, pathlib.Path(temp) / "receipt.json", "wrong")
        remote.assert_not_called()

    def test_existing_receipt_is_rejected_before_ssm(self) -> None:
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(control, "_run_remote") as remote:
            root = pathlib.Path(temp)
            path = root / "plan.json"
            receipt = root / "receipt.json"
            path.write_text(json.dumps(plan()), encoding="utf-8")
            receipt.write_text("{}\n", encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "already exists"):
                control.apply_first(path, receipt, CONFIRM)
        remote.assert_not_called()

    def test_stale_plan_can_only_use_host_guarded_resume_mode(self) -> None:
        old_clock = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(hours=1)).isoformat()
        remote_receipt = {
            "mode": "prod_qa_age_retention_first_apply",
            "cutoff": CUTOFF,
            "applied_at": "2026-08-07T12:01:00.000000Z",
            "authorization_expires_at": "2026-08-07T12:11:00.000000Z",
            "planned_rows": 42,
            "deleted_rows_this_attempt": 1,
            "planned_blob_files": 3,
            "planned_dlq_files": 1,
            "remaining_rows": 0,
            "remaining_blob_files": 0,
            "remaining_dlq_files": 0,
            "marker_sha256": MARKER_SHA,
            "deletion_authorized": True,
        }
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote", return_value=("ssm-1", remote_receipt)
        ) as remote:
            root = pathlib.Path(temp)
            path = root / "plan.json"
            path.write_text(json.dumps(plan(old_clock)), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "stale"):
                control.apply_first(path, root / "wrong-mode.json", CONFIRM)
            control.apply_first(path, root / "receipt.json", CONFIRM, resume=True)
        self.assertIn("--resume-first", remote.call_args.args[1])
        self.assertNotIn("--apply-first", remote.call_args.args[1])

    def test_future_plan_is_rejected_before_ssm(self) -> None:
        future_clock = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(minutes=1)).isoformat()
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(control, "_run_remote") as remote:
            root = pathlib.Path(temp)
            path = root / "plan.json"
            path.write_text(json.dumps(plan(future_clock)), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "future"):
                control.apply_first(path, root / "receipt.json", CONFIRM)
        remote.assert_not_called()

    def test_incomplete_remote_cleanup_never_persists_receipt(self) -> None:
        remote_receipt = {
            "mode": "prod_qa_age_retention_first_apply",
            "cutoff": CUTOFF,
            "applied_at": "2026-08-07T12:01:00.000000Z",
            "authorization_expires_at": "2026-08-07T12:11:00.000000Z",
            "planned_rows": 42,
            "deleted_rows_this_attempt": 41,
            "planned_blob_files": 3,
            "planned_dlq_files": 1,
            "remaining_rows": 1,
            "remaining_blob_files": 0,
            "remaining_dlq_files": 0,
            "marker_sha256": MARKER_SHA,
            "deletion_authorized": True,
        }
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote", return_value=("ssm-1", remote_receipt)
        ):
            root = pathlib.Path(temp)
            path = root / "plan.json"
            output = root / "receipt.json"
            path.write_text(json.dumps(plan()), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "failed validation"):
                control.apply_first(path, output, CONFIRM)
            self.assertFalse(output.exists())

    def test_apply_delivers_exact_bound_arguments_and_persists_receipt(self) -> None:
        receipt = {
            "mode": "prod_qa_age_retention_first_apply",
            "cutoff": CUTOFF,
            "applied_at": "2026-08-07T12:01:00.000000Z",
            "authorization_expires_at": "2026-08-07T12:11:00.000000Z",
            "planned_rows": 42,
            "deleted_rows_this_attempt": 42,
            "planned_blob_files": 3,
            "planned_dlq_files": 1,
            "remaining_rows": 0,
            "remaining_blob_files": 0,
            "remaining_dlq_files": 0,
            "marker_sha256": MARKER_SHA,
            "deletion_authorized": True,
        }
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote", return_value=("ssm-1", receipt)
        ) as remote:
            root = pathlib.Path(temp)
            path = root / "plan.json"
            output = root / "receipt.json"
            path.write_text(json.dumps(plan()), encoding="utf-8")
            result = control.apply_first(path, output, CONFIRM)
            persisted = json.loads(output.read_text(encoding="utf-8"))
        self.assertEqual(result, persisted)
        self.assertEqual(result["instance_id"], INSTANCE)
        args = remote.call_args.args
        self.assertEqual(args[0], INSTANCE)
        command = args[1]
        for expected in (CUTOFF, "--expected-rows 42", "--expected-blob-files 3", "--expected-dlq-files 1", "1.8.140", CONFIRM):
            self.assertIn(expected, command)

    def test_export_orphan_operator_requires_and_binds_exact_plan_hash(self) -> None:
        planned = plan()
        export = planned["qa"]["export_tmp"]
        receipt = {
            "mode": "prod_qa_export_orphan_apply",
            "cutoff": CUTOFF,
            "container_dir": export["container_dir"],
            "host_dir": export["host_dir"],
            "files": export["files"],
            "planned_count": export["count"],
            "planned_bytes": export["total_bytes"],
            "plan_hash": export["plan_hash"],
            "deleted_count": export["count"],
            "deleted_bytes": export["total_bytes"],
            "deletion_authorized": True,
            "activation_marker_sha256": MARKER_SHA,
        }
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote", return_value=("ssm-export", receipt)
        ) as remote:
            root = pathlib.Path(temp)
            source = root / "plan.json"
            output = root / "receipt.json"
            source.write_text(json.dumps(planned), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "confirmation"):
                control.apply_export_orphans(source, output, "wrong")
            result = control.apply_export_orphans(
                source, output, export["required_confirmation"]
            )
        self.assertEqual(result["command_id"], "ssm-export")
        command = remote.call_args.args[1]
        self.assertIn("--apply-export-orphans", command)
        self.assertIn(f"--expected-plan-hash {export['plan_hash']}", command)
        self.assertIn(export["required_confirmation"], command)

    def test_export_orphan_operator_rejects_tampered_plan_before_ssm(self) -> None:
        planned = plan()
        planned["qa"]["export_tmp"]["files"] = [
            {"basename": "tampered.zip", "size_bytes": 9, "mtime": 1}
        ]
        with tempfile.TemporaryDirectory() as temp, mock.patch.object(
            control, "_run_remote"
        ) as remote:
            root = pathlib.Path(temp)
            source = root / "plan.json"
            source.write_text(json.dumps(planned), encoding="utf-8")
            with self.assertRaisesRegex(control.StaleCleanupError, "failed validation|hash"):
                control.apply_export_orphans(
                    source,
                    root / "receipt.json",
                    planned["qa"]["export_tmp"]["required_confirmation"],
                )
        remote.assert_not_called()


if __name__ == "__main__":
    unittest.main(verbosity=2)
