#!/usr/bin/env python3
"""Stage0 physical-backup policy and operator entry point.

pgBackRest owns backup chains, WAL and expiration. This module owns the small
Stage0 boundary: configuration, cadence, serialization and truthful receipts.
Approval: docs/approved/design-prod-physical-backup.md
"""

from __future__ import annotations

import argparse
import fcntl
import json
import re
import shutil
import subprocess
import sys
import time
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
from pathlib import Path

STANZA = "tokenkey"
CONTAINER = "tokenkey-postgres"
STATE = Path("/var/lib/tokenkey/postgres-backup")
INCREMENTAL_CALENDAR = "*-*-* 00/2:30:00 UTC"
RETENTION_DAYS = 7
ARCHIVE_TIMEOUT_SECONDS = 300
RPO_SECONDS = 900
BACKUP_INTERVAL_SECONDS = 7200
FULL_INTERVAL_SECONDS = 7 * 86400

# Repository settings are rendered without credentials.  The host identity
# refresher is responsible for adding short-lived keys at runtime.  Keeping
# the secondary repository as data (rather than a second hand-written config)
# makes the migration reviewable and leaves the single-repository default
# byte-for-byte compatible.
_HOST_RE = re.compile(r"[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?\Z")
_COS_REGION_RE = re.compile(r"[a-z]{2}(?:-[a-z]+)+\Z")


class BackupError(RuntimeError):
    pass


def _validate_endpoint(endpoint: str) -> str:
    if not isinstance(endpoint, str) or not _HOST_RE.fullmatch(endpoint):
        raise BackupError("invalid S3-compatible endpoint")
    if "://" in endpoint or "/" in endpoint or "\\" in endpoint:
        raise BackupError("S3 endpoint must be a host name without a scheme or path")
    return endpoint


def _validate_repo_path(repo_path: str) -> str:
    if not isinstance(repo_path, str) or not re.fullmatch(r"(?:/[a-z0-9][a-z0-9.-]*)+", repo_path):
        raise BackupError("invalid repository path")
    return repo_path


def _validate_secondary(secondary: dict) -> dict:
    required = {"bucket", "region", "endpoint", "path"}
    if not isinstance(secondary, dict) or set(secondary) - required - {"uri_style"} or not required <= set(secondary):
        raise BackupError("secondary repository requires bucket, region, endpoint and path")
    bucket = secondary["bucket"]
    if not isinstance(bucket, str) or not re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", bucket):
        raise BackupError("invalid secondary S3 bucket")
    region = secondary["region"]
    if not isinstance(region, str) or not _COS_REGION_RE.fullmatch(region):
        raise BackupError("invalid secondary S3-compatible region")
    endpoint = _validate_endpoint(secondary["endpoint"])
    path = _validate_repo_path(secondary["path"])
    # Tencent COS rejects path-style HEAD requests on the formal buckets used
    # by this migration. Keep host-style as the safe default; callers may still
    # opt into path-style for a separately validated S3-compatible endpoint.
    uri_style = secondary.get("uri_style", "host")
    if uri_style not in {"path", "host"}:
        raise BackupError("secondary uri_style must be path or host")
    return {"bucket": bucket, "region": region, "endpoint": endpoint, "path": path, "uri_style": uri_style}


def configuration(
    bucket: str,
    region: str,
    repo_path: str = "/prod/postgres",
    *,
    secondary: dict | None = None,
) -> str:
    if not re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", bucket):
        raise BackupError("invalid S3 bucket")
    if not re.fullmatch(r"[a-z]{2}(?:-[a-z]+)+-\d", region):
        raise BackupError("invalid AWS region")
    repo_path = _validate_repo_path(repo_path)
    rendered = f"""[global]
repo1-type=s3
repo1-path={repo_path}
repo1-s3-bucket={bucket}
repo1-s3-region={region}
repo1-s3-endpoint=s3.{region}.amazonaws.com
repo1-s3-key-type=auto
repo1-retention-full-type=time
repo1-retention-full={RETENTION_DAYS}
repo1-bundle=y
repo1-block=y
compress-type=zst
process-max=2
start-fast=y
archive-async=n
archive-timeout=120
log-level-console=info
log-level-file=off

[{STANZA}]
pg1-path=/var/lib/postgresql/data
pg1-socket-path=/var/run/postgresql
pg1-user=tokenkey
"""
    if secondary is not None:
        repo2 = _validate_secondary(secondary)
        repo2_options = "\n".join([
            "repo2-type=s3",
            f"repo2-path={repo2['path']}",
            f"repo2-s3-bucket={repo2['bucket']}",
            f"repo2-s3-region={repo2['region']}",
            f"repo2-s3-endpoint={repo2['endpoint']}",
            f"repo2-s3-uri-style={repo2['uri_style']}",
            "repo2-retention-full-type=time",
            f"repo2-retention-full={RETENTION_DAYS}",
            "repo2-bundle=y",
            "repo2-block=y",
        ])
        # repo2 options must remain in [global], before the stanza section.
        rendered = rendered.replace("\n\n[" + STANZA + "]", "\n" + repo2_options + "\n\n[" + STANZA + "]", 1)
    return rendered


