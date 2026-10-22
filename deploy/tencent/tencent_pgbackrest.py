#!/usr/bin/env python3
"""Tencent cutover primary: scheduled pgBackRest backup/check on candidate repo.

Repo record of the host-installed copy at
/usr/local/lib/tokenkey/tencent-pgbackrest/tencent_pgbackrest.py (fetched read-only
2026-10-22, host sha256 f30b556d7f1aef10...; this header is the only difference).
Run by tokenkey-postgres-backup.service / tokenkey-postgres-archive-check.service.
Contains no credentials; the repo identity is renewed by tokenkey-backup-identity.timer.
"""
from __future__ import annotations
import fcntl, json, subprocess, sys, time
from datetime import datetime, timedelta, timezone
from pathlib import Path

CONTAINER = "tk-archive-standby"
STANZA = "tokenkey"
STATE = Path("/var/lib/tokenkey/postgres-backup")
LIVE_REPO = "candidates/tencent-usw-20261022/postgres"

class Err(RuntimeError):
    pass

def run(cmd, timeout=120):
    r = subprocess.run(cmd, text=True, capture_output=True, check=False, timeout=timeout)
    if r.returncode:
        raise Err(f"{cmd[0]} exit {r.returncode}: {(r.stderr or r.stdout)[-2000:]}")
    return r.stdout

def backrest(*args, timeout=120):
    return run([
        "docker", "exec", "--user", "postgres", CONTAINER,
        "timeout", "-s", "TERM", str(timeout),
        "pgbackrest", f"--stanza={STANZA}", *args,
    ], timeout=timeout + 15)

def atomic_json(path: Path, value: dict):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o750)
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(value, indent=2) + "\n")
    tmp.chmod(0o640)
    tmp.replace(path)

def exclusive(name: str):
    STATE.mkdir(parents=True, exist_ok=True, mode=0o750)
    lock = (STATE / f"{name}.lock").open("a")
    try:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError as e:
        lock.close()
        raise Err("another backup/check is running") from e
    return lock

def backup_kind(backups, now):
    full = [b["timestamp"]["stop"] for b in backups if b["type"] == "full"]
    current = datetime.fromtimestamp(now, timezone.utc)
    boundary = current.replace(hour=0, minute=30, second=0, microsecond=0)
    boundary -= timedelta(days=(current.weekday() + 1) % 7)
    if boundary > current:
        boundary -= timedelta(days=7)
    return "full" if not full or max(full) < boundary.timestamp() else "incr"

def cmd_backup():
    lock = exclusive("operator")
    try:
        before = json.loads(backrest("--output=json", "info"))[0]
        kind = backup_kind(before.get("backup") or [], time.time())
        previous = {b["label"] for b in (before.get("backup") or [])}
        started = time.time()
        backrest(f"--type={kind}", "backup", timeout=3600)
        after = json.loads(backrest("--output=json", "info"))[0]
        fresh = [b for b in (after.get("backup") or []) if b["label"] not in previous]
        if len(fresh) != 1 or fresh[0]["type"] != kind:
            raise Err("completed backup missing from repo info")
        receipt = {
            "schema_version": 1,
            "status": "ok",
            "kind": kind,
            "label": fresh[0]["label"],
            "completed_at": time.time(),
            "duration_seconds": round(time.time() - started, 3),
            "live_repo": LIVE_REPO,
            "host_role": "tencent-usw-prod",
        }
        atomic_json(STATE / "backup.json", receipt)
        print(json.dumps(receipt))
    finally:
        lock.close()

def cmd_check():
    lock = exclusive("check")
    try:
        started = time.time()
        backrest("check", timeout=120)
        sql = (
            "SELECT json_build_object("
            "'last_archived_wal', last_archived_wal, "
            "'last_archived_time', last_archived_time, "
            "'failed_count', failed_count) FROM pg_stat_archiver;"
        )
        arch = json.loads(run([
            "docker", "exec", "--user", "postgres", CONTAINER,
            "psql", "-U", "tokenkey", "-d", "tokenkey", "-XAtc", sql,
        ]))
        info = json.loads(backrest("--output=json", "info"))[0]
        status_code = (info.get("status") or {}).get("code")
        failed = int(arch.get("failed_count") or 0)
        ok = status_code == 0 and failed == 0
        receipt = {
            "schema_version": 1,
            "status": "ok" if ok else "degraded",
            "started_at": started,
            "completed_at": time.time(),
            "archiver": arch,
            "stanza_status": info.get("status"),
            "live_repo": LIVE_REPO,
            "host_role": "tencent-usw-prod",
            "backup_count": len(info.get("backup") or []),
        }
        atomic_json(STATE / "archive_check.json", receipt)
        print(json.dumps(receipt))
        if not ok:
            raise SystemExit(2)
    finally:
        lock.close()

def main():
    if len(sys.argv) != 2 or sys.argv[1] not in ("backup", "check"):
        print("usage: tencent_pgbackrest.py backup|check", file=sys.stderr)
        raise SystemExit(2)
    try:
        {"backup": cmd_backup, "check": cmd_check}[sys.argv[1]]()
    except Err as e:
        print(f"tencent-pgbackrest: {e}", file=sys.stderr)
        raise SystemExit(1)

if __name__ == "__main__":
    main()
