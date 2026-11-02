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

def repo2_enabled() -> bool:
    """Detect the opt-in Beijing COS repository from the runtime config."""
    result = subprocess.run([
        "docker", "exec", "--user", "postgres", _container(),
        "sh", "-c", "grep -q '^repo2-type=' /etc/pgbackrest/pgbackrest.conf",
    ], capture_output=True, check=False)
    return result.returncode == 0

def repository_numbers() -> tuple[int, ...]:
    return (1, 2) if repo2_enabled() else (1,)

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
#   - only for repositories with index >= 2.  repo1 is the primary recovery
#     chain; "no valid backups" there is a disaster, never a bootstrap.
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
    if repo >= 2 and code == 2 and not (info.get("backup") or []):
        return "bootstrapping"
    return "failed"

CHECK_FAILED_REASON = "check_command_failed"

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
        repos = repository_numbers()
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
                "cos_repo": COS_REPO if 2 in repos else None,
                "cos_repo2": 2 in repos,
                "host_role": HOST_ROLE,
                "repositories": [{"repo": repo, "status": "unknown"} for repo in repos],
            })
            raise
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
            "host_role": HOST_ROLE,
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