def info_entry(raw: str) -> dict:
    values = json.loads(raw)
    matches = [value for value in values if value.get("name") == STANZA]
    if len(matches) != 1:
        raise BackupError("backup repository does not contain the expected stanza")
    entry = matches[0]
    if entry.get("status", {}).get("code") != 0:
        raise BackupError("pgBackRest reports an unhealthy or empty repository")
    return entry


def backup_kind(backups: list[dict], now: float) -> str:
    full = [
        backup["timestamp"]["stop"] for backup in backups if backup["type"] == "full"
    ]
    # Select from completed repository backups, not weekday or a local stamp:
    # a missed Sunday cannot silently postpone the full backup for another week.
    current = datetime.fromtimestamp(now, timezone.utc)  # noqa: UP017 — production Python 3.9
    boundary = current.replace(hour=0, minute=30, second=0, microsecond=0)
    boundary -= timedelta(days=(current.weekday() + 1) % 7)
    if boundary > current:
        boundary -= timedelta(days=7)
    return "full" if not full or max(full) < boundary.timestamp() else "incr"


def run(command: list[str], *, timeout: int = 120) -> str:
    result = subprocess.run(
        command, text=True, capture_output=True, check=False, timeout=timeout
    )
    if result.returncode:
        raise BackupError(
            f"{command[0]} failed (exit {result.returncode}): {result.stderr[-3000:]}"
        )
    return result.stdout


def backrest(*args: str, timeout: int = 120) -> str:
    # The in-container deadline also stops the backup if its host-side client
    # disappears. Killing `docker exec` alone would leave pgBackRest running.
    return run(
        [
            "docker",
            "exec",
            "--user",
            "postgres",
            CONTAINER,
            "timeout",
            "-s",
            "TERM",
            str(timeout),
            "pgbackrest",
            f"--stanza={STANZA}",
            *args,
        ],
        timeout=timeout + 15,
    )


def atomic_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o750)
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    temporary.chmod(0o640)
    temporary.replace(path)


@contextmanager
def exclusive(state: Path, name: str = "operator"):
    state.mkdir(parents=True, exist_ok=True, mode=0o750)
    with (state / f"{name}.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise BackupError(
                "another backup or repository verification is running"
            ) from exc
        yield


def take_backup(state: Path, kind: str) -> dict:
    with exclusive(state):
        before = json.loads(backrest("--output=json", "info"))[0]
        if before.get("name") != STANZA:
            raise BackupError("unexpected stanza")
        if kind == "auto":
            kind = backup_kind(before.get("backup", []), time.time())
        previous = {item["label"] for item in before.get("backup", [])}
        started = time.time()
        backrest(f"--type={kind}", "backup", timeout=3600)
        after = info_entry(backrest("--output=json", "info"))
        fresh = [item for item in after["backup"] if item["label"] not in previous]
        if len(fresh) != 1 or fresh[0]["type"] != kind:
            raise BackupError("completed backup was not found in the remote repository")
        receipt = {
            "schema_version": 1,
            "status": "ok",
            "kind": kind,
            "label": fresh[0]["label"],
            "completed_at": time.time(),
            "duration_seconds": round(time.time() - started, 3),
            "archive": fresh[0].get("archive"),
            "backup": fresh[0],
        }
        atomic_json(state / "backup.json", receipt)
        return receipt


