#!/usr/bin/env python3
"""Tencent USW prod: scheduled pgBackRest backup/check on the COS repository.

Repo SSOT for the host-installed copy at
/usr/local/lib/tokenkey/tencent-pgbackrest/tencent_pgbackrest.py. The host copy
is NOT updated by merging; installing it is a separate approved host step.
Run by tokenkey-postgres-backup.service / tokenkey-postgres-archive-check.service.
Contains no credentials; the COS config is rendered by tokenkey-backup-identity.timer
(ops/stage0/tencent_backup_identity.py).

Since 2026-11-08 COS (repo2) is the only repository; AWS repo1
(candidates/tencent-usw-20261022/postgres) is no longer written.
"""
from __future__ import annotations
import fcntl, json, re, subprocess, sys, time
from datetime import datetime, timedelta, timezone
from pathlib import Path

CONTAINER = "tk-archive-standby"
STANZA = "tokenkey"
STATE = Path("/var/lib/tokenkey/postgres-backup")
COS_REPO = "prod/postgres"
LIVE_REPO = COS_REPO

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

# COS is the only repository since 2026-11-08 (user decision: stop writing
# AWS repo1) and keeps index 2, so `--repo=2` means COS everywhere: here, in
# ops/backup/cos_restore_acceptance.py and in every runbook. The primary chain
# is whatever PRIMARY_REPO names, never "the lowest index".
PRIMARY_REPO = 2

def configured_repositories() -> tuple:
    """Repository indices present in the mounted runtime config."""
    out = run([
        "docker", "exec", "--user", "postgres", CONTAINER,
        "grep", "-E", "^repo[0-9]+-type=", "/etc/pgbackrest/pgbackrest.conf",
    ])
    return tuple(sorted({int(m) for m in re.findall(r"^repo(\d+)-type=", out, re.M)}))

def repository_numbers() -> tuple:
    repos = configured_repositories()
    if PRIMARY_REPO not in repos:
        # Never fall back to backing up whatever else is configured: a config
        # without COS is a fault, not a reason to write AWS again.
        raise Err(f"primary COS repository repo{PRIMARY_REPO} is not configured")
    return repos

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
#   - never for PRIMARY_REPO (COS repo2, the only recovery chain since
#     2026-11-08); "no valid backups" there is a disaster, never a bootstrap.
#   - only when that repository's backup list is EMPTY.  Code 2 with backups
#     present means a repository that once had them and now reports invalid,
#     which stays a failure.
BOOTSTRAP_REASON = "repository_bootstrapping"

def repository_state(repo: int, info: dict) -> str:
    code = (info.get("status") or {}).get("code")
    if code == 0:
        return "ok"
    if repo != PRIMARY_REPO and code == 2 and not (info.get("backup") or []):
        return "bootstrapping"
    return "failed"

CHECK_FAILED_REASON = "check_command_failed"
ARCHIVE_FAILING_REASON = "wal_archive_failing"
REPOSITORY_FAILED_REASON = "repository_failed"
PRIMARY_MISSING_REASON = "primary_repository_missing"

def archiver_healthy(arch: dict) -> bool:
    """Is WAL archiving working NOW, rather than has it ever failed?

    pg_stat_archiver's archived_count/failed_count are cumulative since
    stats_reset, so `failed_count == 0` asks "has this host ever failed to
    archive". cn-prod answered no forever after a 3-minute incident on
    2026-11-02 left failed_count at 15: every repository read code 0, archiving
    had long recovered, and the receipt still said degraded on every run.
    Counters never decrease, so that verdict could not clear without resetting
    Postgres statistics -- which would also throw away real evidence.

    The producer already knows which signal answers the real question: a
    last_archived_wal newer than last_failed_wal means the archive_command is
    succeeding right now. `archiving_now` is computed in SQL (NULL-safe for a
    host that has never failed); this falls back to the timestamps, then to the
    cumulative counter, so an older receipt shape is still judged, not crashed.
    """
    current = arch.get("archiving_now")
    if isinstance(current, bool):
        return current
    archived, failed_at = arch.get("last_archived_time"), arch.get("last_failed_time")
    if failed_at is None:
        return archived is not None
    if archived is None:
        return False
    # Parsed, never compared as strings: equal wall-clock text can carry
    # different UTC offsets, where lexical order inverts the real one.
    try:
        return _instant(archived) > _instant(failed_at)
    except ValueError:
        # Unparseable timestamps must not silently pass as healthy.
        return False

def _instant(value) -> datetime:
    text = str(value).strip().replace(" ", "T", 1)
    moment = datetime.fromisoformat(text)
    return moment if moment.tzinfo else moment.replace(tzinfo=timezone.utc)

