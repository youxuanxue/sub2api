#!/usr/bin/env python3
"""Cadence, health evidence and failure behavior for physical backups."""

from __future__ import annotations

import hashlib
import io
import json
import tarfile
import tempfile
import unittest
from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import patch

import install_postgres_backup as install
import postgres_backup as backup
import prepare_physical_backup as bundle
import restore_postgres_backup as restore
import run_physical_restore as cloud
import retire_pgdump as retirement
import preserve_legacy_pgdump as retention


def entry(backups):
    return json.dumps([{"name": "tokenkey", "status": {"code": 0}, "backup": backups}])


class PhysicalBackupTests(unittest.TestCase):
    def test_retirement_requires_live_physical_health_and_both_restore_paths(self):
        now = 2000000
        accepted = {"status": "ok", "completed_at": now - 60,
                    "paths": ["latest-archived", "tk_point"]}
        health = {"status": "ok", "reasons": [], "latest_backup_at": now - 600,
                  "archive_check": {"status": "ok", "started_at": now - 10},
                  "restore_acceptance": accepted, "repository_verification": accepted}
        retirement.validate_health(health, now)
        for bad in (dict(health, latest_backup_at=now-9000),
                    dict(health, archive_check={}),
                    dict(health, restore_acceptance=dict(accepted, paths=["latest-archived"])),
                    dict(health, status="degraded", reasons=["WAL gap"])):
            with self.assertRaises(backup.BackupError):
                retirement.validate_health(bad, now)
        receipt = {"status": "ok", "s3_uri": "s3://bucket/prod/pgdump",
                   "expiration_disabled": True, "retained_objects": 5,
                   "observed_at": now-5, "retain_until": now+31*86400}
        retirement.validate_retention(receipt, now, receipt["s3_uri"])
        for bad in (dict(receipt, expiration_disabled=False),
                    dict(receipt, retain_until=now+7*86400),
                    dict(receipt, s3_uri="s3://other/prod/pgdump")):
            with self.assertRaises(backup.BackupError):
                retirement.validate_retention(bad, now, receipt["s3_uri"])

    def test_legacy_retention_removes_expiry_but_preserves_upload_cleanup(self):
        before = {"Rules": [{"ID": "expire-pgdump-copies", "Filter": {"Prefix": ""},
                             "Status": "Enabled", "Expiration": {"Days": 7}}]}
        after = retention.retained_lifecycle(before)
        self.assertNotIn("Expiration", after["Rules"][0])
        self.assertEqual(after["Rules"][0]["AbortIncompleteMultipartUpload"],
                         {"DaysAfterInitiation": 1})
        with self.assertRaises(backup.BackupError):
            retention.retained_lifecycle({"Rules": before["Rules"] * 2})

    def test_bundle_accepts_classic_and_oci_identity_and_rejects_wrong_image(self):
        config = b'{"architecture":"arm64"}'
        config_id = "sha256:" + hashlib.sha256(config).hexdigest()
        image = json.dumps({"config": {"digest": config_id}}).encode()
        image_id = "sha256:" + hashlib.sha256(image).hexdigest()
        index = json.dumps({"manifests": [{"digest": image_id}]}).encode()
        index_id = "sha256:" + hashlib.sha256(index).hexdigest()
        files = {
            "manifest.json": json.dumps([{"Config": "config.json"}]).encode(),
            "config.json": config,
            "index.json": json.dumps({"manifests": [{"digest": index_id}]}).encode(),
            "blobs/sha256/" + index_id[7:]: index,
            "blobs/sha256/" + image_id[7:]: image,
        }
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "image.tar"
            for corrupt in (False, True):
                if corrupt:
                    files["config.json"] = b'{"architecture":"amd64"}'
                with tarfile.open(path, "w") as archive:
                    for name, data in files.items():
                        member = tarfile.TarInfo(name)
                        member.size = len(data)
                        archive.addfile(member, io.BytesIO(data))
                with tarfile.open(path) as archive:
                    if corrupt:
                        with self.assertRaises(ValueError):
                            bundle.validate_image_export(archive, index_id)
                    else:
                        self.assertEqual(
                            bundle.validate_image_export(archive, config_id), config_id
                        )
                        self.assertEqual(
                            bundle.validate_image_export(archive, index_id), config_id
                        )
                        with self.assertRaises(ValueError):
                            bundle.validate_image_export(archive, "sha256:" + "0" * 64)

    def test_full_due_after_missed_week_and_incremental_between_fulls(self):
        now = datetime(2026, 10, 19, 2, 30, tzinfo=UTC).timestamp()
        self.assertEqual(backup.backup_kind([], now), "full")
        recent = [{"type": "full", "timestamp": {"stop": now - 3600}}]
        self.assertEqual(backup.backup_kind(recent, now), "incr")
        recent[0]["timestamp"]["stop"] = now - backup.FULL_INTERVAL_SECONDS
        self.assertEqual(backup.backup_kind(recent, now), "full")

    def test_failed_backup_preserves_last_success(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            previous = {"status": "ok", "label": "prior"}
            backup.atomic_json(state / "backup.json", previous)
            with (
                patch.object(
                    backup,
                    "backrest",
                    side_effect=[entry([]), backup.BackupError("S3 unavailable")],
                ),
                self.assertRaisesRegex(backup.BackupError, "S3 unavailable"),
            ):
                backup.take_backup(state, "auto")
            self.assertEqual(json.loads((state / "backup.json").read_text()), previous)

    def test_success_requires_new_completed_remote_backup(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            fresh = {
                "label": "20261018-120000F",
                "type": "full",
                "timestamp": {"stop": 10},
            }
            with patch.object(
                backup, "backrest", side_effect=[entry([]), "", entry([fresh])]
            ):
                result = backup.take_backup(state, "auto")
            self.assertEqual(result["label"], fresh["label"])
            self.assertEqual(json.loads((state / "backup.json").read_text()), result)
            with (
                patch.object(
                    backup, "backrest", side_effect=[entry([fresh]), "", entry([fresh])]
                ),
                self.assertRaisesRegex(backup.BackupError, "not found"),
            ):
                backup.take_backup(state, "full")
            self.assertEqual(json.loads((state / "backup.json").read_text()), result)

    def test_overlapping_backup_cannot_start(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            with backup.exclusive(state), patch.object(backup, "backrest") as command:
                with self.assertRaisesRegex(backup.BackupError, "another backup"):
                    backup.take_backup(state, "full")
                command.assert_not_called()

    def test_recent_backup_does_not_hide_missing_wal_or_restore_evidence(self):
        now = 2000000
        current = {"backup": [{"timestamp": {"stop": now - 30}}]}
        health = backup.status(current, now, None, None, None)
        self.assertEqual(health["status"], "degraded")
        self.assertEqual(
            health["reasons"],
            [
                "archive_round_trip_missing_or_stale",
                "repository_verification_missing_or_stale",
                "restore_acceptance_missing_or_stale",
            ],
        )
        success = {"status": "ok", "started_at": now - 10, "completed_at": now - 5}
        self.assertEqual(
            backup.status(current, now, success, success, success)["status"], "ok"
        )
        stale = dict(success, started_at=now - backup.RPO_SECONDS - 1)
        self.assertEqual(
            backup.status(current, now, stale, success, success)["reasons"],
            ["archive_round_trip_missing_or_stale"],
        )
        failed = dict(success, status="failed")
        self.assertEqual(
            backup.status(current, now, success, failed, success)["status"], "degraded"
        )

    def test_wal_gap_and_timeline_change_are_not_healthy(self):
        first, middle, last = [f"00000001000000000000000{x}" for x in (1, 2, 3)]
        objects = {
            wal + "-" + "a" * 40 + ".zst": {"type": "file"}
            for wal in (first, middle, last)
        }
        self.assertEqual(
            backup.wal_coverage(objects, first, last, 16 * 1024**2)["segments"], 3
        )
        del objects[middle + "-" + "a" * 40 + ".zst"]
        with self.assertRaisesRegex(backup.BackupError, "gap"):
            backup.wal_coverage(objects, first, last, 16 * 1024**2)
        with self.assertRaisesRegex(backup.BackupError, "timeline"):
            backup.wal_coverage(
                objects, first, "000000020000000000000003", 16 * 1024**2
            )

    def test_verify_rejects_empty_and_corrupt_reports(self):
        report = "stanza: tokenkey\nstatus: ok\n backup: abc, status: valid\n missing: 0, checksum invalid: 0"
        backup.validate_verification(report)
        for invalid in (
            "",
            report.replace("checksum invalid: 0", "checksum invalid: 1"),
            report.replace("status: ok", "status: error"),
        ):
            with self.assertRaises(backup.BackupError):
                backup.validate_verification(invalid)

    def test_future_or_malformed_health_stamp_is_rejected(self):
        for stamp in (101, "100", None):
            self.assertFalse(
                backup.fresh(
                    {"status": "ok", "started_at": stamp}, "started_at", 100, 900
                )
            )

    def test_failed_check_keeps_wal_growth_baseline_but_invalidates_health(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            previous = {"status": "ok", "started_at": 100, "wal_bytes": 1000}
            backup.atomic_json(state / "check.json", previous)
            with (
                patch.object(
                    backup.sys, "argv", ["backup", "--state", directory, "check"]
                ),
                patch.object(backup.sys, "stderr"),
                patch.object(
                    backup,
                    "archive_health",
                    side_effect=backup.BackupError("disk pressure"),
                ),
            ):
                self.assertEqual(backup.main(), 1)
            observed = backup.read_receipt(state / "check.json")
            self.assertEqual(observed["started_at"], 100)
            self.assertEqual(observed["wal_bytes"], 1000)
            self.assertFalse(backup.fresh(observed, "started_at", 200, 900))
            self.assertEqual(
                backup.read_receipt(state / "health.json")["reasons"], ["disk pressure"]
            )

    def test_installer_preserves_settings_and_keeps_two_hour_timer(self):
        env = "POSTGRES_PASSWORD='private'\nPOSTGRES_SHARED_BUFFERS=1GB\nPOSTGRES_ARCHIVE_MODE=off\n"
        result = install.patch_env(
            env, {"POSTGRES_IMAGE": "sha256:abc", "POSTGRES_ARCHIVE_MODE": "on"}
        )
        self.assertEqual(
            result.splitlines(),
            [
                "POSTGRES_PASSWORD='private'",
                "POSTGRES_SHARED_BUFFERS=1GB",
                'POSTGRES_IMAGE="sha256:abc"',
                'POSTGRES_ARCHIVE_MODE="on"',
            ],
        )
        timer = install.units(Path("/opt/postgres_backup.py"))[
            "tokenkey-postgres-backup.timer"
        ]
        import configparser

        parsed = configparser.ConfigParser()
        parsed.read_string(timer)
        self.assertEqual(parsed["Timer"]["OnCalendar"], backup.INCREMENTAL_CALENDAR)
        self.assertTrue(parsed["Timer"].getboolean("Persistent"))

    def test_restore_requires_prior_incremental_and_never_uses_live_volume(self):
        full = {"label": "20261018-000000F", "type": "full", "timestamp": {"stop": 100}}
        incr = {
            "label": full["label"] + "_20261018-020000I",
            "type": "incr",
            "timestamp": {"stop": 200},
        }
        data = {"backup": [full, incr]}
        self.assertEqual(
            restore.select_backups(data, 210), [full["label"], incr["label"]]
        )
        self.assertEqual(restore.select_backups(data, None), [incr["label"]])
        with self.assertRaises(backup.BackupError):
            restore.select_backups(data, 150)
        with (
            patch.object(restore.Path, "exists", return_value=True),
            patch.object(restore, "run") as command,
        ):
            with self.assertRaisesRegex(backup.BackupError, "production"):
                restore.require_disposable_host()
            command.assert_not_called()
        with self.assertRaises(backup.BackupError):
            restore.restore_arguments(incr["label"], "tk_x; rm -rf /")
        self.assertEqual(
            restore.restore_arguments(incr["label"], "tk_point")[-3:],
            ["--type=name", "--target=tk_point", "--target-action=promote"],
        )

    def test_restore_cleanup_failure_cannot_become_success(self):
        task = restore.Restore("sha256:test", Path("/tmp/config"))
        task.containers = ["owned-container"]
        task.volumes = ["owned-volume"]
        with (
            patch.object(
                restore, "run", side_effect=[backup.BackupError("busy"), ""]
            ) as command,
            self.assertRaisesRegex(backup.BackupError, "cleanup failed"),
        ):
            task.cleanup()
        self.assertEqual(
            command.call_args_list[1].args[0],
            ["docker", "volume", "rm", "owned-volume"],
        )

    def test_recovery_startup_probe_is_not_a_terminal_failure(self):
        startup = 'FATAL:  the database system is starting up'
        self.assertFalse(restore.recovery_log_is_terminal(startup))
        self.assertTrue(
            restore.recovery_log_is_terminal(
                'FATAL: recovery aborted because of insufficient parameter settings'
            )
        )

    def test_recovery_waits_through_probe_failure_until_ready(self):
        task = restore.Restore("sha256:test", Path("/tmp/config"))
        with (
            patch.object(restore, "run") as command,
            patch.object(task, "sql", side_effect=[backup.BackupError("starting"), "f", "t"]),
            patch.object(task, "recovery_diagnostics", return_value="FATAL: the database system is starting up"),
            patch.object(restore.time, "sleep"),
        ):
            task.start("isolated")
        options = command.call_args.args[0]
        self.assertIn("-c max_connections=200", options[options.index("-o") + 1])

    def test_terminal_recovery_failure_preserves_reason(self):
        task = restore.Restore("sha256:test", Path("/tmp/config"))
        reason = "FATAL: recovery aborted because of insufficient parameter settings"
        with (
            patch.object(restore, "run"),
            patch.object(task, "sql", side_effect=backup.BackupError("starting")),
            patch.object(task, "recovery_diagnostics", return_value=reason),
            self.assertRaisesRegex(backup.BackupError, "insufficient parameter settings"),
        ):
            task.start("isolated")

    def test_remote_worker_rejects_mutable_or_injected_artifact(self):
        with self.assertRaises(backup.BackupError):
            cloud.worker_command(
                "bucket", "latest.tar.gz", "a" * 64, "sha256:" + "b" * 64, None, None
            )
        with self.assertRaises(backup.BackupError):
            cloud.worker_command(
                "bucket;evil",
                "artifacts/" + "a" * 64 + ".tar.gz",
                "a" * 64,
                "sha256:" + "b" * 64,
                None,
                None,
            )

    def test_cloud_cleanup_recovers_ambiguous_launch_and_checks_disk_deletion(self):
        calls = []

        def aws(region, *args, **kwargs):
            calls.append(args)
            if args[:2] == ("ec2", "describe-instances"):
                return {
                    "Reservations": [
                        {
                            "Instances": [
                                {
                                    "InstanceId": "i-owned",
                                    "BlockDeviceMappings": [
                                        {"Ebs": {"VolumeId": "vol-owned"}}
                                    ],
                                }
                            ]
                        }
                    ]
                }
            if args[:2] == ("ec2", "describe-security-groups"):
                return {"SecurityGroups": [{"GroupId": "sg-owned"}]}
            return {}

        with patch.object(cloud, "aws", side_effect=aws):
            cloud.cleanup_cloud("region", "owned-token", None, None)
        self.assertEqual(
            calls[1:4],
            [
                ("ec2", "terminate-instances", "--instance-ids", "i-owned"),
                ("ec2", "wait", "instance-terminated", "--instance-ids", "i-owned"),
                ("ec2", "wait", "volume-deleted", "--volume-ids", "vol-owned"),
            ],
        )
        self.assertEqual(
            calls[-1], ("ec2", "delete-security-group", "--group-id", "sg-owned")
        )

        def disk_still_present(region, *args, **kwargs):
            if args[:3] == ("ec2", "wait", "volume-deleted"):
                raise backup.BackupError("disk still present")
            return aws(region, *args, **kwargs)

        calls.clear()
        with (
            patch.object(cloud, "aws", side_effect=disk_still_present),
            self.assertRaisesRegex(backup.BackupError, "disk still present"),
        ):
            cloud.cleanup_cloud("region", "owned-token", "i-owned", "sg-owned")
        self.assertEqual(
            calls[-1], ("ec2", "delete-security-group", "--group-id", "sg-owned")
        )

    def test_rollback_handles_partial_install_and_preserves_retry_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root / "state"
            baseline = state / "rollback"
            baseline.mkdir(parents=True)
            (baseline / ".env").write_text(
                "POSTGRES_SHARED_BUFFERS=1GB\nPOSTGRES_PASSWORD=secret\n"
            )
            (baseline / "docker-compose.prod-pg.yml").write_text(
                "services:\n  postgres:\n    command: postgres\n"
            )
            backup.atomic_json(baseline / "identity.json", {"image": "sha256:prior"})
            backup.atomic_json(state / "enabled.json", {"status": "activating"})
            with (
                patch.object(install, "ROOT", root),
                patch.object(install, "STATE", state),
                patch.object(install, "UNITS", root / "unwritten-units"),
                patch.object(install.os, "geteuid", return_value=0),
                patch.object(install, "run") as command,
                patch.object(install, "compose"),
                patch.object(
                    install,
                    "wait_database",
                    side_effect=backup.BackupError("not ready"),
                ),
            ):
                with self.assertRaisesRegex(backup.BackupError, "not ready"):
                    install.rollback()
                self.assertEqual(
                    backup.read_receipt(state / "enabled.json"),
                    {"status": "activating"},
                )
                with patch.object(install, "wait_database"):
                    self.assertEqual(install.rollback()["status"], "rolled_back")
                command.assert_not_called()
            self.assertEqual(
                (root / ".env").read_text(),
                'POSTGRES_SHARED_BUFFERS=1GB\nPOSTGRES_PASSWORD=secret\nPOSTGRES_IMAGE="sha256:prior"\n',
            )
            self.assertEqual(
                (root / "docker-compose.prod-pg.yml").read_text(),
                "services:\n  postgres:\n    pull_policy: never\n    image: sha256:prior\n    command: postgres\n",
            )
            self.assertIsNone(backup.read_receipt(state / "enabled.json"))
            retained = next(state.glob("rollback-completed-*/identity.json"))
            self.assertEqual(
                json.loads(retained.read_text()), {"image": "sha256:prior"}
            )

    def test_acceptance_requires_both_paths_and_rejects_wrong_database_or_slow_restore(
        self,
    ):
        with (
            tempfile.TemporaryDirectory() as root,
            patch.object(backup.time, "time", return_value=1000),
        ):
            state = Path(root)
            backup.atomic_json(
                state / "enabled.json", {"after_image": "image1", "started_at": 900}
            )
            db = {"db": [{"system-id": 42}]}
            result = {
                "billing": {
                    "status": "ok",
                    "charge_retry": True,
                    "archive_retry": True,
                    "refund_retry": True,
                }
            }
            receipt = {
                "status": "ok",
                "completed_at": 990,
                "cleanup_verified": True,
                "instance_terminated": True,
                "volumes_deleted": True,
                "repository_verified": True,
                "within_30_minutes": True,
                "business_acceptance": "passed",
                "system_id": 42,
                "image": "image1",
                "target": "latest-archived",
                "results": [result],
            }
            self.assertEqual(
                backup.accept_restore(state, receipt, db)["status"], "pending"
            )
            self.assertIsNone(backup.read_receipt(state / "restore.json"))
            for bad in (
                dict(receipt, system_id=43),
                dict(receipt, within_30_minutes=False),
                dict(receipt, instance_terminated=False),
                dict(receipt, volumes_deleted=False),
                dict(receipt, completed_at=1001),
            ):
                with self.assertRaises(backup.BackupError):
                    backup.accept_restore(state, bad, db)
            historical = dict(receipt, target="tk_point", results=[result, result])
            self.assertEqual(
                backup.accept_restore(state, historical, db)["status"], "ok"
            )
            weekly = dict(receipt, completed_at=999)
            self.assertEqual(
                backup.accept_restore(state, weekly, db)["completed_at"], 999
            )
            backup.atomic_json(
                state / "enabled.json", {"after_image": "image1", "started_at": 999.5}
            )
            self.assertEqual(
                backup.accept_restore(state, dict(weekly, completed_at=1000), db)[
                    "status"
                ],
                "pending",
            )

    def test_config_rejects_injection_and_uses_chain_retention(self):
        with self.assertRaises(backup.BackupError):
            backup.configuration("bucket\nrepo1-retention-full=1", "us-east-1")
        with self.assertRaises(backup.BackupError):
            backup.configuration("tokenkey-backups", "us-east-1\nrepo1-type=posix")
        # Parsing tests the effective values, not whether a source string exists.
        import configparser

        config = configparser.ConfigParser()
        config.BOOLEAN_STATES = dict(config.BOOLEAN_STATES, y=True, n=False)
        config.read_string(backup.configuration("tokenkey-backups", "us-east-1"))
        self.assertEqual(config["global"]["repo1-retention-full-type"], "time")
        self.assertEqual(config["global"].getint("repo1-retention-full"), 7)
        self.assertFalse(config["global"].getboolean("archive-async"))
        self.assertTrue(config["global"].getboolean("repo1-block"))
        self.assertEqual(config["global"]["repo1-s3-key-type"], "auto")
        self.assertEqual(config["global"]["repo1-path"], "/prod/postgres")
        live = configparser.ConfigParser()
        live.read_string(
            backup.configuration(
                "tokenkey-backups",
                "us-east-1",
                "/candidates/tencent-usw-20261022/postgres",
            )
        )
        self.assertEqual(
            live["global"]["repo1-path"], "/candidates/tencent-usw-20261022/postgres"
        )
        with self.assertRaises(backup.BackupError):
            backup.configuration("tokenkey-backups", "us-east-1", "/x\nrepo1-type=posix")

    def test_config_can_render_cos_repo2_without_changing_aws_repo1(self):
        rendered = backup.configuration(
            "tokenkey-backups",
            "us-east-1",
            secondary={
                "bucket": "tokenkey-prod-cos-123456789012",
                "region": "ap-beijing",
                "endpoint": "cos.ap-beijing.myqcloud.com",
                "path": "/prod/postgres",
            },
        )
        import configparser

        config = configparser.ConfigParser()
        config.read_string(rendered)
        self.assertEqual(config["global"]["repo1-s3-endpoint"], "s3.us-east-1.amazonaws.com")
        self.assertEqual(config["global"]["repo1-path"], "/prod/postgres")
        self.assertEqual(config["global"]["repo2-type"], "s3")
        self.assertEqual(config["global"]["repo2-s3-endpoint"], "cos.ap-beijing.myqcloud.com")
        self.assertEqual(config["global"]["repo2-s3-uri-style"], "host")
        self.assertEqual(config["global"].getint("repo2-retention-full"), 7)
        self.assertNotIn("repo2-s3-key=", rendered)

    def test_config_rejects_unsafe_or_partial_cos_repo2(self):
        valid = {
            "bucket": "tokenkey-prod-cos-123456789012",
            "region": "ap-beijing",
            "endpoint": "cos.ap-beijing.myqcloud.com",
            "path": "/prod/postgres",
        }
        for key, value in (
            ("endpoint", "https://cos.ap-beijing.myqcloud.com"),
            ("endpoint", "cos.ap-beijing.myqcloud.com/evil"),
            ("path", "/prod/postgres\nrepo1-type=posix"),
            ("region", "us-east-1"),
        ):
            candidate = dict(valid, **{key: value})
            with self.assertRaises(backup.BackupError):
                backup.configuration("tokenkey-backups", "us-east-1", secondary=candidate)

    def test_repo2_config_is_dry_run_only_and_backup_stays_repo1_by_default(self):
        """Adding repo2 to rendered config must not silently change live calls."""
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            fresh = {
                "label": "20261018-120000F",
                "type": "full",
                "timestamp": {"stop": 10},
            }
            with patch.object(
                backup,
                "backrest",
                side_effect=[entry([]), "", entry([fresh])],
            ) as command:
                backup.take_backup(state, "full")
            for call in command.call_args_list:
                self.assertNotIn("--repo=all", call.args)
                self.assertNotIn("--repo=2", call.args)

    def test_bundle_architecture_comes_from_target_not_constant(self):
        root = Path(__file__).resolve().parents[2]
        live = json.loads(
            (root / "deploy/tencent/candidate-usw-20261022.json").read_text()
        )
        self.assertEqual(
            bundle.target_architecture(None, bundle.DEFAULT_MATRIX),
            live["target"]["architecture"],
        )
        self.assertEqual(bundle.target_architecture("arm64", bundle.DEFAULT_MATRIX), "arm64")
        with tempfile.TemporaryDirectory() as directory:
            matrix = Path(directory) / "matrix.json"
            matrix.write_text(json.dumps({"target": {}}))
            with self.assertRaisesRegex(ValueError, "target.architecture"):
                bundle.target_architecture(None, matrix)

    def _installer_docker(self, container, data_source, current_arch, candidate_arch):
        image = "sha256:" + "c" * 64

        def inspect(cmd, **kwargs):
            name = cmd[-1]
            if name in install.LAYOUTS and name != container:
                return type("R", (), {"returncode": 1, "stdout": ""})()
            item = {
                "State": {"Running": True},
                "Image": "sha256:" + "d" * 64,
                "Mounts": [
                    {"Destination": "/var/lib/postgresql/data", "Source": data_source}
                ],
            }
            return type("R", (), {"returncode": 0, "stdout": json.dumps([item])})()

        def run(cmd, **kwargs):
            if cmd[:3] == ["docker", "image", "inspect"]:
                arch = candidate_arch if cmd[3] == image else current_arch
                return json.dumps([{"Architecture": arch}])
            if "--version" in cmd:
                return "postgres (PostgreSQL) 18.6"
            raise AssertionError(cmd)

        return image, inspect, run

    def test_installer_matches_running_host_architecture_and_tencent_layout(self):
        cases = (
            ("tokenkey-postgres", "/var/lib/tokenkey/postgres", "x86_64", "amd64", "amd64", None),
            ("tokenkey-postgres", "/var/lib/tokenkey/postgres", "aarch64", "arm64", "arm64", None),
            ("tk-archive-standby", "/srv/tokenkey-rehearsal/standby-pg", "x86_64", "amd64", "amd64", None),
            # Tokyo-era arm64 bundle on the amd64 Tencent host must fail.
            ("tk-archive-standby", "/srv/tokenkey-rehearsal/standby-pg", "x86_64", "amd64", "arm64", "architecture"),
            ("tokenkey-postgres", "/var/lib/tokenkey/postgres", "x86_64", "arm64", "arm64", "architecture"),
            ("tk-archive-standby", "/var/lib/tokenkey/postgres", "x86_64", "amd64", "amd64", "data mount"),
        )
        for container, source, machine, current, candidate, error in cases:
            with self.subTest(container=container, machine=machine, candidate=candidate):
                image, inspect, run = self._installer_docker(
                    container, source, current, candidate
                )
                with (
                    patch.object(install.socket, "gethostname", return_value="host"),
                    patch.object(install.platform, "machine", return_value=machine),
                    patch.object(install.subprocess, "run", side_effect=inspect),
                    patch.object(install, "run", side_effect=run),
                ):
                    if error:
                        with self.assertRaisesRegex(backup.BackupError, error):
                            install.install("tokenkey-backups", "us-east-1", image, "host", False)
                        continue
                    plan = install.install("tokenkey-backups", "us-east-1", image, "host", False)
                    self.assertEqual(plan["container"], container)
                    self.assertEqual(plan["architecture"], candidate)
                    self.assertEqual(plan["status"], "planned")

    def test_installer_refuses_apply_on_non_compose_tencent_layout(self):
        image, inspect, run = self._installer_docker(
            "tk-archive-standby", "/srv/tokenkey-rehearsal/standby-pg", "amd64", "amd64"
        )
        with (
            patch.object(install.socket, "gethostname", return_value="host"),
            patch.object(install.platform, "machine", return_value="x86_64"),
            patch.object(install.subprocess, "run", side_effect=inspect),
            patch.object(install, "run", side_effect=run),
            self.assertRaisesRegex(backup.BackupError, "not compose-managed"),
        ):
            install.install("tokenkey-backups", "us-east-1", image, "host", True)

    def test_installer_requires_exactly_one_running_postgres(self):
        def both(cmd, **kwargs):
            item = {"State": {"Running": True}}
            return type("R", (), {"returncode": 0, "stdout": json.dumps([item])})()

        def none(cmd, **kwargs):
            return type("R", (), {"returncode": 1, "stdout": ""})()

        for side_effect in (both, none):
            with (
                patch.object(install.subprocess, "run", side_effect=side_effect),
                self.assertRaisesRegex(backup.BackupError, "exactly one running"),
            ):
                install.resolve_postgres()

    def test_restore_runner_requires_explicit_subnet_and_matching_shape(self):
        self.assertEqual(cloud.SHAPES["amd64"]["ami_arch"], "x86_64")
        self.assertEqual(cloud.SHAPES["arm64"]["instance_type"], "c7g.xlarge")
        cloud.validate_network("subnet-0123abcd")
        for bad in ("mi-080ed76dc6d97d8b5", "", "subnet-x; rm", "None"):
            with self.subTest(subnet=bad), self.assertRaises(backup.BackupError):
                cloud.validate_network(bad)

    def test_restore_workflow_never_derives_subnet_from_prod_instance(self):
        root = Path(__file__).resolve().parents[2]
        text = (root / ".github/workflows/ops-physical-restore.yml").read_text()
        self.assertNotIn("describe-instances", text)
        self.assertNotIn("ap-northeast-1", text)
        for name in ("PHYSICAL_RESTORE_REGION", "PHYSICAL_RESTORE_SUBNET", "PHYSICAL_RESTORE_ARCH"):
            self.assertIn("vars." + name, text)
        self.assertIn("--architecture", text)
        fence = text.index("Require an approved restore network")
        self.assertLess(fence, text.index("configure-aws-credentials"))


if __name__ == "__main__":
    unittest.main()
