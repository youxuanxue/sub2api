#!/usr/bin/env python3
"""Behavior tests for the local/non-production archive rehearsal."""

from __future__ import annotations

import hashlib
import importlib.util
import json
import os
import pathlib
import shutil
import sqlite3
import subprocess
import tempfile
import time
import unittest
from unittest import mock

_DIR = pathlib.Path(__file__).resolve().parent
_TOOL = _DIR / "data_layer_archive_rehearsal.py"
_REPO = _DIR.parents[1]


def _load_module():
    spec = importlib.util.spec_from_file_location("data_layer_archive_rehearsal", _TOOL)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {_TOOL}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


rehearsal = _load_module()


def _file_sha(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _create_source(path: pathlib.Path, rows: list[tuple[str, str, str, object]]) -> None:
    connection = sqlite3.connect(path)
    try:
        connection.execute(
            "CREATE TABLE archive_rehearsal_records ("
            "dataset TEXT NOT NULL, record_id TEXT NOT NULL, created_at TEXT NOT NULL, "
            "payload_json TEXT NOT NULL, PRIMARY KEY (dataset, record_id))"
        )
        connection.executemany(
            "INSERT INTO archive_rehearsal_records "
            "(dataset, record_id, created_at, payload_json) VALUES (?, ?, ?, ?)",
            [
                (dataset, record_id, created_at, json.dumps(payload, sort_keys=True))
                for dataset, record_id, created_at, payload in rows
            ],
        )
        connection.commit()
    finally:
        connection.close()


def _fixture_rows() -> list[tuple[str, str, str, object]]:
    return [
        ("usage", "usage-cold-1", "2026-04-01T00:00:00Z", {"tokens": 100}),
        ("usage", "usage-hot-1", "2026-07-01T00:00:00Z", {"tokens": 200}),
        ("ops", "ops-cold-1", "2026-06-01T00:00:00Z", {"kind": "error"}),
        ("ops", "ops-hot-1", "2026-07-01T00:00:00Z", {"kind": "system"}),
        ("qa", "qa-cold-1", "2026-07-16T00:00:00Z", {"blob": "a"}),
        ("qa", "qa-hot-1", "2026-07-19T12:00:00Z", {"blob": "b"}),
    ]


class DataLayerArchiveRehearsalTest(unittest.TestCase):
    maxDiff = None

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temporary.name)
        self.source = self.root / "source.sqlite"
        self.archive_root = self.root / "archives"
        self.target = self.root / "restore.sqlite"
        _create_source(self.source, _fixture_rows())
        self.policy = dict(rehearsal.DEFAULT_RETENTION_DAYS)
        self.as_of = "2026-07-20T00:00:00Z"

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def _seal(self) -> dict:
        return rehearsal.seal_batch(
            self.source,
            self.archive_root,
            environment="local",
            as_of=self.as_of,
            retention_days=self.policy,
        )

    def test_us037_dry_run_uses_retention_without_mutating_source(self) -> None:
        before = _file_sha(self.source)
        report = rehearsal.dry_run(
            self.source,
            environment="local",
            as_of=self.as_of,
            retention_days=self.policy,
        )
        after = _file_sha(self.source)

        self.assertEqual(before, after)
        self.assertEqual(report["source_rows"], 6)
        self.assertEqual(report["candidate_rows"], 3)
        self.assertEqual(
            {
                item["dataset"]: (item["retention_days"], item["candidate_rows"])
                for item in report["datasets"]
            },
            {"usage": (90, 1), "ops": (30, 1), "qa": (2, 1)},
        )
        self.assertFalse(report["source_mutated"])
        self.assertFalse(report["deletion_authorized"])

    def test_us037_cutoff_is_strict_and_empty_batches_are_refused(self) -> None:
        boundary_source = self.root / "boundary.sqlite"
        _create_source(
            boundary_source,
            [("usage", "at-cutoff", "2026-04-21T00:00:00Z", {"hot": True})],
        )
        report = rehearsal.dry_run(
            boundary_source,
            environment="local",
            as_of=self.as_of,
            retention_days=self.policy,
        )
        self.assertEqual(report["candidate_rows"], 0)
        with self.assertRaisesRegex(rehearsal.RehearsalError, "empty rehearsal batch"):
            rehearsal.seal_batch(
                boundary_source,
                self.root / "boundary-archives",
                environment="local",
                as_of=self.as_of,
                retention_days=self.policy,
            )

    def test_us037_seal_verify_and_reseal_are_deterministic(self) -> None:
        first = self._seal()
        batch = pathlib.Path(first["batch_dir"])
        verified = rehearsal.verify_batch(batch)
        second = self._seal()

        self.assertFalse(first["idempotent_reuse"])
        self.assertTrue(second["idempotent_reuse"])
        self.assertEqual(first["batch_id"], second["batch_id"])
        self.assertEqual(first["total_rows"], 3)
        self.assertEqual([item["dataset"] for item in first["artifacts"]], ["usage", "ops", "qa"])
        self.assertTrue(verified["verified"])
        self.assertEqual(verified["row_count"], 3)
        self.assertEqual(
            _file_sha(batch / "manifest.json"), verified["manifest_sha256"]
        )

    def test_us037_corrupt_artifact_fails_closed_before_restore(self) -> None:
        sealed = self._seal()
        batch = pathlib.Path(sealed["batch_dir"])
        artifact = batch / sealed["artifacts"][0]["path"]
        artifact.write_bytes(artifact.read_bytes() + b"corrupt")

        with self.assertRaisesRegex(rehearsal.RehearsalError, "checksum mismatch"):
            rehearsal.verify_batch(batch)
        with self.assertRaisesRegex(rehearsal.RehearsalError, "checksum mismatch"):
            rehearsal.restore_random(batch, self.target, seed=7)
        self.assertFalse(self.target.exists())

    def test_us037_random_restore_is_verified_and_idempotent(self) -> None:
        sealed = self._seal()
        source_before = _file_sha(self.source)
        with self.assertRaisesRegex(
            rehearsal.RehearsalError, "must not be the sealed batch source"
        ):
            rehearsal.restore_random(sealed["batch_dir"], self.source, seed=11)
        self.assertEqual(_file_sha(self.source), source_before)

        source_hardlink = self.root / "source-hardlink.sqlite"
        os.link(self.source, source_hardlink)
        with self.assertRaisesRegex(
            rehearsal.RehearsalError, "must not reference the sealed source file"
        ):
            rehearsal.restore_random(sealed["batch_dir"], source_hardlink, seed=11)
        self.assertEqual(_file_sha(self.source), source_before)

        first = rehearsal.restore_random(sealed["batch_dir"], self.target, seed=11)
        second = rehearsal.restore_random(sealed["batch_dir"], self.target, seed=11)

        self.assertTrue(first["verified"])
        self.assertEqual(first["expected_rows"], 1)
        self.assertEqual(first["inserted_rows"], 1)
        self.assertFalse(first["idempotent_reuse"])
        self.assertEqual(second["inserted_rows"], 0)
        self.assertTrue(second["idempotent_reuse"])
        self.assertEqual(first["logical_sha256"], second["logical_sha256"])

        connection = sqlite3.connect(self.target)
        try:
            restored = connection.execute(
                "SELECT COUNT(*) FROM archive_rehearsal_restored"
            ).fetchone()[0]
        finally:
            connection.close()
        self.assertEqual(restored, 1)

    def test_us037_conflicting_restore_target_rolls_back(self) -> None:
        single_source = self.root / "single.sqlite"
        single_archive = self.root / "single-archives"
        single_target = self.root / "single-restore.sqlite"
        _create_source(
            single_source,
            [("usage", "same-id", "2026-01-01T00:00:00Z", {"value": "sealed"})],
        )
        sealed = rehearsal.seal_batch(
            single_source,
            single_archive,
            environment="nonprod",
            as_of=self.as_of,
            retention_days=self.policy,
        )
        connection = sqlite3.connect(single_target)
        try:
            connection.execute(
                "CREATE TABLE archive_rehearsal_restored ("
                "batch_id TEXT NOT NULL, dataset TEXT NOT NULL, record_id TEXT NOT NULL, "
                "created_at TEXT NOT NULL, payload_json TEXT NOT NULL, "
                "PRIMARY KEY (batch_id, dataset, record_id))"
            )
            connection.execute(
                "INSERT INTO archive_rehearsal_restored VALUES (?, ?, ?, ?, ?)",
                (
                    sealed["batch_id"],
                    "usage",
                    "same-id",
                    "2026-01-01T00:00:00Z",
                    '{"value":"conflict"}',
                ),
            )
            connection.commit()
        finally:
            connection.close()

        with self.assertRaisesRegex(
            rehearsal.RehearsalError, "restored rows do not match"
        ):
            rehearsal.restore_random(sealed["batch_dir"], single_target, seed=1)

        connection = sqlite3.connect(single_target)
        try:
            payload = connection.execute(
                "SELECT payload_json FROM archive_rehearsal_restored"
            ).fetchone()[0]
        finally:
            connection.close()
        self.assertEqual(payload, '{"value":"conflict"}')

    def test_us037_cli_rejects_prod_and_network_inputs(self) -> None:
        prod = subprocess.run(
            [
                "python3",
                str(_TOOL),
                "dry-run",
                "--source",
                str(self.source),
                "--environment",
                "prod",
                "--as-of",
                self.as_of,
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        network = subprocess.run(
            [
                "python3",
                str(_TOOL),
                "dry-run",
                "--source",
                "postgresql://prod.example/tokenkey",
                "--environment",
                "nonprod",
                "--as-of",
                self.as_of,
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        removed_command = subprocess.run(
            ["python3", str(_TOOL), "delete", "--source", str(self.source)],
            capture_output=True,
            text=True,
            check=False,
        )

        self.assertEqual(prod.returncode, 2)
        self.assertIn("invalid choice", prod.stderr)
        self.assertEqual(network.returncode, 2)
        self.assertIn("only local filesystem paths", network.stderr)
        self.assertEqual(removed_command.returncode, 2)
        self.assertIn("invalid choice", removed_command.stderr)

    def test_us037_output_paths_cannot_overwrite_source_restore_or_batch(self) -> None:
        source_before = _file_sha(self.source)
        source_collision = subprocess.run(
            [
                "python3",
                str(_TOOL),
                "dry-run",
                "--source",
                str(self.source),
                "--environment",
                "local",
                "--as-of",
                self.as_of,
                "--output",
                str(self.source),
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(source_collision.returncode, 2)
        self.assertIn("overlaps protected rehearsal input", source_collision.stderr)
        self.assertEqual(_file_sha(self.source), source_before)

        sealed = self._seal()
        batch = pathlib.Path(sealed["batch_dir"])
        manifest = batch / "manifest.json"
        manifest_before = _file_sha(manifest)
        manifest_collision = subprocess.run(
            [
                "python3",
                str(_TOOL),
                "verify",
                "--batch",
                str(batch),
                "--output",
                str(manifest),
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(manifest_collision.returncode, 2)
        self.assertIn("overlaps protected rehearsal input", manifest_collision.stderr)
        self.assertEqual(_file_sha(manifest), manifest_before)

        receipt_collision = subprocess.run(
            [
                "python3",
                str(_TOOL),
                "restore-random",
                "--batch",
                str(batch),
                "--target",
                str(self.target),
                "--seed",
                "5",
                "--receipt",
                str(self.target),
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(receipt_collision.returncode, 2)
        self.assertIn("overlaps protected rehearsal input", receipt_collision.stderr)
        self.assertFalse(self.target.exists())

    def test_us037_manifest_identity_tampering_is_rejected(self) -> None:
        sealed = self._seal()
        batch = pathlib.Path(sealed["batch_dir"])
        manifest_path = batch / "manifest.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        original = json.loads(json.dumps(manifest))
        manifest["retention_days"]["usage"] = 91
        manifest_path.write_text(
            json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n",
            encoding="utf-8",
        )

        with self.assertRaisesRegex(rehearsal.RehearsalError, "batch identity"):
            rehearsal.verify_batch(batch)

        original["source_path_sha256"] = "0" * 64
        manifest_path.write_text(
            json.dumps(original, sort_keys=True, separators=(",", ":")) + "\n",
            encoding="utf-8",
        )
        with self.assertRaisesRegex(rehearsal.RehearsalError, "batch identity"):
            rehearsal.verify_batch(batch)

        manifest_path.write_text("[]\n", encoding="utf-8")
        with self.assertRaisesRegex(rehearsal.RehearsalError, "JSON object"):
            rehearsal.verify_batch(batch)

    def test_us037_restore_rejects_manifest_changed_after_verify(self) -> None:
        sealed = self._seal()
        batch = pathlib.Path(sealed["batch_dir"])
        manifest_path = batch / "manifest.json"
        real_verify = rehearsal.verify_batch

        def verify_then_replace(value: pathlib.Path) -> dict:
            receipt = real_verify(value)
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            manifest["concurrent_change"] = True
            manifest_path.write_text(
                json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n",
                encoding="utf-8",
            )
            return receipt

        with mock.patch.object(rehearsal, "verify_batch", side_effect=verify_then_replace):
            with self.assertRaisesRegex(rehearsal.RehearsalError, "changed after"):
                rehearsal.restore_random(batch, self.target, seed=3)
        self.assertFalse(self.target.exists())

    def test_us037_symlinks_are_rejected_and_timezone_order_is_canonical(self) -> None:
        source_link = self.root / "source-link.sqlite"
        source_link.symlink_to(self.source)
        with self.assertRaisesRegex(rehearsal.RehearsalError, "symlink paths"):
            rehearsal.dry_run(
                source_link,
                environment="local",
                as_of=self.as_of,
                retention_days=self.policy,
            )

        offset_source = self.root / "offset.sqlite"
        offset_archive = self.root / "offset-archives"
        _create_source(
            offset_source,
            [
                ("usage", "exact", "2025-12-31T22:30:00Z", {"order": 0}),
                ("usage", "later", "2025-12-31T23:00:00Z", {"order": 2}),
                (
                    "usage",
                    "earlier",
                    "2026-01-01T00:30:00.123456+02:00",
                    {"order": 1},
                ),
            ],
        )
        sealed = rehearsal.seal_batch(
            offset_source,
            offset_archive,
            environment="local",
            as_of=self.as_of,
            retention_days=self.policy,
        )
        batch = pathlib.Path(sealed["batch_dir"])
        records, _ = rehearsal._parse_artifact(batch, sealed["artifacts"][0])
        self.assertEqual(
            [record["record_id"] for record in records], ["exact", "earlier", "later"]
        )
        self.assertEqual(records[1]["created_at"], "2025-12-31T22:30:00.123456Z")

        offset_target = self.root / "offset-restore.sqlite"
        rehearsal.restore_random(batch, offset_target, seed=1)
        connection = sqlite3.connect(offset_target)
        try:
            restored_timestamps = [
                row[0]
                for row in connection.execute(
                    "SELECT created_at FROM archive_rehearsal_restored "
                    "ORDER BY created_at, record_id"
                ).fetchall()
            ]
        finally:
            connection.close()
        self.assertEqual(
            restored_timestamps,
            [
                "2025-12-31T22:30:00.000000Z",
                "2025-12-31T22:30:00.123456Z",
                "2025-12-31T23:00:00.000000Z",
            ],
        )

    def test_us037_cli_runs_full_local_rehearsal(self) -> None:
        commands = [
            [
                "dry-run",
                "--source",
                str(self.source),
                "--environment",
                "local",
                "--as-of",
                self.as_of,
            ],
            [
                "seal",
                "--source",
                str(self.source),
                "--environment",
                "local",
                "--as-of",
                self.as_of,
                "--archive-root",
                str(self.archive_root),
            ],
        ]
        outputs = []
        for args in commands:
            result = subprocess.run(
                ["python3", str(_TOOL), *args],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, msg=result.stderr)
            outputs.append(json.loads(result.stdout))
        batch = outputs[1]["batch_dir"]
        for args in (
            ["verify", "--batch", batch],
            [
                "restore-random",
                "--batch",
                batch,
                "--target",
                str(self.target),
                "--seed",
                "19",
            ],
        ):
            result = subprocess.run(
                ["python3", str(_TOOL), *args],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, msg=result.stderr)
            self.assertTrue(json.loads(result.stdout)["verified"])

    def test_us037_tool_has_no_runtime_or_prod_consumer(self) -> None:
        references = []
        for root in (_REPO / ".github", _REPO / "ops" / "prod", _REPO / "deploy"):
            for path in root.rglob("*"):
                if not path.is_file():
                    continue
                try:
                    body = path.read_text(encoding="utf-8")
                except UnicodeDecodeError:
                    continue
                if _TOOL.name in body:
                    references.append(str(path.relative_to(_REPO)))
        self.assertEqual(references, [], msg=f"rehearsal unexpectedly activated by {references}")

    def test_us037_postgres_dsn_and_table_guards_are_nonprod_only(self) -> None:
        with self.assertRaisesRegex(rehearsal.RehearsalError, "localhost Docker"):
            rehearsal._postgres_dsn_info(
                "postgresql://postgres:secret@prod.example/tokenkey_archive_rehearsal",
                target=False,
            )
        with self.assertRaisesRegex(rehearsal.RehearsalError, "source database"):
            rehearsal._postgres_dsn_info(
                "postgresql://postgres:secret@127.0.0.1/tokenkey",
                target=False,
            )
        with self.assertRaisesRegex(rehearsal.RehearsalError, "restore database"):
            rehearsal._postgres_dsn_info(
                "postgresql://postgres:secret@127.0.0.1/tokenkey_archive_rehearsal",
                target=True,
            )
        with self.assertRaisesRegex(rehearsal.RehearsalError, "unsupported"):
            rehearsal._postgres_record_query("users", cutoff=self.as_of, limit=1)
        remote_cli = subprocess.run(
            [
                "python3",
                str(_TOOL),
                "snapshot-postgres",
                "--source-dsn",
                "postgresql://postgres:secret@prod.example/tokenkey_archive_rehearsal",
                "--target-dsn",
                "postgresql://postgres:secret@127.0.0.1/tokenkey_archive_restore_cli",
                "--archive-root",
                str(self.archive_root),
                "--as-of",
                self.as_of,
                "--seed",
                "1",
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(remote_cli.returncode, 2)
        self.assertIn("localhost Docker", remote_cli.stderr)
        prod_cli = subprocess.run(
            [
                "python3",
                str(_TOOL),
                "snapshot-postgres",
                "--environment",
                "prod",
                "--source-dsn",
                "postgresql://postgres:secret@127.0.0.1/tokenkey_archive_rehearsal",
                "--target-dsn",
                "postgresql://postgres:secret@127.0.0.1/tokenkey_archive_restore_cli",
                "--archive-root",
                str(self.archive_root),
                "--as-of",
                self.as_of,
                "--seed",
                "1",
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(prod_cli.returncode, 2)
        self.assertIn("invalid choice", prod_cli.stderr)
        completed = subprocess.CompletedProcess([], 0, stdout="", stderr="")
        with mock.patch.object(
            rehearsal.subprocess, "run", return_value=completed
        ) as run_psql:
            rehearsal._run_psql(
                "postgresql://postgres:secret@127.0.0.1/tokenkey_archive_rehearsal",
                "SELECT 1",
                timeout_seconds=1,
                read_only=True,
            )
        command = run_psql.call_args.args[0]
        self.assertNotIn("secret", " ".join(command))
        self.assertEqual(run_psql.call_args.kwargs["env"]["PGPASSWORD"], "secret")


def _have_postgres_integration() -> bool:
    return bool(shutil.which("docker") and shutil.which("psql"))


@unittest.skipUnless(
    _have_postgres_integration(),
    "needs docker and psql for the real PostgreSQL rehearsal integration test",
)
class PostgresArchiveRehearsalIntegrationTest(unittest.TestCase):
    """Exercise the phase-3 contract against a throwaway real PostgreSQL."""

    _container: str | None = None
    _port: str = ""
    _source_dsn: str = ""
    _admin_dsn: str = ""
    _target_dsn: str = ""

    @classmethod
    def _psql(cls, dsn: str, sql: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [
                "psql",
                dsn,
                "-X",
                "-q",
                "-t",
                "-A",
                "-v",
                "ON_ERROR_STOP=1",
                "-c",
                sql,
            ],
            capture_output=True,
            text=True,
            check=False,
        )

    @classmethod
    def setUpClass(cls) -> None:
        cls._container = f"tk-archive-rehearsal-{os.getpid()}"
        image = os.environ.get("TOKENKEY_ARCHIVE_POSTGRES_IMAGE", "postgres:18-alpine")
        try:
            subprocess.run(
                [
                    "docker",
                    "run",
                    "-d",
                    "--rm",
                    "--name",
                    cls._container,
                    "-p",
                    "127.0.0.1::5432",
                    "-e",
                    "POSTGRES_PASSWORD=test",
                    "-e",
                    "POSTGRES_DB=tokenkey_archive_rehearsal",
                    image,
                ],
                check=True,
                capture_output=True,
                text=True,
            )
            deadline = time.time() + 60
            while time.time() < deadline:
                port = subprocess.run(
                    ["docker", "port", cls._container, "5432/tcp"],
                    check=False,
                    capture_output=True,
                    text=True,
                ).stdout.strip()
                if ":" in port:
                    cls._port = port.rsplit(":", 1)[-1]
                ready = subprocess.run(
                    [
                        "docker",
                        "exec",
                        cls._container,
                        "pg_isready",
                        "-U",
                        "postgres",
                        "-d",
                        rehearsal.POSTGRES_REHEARSAL_DATABASE,
                    ],
                    check=False,
                    capture_output=True,
                ).returncode == 0
                if cls._port and ready:
                    break
                time.sleep(1)
            if not cls._port:
                raise RuntimeError("postgres port was not published")
            base = f"postgresql://postgres:test@127.0.0.1:{cls._port}"
            cls._source_dsn = f"{base}/{rehearsal.POSTGRES_REHEARSAL_DATABASE}"
            cls._admin_dsn = f"{base}/postgres"
            target_db = f"{rehearsal.POSTGRES_RESTORE_PREFIX}integration"
            create_db = cls._psql(cls._admin_dsn, f"CREATE DATABASE {target_db}")
            if create_db.returncode != 0:
                raise RuntimeError(create_db.stderr)
            cls._target_dsn = f"{base}/{target_db}"
            schema = """
            CREATE TABLE archive_rehearsal_sentinel (label text primary key);
            INSERT INTO archive_rehearsal_sentinel VALUES ('tokenkey_archive_rehearsal');
            CREATE TABLE usage_logs (id bigint, created_at timestamptz, model text, input_tokens integer);
            CREATE TABLE ops_system_logs (id bigint, created_at timestamptz, level text, message text);
            CREATE TABLE ops_error_logs (id bigint, created_at timestamptz, error_type text, error_message text);
            CREATE TABLE qa_records (id bigint, created_at timestamptz, request_id text, status_code integer);
            INSERT INTO usage_logs VALUES
              (1,'2026-04-01T00:00:00Z','model-a',10),(2,'2026-07-01T00:00:00Z','model-b',20);
            INSERT INTO ops_system_logs VALUES (1,'2026-06-01T00:00:00Z','WARN','old');
            INSERT INTO ops_error_logs VALUES (1,'2026-06-02T00:00:00Z','upstream','old');
            INSERT INTO qa_records VALUES (1,'2026-07-16T00:00:00Z','req-old',500);
            """
            seeded = cls._psql(cls._source_dsn, schema)
            if seeded.returncode != 0:
                raise RuntimeError(seeded.stderr)
        except (OSError, RuntimeError, subprocess.CalledProcessError) as exc:
            cls.tearDownClass()
            raise unittest.SkipTest(f"postgres backend unavailable: {exc}")

    @classmethod
    def tearDownClass(cls) -> None:
        if cls._container:
            subprocess.run(["docker", "stop", cls._container], capture_output=True)
            cls._container = None

    def test_postgres_source_guards_fail_closed(self) -> None:
        with self.assertRaisesRegex(rehearsal.RehearsalError, "exceed max_rows"):
            rehearsal.postgres_dry_run(
                self._source_dsn,
                as_of="2026-07-20T00:00:00Z",
                retention_days=dict(rehearsal.DEFAULT_RETENTION_DAYS),
                timeout_seconds=10,
                max_rows=1,
            )
        with self.assertRaisesRegex(rehearsal.RehearsalError, "read-only"):
            rehearsal._run_psql(
                self._source_dsn,
                "INSERT INTO usage_logs VALUES "
                "(99,'2026-01-01T00:00:00Z','must-not-write',1)",
                timeout_seconds=10,
                read_only=True,
            )
        with self.assertRaisesRegex(rehearsal.RehearsalError, "timed out|query failed"):
            rehearsal._run_psql(
                self._source_dsn,
                "SELECT pg_sleep(2)",
                timeout_seconds=1,
                read_only=True,
            )
        changed = self._psql(
            self._source_dsn,
            "UPDATE archive_rehearsal_sentinel SET label = 'wrong-label'",
        )
        self.assertEqual(changed.returncode, 0, changed.stderr)
        try:
            with self.assertRaisesRegex(rehearsal.RehearsalError, "sentinel label"):
                rehearsal.postgres_dry_run(
                    self._source_dsn,
                    as_of="2026-07-20T00:00:00Z",
                    retention_days=dict(rehearsal.DEFAULT_RETENTION_DAYS),
                    timeout_seconds=10,
                    max_rows=100,
                )
        finally:
            restored = self._psql(
                self._source_dsn,
                "UPDATE archive_rehearsal_sentinel "
                "SET label = 'tokenkey_archive_rehearsal'",
            )
            self.assertEqual(restored.returncode, 0, restored.stderr)

    def test_snapshot_postgres_runs_all_phases_and_is_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            archive_root = pathlib.Path(temporary) / "archives"
            cli = subprocess.run(
                [
                    "python3",
                    str(_TOOL),
                    "snapshot-postgres",
                    "--source-dsn",
                    self._source_dsn,
                    "--target-dsn",
                    self._target_dsn,
                    "--archive-root",
                    str(archive_root),
                    "--environment",
                    "nonprod",
                    "--as-of",
                    "2026-07-20T00:00:00Z",
                    "--seed",
                    "7",
                    "--timeout-seconds",
                    "10",
                    "--max-rows",
                    "100",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(cli.returncode, 0, cli.stderr)
            first = json.loads(cli.stdout)
            self.assertEqual(first["dry_run"]["candidate_rows"], 4)
            self.assertTrue(first["verify"]["verified"])
            self.assertTrue(first["restore"]["verified"])
            self.assertGreater(first["metrics"]["logical_bytes"], 0)
            self.assertGreater(first["metrics"]["artifact_bytes"], 0)
            self.assertIsNotNone(first["metrics"]["compression_ratio"])
            self.assertNotIn("postgres:test", json.dumps(first, sort_keys=True))

            second = rehearsal.snapshot_postgres(
                self._source_dsn,
                self._target_dsn,
                archive_root,
                as_of="2026-07-20T00:00:00Z",
                seed=7,
                retention_days=dict(rehearsal.DEFAULT_RETENTION_DAYS),
                timeout_seconds=10,
                max_rows=100,
            )
            self.assertTrue(second["seal"]["idempotent_reuse"])
            self.assertTrue(second["restore"]["idempotent_reuse"])
            self.assertEqual(second["restore"]["inserted_rows"], 0)
            source_counts = self._psql(
                self._source_dsn,
                "SELECT (SELECT count(*) FROM usage_logs) + "
                "(SELECT count(*) FROM ops_system_logs) + "
                "(SELECT count(*) FROM ops_error_logs) + "
                "(SELECT count(*) FROM qa_records)",
            )
            self.assertEqual(source_counts.returncode, 0, source_counts.stderr)
            self.assertEqual(source_counts.stdout.strip(), "5")


if __name__ == "__main__":
    unittest.main()
