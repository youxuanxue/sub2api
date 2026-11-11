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
  * the restore container uses its own disposable bridge and never joins a
    production network (data machines also run their primary on the default
    bridge). Do NOT harden this to `--network none`:
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
from datetime import datetime, timedelta, timezone
from pathlib import Path

STANZA = "tokenkey"
STATE = Path("/var/lib/tokenkey/postgres-backup")
CONTAINER_CANDIDATES = ("tokenkey-postgres", "tk-archive-standby")
# Verified against the live schema rather than guessed: `usage_logs` carries the
# cost columns and `usage_billing_dedup` mirrors it row for row, so their row
# delta is a strong signal that a restored copy is internally consistent.
# usage_billing_dedup keeps only 7 days and is trimmed nightly, so it is counted
# only inside a fixed window (window_sql below), never as a whole table.
# Count accounts and API keys including soft-deleted rows: deletion after a
# recovery point changes active counts legitimately, while physical row counts
# remain comparable. Never compare a historical active count with today's.
DATA_SQL = """
SELECT json_build_object(
  'accounts',(SELECT count(*) FROM accounts),
  'api_keys',(SELECT count(*) FROM api_keys),
  'usage_logs',(SELECT count(*) FROM usage_logs),
  'cost_sum',(SELECT round(COALESCE(sum(total_cost),0),6)::text FROM usage_logs),
  'actual_cost_sum',(SELECT round(COALESCE(sum(actual_cost),0),6)::text FROM usage_logs),
  'db_size_bytes',pg_database_size('tokenkey')
) -- ops-allow-soft-deleted: physical row population across recovery times
"""
SCHEMA_SQL = """
SELECT json_build_object(
  'tables',(SELECT count(*) FROM information_schema.tables t
            JOIN pg_namespace n ON n.nspname=t.table_schema
            JOIN pg_class c ON c.relnamespace=n.oid AND c.relname=t.table_name
            WHERE t.table_schema='public' AND NOT c.relispartition),
  'fkeys',(SELECT count(*) FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid
           WHERE k.contype='f' AND NOT c.relispartition),
  'pkeys',(SELECT count(*) FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid
           WHERE k.contype='p' AND NOT c.relispartition),
  'sequences',(SELECT count(*) FROM information_schema.sequences WHERE sequence_schema='public'),
  'constraints',(SELECT COALESCE(json_object_agg(c.relname||'.'||k.conname,
                 json_build_object('type',k.contype,'definition',pg_get_constraintdef(k.oid))), '{}'::json)
                 FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid
                 JOIN pg_namespace n ON n.oid=c.relnamespace
                 WHERE k.contype IN ('f','p') AND NOT c.relispartition AND n.nspname='public')
)"""
# Hour/day partitions are provisioned continuously. Their child constraints and
# table counts legitimately differ from a past backup; compare stable owners.

# The billing pair is compared inside [window_start, now). window_start is fixed
# once from the live clock and sent to both sides, so the live snapshot and the
# restored copy count the same span. It sits well inside the 7-day dedup window,
# so the nightly trim never removes rows the comparison depends on.
PAIR_WINDOW = timedelta(days=3)
_WINDOW_START_RE = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z")


def window_sql(window_start: str, window_end: str | None = None) -> str:
    if not _WINDOW_START_RE.fullmatch(window_start):
        raise ValueError(f"window_start must be UTC ISO-8601: {window_start!r}")
    if window_end is not None and not _WINDOW_START_RE.fullmatch(window_end):
        raise ValueError("window_end must be UTC ISO-8601")
    end_clause = f" AND created_at < '{window_end}'::timestamptz" if window_end else ""
    return f"""
SELECT json_build_object(
  'window_end',{repr(window_end) if window_end else 'NULL'},
  'window_start','{window_start}',
  'window_usage_logs',(SELECT count(*) FROM usage_logs WHERE created_at >= '{window_start}'::timestamptz{end_clause}),
  'window_billing_dedup',(SELECT count(*) FROM usage_billing_dedup WHERE created_at >= '{window_start}'::timestamptz{end_clause})
)"""


def pair_window_start(now: datetime) -> str:
    start = (now.astimezone(timezone.utc) - PAIR_WINDOW).replace(minute=0, second=0, microsecond=0)
    return start.strftime("%Y-%m-%dT%H:%M:%SZ")


