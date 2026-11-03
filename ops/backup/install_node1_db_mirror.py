#!/usr/bin/env python3
"""Install the node1 timer that mirrors the COS database backup.

Until now ``cos_node1_mirror.py`` was a manual command: nothing in the
repository scheduled it, and node1's db-prod/ and db-cn-prod/ directories were
still empty on 2026-11-03. This installer is what makes the third copy actually
exist without someone remembering to run it.

The timer lives ON NODE1, not on the production hosts, because node1 pulling
from COS measured 29.7 MiB/s with 16 workers while the prod host pushing over
the jump host managed 0.8 MiB/s -- and a pull costs the production host no CPU
and no egress at all.

Credentials: the unit reads them from an env file on node1 that only root can
read. The credential currently available carries write permission, so the fetch
path deliberately calls nothing but list_objects_v2 and get_object (enforced by
test_cos_s3_fetch.ReadOnlyContractTest). Replacing it with a read-only key is
still worth doing; this does not depend on it.
"""
from __future__ import annotations

import argparse
import json
import os
import shlex
import subprocess
import sys
from pathlib import Path

UNIT = "tokenkey-node1-db-mirror"
DEST = Path("/opt/tokenkey/node1-mirror")
ENV_FILE = Path("/etc/tokenkey/cos-mirror.env")
ARCHIVE_ROOT = "/storage/tokenkey-archive"
# COS keeps 7 days of full backups (repo2-retention-full=7), and an incremental
# lands every two hours, so a four-hour cadence never falls more than one
# increment behind while leaving the link idle most of the time.
SCHEDULE = "*-*-* 00/4:17:00 UTC"
MODULES = ("cos_node1_mirror.py", "cos_s3_fetch.py", "node1_storage_policy.py")
# Read from each host's pgbackrest.conf on 2026-11-03. Both hosts carry the
# same Tencent credential (identical sha256 of id and secret), so one env file
# on node1 reaches both buckets.
ENVIRONMENTS = {
    "prod": {"bucket": "tokenkey-prod-backup-usw-1320330536",
             "prefix": "prod/postgres",
             "endpoint": "cos.na-siliconvalley.myqcloud.com",
             "region": "na-siliconvalley"},
    "cn-prod": {"bucket": "tokenkey-cnprod-backup-bj-1320330536",
                "prefix": "cn-prod/postgres",
                "endpoint": "cos.ap-beijing.myqcloud.com",
                "region": "ap-beijing"},
}


class InstallError(RuntimeError):
    pass


def run(argv: list[str], *, check: bool = True) -> str:
    result = subprocess.run(argv, capture_output=True, text=True, check=False)
    if check and result.returncode:
        raise InstallError(f"{argv[0]} exit {result.returncode}: "
                           f"{(result.stderr or result.stdout)[-400:]}")
    return result.stdout


def service_unit(environments: list[str]) -> str:
    """One oneshot service that mirrors each environment in turn.

    Nice/ionice keep the fetch behind anything else node1 is doing, and the
    archive volume is a cold disk shared with the QA archive.
    """
    lines = []
    for name in environments:
        spec = ENVIRONMENTS[name]
        lines.append(
            "ExecStart=/usr/bin/python3 " + str(DEST / "mirror_once.py")
            + f" --environment {shlex.quote(name)}"
            + f" --bucket {shlex.quote(spec['bucket'])}"
            + f" --prefix {shlex.quote(spec['prefix'])}"
            + f" --endpoint {shlex.quote(spec['endpoint'])}"
            + f" --region {shlex.quote(spec['region'])}"
            + f" --destination {ARCHIVE_ROOT}")
    body = "\n".join(lines)
    return f"""[Unit]
Description=TokenKey node1 COS database backup mirror
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile={ENV_FILE}
{body}
TimeoutStartSec=5400
UMask=0077
Nice=19
IOSchedulingClass=idle
"""


def timer_unit() -> str:
    return f"""[Unit]
Description=TokenKey node1 COS database mirror schedule

[Timer]
OnCalendar={SCHEDULE}
OnBootSec=12min
Persistent=true
AccuracySec=2min
RandomizedDelaySec=3min

[Install]
WantedBy=timers.target
"""


