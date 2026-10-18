#!/usr/bin/env python3
"""Exercise real PostgreSQL 18 + pgBackRest in disposable Docker resources.

No production credentials, mounts, published ports or external network. An
optional MinIO instance exercises S3 over TLS on a private Docker network;
production AWS/IAM acceptance is separate.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import tempfile
import time
import uuid
from pathlib import Path

from postgres_backup import (
    BackupError,
    atomic_json,
    validate_verification,
    wal_coverage,
)
from restore_postgres_backup import Restore, copy_billing_check, restore_arguments

IMAGE = "tokenkey-postgres-backup:dev"
PGDATA = "/var/lib/postgresql/data"

SCHEMA = """
CREATE TABLE users (id bigint PRIMARY KEY, balance numeric(20,8) NOT NULL, deleted_at timestamptz, updated_at timestamptz);
CREATE TABLE api_keys(id bigint PRIMARY KEY,user_id bigint REFERENCES users,quota numeric(20,8) DEFAULT 0,quota_used numeric(20,8) DEFAULT 0,status text,deleted_at timestamptz,updated_at timestamptz);
CREATE TABLE usage_billing_dedup (
    id bigserial, created_at timestamptz DEFAULT now(),
    request_id text NOT NULL, api_key_id bigint NOT NULL,
    request_fingerprint text NOT NULL, PRIMARY KEY(request_id,api_key_id));
CREATE TABLE usage_billing_dedup_archive (LIKE usage_billing_dedup INCLUDING ALL);
CREATE SEQUENCE usage_logs_id_seq;
CREATE TABLE usage_logs (
    id bigint DEFAULT nextval('usage_logs_id_seq'), created_at timestamptz NOT NULL,
    request_id text NOT NULL, user_id bigint REFERENCES users,
    actual_cost numeric(20,8) NOT NULL, total_cost numeric(20,8) DEFAULT 0, PRIMARY KEY(created_at,id)
) PARTITION BY RANGE(created_at);
CREATE TABLE usage_logs_day PARTITION OF usage_logs
    FOR VALUES FROM ('2026-10-18') TO ('2026-10-19');
CREATE TABLE usage_logs_default PARTITION OF usage_logs DEFAULT;
CREATE INDEX usage_logs_user_time ON usage_logs(user_id,created_at);
CREATE TABLE notes (id integer PRIMARY KEY, value text);
INSERT INTO users(id,balance) VALUES (1,100.00000000);
INSERT INTO api_keys(id,user_id,quota,status) VALUES(1,1,100,'active');
INSERT INTO notes VALUES (1,'before'),(2,'delete me');
CREATE FUNCTION charge(request text, cost numeric) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM usage_billing_dedup_archive WHERE request_id=request AND api_key_id=1)
    THEN RETURN; END IF;
  INSERT INTO usage_billing_dedup(request_id,api_key_id,request_fingerprint) VALUES (request,1,md5(cost::text))
    ON CONFLICT DO NOTHING;
  IF NOT FOUND THEN RETURN; END IF;
  UPDATE users SET balance=balance-cost WHERE id=1;
  INSERT INTO usage_logs(created_at,request_id,user_id,actual_cost)
    VALUES ('2026-10-18 01:00:00+00',request,1,cost);