def validate_verification(output: str) -> None:
    # 2.58 emits its text report at INFO level. Exit zero alone is insufficient.
    if (
        not re.search(r"(?m)^status: ok$", output)
        or not re.search(r"backup: .*status: valid", output)
        or re.search(r"\b(?:WARN|ERROR):|status: (?!ok|valid)\w+", output)
        or re.search(r"(?:missing|checksum invalid|size invalid|other): [1-9]", output)
    ):
        raise BackupError("repository verification did not prove valid backup contents")


def wal_coverage(objects: dict, first: str, last: str, segment_bytes: int) -> dict:
    # pgBackRest verify checks existing objects, but does not detect every gap
    # between backups. Assert the continuous range separately, never min/max.
    if (
        not all(re.fullmatch(r"[0-9A-F]{24}", value or "") for value in (first, last))
        or first[:8] != last[:8]
        or segment_bytes != 16 * 1024**2
    ):
        raise BackupError(
            "WAL timeline/segment size changed; continuity needs a fresh reviewed baseline"
        )

    def number(value):
        return int(value[8:16], 16) * 256 + int(value[16:24], 16)

    start, stop = number(first), number(last)
    if stop < start or stop - start > 2_000_000:
        raise BackupError("invalid or unexpectedly large WAL range")
    present = {
        Path(name).name[:24]
        for name, item in objects.items()
        if item.get("type") == "file"
        and re.fullmatch(r"[0-9A-F]{24}-[0-9a-f]{40}(?:\.[a-z0-9]+)?", Path(name).name)
    }
    for value in range(start, stop + 1):
        wal = f"{first[:8]}{value // 256:08X}{value % 256:08X}"
        if wal not in present:
            raise BackupError(f"WAL continuity gap: {wal}")
    return {"first_wal": first, "last_wal": last, "segments": stop - start + 1}


def archive_health(state: Path) -> dict:
    # Independent from the backup lock: a long full backup must not suppress
    # the 15-minute archive check. pgBackRest check supports concurrent backup.
    with exclusive(state, "check"):
        started = time.time()
        backrest("check", timeout=120)
        sql = """SELECT json_build_object('wal',last_archived_wal,
          'segment_bytes',pg_size_bytes(current_setting('wal_segment_size')),
          'wal_bytes',(SELECT coalesce(sum(size),0) FROM pg_ls_waldir()))
          FROM pg_stat_archiver;"""
        archived = json.loads(
            run(
                [
                    "docker",
                    "exec",
                    "--user",
                    "postgres",
                    CONTAINER,
                    "psql",
                    "-U",
                    "tokenkey",
                    "-d",
                    "tokenkey",
                    "-XAtc",
                    sql,
                ]
            )
        )
        entry = info_entry(backrest("--output=json", "info"))
        oldest = min(
            (b for b in entry["backup"] if b["type"] == "full"),
            key=lambda b: b["timestamp"]["stop"],
        )
        archive = entry["archive"][-1]["id"]
        objects = json.loads(
            backrest(
                "--recurse", "--output=json", "repo-ls", f"archive/{STANZA}/{archive}"
            )
        )
        coverage = wal_coverage(
            objects,
            oldest["archive"]["start"],
            archived["wal"],
            archived["segment_bytes"],
        )
        disk = shutil.disk_usage("/var/lib/tokenkey/postgres")
        previous = read_receipt(state / "check.json") or {}
        elapsed = started - previous.get("started_at", started)
        growth = max(
            0, archived["wal_bytes"] - previous.get("wal_bytes", archived["wal_bytes"])
        )
        rate = growth / elapsed if elapsed > 0 else 0
        hours = disk.free / rate / 3600 if rate else None
        if disk.free < max(10 * 1024**3, disk.total * 0.15) or (
            hours is not None and hours < 6
        ):
            raise BackupError(
                "WAL/disk pressure: less than 15%/10 GiB free or 6 hours estimated capacity"
            )
        result = {
            "status": "ok",
            "started_at": started,
            "completed_at": time.time(),
            "coverage": coverage,
            "recoverable_from": oldest["timestamp"]["stop"],
            "wal_bytes": archived["wal_bytes"],
            "free_bytes": disk.free,
            "estimated_hours_to_full": hours,
        }
        atomic_json(state / "check.json", result)
        health = status(
            entry,
            time.time(),
            result,
            read_receipt(state / "verify.json"),
            read_receipt(state / "restore.json"),
        )
        atomic_json(state / "health.json", dict(health, observed_at=time.time()))
        return result


