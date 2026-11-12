#!/usr/bin/env python3
"""Data-machine QA archive runner. Configuration and credentials are root-owned.

No raw payload is copied through this machine. SSH invokes fixed application
workers, which transfer directly to node1. Failed sources do not stop other
sources. Independent host-local retention jobs expire hot data after one day.
"""
from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import hashlib
import json
from contextlib import ExitStack
from pathlib import Path
import shlex
import socket
import subprocess
import sys

import data_machine_coordinator as policy
import node1_archive as archive


def validate_config(config):
    if config.get("schema_version") != 1 or config.get("environment") not in ("prod", "cn-prod"):
        raise policy.CoordinatorError("invalid coordinator config")
    if not config.get("hostname") or not config.get("sources"):
        raise policy.CoordinatorError("coordinator requires a host and historical source registry")
    policy._sources([source["id"] for source in config["sources"]])
    for source in config["sources"]:
        start = policy._hour(source["active_from"])
        end = policy._hour(source["active_until"]) if source.get("active_until") else None
        if end and end <= start:
            raise policy.CoordinatorError("source participation interval is invalid")
        if not policy.re.fullmatch(r"[A-Za-z0-9_.@:-]+", source["ssh_target"]) or source["ssh_target"].startswith("-"):
            raise policy.CoordinatorError("invalid SSH target")
        for key in ("worker", "worker_config"):
            if not policy.safe_path(source[key]):
                raise policy.CoordinatorError("worker paths must be absolute")
    for key in ("binary", "state_root", "host_lock", "ssh_key", "known_hosts"):
        if not policy.safe_path(config[key]):
            raise policy.CoordinatorError(f"{key} must be absolute")
    if not 1 <= config.get("max_hours_per_run", 4) <= 48:
        raise policy.CoordinatorError("hour batch must be between 1 and 48")
    if not policy.re.fullmatch(r"[0-9a-f]{64}", config.get("binary_sha256", "")):
        raise policy.CoordinatorError("invalid coordinator binary digest")
    # Gap duration is a business/data-loss decision, not an inferred default.
    if config.get("allow_gap"):
        raise policy.CoordinatorError("automatic gap deletion is not activated; explicit policy approval is still required")


def participants(config, hour):
    return [source for source in config["sources"]
            if policy._hour(source["active_from"]) < hour + dt.timedelta(hours=1)
            and (not source.get("active_until") or policy._hour(source["active_until"]) > hour)]


