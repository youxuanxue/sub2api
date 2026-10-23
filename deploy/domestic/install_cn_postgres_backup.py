#!/usr/bin/env python3
"""Install cn-prod pgBackRest backup + archive-check units with JSON receipts.

Run ON the cn-prod host as root. Idempotent: writing the same unit bodies and
re-enabling the timers is a no-op.

Why archive-check exists separately from backup: daily diagnostics classifies
physical backup health through ops/observability/host_https_watch.py, which
requires a receipt stamped within 900 SECONDS. Backups run every 2 hours, so a
receipt written only at backup time always reads stale. prod solves this with a
5-minute `check` timer (ops/stage0/install_postgres_backup.py) and cn-prod now
matches, deliberately, rather than inventing a second freshness contract.

The runner it installs replaces /opt/tokenkey/cn-backup-run.py, an orphan with no
repo SSOT that wrote only backup.log -- pgBackRest was healthy on cn-prod while
`physical_backup` could not see it at all.
"""
from __future__ import annotations
import argparse, json, os, shutil, subprocess, sys, time
from pathlib import Path

RUNNER_SRC = Path(__file__).resolve().parent / "cn_pgbackrest.py"
RUNNER_DST = Path("/opt/tokenkey/cn-pgbackrest.py")
UNIT_DIR = Path("/etc/systemd/system")
STATE = Path("/var/lib/tokenkey/postgres-backup")
LEGACY_RUNNER = Path("/opt/tokenkey/cn-backup-run.py")

# Match prod: incremental every 2 hours at :30 UTC, weekly full decided by the
# runner itself (deploy/domestic/cn_pgbackrest.py:backup_kind).
INCREMENTAL_CALENDAR = "*-*-* 00/2:30:00 UTC"


class InstallError(RuntimeError):
    pass


def run(cmd: list[str], *, timeout: int = 120) -> str:
    result = subprocess.run(cmd, text=True, capture_output=True, check=False, timeout=timeout)
    if result.returncode:
        raise InstallError(f"{cmd[0]} exit {result.returncode}: {(result.stderr or result.stdout)[-2000:]}")
    return result.stdout


def unit_bodies() -> dict[str, str]:
    bodies: dict[str, str] = {}
    for name, command, timer, deadline in (
        ("backup", "backup", f"OnCalendar={INCREMENTAL_CALENDAR}\nPersistent=true", 3670),
        ("archive-check", "check", "OnBootSec=2min\nOnUnitActiveSec=5min", 300),
    ):
        base = "tokenkey-cn-postgres-" + name
        # After=tokenkey-cn-backup-identity.service is load-bearing and must not
        # be dropped: the S3 repo credentials are short-lived and refreshed by
        # tokenkey-cn-backup-identity.timer (OnUnitActiveSec=15min). Without the
        # ordering, a backup or check can start against expired credentials and
        # fail for a reason that has nothing to do with backup health. The
        # pre-existing host unit carried this; preserving it is why the units are
        # read before being rewritten.
        bodies[base + ".service"] = f"""[Unit]
Description=TokenKey cn-prod PostgreSQL {command}
After=docker.service network-online.target tokenkey-cn-backup-identity.service
Wants=tokenkey-cn-backup-identity.service
[Service]
Type=oneshot
ExecStart=/usr/bin/python3 {RUNNER_DST} {command}
TimeoutStartSec={deadline}
UMask=0077
Nice=10
"""
        bodies[base + ".timer"] = f"""[Unit]
Description=TokenKey cn-prod PostgreSQL {command} schedule
[Timer]
{timer}
AccuracySec=30s
RandomizedDelaySec=60s
[Install]
WantedBy=timers.target
"""
    return bodies


def plan() -> dict:
    """Describe the change without touching the host."""
    bodies = unit_bodies()
    changes = []
    for unit, body in sorted(bodies.items()):
        path = UNIT_DIR / unit
        current = path.read_text() if path.is_file() else None
        changes.append({
            "unit": unit,
            "action": "unchanged" if current == body else ("update" if current else "create"),
        })
    return {
        "runner": {"src": str(RUNNER_SRC), "dst": str(RUNNER_DST),
                   "action": "unchanged" if RUNNER_DST.is_file()
                   and RUNNER_DST.read_text() == RUNNER_SRC.read_text() else "install"},
        "legacy_runner_present": LEGACY_RUNNER.is_file(),
        "units": changes,
    }


def apply() -> dict:
    if os.geteuid() != 0:
        raise InstallError("apply requires root")
    if not RUNNER_SRC.is_file():
        raise InstallError(f"runner source missing: {RUNNER_SRC}")

    RUNNER_DST.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(RUNNER_SRC, RUNNER_DST)
    RUNNER_DST.chmod(0o700)

    for unit, body in unit_bodies().items():
        (UNIT_DIR / unit).write_text(body)
    run(["systemctl", "daemon-reload"])
    run(["systemctl", "enable", "--now",
         "tokenkey-cn-postgres-backup.timer",
         "tokenkey-cn-postgres-archive-check.timer"])

    # Produce a receipt immediately so diagnostics does not wait up to 5 minutes
    # for the first one. Synchronous on purpose: the caller should see it fail.
    run(["systemctl", "start", "tokenkey-cn-postgres-archive-check.service"], timeout=330)

    # The legacy orphan runner is superseded by overwriting the SERVICE body that
    # used to invoke it -- the unit name is the same, so there is no stray timer
    # left pointing at it. The file itself stays on disk as a rollback path until
    # the new units have proven themselves; it can no longer be triggered by
    # systemd. Reported as a fact rather than asserted, so the caller can see it.
    ran_by_systemd = LEGACY_RUNNER.name in run(
        ["systemctl", "cat", "tokenkey-cn-postgres-backup.service"]
    )

    STATE.mkdir(parents=True, exist_ok=True, mode=0o750)
    result = {"status": "installed", "installed_at": time.time(),
              "runner": str(RUNNER_DST),
              "legacy_runner_on_disk": LEGACY_RUNNER.is_file(),
              "legacy_runner_still_invoked": ran_by_systemd}
    tmp = STATE / "cn-install.tmp"
    tmp.write_text(json.dumps(result, indent=2) + "\n")
    tmp.replace(STATE / "cn-install.json")
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="write units and enable timers")
    args = parser.parse_args()
    print(json.dumps(apply() if args.apply else plan(), indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except InstallError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1)
