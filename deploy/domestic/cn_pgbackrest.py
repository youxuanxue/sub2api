#!/usr/bin/env python3
"""cn-prod (Tencent Beijing) scheduled pgBackRest backup/check with JSON receipts.

Repo SSOT for the host-installed copy at /opt/tokenkey/cn-pgbackrest.py.
Run by tokenkey-cn-postgres-backup.service and
tokenkey-cn-postgres-archive-check.service. Contains no credentials; the repo
identity is renewed by tokenkey-cn-backup-identity.timer.

Why this exists: the previous host runner (/opt/tokenkey/cn-backup-run.py) was an
orphan with no repo SSOT, and it only wrote backup.log -- never the JSON receipts
that daily diagnostics reads. pgBackRest was healthy on cn-prod while
`physical_backup` could not see it at all, so the only thing actually watching
cn-prod backups was the pgdump workflow this replaces.

Receipt shape and freshness deliberately match deploy/tencent/tencent_pgbackrest.py:
ops/observability/host_https_watch.py:physical_backup_fact requires a receipt
stamped within 900s, which is why `check` runs every 5 minutes while `backup`
runs every 2 hours. A receipt written only at backup time would always read stale.
"""
from __future__ import annotations
import fcntl, json, subprocess, sys, time
from datetime import datetime, timedelta, timezone
from pathlib import Path

STANZA = "tokenkey"
STATE = Path("/var/lib/tokenkey/postgres-backup")
LIVE_REPO = "/cn-prod/postgres"
HOST_ROLE = "tencent-beijing-cn-prod"

# Stage0 compose names the database tokenkey-postgres. cn-prod currently matches,
# but the container is resolved rather than hard-coded: the Tencent USW layout
# exception renamed exactly this container and published `postgres` as a network
# alias, and a literal exec target there dies with "No such container".
# ops/lib/resolve_app_container.py owns the rule for both hosts.
_CONTAINER: str | None = None

def _container() -> str:
    """Resolve the database container once per process, failing closed.

    Prefers the repo owner (ops/lib/resolve_app_container.py) so the candidate
    list has exactly one home. The host-installed copy runs without the repo
    tree beside it, so it falls back to the same single-running-candidate rule
    inline -- deliberately NOT a literal exec target: the Tencent USW layout
    exception renamed this very container, and a literal there dies with
    "No such container" while looking like a healthy probe.
    """
    global _CONTAINER
    if _CONTAINER:
        return _CONTAINER
    candidates = ("tokenkey-postgres", "tk-archive-standby")
    for base in Path(__file__).resolve().parents:
        owner = base / "ops" / "lib" / "resolve_app_container.py"
        if owner.is_file():
            import importlib.util
            spec = importlib.util.spec_from_file_location("tk_resolve_app_container", owner)
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            candidates = module.POSTGRES_CANDIDATES
            resolved = module.resolve_postgres()
            if not resolved:
                raise Err(f"postgres container unresolved (candidates: {', '.join(candidates)})")
            _CONTAINER = resolved
            return _CONTAINER
    names = subprocess.run(["docker", "ps", "--format", "{{.Names}}"],
                           text=True, capture_output=True, check=False).stdout.split()
    matched = [name for name in names if name in candidates]
    if len(matched) != 1:
        raise Err(f"postgres container unresolved (running candidates: {matched or 'none'})")
    _CONTAINER = matched[0]
    return _CONTAINER

class Err(RuntimeError):
    pass

def run(cmd, timeout=120):
    r = subprocess.run(cmd, text=True, capture_output=True, check=False, timeout=timeout)
    if r.returncode:
        raise Err(f"{cmd[0]} exit {r.returncode}: {(r.stderr or r.stdout)[-2000:]}")
    return r.stdout

def backrest(*args, timeout=120):
    return run([
        "docker", "exec", "--user", "postgres", _container(),
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
    """Weekly full, otherwise incremental -- same cadence as prod."""
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
            "host_role": HOST_ROLE,
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
            "docker", "exec", "--user", "postgres", _container(),
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
            "host_role": HOST_ROLE,
            "backup_count": len(info.get("backup") or []),
        }
        atomic_json(STATE / "archive_check.json", receipt)
        print(json.dumps(receipt))
        if not ok:
            raise SystemExit(2)
    finally:
        lock.close()

def main() -> int:
    command = sys.argv[1] if len(sys.argv) > 1 else ""
    if command == "backup":
        cmd_backup()
    elif command == "check":
        cmd_check()
    else:
        print("usage: cn_pgbackrest.py backup|check", file=sys.stderr)
        return 2
    return 0

if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Err as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1)