END $$;
SELECT charge('first',1.25000000);
"""

SNAPSHOT = """SELECT json_build_object(
 'users',(SELECT json_agg(row_to_json(t) ORDER BY id) FROM users t), -- ops-allow-soft-deleted: audit every restored row
 'usage',(SELECT json_agg(row_to_json(t) ORDER BY id) FROM usage_logs t),
 'dedup',(SELECT json_agg(row_to_json(t) ORDER BY request_id) FROM usage_billing_dedup t),
 'archive',(SELECT json_agg(row_to_json(t) ORDER BY request_id) FROM usage_billing_dedup_archive t),
 'notes',(SELECT json_agg(row_to_json(t) ORDER BY id) FROM notes t),
 'sequence',(SELECT last_value FROM usage_logs_id_seq),
 'cost',(SELECT sum(actual_cost)::text FROM usage_logs),
 'indexes',(SELECT json_agg(indexname ORDER BY indexname) FROM pg_indexes
     WHERE schemaname='public')
);"""


def command(
    args: list[str], *, input_text: str | None = None, timeout: int = 180
) -> str:
    result = subprocess.run(
        args,
        input=input_text,
        text=True,
        capture_output=True,
        check=False,
        timeout=timeout,
    )
    if result.returncode:
        raise RuntimeError(
            f"command failed: {' '.join(args[:5])}\n{result.stdout[-3000:]}\n{result.stderr[-3000:]}"
        )
    return result.stdout.strip()


class Rehearsal:
    def __init__(
        self,
        root: Path,
        image: str,
        storage: str = "posix",
        billing_check: Path | None = None,
    ):
        self.root, self.image = root, image
        self.billing_check = billing_check
        self.audits = {}
        self.storage = storage
        self.network = "none"
        self.prefix = "tk-backup-test-" + uuid.uuid4().hex[:10]
        self.source = self.prefix + "-source"
        self.repo = self.prefix + "-repo"
        self.containers: list[str] = []
        self.volumes: list[str] = []
        self.results: dict = {}
        self.config = root / "pgbackrest.conf"
        self.config.write_text(f"""[global]
repo1-path=/backup
repo1-retention-full=2
repo1-block=y
repo1-bundle=y
compress-type=zst
process-max=2
start-fast=y
archive-async=n
archive-timeout=15
log-level-console=info
log-level-file=off
[tokenkey]
pg1-path={PGDATA}
pg1-socket-path=/var/run/postgresql
pg1-user=tokenkey
""")
        self.config.chmod(0o644)

    def start_s3(self) -> None:
        self.network = self.prefix
        command(["docker", "network", "create", "--internal", self.network])
        cert = self.root / "public.crt"
        key = self.root / "private.key"
        command(
            [
                "openssl",
                "req",
                "-x509",
                "-newkey",
                "rsa:2048",
                "-nodes",
                "-days",
                "1",
                "-subj",
                "/CN=minio",
                "-addext",
                "subjectAltName=DNS:minio",
                "-keyout",
                str(key),
                "-out",
                str(cert),
            ]
        )
        name = self.prefix + "-minio"
        self.containers.append(name)
        data = self.volume("s3-data")
        command(
            [
                "docker",
                "create",
                "--name",
                name,
                "--network",
                self.network,
                "--network-alias",
                "minio",
                "--entrypoint",
                "sh",
                "--tmpfs",
                "/tmp:exec",
                "-v",
                f"{data}:/data",
                "-e",
                "MINIO_ROOT_USER=rehearsal",
                "-e",
                "MINIO_ROOT_PASSWORD=synthetic-rehearsal-only",
                self.image,
                "-ec",
                "mkdir -p /data/backups; exec minio server --certs-dir /etc/minio-certs /data",
            ]
        )
        command(["docker", "cp", str(self.root), f"{name}:/etc/minio-certs"])
        command(["docker", "start", name])
        for _ in range(100):
            result = subprocess.run(
                [
                    "docker",
                    "exec",
                    name,
                    "wget",
                    "-q",
                    "--no-check-certificate",
                    "-O",
                    "/dev/null",
                    "https://127.0.0.1:9000/minio/health/ready",
                ],
                capture_output=True,
                check=False,
                timeout=3,
            )
            if result.returncode == 0:
                break
            time.sleep(0.2)
        else:
            raise RuntimeError("isolated S3 server did not become ready")
        content = self.config.read_text().replace(
            "repo1-path=/backup",
            """repo1-type=s3