def pair_window_end(entry: dict, label: str) -> str:
    # A backup can stop between the billing claim and asynchronous usage insert.
    # Compare only hours completed before this backup STARTED, on both databases.
    selected = next((b for b in entry.get("backup", []) if b["label"] == label), None)
    if selected is None or not isinstance(selected.get("timestamp", {}).get("start"), (int, float)):
        raise AcceptanceError("backup start timestamp missing for closed billing window")
    end = datetime.fromtimestamp(selected["timestamp"]["start"], timezone.utc).replace(minute=0, second=0, microsecond=0)
    return end.strftime("%Y-%m-%dT%H:%M:%SZ")


# ops-sql-coverage: every *_SQL / *_sql symbol must be enumerated or exempted,
# so the execution gate runs it against a real Postgres rather than trusting a
# substring assertion.
SELF_CHECK_EXEMPT: dict[str, str] = {
    "live_sql": "runner: executes a caller-supplied statement in a container, builds no SQL",
}


def iter_self_check_sql() -> list[tuple[str, str]]:
    """(label, rendered_sql) for the ops-sql-coverage real-Postgres self-check."""
    return [("SCHEMA_SQL", SCHEMA_SQL), ("DATA_SQL", DATA_SQL),
            ("window_sql", window_sql("2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z"))]


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


def targets(entry: dict, timeline: str | None = None) -> tuple[str, str]:
    """A historical point (oldest full) and the latest point."""
    backups = entry.get("backup") or []
    if timeline is not None:
        if not re.fullmatch(r"[0-9A-F]{8}", timeline):
            raise AcceptanceError("timeline must be eight uppercase hexadecimal digits")
        backups = [b for b in backups
                   if (b.get("archive") or {}).get("start", "").startswith(timeline)
                   and (b.get("archive") or {}).get("stop", "").startswith(timeline)
                   and not b.get("error")]
    fulls = [b for b in backups if b["type"] == "full"]
    if not fulls or not backups:
        raise AcceptanceError("repository has no full backup to restore from")
    return fulls[0]["label"], backups[-1]["label"]


def wal_continuity(entry: dict, timeline: str | None = None) -> dict:
    archive = (entry.get("archive") or [{}])[0]
    low, high = archive.get("min") or "", archive.get("max") or ""
    if timeline is not None:
        historical, _ = targets(entry, timeline)
        low = next(b["archive"]["start"] for b in entry["backup"] if b["label"] == historical)
        if not high.startswith(timeline):
            raise AcceptanceError("repository WAL head does not match the selected live timeline")
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
        self.network = self.name + "-net"
        self.cpus, self.memory = cpus, memory

    def __enter__(self):
        self._fresh_pgdata()
        run(["docker", "network", "create", self.network])
        return self

    def __exit__(self, *_):
        run(["docker", "rm", "-f", self.name], check=False)
        run(["docker", "network", "rm", self.network], check=False)
        if self.work.exists():
            run(["sudo", "rm", "-rf", str(self.work)], check=False)

    def _fresh_pgdata(self) -> None:
        if self.pgdata.exists():
            run(["sudo", "rm", "-rf", str(self.pgdata)], check=False)
        run(["sudo", "install", "-d", "-m", "700", "-o", "70", "-g", "70", str(self.pgdata)])

    def _limits(self) -> list[str]:
        return ["--cpus", self.cpus, "--memory", self.memory,
                "--memory-swap", self.memory, "--user", "70:70",
                "--network", self.network]

    def restore(self, repo: int, label: str, timeout: int) -> None:
        self._fresh_pgdata()
        run(["docker", "rm", "-f", self.name], check=False)
        # Downloading (and archive-get at startup) needs COS egress, so the
        # container uses a disposable bridge rather than a production network.
        # Only the read-only COS configuration and disposable PGDATA are mounted.
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
             "-c", "archive_mode=off",
             "-c", "archive_command=",
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


def expected_schema(live: dict, phase: str, label: str, timeline: str | None,
                    baseline: dict | None) -> dict:
    """Only a pinned, reviewed historical constraint delta may differ from live."""
    if baseline is None or phase != "historical":
        return live
    if baseline.get("label") != label or baseline.get("timeline") != timeline or not baseline.get("reference"):
        raise AcceptanceError("historical schema baseline does not match the selected label/timeline")
    expected = {**live, "constraints": dict(live["constraints"])}
    extras = baseline.get("historical_extra_constraints")
    if not isinstance(extras, dict) or not extras:
        raise AcceptanceError("historical schema baseline has no reviewed constraints")
    for name, constraint in extras.items():
        if name in expected["constraints"] or constraint.get("type") not in ("p", "f") or not constraint.get("definition"):
            raise AcceptanceError("invalid historical constraint delta")
        expected["constraints"][name] = constraint
        expected["pkeys" if constraint["type"] == "p" else "fkeys"] += 1
    return expected