def database(config, phase, *args, dry_run=False):
    binary = Path(config["binary"])
    if hashlib.sha256(binary.read_bytes()).hexdigest() != config["binary_sha256"]:
        raise policy.CoordinatorError("coordinator binary digest mismatch")
    command = [str(binary), "--phase", phase, "--confirm", f"tokenkey-qa-local-{phase}-v1", *args]
    if dry_run:
        command.append("--dry-run")
    with ExitStack() as stack:
        if phase != "inspect" and not dry_run:
            timer = "tokenkey-cn-postgres-backup.timer" if config["environment"] == "cn-prod" else "tokenkey-postgres-backup.timer"
            upcoming = subprocess.run(["systemctl", "show", timer, "-p", "NextElapseUSecRealtime", "--value"],
                                      capture_output=True, text=True, timeout=10, check=True).stdout.strip()
            next_epoch = subprocess.run(["date", "--date", upcoming, "+%s"], capture_output=True,
                                        text=True, timeout=10, check=True).stdout.strip()
            if not upcoming or int(next_epoch) - dt.datetime.now(dt.timezone.utc).timestamp() < 360:
                raise policy.CoordinatorError("QA database work deferred near scheduled backup")
            backup_lock = stack.enter_context(open("/var/lib/tokenkey/postgres-backup/operator.lock", "a"))
            fcntl.flock(backup_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        completed = subprocess.run(command, check=True, capture_output=True, text=True, timeout=270)
    value = json.loads(completed.stdout)
    if value.get("ok") is not True or value.get("phase") != phase:
        raise policy.CoordinatorError("database maintenance receipt invalid")
    return value


def source_command(config, source, command, hour, *, plan=None, apply=False):
    request = {"command": command, "source_id": source["id"], "environment": config["environment"],
               "hour": hour.isoformat(), "apply": apply}
    if plan is not None:
        request["plan"] = plan
    remote = shlex.join(["/usr/bin/nice", "-n", "19", "/usr/bin/ionice", "-c", "3",
                         "/usr/bin/python3", source["worker"], "--config", source["worker_config"]])
    completed = subprocess.run(["ssh", "-T", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
                               "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10",
                               "-o", f"UserKnownHostsFile={config['known_hosts']}",
                               "-i", config["ssh_key"], source["ssh_target"], remote],
                              input=json.dumps(request), capture_output=True, text=True, check=True,
                              timeout=1800 if command == "archive" else 300)
    result = json.loads(completed.stdout)
    if result.get("ok") is not True or result.get("source_id") != source["id"]:
        raise policy.CoordinatorError("application worker response identity invalid")
    if command == "cleanup" and (policy._hour(result.get("window_start", "")) != hour
                                  or result.get("action") != ("delete" if apply else "plan")):
        raise policy.CoordinatorError("application worker did not prove requested cleanup")
    return result


def run(config, *, apply=False):
    validate_config(config)
    if socket.gethostname() != config["hostname"]:
        raise policy.CoordinatorError("coordinator must run on its registered data machine")
    state = Path(config["state_root"])
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    lock_path = Path(config["host_lock"])
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        inspected = database(config, "inspect")
        anchor = policy._hour(inspected["db_anchor_utc"])
        hours = {anchor - dt.timedelta(hours=offset) for offset in range(1, 25)}
        pending = state / "pending.json"
        if pending.exists():
            hours.update(policy._hour(value) for value in json.loads(pending.read_text()))
        completed_path = state / "completed.json"
        completed = json.loads(completed_path.read_text()) if completed_path.exists() else {}
        selected = sorted(hour for hour in hours if participants(config, hour)
                          and hour + dt.timedelta(hours=25) > anchor
                          and completed.get(hour.isoformat()) !=
                               sorted(source["id"] for source in participants(config, hour)))
        cursor = state / "cursor.json"
        offset = json.loads(cursor.read_text()).get("offset", 0) if cursor.exists() else 0
        limit = config.get("max_hours_per_run", 4)
        chosen = [selected[(offset + i) % len(selected)] for i in range(min(limit, len(selected)))] if selected else []
        result = {"ok": True, "environment": config["environment"], "apply": apply, "hours": [], "errors": [],
                  "observed_at": dt.datetime.now(dt.timezone.utc).isoformat()}
        if apply:
            provision = state / "provision.json"
            previous = json.loads(provision.read_text()).get("anchor") if provision.exists() else None
            if previous != anchor.isoformat():
                try:
                    database(config, "provision")
                    archive.atomic_json(provision, {"anchor": anchor.isoformat()})
                except (OSError, ValueError, subprocess.SubprocessError) as exc:
                    result["errors"].append({"operation": "provision", "error": str(exc)})
        incomplete = set(selected)
        for hour in chosen:
            sources = participants(config, hour)
            receipts = state / "observations"
            for source in sources:
                path = policy.receipt_path(receipts, config["environment"], source["id"], hour)
                # Never reuse yesterday's successful observation after a fresh
                # source probe has failed. Archive payloads remain immutable.
                if apply:
                    path.unlink(missing_ok=True)
                try:
                    if apply:
                        evidence = source_command(config, source, "archive", hour)
                        archive.atomic_json(path, evidence)
                except (OSError, ValueError, subprocess.SubprocessError) as exc:
                    result["errors"].append({"source_id": source["id"], "hour": hour.isoformat(), "operation": "archive", "error": str(exc)})
            plan = policy.build_plan(receipt_root=receipts, environment=config["environment"], hour=hour,
                                     expected_sources=[source["id"] for source in sources], now=anchor)
            item = {"hour": hour.isoformat(), "sources": plan["sources"]}
            if all(item["state"] == "complete" for item in plan["sources"].values()):
                incomplete.discard(hour)
                if apply:
                    completed[hour.isoformat()] = plan["expected_sources"]
                    archive.atomic_json(completed_path, completed)
            result["hours"].append(item)
        if apply:
            completed = {hour: sources for hour, sources in completed.items()
                         if policy._hour(hour) + dt.timedelta(hours=25) > anchor}
            archive.atomic_json(completed_path, completed)
            archive.atomic_json(pending, [hour.isoformat() for hour in sorted(incomplete)])
            archive.atomic_json(cursor, {"offset": (offset + len(chosen)) % max(len(selected), 1)})
        result["ok"] = not result["errors"]
        blocked = sorted(hour for hour in incomplete if hour + dt.timedelta(hours=2) <= anchor)
        result["blocked_hours"] = len(blocked)
        result["oldest_blocked_hour_utc"] = blocked[0].isoformat() if blocked else None
        archive.atomic_json(state / ("last-run.json" if apply else "last-plan.json"), result)
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        config = policy.load_private_config(args.config)
        result = run(config, apply=args.apply)
        print(json.dumps(result, sort_keys=True))
        return 0 if result["ok"] else 1
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