repo1-path=/test
repo1-s3-bucket=backups
repo1-s3-region=us-east-1
repo1-s3-endpoint=minio
repo1-s3-uri-style=path
repo1-storage-port=9000
repo1-storage-ca-file=/etc/pgbackrest/ca.crt
repo1-s3-key=rehearsal
repo1-s3-key-secret=synthetic-rehearsal-only""",
        )
        self.config.write_text(content)

    def copy_config(self, name: str) -> None:
        command(
            [
                "docker",
                "cp",
                str(self.config),
                f"{name}:/etc/pgbackrest/pgbackrest.conf",
            ]
        )
        if self.storage == "s3":
            command(
                [
                    "docker",
                    "cp",
                    str(self.root / "public.crt"),
                    f"{name}:/etc/pgbackrest/ca.crt",
                ]
            )

    def volume(self, suffix: str) -> str:
        name = self.prefix + "-" + suffix
        command(
            [
                "docker",
                "volume",
                "create",
                "--label",
                f"tokenkey.rehearsal={self.prefix}",
                "--opt",
                "type=tmpfs",
                "--opt",
                "device=tmpfs",
                "--opt",
                "o=size=512m",
                name,
            ]
        )
        self.volumes.append(name)
        return name

    def pg(self, name: str, sql: str) -> str:
        return command(
            [
                "docker",
                "exec",
                "-i",
                "--user",
                "postgres",
                name,
                "psql",
                "-U",
                "tokenkey",
                "-d",
                "tokenkey",
                "-XAt",
                "-v",
                "ON_ERROR_STOP=1",
            ],
            input_text=sql,
        )

    sql = pg  # shared production audit uses this executor on synthetic data

    def backrest(self, name: str, *args: str) -> str:
        return command(
            [
                "docker",
                "exec",
                "--user",
                "postgres",
                name,
                "timeout",
                "150",
                "pgbackrest",
                "--stanza=tokenkey",
                *args,
            ]
        )

    def wait_database(self, name: str, *, expect_failure: bool = False) -> None:
        for _ in range(150):
            state = command(
                ["docker", "inspect", "--format", "{{.State.Running}}", name]
            )
            if state != "true":
                logs = command(["docker", "logs", name])
                if (
                    expect_failure
                    and "recovery ended before configured recovery target was reached"
                    in logs
                ):
                    return
                raise RuntimeError(f"database exited unexpectedly: {logs[-4000:]}")
            probe = subprocess.run(
                [
                    "docker",
                    "exec",
                    "--user",
                    "postgres",
                    name,
                    "psql",
                    "-U",
                    "tokenkey",
                    "-d",
                    "tokenkey",
                    "-XAtc",
                    "SELECT 1",
                ],
                capture_output=True,
                check=False,
                text=True,
                timeout=5,
            )
            if probe.returncode == 0 and probe.stdout.strip() == "1":
                if expect_failure:
                    raise AssertionError(
                        "incomplete recovery incorrectly became available"
                    )
                return
            time.sleep(0.2)
        raise RuntimeError("database readiness deadline exceeded")

    def start_source(self) -> None:
        if self.storage == "s3":
            self.start_s3()
        self.repo = self.volume("repo")
        data = self.volume("source-data")
        self.containers.append(self.source)
        command(
            [
                "docker",
                "create",
                "--name",
                self.source,
                "--network",
                self.network,
                "--tmpfs",
                "/tmp:exec",
                "--tmpfs",
                "/var/run/postgresql:uid=70,gid=70",
                "--label",
                f"tokenkey.rehearsal={self.prefix}",
                "-v",
                f"{data}:/var/lib/postgresql",
                "-v",
                f"{self.repo}:/backup",
                "-e",
                f"PGDATA={PGDATA}",
                "-e",
                "POSTGRES_USER=tokenkey",
                "-e",
                "POSTGRES_DB=tokenkey",
                "-e",
                "POSTGRES_HOST_AUTH_METHOD=trust",
                self.image,
                "postgres",
                "-c",
                "archive_mode=on",
                "-c",
                "archive_command=pgbackrest --stanza=tokenkey archive-push %p",
            ]
        )
        self.copy_config(self.source)
        command(["docker", "start", self.source])
        self.wait_database(self.source)
        command(
            ["docker", "exec", self.source, "chown", "postgres:postgres", "/backup"]
        )
        self.backrest(self.source, "stanza-create")
        self.pg(self.source, SCHEMA)

    def backup(self, kind: str) -> str:
        self.backrest(self.source, f"--type={kind}", "backup")
        info = json.loads(self.backrest(self.source, "--output=json", "info"))[0]
        latest = info["backup"][-1]
        if latest["type"] != kind:
            raise AssertionError(f"expected {kind} backup, got {latest['type']}")
        return latest["label"]

    def snapshot(self, name: str) -> dict:
        return json.loads(self.pg(name, SNAPSHOT))

    def restore(
        self,
        label: str,
        suffix: str,
        target: str | None = None,
        *,
        expect_failure: bool = False,
    ) -> str:
        name = self.prefix + "-" + suffix
        data = self.volume(suffix)
        self.containers.append(name)
        command(
            [
                "docker",
                "create",
                "--name",
                name,
                "--network",
                self.network,
                "--tmpfs",
                "/tmp:exec",
                "--tmpfs",
                "/var/run/postgresql:uid=70,gid=70",
                "--label",
                f"tokenkey.rehearsal={self.prefix}",
                "--entrypoint",
                "sh",
                "-v",
                f"{data}:/var/lib/postgresql",
                "-v",
                f"{self.repo}:/backup:ro",
                self.image,
                "-c",
                f"mkdir -p {PGDATA}; chown postgres:postgres {PGDATA}; exec sleep 600",
            ]
        )
        self.copy_config(name)
        command(["docker", "start", name])
        args = restore_arguments(label, target)
        self.backrest(name, *args, "restore")
        # archive_mode=off prevents restored test clusters from publishing WAL.
        command(
            [
                "docker",
                "exec",
                "--user",
                "postgres",
                name,
                "pg_ctl",
                "-D",
                PGDATA,
                "-l",
                "/tmp/recovery.log",
                "-o",
                "-c listen_addresses=''",
                "-W",
                "start",
            ]
        )
        for _ in range(150):
            probe = subprocess.run(
                [
                    "docker",
                    "exec",
                    "--user",
                    "postgres",
                    name,
                    "psql",
                    "-U",
                    "tokenkey",
                    "-d",
                    "tokenkey",
                    "-XAtc",
                    "SELECT pg_is_in_recovery()",
                ],
                capture_output=True,
                check=False,
                text=True,
                timeout=5,
            )
            if probe.returncode == 0:
                if expect_failure:
                    raise AssertionError(
                        "missing WAL recovery unexpectedly became queryable"
                    )
                if probe.stdout.strip() == "t":
                    self.pg(name, "SELECT pg_wal_replay_resume();")
                return name
            logs = command(["docker", "exec", name, "cat", "/tmp/recovery.log"])
            if "recovery ended before configured recovery target was reached" in logs:
                if expect_failure:
                    return name
                raise RuntimeError(logs[-2000:])
            time.sleep(0.2)
        raise RuntimeError("restore did not reach its requested recovery point")

    def exercise(self) -> dict:
        self.start_source()
        full = self.backup("full")
        self.pg(
            self.source,
            """BEGIN;
