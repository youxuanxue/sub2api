#!/usr/bin/env python3
"""Stop obsolete QA/alert timers on an explicitly selected, idle AWS prod standby."""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.request

TIMERS = (
    "tokenkey-qa-maintenance.timer",
    "tokenkey-qa-boundary.timer",
    "tokenkey-disk-metrics.timer",
)
BACKUP_ROOT = Path("/var/backups/tokenkey-aws-standby-timers")


def command(*args: str, check: bool = True) -> str:
    result = subprocess.run(args, capture_output=True, text=True, timeout=30)
    if check and result.returncode:
        raise RuntimeError(f"command failed: {args[0]} {args[1]}")
    return result.stdout.strip()


def instance_identity() -> str:
    # IMDSv2 identity only; never read instance-role credentials.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    request = urllib.request.Request(
        "http://169.254.169.254/latest/api/token", method="PUT",
        headers={"X-aws-ec2-metadata-token-ttl-seconds": "60"},
    )
    with opener.open(request, timeout=3) as response:
        token = response.read(4096).decode()
    request = urllib.request.Request(
        "http://169.254.169.254/latest/dynamic/instance-identity/document",
        headers={"X-aws-ec2-metadata-token": token},
    )
    with opener.open(request, timeout=3) as response:
        return json.load(response)["instanceId"]


def validate_host(expected: str) -> None:
    if not re.fullmatch(r"i-[0-9a-f]{17}", expected) or instance_identity() != expected:
        raise RuntimeError("AWS standby identity mismatch; Hybrid/HCloud targets are forbidden")
    running = set(command("docker", "ps", "--filter", "name=tokenkey", "--format", "{{.Names}}").splitlines())
    if running - {"tokenkey-postgres", "tokenkey-redis"}:
        raise RuntimeError("standby still has a running gateway or worker")
    for timer in TIMERS:
        service = timer.removesuffix(".timer") + ".service"
        state = command("systemctl", "show", service, "--property=ActiveState", "--value")
        if state not in {"inactive", "failed"}:
            raise RuntimeError("standby job is running or its state is unknown")


def timer_states() -> dict:
    result = {}
    for timer in TIMERS:
        text = command("systemctl", "show", timer, "--property=ActiveState,UnitFileState")
        state = dict(line.split("=", 1) for line in text.splitlines() if "=" in line)
        if state.get("ActiveState") not in {"active", "inactive"} or state.get("UnitFileState") not in {"enabled", "disabled"}:
            raise RuntimeError("unsupported or unknown standby timer state")
        result[timer] = state
    return result


def restore(states: dict) -> None:
    for timer in TIMERS:
        state = states[timer]
        command("systemctl", "enable" if state["UnitFileState"] == "enabled" else "disable", timer)
        command("systemctl", "start" if state["ActiveState"] == "active" else "stop", timer)
    if timer_states() != states:
        raise RuntimeError("standby timer rollback verification failed")


def load_backup(path: Path, expected: str) -> dict:
    info = path.lstat()
    if path.is_symlink() or not path.is_file() or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise RuntimeError("backup must be a private, owned regular file")
    payload = json.loads(path.read_text())
    states = payload.get("timers")
    if payload.get("instance_id") != expected or not isinstance(states, dict) or set(states) != set(TIMERS):
        raise RuntimeError("backup does not match standby identity and timer allowlist")
    for state in states.values():
        if not isinstance(state, dict) or set(state) != {"ActiveState", "UnitFileState"} or state["ActiveState"] not in {"active", "inactive"} or state["UnitFileState"] not in {"enabled", "disabled"}:
            raise RuntimeError("invalid timer backup")
    return states


def require_root() -> None:
    if os.geteuid() != 0:
        raise RuntimeError("apply/rollback requires root")


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expected-instance", required=True)
    parser.add_argument("--operation", choices=("plan", "apply", "rollback"), default="plan")
    parser.add_argument("--backup", type=Path)
    args = parser.parse_args(argv)
    validate_host(args.expected_instance)
    states = timer_states()
    if args.operation == "plan":
        print(json.dumps({"operation": "plan", "instance_id": args.expected_instance, "timers": states}, sort_keys=True))
        return 0
    require_root()
    if args.operation == "rollback":
        if args.backup is None:
            raise RuntimeError("rollback requires --backup")
        restore(load_backup(args.backup, args.expected_instance))
    else:
        if all(state == {"ActiveState": "inactive", "UnitFileState": "disabled"} for state in states.values()):
            print(json.dumps({"operation": "apply", "changed": False}))
            return 0
        BACKUP_ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
        stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        fd, name = tempfile.mkstemp(prefix=stamp + "-", suffix=".json", dir=BACKUP_ROOT)
        with os.fdopen(fd, "w") as handle:
            json.dump({"instance_id": args.expected_instance, "timers": states}, handle)
            handle.flush()
            os.fsync(handle.fileno())
        print(json.dumps({"backup": name}), flush=True)
        # A failed precondition must not reactivate any timer via rollback.
        validate_host(args.expected_instance)
        try:
            for timer in TIMERS:
                command("systemctl", "disable", "--now", timer)
            if any(state != {"ActiveState": "inactive", "UnitFileState": "disabled"} for state in timer_states().values()):
                raise RuntimeError("standby timer retirement verification failed")
        except (RuntimeError, subprocess.SubprocessError):
            restore(states)
            raise
    print(json.dumps({"operation": args.operation, "instance_id": args.expected_instance, "timers": timer_states()}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError):
        raise SystemExit("standby timer operation failed; inspect target identity, state and rollback receipt")
