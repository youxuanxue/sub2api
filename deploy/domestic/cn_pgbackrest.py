#!/usr/bin/env python3
"""cn-prod (Tencent Beijing) scheduled pgBackRest backup/check with JSON receipts.

Repo SSOT for the host-installed copy at /opt/tokenkey/cn-pgbackrest.py.
Run by tokenkey-cn-postgres-backup.service and
tokenkey-cn-postgres-archive-check.service. Contains no credentials; the repo
identity is renewed by tokenkey-cn-backup-identity.timer.

Since 2026-11-08 Beijing COS (repo2) is the only repository; AWS Tokyo repo1
is no longer written.

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
import fcntl, json, re, subprocess, sys, time
from datetime import datetime, timedelta, timezone
from pathlib import Path

STANZA = "tokenkey"
STATE = Path("/var/lib/tokenkey/postgres-backup")
LIVE_REPO = "/cn-prod/postgres"
COS_REPO = "/cn-prod/postgres"
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

# COS is the only repository since 2026-11-08 (user decision: stop writing
# AWS repo1) and keeps index 2, so `--repo=2` means COS everywhere: here, in
# ops/backup/cos_restore_acceptance.py and in every runbook. The primary chain
# is whatever PRIMARY_REPO names, never "the lowest index".
PRIMARY_REPO = 2

def configured_repositories() -> tuple:
    """Repository indices present in the mounted runtime config."""
    out = run([
        "docker", "exec", "--user", "postgres", _container(),
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

def repo_info(repo: int) -> dict:
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
# a window that is structurally guaranteed and up to 2 hours wide here, because
# this host refreshes its identity every 15 minutes rather than every minute.
#
# The exemption is deliberately narrow:
#   - never for PRIMARY_REPO (COS repo2, the only recovery chain since
#     2026-11-08); "no valid backups" there is a disaster, never a bootstrap.
#   - only when that repository's backup list is EMPTY.  Code 2 with backups
#     present means a repository that once had them and now reports invalid,
#     which stays a failure.
#
# repository_state must stay behaviourally identical to the copy in
# deploy/tencent/tencent_pgbackrest.py: the two receipts share one consumer
# contract (see this file's module docstring). The logic is duplicated rather
# than imported because the host installs a single file with no repository tree
# beside it -- the same reason _container() inlines its fallback.
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
        repos = repository_numbers()
        before = {repo: repo_info(repo) for repo in repos}
        kinds = {repo: backup_kind(before[repo].get("backup") or [], time.time())
                 for repo in repos}
        previous = {repo: {b["label"] for b in (info.get("backup") or [])}
                    for repo, info in before.items()}
        started = time.time()
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
            "host_role": HOST_ROLE,
            "repositories": [
                {"repo": repo, "label": fresh_by_repo[repo]["label"],
                 "kind": kinds[repo], "status": "ok"}
                for repo in repos
            ],
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
                "host_role": HOST_ROLE,
            })
            raise
        # pgBackRest 2.58 check validates all configured repositories in one
        # invocation and triggers one shared WAL archive round-trip.
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
                "host_role": HOST_ROLE,
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
            "docker", "exec", "--user", "postgres", _container(),
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
            "host_role": HOST_ROLE,
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
