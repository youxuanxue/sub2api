#!/usr/bin/env python3
"""Prove a COS repository can actually be restored, on a host serving traffic.

Approved design stage 3 (docs/approved/design-tencent-cos-backup-node1-qa.md)
requires restoring the latest point and a historical point from COS, checking
WAL continuity, key relations, constraints, sequences and billing aggregates,
and recording how long it took. Until that runs, a COS repository that accepts
writes is unproven: dual writes only show that objects go in, never that a
database comes back out.

The two receipts this writes are the ones host_https_watch already looks for --
repository_verification.json and restore_acceptance.json -- so a successful run
clears `repository_verification_missing_or_stale` and
`restore_acceptance_missing_or_stale` for eight days
(ops/stage0/postgres_backup.py:status).

Isolation, because prod and cn-prod both serve live users from the same host:

  * every restore runs in a throwaway container; the live database container is
    only ever read from (pgbackrest info, one comparison query)
  * no published ports, and the restored instance listens on a unix socket only,
    so nothing can connect to it from outside
  * the restore container stays on Docker's default bridge only and never joins
    the tokenkey network -- it cannot reach the gateway or the live database even
    by name. Verified on cn-prod: `pg_isready -h tokenkey-postgres` from such a
    container returns "no response". Do NOT harden this to `--network none`:
    Postgres runs archive-get against COS for the backup-end WAL at startup, and
    without egress it fails with "could not locate required checkpoint record"
    (seen on prod, 2026-11-08).
  * cpu/memory caps plus nice/ionice keep it off the live workload's back
  * the pgbackrest config is mounted read-only and `--delta` is never used, so
    no production directory is writable from inside
  * a fresh temp directory per run, removed afterwards even on failure

Two details are load-bearing and were each found the hard way on cn-prod:

  * `--target-action=promote`, not `shutdown`. With shutdown the cluster never
    completes recovery, so `pg_controldata` still reads "in production" and the
    restored copy cannot be queried at all.
  * `max_connections` must be at least the primary's value or PostgreSQL
    refuses to recover: "recovery aborted because of insufficient parameter
    settings". The primary's setting is read from the live database rather than
    assumed.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time
import uuid
from pathlib import Path

STANZA = "tokenkey"
STATE = Path("/var/lib/tokenkey/postgres-backup")
CONTAINER_CANDIDATES = ("tokenkey-postgres", "tk-archive-standby")
# Verified against the live schema rather than guessed: `usage_logs` carries the
# cost columns and `usage_billing_dedup` mirrors it row for row, so their row
# delta is a strong signal that a restored copy is internally consistent.
# accounts and api_keys are soft-delete tables: `deleted_at IS NULL` is what Ent
# adds automatically and hand-written SQL must add itself. It matters here
# beyond tidiness -- a row soft-deleted AFTER the recovery point still reads as
# live in the restored copy, so counting ghosts would make the "restored must
# not exceed live" comparison drift for a reason that has nothing to do with
# the backup. cn-prod carries 3 such accounts and 2 api_keys today.
DATA_SQL = """
SELECT json_build_object(
  'accounts',(SELECT count(*) FROM accounts WHERE deleted_at IS NULL),
  'api_keys',(SELECT count(*) FROM api_keys WHERE deleted_at IS NULL),
  'usage_logs',(SELECT count(*) FROM usage_logs),
  'billing_dedup',(SELECT count(*) FROM usage_billing_dedup),
  'cost_sum',(SELECT round(COALESCE(sum(total_cost),0),6)::text FROM usage_logs),
  'actual_cost_sum',(SELECT round(COALESCE(sum(actual_cost),0),6)::text FROM usage_logs),
  'db_size_bytes',pg_database_size('tokenkey')
)"""
SCHEMA_SQL = """
SELECT json_build_object(
  'tables',(SELECT count(*) FROM information_schema.tables WHERE table_schema='public'),
  'fkeys',(SELECT count(*) FROM pg_constraint WHERE contype='f'),
  'pkeys',(SELECT count(*) FROM pg_constraint WHERE contype='p'),
  'sequences',(SELECT count(*) FROM information_schema.sequences WHERE sequence_schema='public')
)"""


# ops-sql-coverage: every *_SQL / *_sql symbol must be enumerated or exempted,
# so the execution gate runs it against a real Postgres rather than trusting a
# substring assertion.
SELF_CHECK_EXEMPT: dict[str, str] = {
    "live_sql": "runner: executes a caller-supplied statement in a container, builds no SQL",
}


def iter_self_check_sql() -> list[tuple[str, str]]:
    """(label, rendered_sql) for the ops-sql-coverage real-Postgres self-check."""
    return [("SCHEMA_SQL", SCHEMA_SQL), ("DATA_SQL", DATA_SQL)]


class AcceptanceError(RuntimeError):
    pass


def run(cmd: list[str], timeout: int = 120, check: bool = True) -> str:
    result = subprocess.run(cmd, text=True, capture_output=True, check=False, timeout=timeout)
    if check and result.returncode:
        raise AcceptanceError(f"{cmd[0]} exit {result.returncode}: {(result.stderr or result.stdout)[-1500:]}")
    return result.stdout


def live_container() -> str:
    """Resolve the single running database container, failing closed."""
    running = [name for name in CONTAINER_CANDIDATES
               if run(["docker", "inspect", "-f", "{{.State.Running}}", name],
                      check=False).strip() == "true"]
    if len(running) != 1:
        raise AcceptanceError(f"database container unresolved (candidates: {', '.join(CONTAINER_CANDIDATES)})")
    return running[0]


def live_sql(container: str, sql: str) -> dict:
    out = run(["docker", "exec", "--user", "postgres", container,
               "psql", "-U", "tokenkey", "-d", "tokenkey", "-XAt", "-c", sql])
    return json.loads(out)


def live_setting(container: str, name: str) -> str:
    return run(["docker", "exec", "--user", "postgres", container, "psql", "-U", "tokenkey",
                "-d", "tokenkey", "-XAtc", f"SELECT current_setting('{name}')"]).strip()


def repo_info(container: str, repo: int) -> dict:
    values = json.loads(run(["docker", "exec", "--user", "postgres", container, "pgbackrest",
                             f"--stanza={STANZA}", f"--repo={repo}", "--output=json", "info"],
                            timeout=180))
    if len(values) != 1 or values[0].get("name") != STANZA:
        raise AcceptanceError(f"unexpected repo{repo} info")
    return values[0]


def targets(entry: dict) -> tuple[str, str]:
    """A historical point (oldest full) and the latest point."""
    backups = entry.get("backup") or []
    fulls = [b for b in backups if b["type"] == "full"]
    if not fulls or not backups:
        raise AcceptanceError("repository has no full backup to restore from")
    return fulls[0]["label"], backups[-1]["label"]


def wal_continuity(entry: dict) -> dict:
    archive = (entry.get("archive") or [{}])[0]
    low, high = archive.get("min") or "", archive.get("max") or ""
    if not re.fullmatch(r"[0-9A-F]{24}", low) or not re.fullmatch(r"[0-9A-F]{24}", high):
        raise AcceptanceError("WAL range is not a pair of segment names")
    if low[:8] != high[:8]:
        raise AcceptanceError(f"WAL spans timelines {low[:8]} -> {high[:8]}; needs a reviewed baseline")
    return {"min": low, "max": high, "timeline": low[:8]}


def validate_verification(output: str) -> None:
    """Exit zero alone is not proof; pgBackRest reports findings in its text.

    Same judgment as ops/stage0/postgres_backup.py:validate_verification, which
    was written against pgBackRest 2.58's habit of reporting at INFO level: a
    repository with invalid checksums can still exit zero, so the per-backup
    "status: valid" lines and the zeroed finding counters are what matter.
    """
    if (not re.search(r"backup: .*status: valid", output)
            or re.search(r"\b(?:WARN|ERROR):", output)
            or re.search(r"status: (?!ok|valid)\w+", output)
            or re.search(r"(?:missing|checksum invalid|size invalid|other): [1-9]", output)):
        raise AcceptanceError("repository verification did not prove valid contents")


def verify_repository(container: str, repo: int, timeout: int) -> dict:
    """Recompute checksums and sizes for every object. Writes no pgdata."""
    output = run(["sudo", "nice", "-n", "19", "ionice", "-c3",
                  "docker", "exec", "--user", "postgres", container,
                  "pgbackrest", f"--stanza={STANZA}", f"--repo={repo}",
                  "--output=text", "--verbose", "verify"], timeout=timeout)
    validate_verification(output)
    # --verbose prints each backup twice (per-backup check, then the summary),
    # so count distinct labels: a plain match count reported 38 for 19 backups.
    valid = set(re.findall(r"backup: (\S+?), status: valid", output))
    return {"backups_valid": len(valid), "findings": "none"}


class Sandbox:
    """A throwaway restore target that never touches production paths."""

    def __init__(self, image: str, config: Path, root: Path, cpus: str, memory: str):
        self.image = image
        self.config = config
        self.work = root / f"cos-acceptance-{uuid.uuid4().hex[:12]}"
        self.pgdata = self.work / "pgdata"
        self.name = f"cos-acc-{uuid.uuid4().hex[:8]}"
        self.cpus, self.memory = cpus, memory

    def __enter__(self):
        self._fresh_pgdata()
        return self

    def __exit__(self, *_):
        run(["docker", "rm", "-f", self.name], check=False)
        if self.work.exists():
            run(["sudo", "rm", "-rf", str(self.work)], check=False)

    def _fresh_pgdata(self) -> None:
        if self.pgdata.exists():
            run(["sudo", "rm", "-rf", str(self.pgdata)], check=False)
        run(["sudo", "install", "-d", "-m", "700", "-o", "70", "-g", "70", str(self.pgdata)])

    def _limits(self) -> list[str]:
        return ["--cpus", self.cpus, "--memory", self.memory,
                "--memory-swap", self.memory, "--user", "70:70"]

    def restore(self, repo: int, label: str, timeout: int) -> None:
        self._fresh_pgdata()
        run(["docker", "rm", "-f", self.name], check=False)
        # Downloading (and archive-get at startup) needs COS egress, so the
        # container stays on the default bridge; it never joins the tokenkey
        # network, so the gateway and the live database stay unreachable.
        run(["sudo", "nice", "-n", "19", "ionice", "-c3", "docker", "run", "--rm",
             "--name", self.name, *self._limits(),
             "-v", f"{self.config}:/etc/pgbackrest:ro",
             "-v", f"{self.pgdata}:/var/lib/postgresql/data",
             "--entrypoint", "pgbackrest", self.image,
             f"--stanza={STANZA}", f"--repo={repo}",
             "--pg1-path=/var/lib/postgresql/data", f"--set={label}",
             "--type=immediate", "--target-action=promote",
             "--log-level-console=error", "restore"], timeout=timeout)

    def start(self, max_connections: int, ready_timeout: int) -> float:
        started = time.time()
        run(["sudo", "nice", "-n", "19", "ionice", "-c3", "docker", "run", "-d",
             "--name", self.name, *self._limits(),
             "-v", f"{self.config}:/etc/pgbackrest:ro",
             "-v", f"{self.pgdata}:/var/lib/postgresql/data",
             "--entrypoint", "postgres", self.image,
             "-D", "/var/lib/postgresql/data",
             "-c", "listen_addresses=",
             "-c", f"max_connections={max_connections}",
             "-c", "shared_buffers=128MB"])
        deadline = started + ready_timeout
        while time.time() < deadline:
            if run(["docker", "inspect", "-f", "{{.State.Running}}", self.name],
                   check=False).strip() != "true":
                break
            if subprocess.run(["docker", "exec", "--user", "postgres", self.name,
                               "pg_isready", "-q"], capture_output=True,
                              check=False).returncode == 0:
                return time.time() - started
            time.sleep(2)
        log = run(["docker", "logs", self.name], check=False)[-1500:]
        raise AcceptanceError(f"restored instance never became ready: {log}")

    def sql(self, statement: str) -> dict:
        return json.loads(run(["docker", "exec", "--user", "postgres", self.name,
                               "psql", "-U", "tokenkey", "-d", "tokenkey", "-XAt",
                               "-c", statement]))

    def stop(self) -> None:
        run(["docker", "rm", "-f", self.name], check=False)


def compare(restored: dict, live: dict) -> list[str]:
    """A restored point is a PAST state: it may lag live, never exceed it."""
    problems = []
    for key in ("accounts", "api_keys", "usage_logs", "billing_dedup"):
        got, expected = restored.get(key), live.get(key)
        if isinstance(got, int) and isinstance(expected, int) and got > expected:
            problems.append(f"{key} restored {got} > live {expected}")
    for key in ("cost_sum", "actual_cost_sum"):
        try:
            if float(restored[key]) > float(live[key]) * 1.000001:
                problems.append(f"{key} restored {restored[key]} > live {live[key]}")
        except (KeyError, TypeError, ValueError):
            problems.append(f"{key} missing or unparseable")
    # usage_logs and usage_billing_dedup are written together; the same delta on
    # the restored copy as on live means the pair survived the round trip rather
    # than being captured mid-write.
    try:
        restored_drift = abs(restored["usage_logs"] - restored["billing_dedup"])
        live_drift = abs(live["usage_logs"] - live["billing_dedup"])
        if restored_drift != live_drift:
            problems.append(f"billing pair drift {restored_drift} != live {live_drift}")
    except (KeyError, TypeError):
        problems.append("billing pair drift not computable")
    return problems


def atomic_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o750)
    temporary = path.with_suffix(f".{uuid.uuid4().hex[:8]}.tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    os.chmod(temporary, 0o640)
    temporary.replace(path)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=int, required=True, help="repository to accept (2 = COS)")
    parser.add_argument("--image", default="tokenkey-postgres:18-pgbackrest")
    parser.add_argument("--config", type=Path, required=True,
                        help="host path of the pgbackrest config directory (mounted read-only)")
    parser.add_argument("--work-root", type=Path, default=Path("/var/lib/tokenkey"))
    parser.add_argument("--cpus", default="1")
    parser.add_argument("--memory", default="1g")
    parser.add_argument("--restore-timeout", type=int, default=3600)
    parser.add_argument("--ready-timeout", type=int, default=600)
    parser.add_argument("--host-role", default="")
    parser.add_argument("--apply", action="store_true",
                        help="without this, resolve and report only; restore nothing")
    parser.add_argument("--verify-only", action="store_true",
                        help="verify repository contents without restoring. Writes "
                             "repository_verification.json ONLY -- restore acceptance "
                             "stays unproven, and host_https_watch keeps reporting "
                             "restore_acceptance_missing_or_stale. Use where a restore "
                             "cannot fit, never as a substitute for one.")
    parser.add_argument("--verify-timeout", type=int, default=3000)
    args = parser.parse_args()

    container = live_container()
    entry = repo_info(container, args.repo)
    historical, latest = targets(entry)
    wal = wal_continuity(entry)
    live_schema = live_sql(container, SCHEMA_SQL)
    live_data = live_sql(container, DATA_SQL)
    primary_connections = int(live_setting(container, "max_connections"))

    plan = {
        "repo": args.repo, "historical": historical, "latest": latest,
        "wal": wal, "live_schema": live_schema, "primary_max_connections": primary_connections,
    }
    if not args.apply:
        print(json.dumps({"status": "planned", "verify_only": args.verify_only, **plan},
                         indent=2, sort_keys=True))
        return 0

    if args.verify_only:
        # Deliberately writes one receipt, not two. restore_acceptance.json must
        # keep reflecting whether a database actually came back out; claiming it
        # from a checksum pass would hide exactly the risk it exists to surface.
        started = time.time()
        verified = verify_repository(container, args.repo, args.verify_timeout)
        completed = time.time()
        receipt = {
            "schema_version": 1, "status": "ok", "repo": args.repo, "stanza": STANZA,
            "host_role": args.host_role or None,
            "started_at": started, "completed_at": completed,
            "duration_seconds": round(completed - started, 3),
            "method": "verify_only",
            "restore_acceptance": "not attempted: see restore_acceptance.json",
            "wal": wal, "backup_count": len(entry.get("backup") or []),
            "full_count": sum(1 for b in (entry.get("backup") or []) if b["type"] == "full"),
            **verified,
        }
        atomic_json(STATE / "repository_verification.json", receipt)
        print(json.dumps(receipt, indent=2, sort_keys=True))
        return 0

    before = sorted(run(["docker", "ps", "--format", "{{.Names}}"]).split())
    started = time.time()
    points, failures = [], []
    for phase, label in (("historical", historical), ("latest", latest)):
        with Sandbox(args.image, args.config, args.work_root, args.cpus, args.memory) as box:
            restore_started = time.time()
            box.restore(args.repo, label, args.restore_timeout)
            copied = time.time()
            ready = box.start(primary_connections, args.ready_timeout)
            schema = box.sql(SCHEMA_SQL)
            data = box.sql(DATA_SQL)
            box.stop()
        problems = compare(data, live_data)
        if schema != live_schema:
            problems.append(f"schema {schema} != live {live_schema}")
        if problems:
            failures.extend(f"{phase}: {p}" for p in problems)
        points.append({
            "phase": phase, "label": label, "schema": schema, "data": data,
            "restore_seconds": round(copied - restore_started, 3),
            "recovery_seconds": round(ready, 3),
            "problems": problems,
        })

    after = sorted(run(["docker", "ps", "--format", "{{.Names}}"]).split())
    if before != after:
        failures.append(f"container set changed: {before} -> {after}")

    completed = time.time()
    ok = not failures
    common = {
        "schema_version": 1,
        "status": "ok" if ok else "degraded",
        "repo": args.repo,
        "stanza": STANZA,
        "host_role": args.host_role or None,
        "started_at": started,
        "completed_at": completed,
        "duration_seconds": round(completed - started, 3),
        "reasons": failures or None,
    }
    atomic_json(STATE / "repository_verification.json", {
        **common, "wal": wal, "backup_count": len(entry.get("backup") or []),
        "full_count": sum(1 for b in (entry.get("backup") or []) if b["type"] == "full"),
    })
    atomic_json(STATE / "restore_acceptance.json", {
        **common, "points": points, "live_reference": {"schema": live_schema, "data": live_data},
    })
    print(json.dumps({**common, "points": points}, indent=2, sort_keys=True))
    return 0 if ok else 2


if __name__ == "__main__":
    try:
        sys.exit(main())
    except AcceptanceError as error:
        print(json.dumps({"status": "failed", "error": str(error)[:2000]}), file=sys.stderr)
        sys.exit(1)