def fresh(receipt: dict | None, key: str, now: float, maximum: int) -> bool:
    stamp = receipt.get(key) if isinstance(receipt, dict) else None
    return bool(
        receipt
        and receipt.get("status") == "ok"
        and isinstance(stamp, (int, float))
        and 0 <= now - stamp <= maximum
    )


def status(
    entry: dict,
    now: float,
    archive_check: dict | None,
    verification: dict | None,
    restored: dict | None,
) -> dict:
    backups = entry.get("backup", [])
    reasons = []
    latest = max((item["timestamp"]["stop"] for item in backups), default=0)
    if not latest or not 0 <= now - latest <= BACKUP_INTERVAL_SECONDS + 900:
        reasons.append("backup_missing_or_stale")
    if not fresh(archive_check, "started_at", now, RPO_SECONDS):
        reasons.append("archive_round_trip_missing_or_stale")
    if not fresh(verification, "completed_at", now, 8 * 86400):
        reasons.append("repository_verification_missing_or_stale")
    if not fresh(restored, "completed_at", now, 8 * 86400):
        reasons.append("restore_acceptance_missing_or_stale")
    return {
        "status": "degraded" if reasons else "ok",
        "reasons": reasons,
        "latest_backup_at": latest or None,
        "backup_count": len(backups),
        "archive_check": archive_check,
        "repository_verification": verification,
        "restore_acceptance": restored,
        "continuous_recovery_window": (archive_check or {}).get("coverage"),
    }


def read_receipt(path: Path) -> dict | None:
    try:
        value = json.loads(path.read_text())
        return value if isinstance(value, dict) else None
    except (OSError, ValueError):
        return None


