#!/usr/bin/env python3
"""Restore/audit only on a disposable host, using the read-only repository role.

No live-volume argument, no published port, no application process. A successful
receipt follows cleanup; business acceptance and cutover remain separate gates.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import shutil
import subprocess
import sys
import time
import uuid
from datetime import datetime
from pathlib import Path

from postgres_backup import (
    BackupError,
    atomic_json,
    info_entry,
    run,
    validate_verification,
    wal_coverage,
)

PGDATA = "/var/lib/postgresql/data"
# The prod compose override supplies 200 on the command line, so physical
# backups do not carry it in postgresql.conf. Recovery must match that floor.
PRIMARY_MAX_CONNECTIONS = 200


def recovery_log_is_terminal(log: str) -> bool:
    """Distinguish a failed recovery from normal startup probe messages."""
    return (
        "PANIC" in log
        or "recovery aborted" in log
        or ("startup process" in log and "exited with exit code" in log)
        or "database system is shut down" in log
    )


def restore_arguments(label: str, target: str | None) -> list[str]:
    if not re.fullmatch(r"\d{8}-\d{6}F(?:_\d{8}-\d{6}[DI])?", label):
        raise BackupError("invalid backup label")
    if target and not re.fullmatch(r"tk_[a-zA-Z0-9_]{1,50}", target):
        raise BackupError("invalid named recovery point")
    args = [f"--set={label}", "--archive-mode=off", "--process-max=4"]
    return args + (
        ["--type=name", f"--target={target}", "--target-action=promote"]
        if target
        else ["--type=default"]
    )


def copy_billing_check(container: str, binary: Path) -> None:
    # docker cp does not reliably target tmpfs across daemon versions. Write
    # through the running container's mount namespace, never its overlay layer.
    with binary.open("rb") as source:
        subprocess.run(
            [
                "docker",
                "exec",
                "-i",
                container,
                "sh",
                "-ec",
                "cat > /tmp/backup-billing-check; chmod 755 /tmp/backup-billing-check",
            ],
            stdin=source,
            capture_output=True,
            check=True,
            timeout=120,
        )


def select_backups(entry: dict, target_time: float | None) -> list[str]:
    eligible = [
        b
        for b in entry["backup"]
        if target_time is None or b["timestamp"]["stop"] < target_time
    ]
    if not eligible:
        raise BackupError("no completed backup precedes the recovery target")
    latest = max(eligible, key=lambda b: b["timestamp"]["stop"])
    if target_time is None:
        return [latest["label"]]
    if latest["type"] != "incr":
        raise BackupError(
            "historical acceptance requires a completed incremental before the target"
        )
    base = latest["label"].split("_")[0]
    if not any(b["label"] == base and b["type"] == "full" for b in eligible):
        raise BackupError("incremental base is absent")
    return [base, latest["label"]]


def require_disposable_host() -> None:
    if Path("/var/lib/tokenkey/postgres").exists():
        raise BackupError("refusing a production data host")
    names = run(["docker", "ps", "-a", "--format", "{{.Names}}"]).splitlines()
    if any(
        name == "tokenkey-postgres" or name in ("tokenkey-blue", "tokenkey-green")
        for name in names
    ):
        raise BackupError("refusing a production/application host")


class Restore:
    def __init__(self, image: str, config: Path):
        self.image, self.config = image, config.resolve()
        self.prefix = "tk-physical-restore-" + uuid.uuid4().hex[:10]
        self.containers = []
        self.volumes = []

    def create(self, suffix: str) -> str:
        name = self.prefix + "-" + suffix
        volume = name + "-data"
        run(
            [
                "docker",
                "volume",
                "create",
                "--label",
                f"tokenkey.restore={self.prefix}",
                volume,
            ]
        )
        self.volumes.append(volume)
        self.containers.append(name)
        run(
            [
                "docker",
                "run",
                "-d",
                "--name",
                name,
                "--label",
                f"tokenkey.restore={self.prefix}",
                "--entrypoint",
                "sh",
                "--memory",
                "6g",
                "--cpus",
                "4",
                "--shm-size",
                "256m",
                "--tmpfs",
                "/tmp:exec",
                "--tmpfs",
                "/var/run/postgresql:uid=70,gid=70",
                "-v",
                f"{volume}:/var/lib/postgresql",
                "-v",
                f"{self.config}:/etc/pgbackrest:ro",
                self.image,
                "-ec",
                f"mkdir -p {PGDATA}; chown postgres:postgres {PGDATA}; exec sleep 7200",
            ]
        )
        return name

    def backrest(self, name: str, *args: str) -> str:
        return run(
            [
                "docker",
                "exec",
                "--user",
                "postgres",
                name,
                "timeout",
                "3300",
                "pgbackrest",
                "--stanza=tokenkey",
                *args,
            ],
            timeout=3320,
        )

    def sql(self, name: str, query: str) -> str:
        # Do not log SQL errors that could include private data rows.
        result = subprocess.run(
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
                "-XAt",
                "-v",
                "ON_ERROR_STOP=1",
                "-c",
                query,
            ],
            capture_output=True,
            check=False,
            text=True,
            timeout=1200,
        )
        if result.returncode:
            raise BackupError(
                "isolated database validation failed; private SQL details withheld"
            )
        return result.stdout.strip()

    def recovery_diagnostics(self, name: str) -> str:
        script = (
            "for file in /tmp/recovery.log "
            "/var/lib/postgresql/data/postgresql.auto.conf "
            "/var/lib/postgresql/data/postmaster.opts "
            "/var/lib/postgresql/data/PG_VERSION; do "
            "if [ -f \"$file\" ]; then echo \"--- $file ---\"; "
            "tail -200 \"$file\"; fi; done; "
            "echo '--- pg_ctl status ---'; "
            "su postgres -c 'pg_ctl -D /var/lib/postgresql/data status' 2>&1 || true"
        )
        result = subprocess.run(
            ["docker", "exec", name, "sh", "-ec", script],
            capture_output=True,
            check=False,
            text=True,
            timeout=30,
        )
        output = (result.stdout + result.stderr).strip() or (
            "<no recovery diagnostics captured>"
        )
        return output[-12000:]

    def start(self, name: str) -> None:
        try:
            run(
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
                    "-c listen_addresses='' -c archive_mode=off "
                    f"-c max_connections={PRIMARY_MAX_CONNECTIONS} "
                    "-c shared_buffers=1GB",
                    "-W",
                    "start",
                ]
            )
        except BackupError as exc:
            raise BackupError(
                f"recovery start failed: {exc}; diagnostics:\n"
                f"{self.recovery_diagnostics(name)}"
            ) from None
        for _ in range(900):
            try:
                if self.sql(name, "SELECT NOT pg_is_in_recovery()") == "t":
                    return
            except BackupError:
                log = self.recovery_diagnostics(name)
                # Connection probes during WAL replay legitimately log
                # ``FATAL: the database system is starting up``.  Only abort
                # on a terminal recovery/startup marker, otherwise the probe
                # can kill a healthy recovery before replay completes.
                if recovery_log_is_terminal(log):
                    raise BackupError(
                        "recovery failed before the requested point was reached; "
                        f"diagnostics:\n{log}"
                    ) from None
            time.sleep(1)
        raise BackupError(
            "recovery deadline exceeded; diagnostics:\n"
            f"{self.recovery_diagnostics(name)}"
        )

    def audit(self, name: str) -> dict:
        # Aggregate hashes do not export secrets or PII. Scan each physical leaf
        # once: partitions are included without double-counting their parents.
        tables = json.loads(
            self.sql(
                name,
                """SELECT json_agg(c.relname ORDER BY c.relname)
            FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname='public' AND c.relkind='r'""",
            )
        )
        required = {
            "users",
            "api_keys",
            "usage_billing_dedup",
            "usage_billing_dedup_archive",
        }
        if not required.issubset(tables) or not any(
            t.startswith("usage_logs") for t in tables
        ):
            raise BackupError("required billing/usage tables missing")
        snapshots = {}
        for table in tables:
            quoted = '"' + table.replace('"', '""') + '"'
            snapshots[table] = json.loads(
                self.sql(
                    name,
                    f"""SELECT json_build_object(
                'rows',count(*),'hash_sum',coalesce(sum(('x'||substr(md5(row_to_json(t)::text),1,16))::bit(64)::bigint::numeric),0)::text)
                FROM public.{quoted} t""",
                )
            )
        usage = json.loads(
            self.sql(
                name,
                """SELECT coalesce(json_agg(t ORDER BY partition), '[]') FROM (
            SELECT tableoid::regclass::text AS partition,count(*) AS rows,
              min(created_at) AS first,max(created_at) AS last,
              sum(actual_cost)::text AS actual_cost, sum(total_cost)::text AS total_cost
            FROM usage_logs GROUP BY tableoid) t""",
            )
        )
        catalog = self.sql(
            name,
            """SELECT json_build_object(
            'indexes',(SELECT json_agg(t ORDER BY tablename,indexname) FROM pg_indexes t WHERE schemaname='public'),
            'constraints',(SELECT json_agg(t ORDER BY table_name,constraint_name) FROM information_schema.table_constraints t WHERE constraint_schema='public'),
            'sequences',(SELECT json_agg(to_jsonb(t)-'last_value' ORDER BY sequencename) FROM pg_sequences t WHERE schemaname='public'))""",
        )
        sequences = json.loads(
            self.sql(
                name,
                """SELECT coalesce(json_agg(json_build_object(
            'sequence',s.relname,'table',t.relname,'column',a.attname)), '[]')
            FROM pg_class s JOIN pg_depend d ON d.objid=s.oid AND d.deptype IN ('a','i')
            JOIN pg_class t ON t.oid=d.refobjid JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=d.refobjsubid
            WHERE s.relkind='S' AND t.relnamespace='public'::regnamespace""",
            )
        )
        sequences.append(
            {"sequence": "usage_logs_id_seq", "table": "usage_logs", "column": "id"}
        )
        for sequence in sequences:
            seq, table, column = [
                '"' + sequence[key].replace('"', '""') + '"'
                for key in ("sequence", "table", "column")
            ]
            safe = self.sql(
                name,
                # setval(..., false) returns last_value on the next nextval.
                f"""SELECT CASE WHEN is_called
                THEN last_value >= coalesce((SELECT max({column}) FROM {table}),0)
                ELSE last_value > coalesce((SELECT max({column}) FROM {table}),0)
                END FROM {seq}""",
            )
            if safe != "t":
                raise BackupError("restored sequence could reuse an existing ID")
        invalid = self.sql(
            name, "SELECT count(*) FROM pg_index WHERE NOT indisvalid OR NOT indisready"
        )
        if invalid != "0":
            raise BackupError("restored database has invalid indexes")
        return {
            "tables": snapshots,
            "usage": usage,
            "catalog_sha256": hashlib.sha256(catalog.encode()).hexdigest(),
        }

    def cleanup(self) -> None:
        failures = []
        for name in reversed(self.containers):
            try:
                run(["docker", "rm", "-f", name])
            except BackupError:
                failures.append(name)
        for volume in reversed(self.volumes):
            try:
                run(["docker", "volume", "rm", volume])
            except BackupError:
                failures.append(volume)
        if failures:
            raise BackupError("restore cleanup failed: " + ", ".join(failures))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--config-dir", type=Path, required=True)
    parser.add_argument("--target-name")
    parser.add_argument(
        "--target-time", help="UTC timestamp recorded when the named point was created"
    )
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument(
        "--billing-check",
        type=Path,
        required=True,
        help="static binary built from this reviewed commit",
    )
    args = parser.parse_args()
    if bool(args.target_name) != bool(args.target_time):
        parser.error("--target-name and --target-time must be supplied together")
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", args.image):
        parser.error("use the immutable loaded image ID")
    target_time = (
        datetime.fromisoformat(args.target_time.replace("Z", "+00:00")).timestamp()  # noqa: FURB162 — Python 3.9
        if args.target_time
        else None
    )
    require_disposable_host()
    restore = Restore(args.image, args.config_dir)
    started = time.time()
    try:
        reader = restore.create("reader")
        entry = info_entry(restore.backrest(reader, "--output=json", "info"))
        labels = select_backups(entry, target_time)
        required_bytes = max(
            b["info"]["size"] for b in entry["backup"] if b["label"] in labels
        )
        # Two sequential restores retain only one audit-sized cluster at a time.
        docker_root = run(["docker", "info", "--format", "{{.DockerRootDir}}"]).strip()
        if shutil.disk_usage(docker_root).free < required_bytes * 1.5 + 5 * 1024**3:
            raise BackupError("insufficient independent restore disk capacity")
        archive = entry["archive"][-1]
        objects = json.loads(
            restore.backrest(
                reader,
                "--recurse",
                "--output=json",
                "repo-ls",
                f"archive/tokenkey/{archive['id']}",
            )
        )
        oldest = min(
            (b for b in entry["backup"] if b["type"] == "full"),
            key=lambda b: b["timestamp"]["stop"],
        )
        wal_coverage(objects, oldest["archive"]["start"], archive["max"], 16 * 1024**2)
        validate_verification(
            restore.backrest(reader, "--output=text", "--verbose", "verify")
        )
        repository_check_seconds = time.time() - started
        results, audits = [], []
        for index, label in enumerate(labels):
            name = restore.create(str(index))
            restore_started = time.time()
            restore.backrest(
                name, *restore_arguments(label, args.target_name), "restore"
            )
            copied = time.time()
            restore.start(name)
            ready = time.time()
            if args.target_name is None:
                # Default recovery may stop on an archive-get error. It must
                # still reach the newest WAL observed before restore started.
                last = archive["max"]
                floor = (
                    f"{int(last[8:16], 16):X}/{int(last[16:24], 16) * 16 * 1024**2:X}"
                )
                if (
                    restore.sql(
                        name, f"SELECT pg_last_wal_replay_lsn() >= '{floor}'::pg_lsn"
                    )
                    != "t"
                ):
                    raise BackupError(
                        "latest recovery stopped before the observed remote WAL frontier"
                    )
            audit = restore.audit(name)
            results.append(
                {
                    "label": label,
                    "restore_seconds": copied - restore_started,
                    "recovery_seconds": ready - copied,
                    "audit_seconds": time.time() - ready,
                    "total_seconds": time.time() - restore_started,
                }
            )
            audits.append(audit)
            copy_billing_check(name, args.billing_check.resolve())
            billing = json.loads(
                run(
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
            if billing != {
                "status": "ok",
                "charge_retry": True,
                "archive_retry": True,
                "refund_retry": True,
            }:
                raise BackupError("billing behavior acceptance failed")
            results[-1]["billing"] = billing
            # A ready recovery host still has to read/verify the repository.
            # Charge that shared prerequisite to each path's recovery budget.
            results[-1]["repository_check_seconds"] = repository_check_seconds
            results[-1]["total_seconds"] = (
                repository_check_seconds + time.time() - restore_started
            )
            run(["docker", "rm", "-f", name])
            restore.containers.remove(name)
            volume = name + "-data"
            run(["docker", "volume", "rm", volume])
            restore.volumes.remove(volume)
        if len(audits) == 2 and audits[0] != audits[1]:
            raise BackupError(
                "full and incremental paths differ at the same recovery point"
            )
    finally:
        restore.cleanup()
    # Source-host acceptance also requires EC2 termination, the historical pair,
    # fresh WAL evidence and the observation window; this receipt alone never cuts over.
    atomic_json(
        args.receipt,
        {
            "status": "ok",
            "completed_at": time.time(),
            "cleanup_verified": True,
            "target": args.target_name or "latest-archived",
            "results": results,
            "audit": audits[-1],
            "duration_seconds": time.time() - started,
            "repository_verified": True,
            "business_acceptance": "passed",
            "system_id": entry["db"][-1]["system-id"],
            "image": args.image,
            "within_30_minutes": all(
                result["total_seconds"] <= 1800 for result in results
            ),
        },
    )


if __name__ == "__main__":
    try:
        main()
    except (
        BackupError,
        ValueError,
        OSError,
        KeyError,
        subprocess.SubprocessError,
    ) as exc:
        print(f"physical restore failed: {exc}", file=sys.stderr)
        sys.exit(1)