def runner_script() -> str:
    """Thin wrapper so the unit line stays readable and failures stay JSON.

    It does not re-implement the mirror: cos_node1_mirror owns the manifest,
    the per-file sha256 verification, the staging directory, the capacity
    policy and the atomic receipt. This only picks the snapshot name and
    reports one line of JSON per run for the operator.
    """
    return '''#!/usr/bin/env python3
"""Run one COS -> node1 database mirror pass and print a JSON receipt line."""
from __future__ import annotations

import argparse
import datetime as dt
import json
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--environment", required=True)
    parser.add_argument("--bucket", required=True)
    parser.add_argument("--prefix", required=True)
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--region", required=True)
    parser.add_argument("--destination", required=True)
    args = parser.parse_args()

    # One snapshot per calendar day: a re-run on the same day finds the
    # existing receipt, re-verifies it and copies nothing.
    snapshot = f"{args.environment}-{dt.datetime.now(dt.timezone.utc):%Y%m%d}"
    command = [sys.executable, str(HERE / "cos_node1_mirror.py"), "copy",
               "--namespace", "db", "--snapshot", snapshot,
               "--s3-bucket", args.bucket, "--s3-prefix", args.prefix,
               "--s3-endpoint", args.endpoint, "--s3-region", args.region,
               "--destination", args.destination, "--apply"]
    started = dt.datetime.now(dt.timezone.utc)
    result = subprocess.run(command, capture_output=True, text=True, check=False)
    payload = {"environment": args.environment, "snapshot": snapshot,
               "ok": result.returncode == 0,
               "started_at": started.isoformat(),
               "completed_at": dt.datetime.now(dt.timezone.utc).isoformat()}
    if result.returncode:
        payload["error"] = (result.stderr or result.stdout)[-600:]
    else:
        for line in result.stdout.strip().splitlines()[::-1]:
            try:
                payload["result"] = json.loads(line)
                break
            except ValueError:
                continue
    print(json.dumps(payload, sort_keys=True))
    return result.returncode


if __name__ == "__main__":
    raise SystemExit(main())
'''


def install(args: argparse.Namespace) -> dict:
    if os.geteuid() != 0:
        raise InstallError("must run as root on node1")
    hostname = run(["hostname"]).strip()
    if args.expect_hostname and hostname != args.expect_hostname:
        raise InstallError(f"expected host {args.expect_hostname!r}, this is {hostname!r}")
    environments = [name for name in args.environments.split(",") if name]
    for name in environments:
        if name not in ENVIRONMENTS:
            raise InstallError(f"unknown environment: {name}")
        spec = ENVIRONMENTS[name]
        missing = [k for k in ("bucket", "endpoint", "region") if not spec[k]]
        if missing:
            raise InstallError(
                f"{name} has no COS coordinates yet ({', '.join(missing)}); "
                "pass --environments prod until they are known")
    if not ENV_FILE.is_file():
        raise InstallError(f"credentials are missing: {ENV_FILE}")
    if ENV_FILE.stat().st_mode & 0o077:
        raise InstallError(f"credentials must not be group/world readable: {ENV_FILE}")
    source = Path(args.source).resolve()
    for module in MODULES:
        if not (source / module).is_file():
            raise InstallError(f"missing module in {source}: {module}")

    plan = {"hostname": hostname, "environments": environments,
            "schedule": SCHEDULE, "destination": ARCHIVE_ROOT,
            "units": [UNIT + ".service", UNIT + ".timer"], "apply": args.apply}
    print(json.dumps(plan, sort_keys=True))
    if not args.apply:
        return plan

    DEST.mkdir(parents=True, exist_ok=True, mode=0o755)
    for module in MODULES:
        target = DEST / module
        target.write_bytes((source / module).read_bytes())
        target.chmod(0o644)
    runner = DEST / "mirror_once.py"
    runner.write_text(runner_script())
    runner.chmod(0o755)

    units = Path("/etc/systemd/system")
    (units / f"{UNIT}.service").write_text(service_unit(environments))
    (units / f"{UNIT}.timer").write_text(timer_unit())
    run(["systemctl", "daemon-reload"])
    run(["systemctl", "enable", f"{UNIT}.timer"])
    run(["systemctl", "start", f"{UNIT}.timer"])
    state = run(["systemctl", "show", f"{UNIT}.timer", "-p", "ActiveState,UnitFileState"])
    return plan | {"installed": True, "timer": state.strip().replace("\n", " ")}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("install", choices=("install",))
    parser.add_argument("--source", default=str(DEST),
                        help="directory holding the mirror modules to install")
    parser.add_argument("--environments", default="prod",
                        help="comma separated; cn-prod needs its COS coordinates first")
    parser.add_argument("--expect-hostname", default="",
                        help="refuse to install unless the hostname matches")
    parser.add_argument("--apply", action="store_true",
                        help="without this the command only prints the plan")
    args = parser.parse_args()
    os.umask(0o077)
    try:
        result = install(args)
    except InstallError as exc:
        print(json.dumps({"ok": False, "error": str(exc)}), file=sys.stderr)
        return 2
    if args.apply:
        print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