def compare(restored: dict, live: dict) -> list[str]:
    """A restored point is a PAST state: it may lag live, never exceed it.

    usage_billing_dedup is not in the never-exceed list: live trims it nightly,
    so a week-old point legitimately holds dedup rows that live has deleted.
    """
    problems = []
    for key in ("accounts", "api_keys", "usage_logs"):
        got, expected = restored.get(key), live.get(key)
        if isinstance(got, int) and isinstance(expected, int) and got > expected:
            problems.append(f"{key} restored {got} > live {expected}")
    for key in ("cost_sum", "actual_cost_sum"):
        try:
            if float(restored[key]) > float(live[key]) * 1.000001:
                problems.append(f"{key} restored {restored[key]} > live {live[key]}")
        except (KeyError, TypeError, ValueError):
            problems.append(f"{key} missing or unparseable")
    problems.extend(compare_pair_window(restored, live))
    return problems


def compare_pair_window(restored: dict, live: dict) -> list[str]:
    """usage_logs and usage_billing_dedup are written together. Inside the same
    window, the same delta on the restored copy as on live means the pair
    survived the round trip rather than being captured mid-write.

    A point older than window_start has no rows in the window, so the pair
    cannot be checked there; that is reported, not counted as a failure. The
    latest point always falls inside the window.
    """
    try:
        if restored["window_start"] != live["window_start"] or restored.get("window_end") != live.get("window_end"):
            return [f"pair window mismatch {restored['window_start']} != live {live['window_start']}"]
        if restored["window_usage_logs"] == 0 and restored["window_billing_dedup"] == 0:
            return []
        restored_drift = abs(restored["window_usage_logs"] - restored["window_billing_dedup"])
        live_drift = abs(live["window_usage_logs"] - live["window_billing_dedup"])
    except (KeyError, TypeError):
        return ["billing pair drift not computable"]
    if restored_drift != live_drift:
        return [f"billing pair drift {restored_drift} != live {live_drift} since {live['window_start']}"]
    return []


def atomic_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o750)
    temporary = path.with_suffix(f".{uuid.uuid4().hex[:8]}.tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    os.chmod(temporary, 0o640)
    temporary.replace(path)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=int, required=True, help="repository to accept (2 = COS)")
    parser.add_argument("--timeline", help="reviewed live timeline, eight uppercase hex digits; restrict both restore points")
    parser.add_argument("--historical-schema-baseline", type=Path, help="reviewed constraint delta pinned to historical label and timeline")
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
    if args.timeline is not None:
        actual = live_sql(container, "SELECT json_build_object('timeline',timeline_id) FROM pg_control_checkpoint()")
        if args.timeline != f"{actual['timeline']:08X}":
            raise AcceptanceError("selected timeline does not match the live primary")
    historical, latest = targets(entry, args.timeline)
    wal = wal_continuity(entry, args.timeline)
    baseline = json.loads(args.historical_schema_baseline.read_text()) if args.historical_schema_baseline else None
    live_schema = live_sql(container, SCHEMA_SQL)
    historical_schema = expected_schema(live_schema, "historical", historical, args.timeline, baseline)
    window_start = pair_window_start(datetime.now(timezone.utc))
    live_data = live_sql(container, DATA_SQL)
    primary_connections = int(live_setting(container, "max_connections"))

    plan = {
        "repo": args.repo, "historical": historical, "latest": latest,
        "timeline": args.timeline,
        "wal": wal, "live_schema": live_schema, "historical_schema_baseline": baseline, "primary_max_connections": primary_connections,
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
            window_end = pair_window_end(entry, label)
            reference = {**live_data, **live_sql(container, window_sql(window_start, window_end))}
            data = {**box.sql(DATA_SQL), **box.sql(window_sql(window_start, window_end))}
            box.stop()
        problems = compare(data, reference)
        expected = historical_schema if phase == "historical" else live_schema
        if schema != expected:
            problems.append(f"schema differs from reviewed {phase} baseline")
        if problems:
            failures.extend(f"{phase}: {p}" for p in problems)
        points.append({
            "phase": phase, "label": label, "schema": schema, "data": data,
            "window_reference": {k:v for k,v in reference.items() if k.startswith("window_")},
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
        "timeline": args.timeline,
        "historical_schema_baseline": baseline,
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