SELECT charge('second',2.50000000);
SELECT charge('refund-second',-0.25000000);
INSERT INTO usage_billing_dedup_archive SELECT * FROM usage_billing_dedup WHERE request_id='first';
DELETE FROM usage_billing_dedup WHERE request_id='first';
UPDATE notes SET value='after update' WHERE id=1;
DELETE FROM notes WHERE id=2;
INSERT INTO usage_logs(created_at,request_id,user_id,actual_cost)
VALUES ('2026-10-20','default-partition',1,0);
COMMIT;""",
        )
        first_incremental = self.backup("incr")
        expected_historical = self.snapshot(self.source)
        self.pg(self.source, "SELECT pg_create_restore_point('tk_verified_point');")
        target_wal = self.pg(
            self.source, "SELECT pg_walfile_name(pg_current_wal_lsn());"
        )
        self.pg(self.source, "SELECT pg_switch_wal();")
        self.backrest(self.source, "check")
        self.pg(
            self.source,
            "SELECT charge('after-target',3.00000000); INSERT INTO notes VALUES(3,'later');",
        )
        second_incremental = self.backup("incr")
        expected_latest = self.snapshot(self.source)
        validate_verification(
            self.backrest(self.source, "--output=text", "--verbose", "verify")
        )
        self.results["contents_verified"] = True

        for suffix, label, target, expected in [
            ("latest", second_incremental, None, expected_latest),
            ("historical", first_incremental, "tk_verified_point", expected_historical),
            ("full-audit", full, "tk_verified_point", expected_historical),
        ]:
            started = time.monotonic()
            name = self.restore(label, suffix, target)
            restored = self.snapshot(name)
            # Sequences may advance to the end of their WAL-logged cache on
            # recovery. They must not reuse IDs; exact last_value is not a PITR
            # guarantee. All transactional data/indexes must match exactly.
            sequence = restored.pop("sequence")
            expected_data = {
                key: value for key, value in expected.items() if key != "sequence"
            }
            if restored != expected_data or sequence < expected["sequence"]:
                raise AssertionError(
                    f"{suffix} restored different data: {restored} != {expected_data}"
                )
            self.pg(
                name,
                "SELECT charge('second',2.50000000); SELECT charge('first',1.25000000); SELECT charge('refund-second',-0.25000000);",
            )
            after_retry = self.snapshot(name)
            after_retry.pop("sequence")
            if after_retry != expected_data:
                raise AssertionError(
                    "duplicate charge changed the restored balance/usage"
                )
            self.audits[suffix] = Restore.audit(self, name)
            if suffix == "latest":
                saved_sequence = json.loads(
                    self.pg(
                        name,
                        "SELECT json_build_array(last_value,is_called) FROM usage_logs_id_seq",
                    )
                )
                try:
                    self.pg(
                        name,
                        "SELECT setval('usage_logs_id_seq',(SELECT max(id) FROM usage_logs),false)",
                    )
                    try:
                        Restore.audit(self, name)
                    except BackupError as exc:
                        if "sequence could reuse" not in str(exc):
                            raise
                    else:
                        raise AssertionError(
                            "uncalled sequence could reuse an existing ID"
                        )
                finally:
                    value, called = saved_sequence
                    self.pg(
                        name,
                        f"SELECT setval('usage_logs_id_seq',{int(value)},{str(bool(called)).lower()})",
                    )
                self.results["uncalled_sequence_rejected"] = True
            if self.billing_check:
                copy_billing_check(name, self.billing_check)
                result = json.loads(
                    command(
                        [
                            "docker",
                            "exec",
                            "--user",
                            "postgres",
                            "-e",
                            "TOKENKEY_DISPOSABLE_RESTORE=yes",
                            name,
                            "/tmp/backup-billing-check",
                        ]
                    )
                )
                if result != {
                    "status": "ok",
                    "charge_retry": True,
                    "archive_retry": True,
                    "refund_retry": True,
                }:
                    raise AssertionError("real billing retry verification failed")
            self.results[suffix] = {
                "label": label,
                "target": target,
                "duration_seconds": round(time.monotonic() - started, 3),
                "balance": restored["users"][0]["balance"],
                "cost": restored["cost"],
            }

        if self.audits["historical"] != self.audits["full-audit"]:
            raise AssertionError(
                "production audit differs between full and incremental recovery"
            )
        self.results["real_billing_retries_verified"] = bool(self.billing_check)
        # Fault injection is confined to this synthetic, disposable repository.
        objects = json.loads(
            self.backrest(
                self.source, "--recurse", "--output=json", "repo-ls", "archive/tokenkey"
            )
        )
        missing = [
            name for name in objects if Path(name).name.startswith(target_wal + "-")
        ]
        if not missing:
            raise AssertionError("fault injection did not remove the target WAL")
        for name in missing:
            self.backrest(self.source, "repo-rm", "archive/tokenkey/" + name)
        self.restore(
            first_incremental, "missing-wal", "tk_verified_point", expect_failure=True
        )
        self.results["missing_wal_rejected"] = True
        # Native verify may still report OK: the missing segment is between
        # consistent backups. Our continuity check must reject it separately.
        objects_after = json.loads(
            self.backrest(
                self.source, "--recurse", "--output=json", "repo-ls", "archive/tokenkey"
            )
        )
        wal_names = sorted(
            Path(name).name[:24]
            for name in objects
            if len(Path(name).name) > 25 and Path(name).name[24] == "-"
        )
        wal_coverage(objects, wal_names[0], wal_names[-1], 16 * 1024**2)
        try:
            wal_coverage(objects_after, wal_names[0], wal_names[-1], 16 * 1024**2)
        except BackupError:
            self.results["wal_gap_detected"] = True
        else:
            raise AssertionError(
                "missing WAL was reported as a continuous recovery window"
            )
        # Corrupt a real backup bundle; checksum validation must fail closed.
        files = json.loads(
            self.backrest(
                self.source,
                "--recurse",
                "--output=json",
                "repo-ls",
                f"backup/tokenkey/{full}/bundle",
            )
        )
        bundle = next(
            name for name, value in files.items() if value.get("type") == "file"
        )
        command(
            [
                "docker",
                "exec",
                "-i",
                "--user",
                "postgres",
                self.source,
                "pgbackrest",
                "--stanza=tokenkey",
                "repo-put",
                f"backup/tokenkey/{full}/bundle/{bundle}",
            ],
            input_text="corrupt",
        )
        try:
            validate_verification(
                self.backrest(self.source, "--output=text", "--verbose", "verify")
            )
        except (RuntimeError, BackupError):
            self.results["corrupt_bundle_rejected"] = True
        else:
            raise AssertionError("corrupt backup incorrectly passed verification")
        try:
            self.backrest(self.source, "--repo1-path=/missing-repository", "check")
        except RuntimeError:
            self.results["unavailable_repository_rejected"] = True
        else:
            raise AssertionError("unavailable repository incorrectly passed check")
        return self.results

    def cleanup(self) -> None:
        failures = []
        for name in reversed(self.containers):
            result = subprocess.run(
                ["docker", "rm", "-f", name],
                capture_output=True,
                check=False,
                text=True,
            )
            if result.returncode and "No such container" not in result.stderr:
                failures.append(result.stderr)
        for name in reversed(self.volumes):
            result = subprocess.run(
                ["docker", "volume", "rm", name],
                capture_output=True,
                check=False,
                text=True,
            )
            if result.returncode:
                failures.append(result.stderr)
        if self.network != "none":
            result = subprocess.run(
                ["docker", "network", "rm", self.network],
                capture_output=True,
                check=False,
                text=True,
            )
            if result.returncode:
                failures.append(result.stderr)
        if failures:
            raise RuntimeError("rehearsal cleanup failed: " + "\n".join(failures))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default=IMAGE)
    parser.add_argument("--storage", choices=["posix", "s3"], default="posix")
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--billing-check", type=Path, required=True)
    args = parser.parse_args()
    started = time.monotonic()
    with tempfile.TemporaryDirectory(prefix="tk-physical-rehearsal-") as temporary:
        rehearsal = Rehearsal(
            Path(temporary), args.image, args.storage, args.billing_check.resolve()
        )
        try:
            results = rehearsal.exercise()
        finally:
            rehearsal.cleanup()
    receipt = {
        "status": "ok",
        "scope": f"synthetic-{args.storage}-repository",
        "production_s3_verified": False,
        "completed_at": time.time(),
        "duration_seconds": round(time.monotonic() - started, 3),
        "cleanup_verified": True,
        "results": results,
    }
    atomic_json(args.receipt, receipt)
    print(json.dumps(receipt, indent=2))


if __name__ == "__main__":
    main()