def accept_restore(state: Path, receipt: dict, entry: dict) -> dict:
    now = time.time()
    activation = read_receipt(state / "enabled.json") or {}
    if (
        not fresh(receipt, "completed_at", now, 8 * 86400)
        or not receipt.get("cleanup_verified")
        or not receipt.get("instance_terminated")
        or not receipt.get("volumes_deleted")
        or not receipt.get("repository_verified")
        or not receipt.get("within_30_minutes")
        or receipt.get("business_acceptance") != "passed"
        or receipt.get("system_id") != entry["db"][-1]["system-id"]
        or receipt.get("image") != activation.get("after_image")
        or receipt["completed_at"] < activation.get("started_at", now)
    ):
        raise BackupError(
            "restore acceptance is incomplete, stale or belongs to another database/image"
        )
    for result in receipt.get("results", []):
        if result.get("billing") != {
            "status": "ok",
            "charge_retry": True,
            "archive_retry": True,
            "refund_retry": True,
        }:
            raise BackupError("restore did not prove actual billing retries")
    latest = receipt.get("target") == "latest-archived"
    if len(receipt.get("results", [])) != (1 if latest else 2):
        raise BackupError("restore path coverage incomplete")
    atomic_json(
        state / ("restore-latest.json" if latest else "restore-historical.json"),
        receipt,
    )
    previous = read_receipt(state / "restore.json") or {}
    if (
        previous.get("system_id") == receipt["system_id"]
        and previous.get("image") == receipt["image"]
        and previous.get("completed_at", 0) >= activation.get("started_at", now)
    ):
        accepted = dict(previous, completed_at=receipt["completed_at"])
        atomic_json(state / "restore.json", accepted)
        atomic_json(state / "verify.json", accepted)
        return accepted
    evidence = [
        read_receipt(state / name)
        for name in ("restore-latest.json", "restore-historical.json")
    ]
    if not all(
        fresh(item, "completed_at", now, 8 * 86400)
        and item["completed_at"] >= activation.get("started_at", now)
        for item in evidence
    ):
        return {
            "status": "pending",
            "reason": "latest and historical restore evidence both required",
        }
    if any(
        item.get("system_id") != receipt["system_id"]
        or item.get("image") != receipt["image"]
        for item in evidence
    ):
        raise BackupError("acceptance pair database/image mismatch")
    accepted = {
        "status": "ok",
        "completed_at": min(item["completed_at"] for item in evidence),
        "system_id": receipt["system_id"],
        "image": receipt["image"],
        "paths": [item["target"] for item in evidence],
    }
    atomic_json(state / "restore.json", accepted)
    atomic_json(state / "verify.json", accepted)
    return accepted


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", type=Path, default=STATE)
    commands = parser.add_subparsers(dest="command", required=True)
    render = commands.add_parser(
        "config", help="render configuration; does not install it"
    )
    render.add_argument("--bucket", required=True)
    render.add_argument("--region", required=True)
    render.add_argument("--repo-path", default="/prod/postgres")
    render.add_argument("--secondary-bucket")
    render.add_argument("--secondary-region")
    render.add_argument("--secondary-endpoint")
    render.add_argument("--secondary-path", default="/prod/postgres")
    render.add_argument("--secondary-uri-style", choices=("path", "host"), default="host")
    backup = commands.add_parser("backup")
    backup.add_argument("--type", choices=["auto", "full", "incr"], default="auto")
    commands.add_parser("check", help="verify a WAL archive round trip (switches WAL)")
    commands.add_parser("verify", help="verify remote backup and archive contents")
    commands.add_parser("status")
    accept = commands.add_parser(
        "accept",
        help="import completed isolated restore evidence; never changes pgdump",
    )
    accept.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "config":
            secondary_args = (
                args.secondary_bucket,
                args.secondary_region,
                args.secondary_endpoint,
            )
            if any(value is not None for value in secondary_args) and not all(
                value is not None for value in secondary_args
            ):
                raise BackupError(
                    "secondary-bucket, secondary-region and secondary-endpoint are all required"
                )
            secondary = (
                {
                    "bucket": args.secondary_bucket,
                    "region": args.secondary_region,
                    "endpoint": args.secondary_endpoint,
                    "path": args.secondary_path,
                    "uri_style": args.secondary_uri_style,
                }
                if all(value is not None for value in secondary_args)
                else None
            )
            print(
                configuration(
                    args.bucket,
                    args.region,
                    args.repo_path,
                    secondary=secondary,
                ),
                end="",
            )
            return 0
        if args.command == "backup":
            # Existing pgdump/QA owner: exclusive here, shared in pgdump.
            with exclusive(Path("/var/lib/tokenkey/qa-lifecycle"), "host"):
                result = take_backup(args.state, args.type)
        elif args.command == "accept":
            with exclusive(args.state):
                result = accept_restore(
                    args.state,
                    json.loads(args.receipt.read_text()),
                    info_entry(backrest("--output=json", "info")),
                )
        elif args.command == "check":
            result = archive_health(args.state)
        elif args.command == "verify":
            with exclusive(args.state):
                started = time.time()
                output = backrest("--output=text", "--verbose", "verify", timeout=3600)
                validate_verification(output)
                result = {
                    "status": "ok",
                    "started_at": started,
                    "completed_at": time.time(),
                }
                atomic_json(args.state / "verify.json", result)
        else:
            result = status(
                info_entry(backrest("--output=json", "info")),
                time.time(),
                read_receipt(args.state / "check.json"),
                read_receipt(args.state / "verify.json"),
                read_receipt(args.state / "restore.json"),
            )
        print(json.dumps(result, indent=2))
        return 0 if result.get("status") in ("ok", "pending") else 1
    except (
        BackupError,
        ValueError,
        KeyError,
        IndexError,
        TypeError,
        OSError,
        subprocess.TimeoutExpired,
    ) as exc:
        print(f"postgres-backup: {exc}", file=sys.stderr)
        if args.command == "check":
            atomic_json(
                args.state / "health.json",
                {"status": "failed", "observed_at": time.time(), "reasons": [str(exc)]},
            )
        if args.command in ("check", "verify"):
            prior = read_receipt(args.state / f"{args.command}.json") or {}
            atomic_json(
                args.state / f"{args.command}.json",
                dict(prior, status="failed", completed_at=time.time()),
            )
        return 1


if __name__ == "__main__":
    sys.exit(main())
