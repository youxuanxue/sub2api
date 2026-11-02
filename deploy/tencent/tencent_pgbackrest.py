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
COS_REPO = "prod/postgres"

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

def repo2_enabled():
    """Detect the opt-in COS repository from the mounted runtime config."""
    result = subprocess.run([
        "docker", "exec", "--user", "postgres", CONTAINER,
        "sh", "-c", "grep -q '^repo2-type=' /etc/pgbackrest/pgbackrest.conf",
    ], capture_output=True, check=False)
    return result.returncode == 0

def repository_numbers():
    return (1, 2) if repo2_enabled() else (1,)

def repo_info(repo):
    values = json.loads(backrest(f"--repo={repo}", "--output=json", "info"))
    if len(values) != 1 or values[0].get("name") != STANZA:
        raise Err(f"unexpected repository {repo} info")
    return values[0]

# pgBackRest reports code 2 "no valid backups" for a repository that is
# configured but has never received a backup.  That is the NORMAL initial state
# of a newly added repository, not a fault: the stanza is created by the first
# `backup --repo=N` (never by stanza-create here), and backup runs every 2 hours
# while check runs every 5 minutes.  Treating code 2 as a failure made every
# check between "credentials land" and "first full completes" exit non-zero --
# a window that is structurally guaranteed and was up to 2 hours wide.
#
# The exemption is deliberately narrow:
#   - only for repositories with index >= 2.  repo1 is the primary recovery
#     chain; "no valid backups" there is a disaster, never a bootstrap.
#   - only when that repository's backup list is EMPTY.  Code 2 with backups
#     present means a repository that once had them and now reports invalid,
#     which stays a failure.
BOOTSTRAP_REASON = "repository_bootstrapping"

def repository_state(repo: int, info: dict) -> str:
    code = (info.get("status") or {}).get("code")
    if code == 0:
        return "ok"
    if repo >= 2 and code == 2 and not (info.get("backup") or []):
        return "bootstrapping"
    return "failed"

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
        repos = repository_numbers()
        before = {repo: repo_info(repo) for repo in repos}
        now = time.time()
        kinds = {
            repo: backup_kind(before[repo].get("backup") or [], now)
            for repo in repos
        }
        previous = {
            repo: {b["label"] for b in (info.get("backup") or [])}
            for repo, info in before.items()
        }
        started = time.time()
        # pgBackRest 2.58 (the production image) does not accept repo=all.
        # Run each repository explicitly; archive-push still fans out to all
        # configured repositories from the shared archive_command.
        for repo in repos:
            backrest(f"--repo={repo}", f"--type={kinds[repo]}", "backup", timeout=3600)
        after = {repo: repo_info(repo) for repo in repos}
        fresh_by_repo = {}
        for repo in repos:
            fresh = [b for b in (after[repo].get("backup") or [])
                     if b["label"] not in previous[repo]]
            if len(fresh) != 1 or fresh[0]["type"] != kinds[repo]:
                raise Err(f"completed backup missing from repository {repo} info")
            fresh_by_repo[repo] = fresh[0]
        primary_fresh = fresh_by_repo[1]
        receipt = {
            "schema_version": 1,
            "status": "ok",
            "kind": kinds[1],
            "label": primary_fresh["label"],
            "completed_at": time.time(),
            "duration_seconds": round(time.time() - started, 3),
            "live_repo": LIVE_REPO,
            "cos_repo": COS_REPO if 2 in repos else None,
            "cos_repo2": 2 in repos,
            "repositories": [
                {"repo": repo, "label": fresh_by_repo[repo]["label"],
                 "kind": kinds[repo], "status": "ok"}
                for repo in repos
            ],
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
        repos = repository_numbers()
        # pgBackRest 2.58 does not accept --repo for the check command.  A
        # single check validates every configured repository and deliberately
        # forces one WAL archive round-trip through the shared archive_command.
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
        infos = {repo: repo_info(repo) for repo in repos}
        states = {repo: repository_state(repo, infos[repo]) for repo in repos}
        failed = int(arch.get("failed_count") or 0)
        bootstrapping = [repo for repo in repos if states[repo] == "bootstrapping"]
        # WAL archiving is judged separately: bootstrap excuses an uninitialised
        # repository, never a failing archive_command.
        ok = all(states[repo] != "failed" for repo in repos) and failed == 0
        completed = time.time()
        receipt = {
            "schema_version": 1,
            "status": "ok" if ok and not bootstrapping else "degraded",
            "started_at": started,
            "completed_at": completed,
            "archiver": arch,
            "stanza_status": {str(repo): infos[repo].get("status") for repo in repos},
            "live_repo": LIVE_REPO,
            "cos_repo": COS_REPO if 2 in repos else None,
            "cos_repo2": 2 in repos,
            "host_role": "tencent-usw-prod",
            "backup_count": len(infos[1].get("backup") or []),
            "repositories": [
                {"repo": repo, "status": states[repo]} for repo in repos
            ],
        }
        if ok and bootstrapping:
            # host_https_watch.physical_backup_fact downgrades a degraded receipt
            # to a warning only when every reason has a matching evidence object
            # that is itself ok and completed; see its `evidence` mapping.
            receipt["reasons"] = [BOOTSTRAP_REASON]
            receipt[BOOTSTRAP_REASON] = {
                "status": "ok",
                "completed_at": completed,
                "repositories": bootstrapping,
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