def check_failure_detail(message: str, limit: int = 400) -> str:
    """Keep the ERROR/HINT lines that identify the fault; drop the rest.

    pgBackRest echoes its full command line (hundreds of characters, with
    credentials already <redacted>) before the real error, so the useful part
    sits at the end. Keeping the ERROR code plus HINTs is what makes a receipt
    actionable -- e.g. "[055] ... HINT: has a stanza-create been performed?"
    named the exact cause of the 2026-11-02 cn-prod archive stall.
    """
    lines = [line.strip() for line in message.splitlines() if line.strip()]
    keep = [line for line in lines if "ERROR" in line or "HINT" in line] or lines[-1:]
    return " | ".join(keep)[:limit]

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
        primary_fresh = fresh_by_repo[PRIMARY_REPO]
        receipt = {
            "schema_version": 1,
            "status": "ok",
            "kind": kinds[PRIMARY_REPO],
            "label": primary_fresh["label"],
            "completed_at": time.time(),
            "duration_seconds": round(time.time() - started, 3),
            "live_repo": LIVE_REPO,
            "cos_repo": COS_REPO,
            "cos_repo2": True,
            "aws_repo1": "configured" if 1 in repos else "retired",
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
        try:
            repos = repository_numbers()
        except Err as exc:
            # Same rule as a failing check: leave a diagnosable receipt rather
            # than none. Not in host_https_watch's evidence map -> hard fault.
            atomic_json(STATE / "archive_check.json", {
                "schema_version": 1,
                "status": "degraded",
                "started_at": started,
                "completed_at": time.time(),
                "reasons": [PRIMARY_MISSING_REASON],
                PRIMARY_MISSING_REASON: {"detail": str(exc)[:400]},
                "live_repo": LIVE_REPO,
                "cos_repo": COS_REPO,
                "host_role": "tencent-usw-prod",
            })
            raise
        # pgBackRest 2.58 does not accept --repo for the check command.  A
        # single check validates every configured repository and deliberately
        # forces one WAL archive round-trip through the shared archive_command.
        #
        # A raised Err here used to escape to main() and exit before any receipt
        # was written, which reads as "missing" to the freshness gate -- strictly
        # worse than "degraded", because a missing receipt is indistinguishable
        # from a dead timer. Record the failure, then fail.
        try:
            backrest("check", timeout=120)
        except Err as exc:
            atomic_json(STATE / "archive_check.json", {
                "schema_version": 1,
                "status": "degraded",
                "started_at": started,
                "completed_at": time.time(),
                "reasons": [CHECK_FAILED_REASON],
                CHECK_FAILED_REASON: {"detail": check_failure_detail(str(exc))},
                "live_repo": LIVE_REPO,
                "cos_repo": COS_REPO,
                "cos_repo2": True,
                "aws_repo1": "configured" if 1 in repos else "retired",
                "host_role": "tencent-usw-prod",
                "repositories": [{"repo": repo, "status": "unknown"} for repo in repos],
            })
            raise
        sql = (
            "SELECT json_build_object("
            "'last_archived_wal', last_archived_wal, "
            "'last_archived_time', last_archived_time, "
            "'last_failed_wal', last_failed_wal, "
            "'last_failed_time', last_failed_time, "
            "'archived_count', archived_count, "
            "'failed_count', failed_count, "
            "'stats_reset', stats_reset, "
            # archived_count/failed_count are CUMULATIVE since stats_reset, so
            # they answer "has this ever failed", not "is it failing now".
            # Postgres compares the two timestamps server-side, where a NULL
            # last_failed_time (never failed) is handled by the COALESCE.
            "'archiving_now', last_archived_time > "
            "COALESCE(last_failed_time, '-infinity'::timestamptz)"
            ") FROM pg_stat_archiver;"
        )
        arch = json.loads(run([
            "docker", "exec", "--user", "postgres", CONTAINER,
            "psql", "-U", "tokenkey", "-d", "tokenkey", "-XAtc", sql,
        ]))
        infos = {repo: repo_info(repo) for repo in repos}
        states = {repo: repository_state(repo, infos[repo]) for repo in repos}
        bootstrapping = [repo for repo in repos if states[repo] == "bootstrapping"]
        broken = [repo for repo in repos if states[repo] == "failed"]
        # WAL archiving is judged separately: bootstrap excuses an uninitialised
        # repository, never a failing archive_command.
        archiving_now = archiver_healthy(arch)
        ok = not broken and archiving_now
        completed = time.time()
        # Every non-ok receipt must name a reason. A bare "degraded" tells the
        # watcher something is wrong but not what, which is barely better than a
        # missing receipt -- and it is exactly what cn-prod reported after its
        # cumulative failed_count made an already-recovered host look broken.
        reasons = []
        if broken:
            reasons.append(REPOSITORY_FAILED_REASON)
        if not archiving_now:
            reasons.append(ARCHIVE_FAILING_REASON)
        if ok and bootstrapping:
            reasons.append(BOOTSTRAP_REASON)
        receipt = {
            "schema_version": 1,
            "status": "ok" if ok and not bootstrapping else "degraded",
            "started_at": started,
            "completed_at": completed,
            "archiver": arch,
            "stanza_status": {str(repo): infos[repo].get("status") for repo in repos},
            "live_repo": LIVE_REPO,
            "cos_repo": COS_REPO,
            "cos_repo2": True,
            "aws_repo1": "configured" if 1 in repos else "retired",
            "host_role": "tencent-usw-prod",
            "backup_count": len(infos[PRIMARY_REPO].get("backup") or []),
            "repositories": [
                {"repo": repo, "status": states[repo]} for repo in repos
            ],
        }
        if reasons:
            receipt["reasons"] = reasons
        if broken:
            receipt[REPOSITORY_FAILED_REASON] = {"repositories": broken}
        if not archiving_now:
            receipt[ARCHIVE_FAILING_REASON] = {
                "last_archived_wal": arch.get("last_archived_wal"),
                "last_failed_wal": arch.get("last_failed_wal"),
                "last_failed_time": arch.get("last_failed_time"),
            }
        if ok and bootstrapping:
            # host_https_watch.physical_backup_fact downgrades a degraded receipt
            # to a warning only when every reason has a matching evidence object
            # that is itself ok and completed; see its `evidence` mapping.
            receipt[BOOTSTRAP_REASON] = {
                "status": "ok",
                "completed_at": completed,
                "repositories": bootstrapping,
            }
        if receipt["status"] != "ok" and not receipt.get("reasons"):
            raise Err("refusing to write a degraded receipt without a reason")
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
