#!/usr/bin/env python3
"""Small cross-region HTTPS watch; reuse fleet probing and Feishu delivery owners."""
from __future__ import annotations

import argparse
import concurrent.futures
import fcntl
import json
import os
import sys
import subprocess
from pathlib import Path
import time
from urllib.parse import urlparse

from edge_https_health import probe_health
from edge_health_delivery import _atomic_write, post_feishu


def next_state(previous: dict, healthy: bool, now: int) -> tuple[dict, str]:
    state = dict(previous)
    failures = int(state.get("failures", 0))
    if now - int(state.get("observed_at", now)) > 600:
        failures = 0
    state.update(observed_at=now, failures=0 if healthy else failures + 1)
    active = bool(state.get("active", False))
    if healthy and active:
        return state, "recovered"
    if not healthy and state["failures"] >= 2:
        if not active or now - int(state.get("sent_at", 0)) >= 1800:
            return state, "unhealthy"
    return state, ""


def tick(targets: dict, previous: dict, now: int, probe, send) -> dict:
    state = dict(previous)
    with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
        facts = dict(zip(targets, pool.map(probe, targets.values())))
    for name, fact in facts.items():
        updated, action = next_state(previous.get(name, {}), fact["healthy"], now)
        if action:
            send(f"TokenKey HTTPS {action}: {name}; HTTP {fact['http_code']}. Independent host watch.")
            updated.update(active=action == "unhealthy", sent_at=now)
        state[name] = updated
    return state


# Allowlisted local jobs share the HTTPS watch's delivery and dedup state.
# (max age between completions, maximum normal execution time), in seconds.
JOB_LIMITS = {
    "tokenkey-pgdump": (3 * 3600, 45 * 60),
    "tokenkey-disk-metrics": (15 * 60, 60),
    "tokenkey-ghcr-prune-daily": (30 * 3600, 15 * 60),
    "tokenkey-qa-maintenance": (2 * 3600, 40 * 60 + 60),
    "tokenkey-qa-boundary": (2 * 3600, 40 * 60 + 60),
}


def job_fact(service: dict, timer: dict, uptime: float, limits: tuple) -> dict:
    healthy, reason = False, "job has no successful completion"
    def age(key):
        stamp = int(service.get(key, "0") or 0) / 1_000_000
        return uptime - stamp if 0 < stamp <= uptime else float("inf")
    if timer.get("ActiveState") != "active" or timer.get("UnitFileState") != "enabled":
        reason = "timer disabled or inactive"
    elif service.get("ActiveState") == "failed" or service.get("Result") not in ("success", ""):
        reason = "service failed: " + service.get("Result", "unknown")
    elif service.get("ActiveState") in ("activating", "active"):
        healthy = age("ExecMainStartTimestampMonotonic") <= limits[1]
        reason = "running" if healthy else "execution overdue"
    else:
        healthy = age("ExecMainExitTimestampMonotonic") <= limits[0]
        reason = "recent completion" if healthy else "completion stale or missing"
    return {"healthy": healthy, "reason": reason}


def probe_job(name: str) -> dict:
    def properties(unit):
        result = subprocess.run(["systemctl", "show", unit, "--property=ActiveState,UnitFileState,Result,ExecMainStartTimestampMonotonic,ExecMainExitTimestampMonotonic"],
                                capture_output=True, text=True, timeout=5, check=True)
        return dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)
    service = properties(name + ".service")
    timer = properties(name + ".timer")
    # Read after systemd: the job may start while properties are collected.
    # Linux monotonic() shares systemd's CLOCK_MONOTONIC; /proc/uptime is
    # truncated to centiseconds and includes suspend time (CLOCK_BOOTTIME).
    return job_fact(service, timer, time.monotonic(), JOB_LIMITS[name])


def tick_jobs(jobs, previous, now, probe, send, node="unknown"):

    state = dict(previous)
    for name in jobs:
        try:
            fact = probe(name)
        except (OSError, ValueError, subprocess.SubprocessError) as exc:
            fact = {"healthy": False, "reason": "probe failed: " + type(exc).__name__}
        key = "job:" + name
        updated, action = next_state(previous.get(key, {}), fact["healthy"], now)
        updated["reason"] = fact["reason"]
        if action:
            send(f"TokenKey host job {action}: {node}/{name}; {fact['reason']}.")
            updated.update(active=action == "unhealthy", sent_at=now)
        state[key] = updated
    return state


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--env-file", type=Path, default=Path("/var/lib/tokenkey/.env"))
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    os.umask(0o077)
    config = json.loads(args.config.read_text())
    targets = config["targets"]
    jobs = config.get("jobs", [])
    if not isinstance(jobs, list) or any(name not in JOB_LIMITS for name in jobs):
        raise ValueError("unknown host job")
    if not isinstance(targets, dict) or len(targets) > 5 or not (targets or jobs):
        raise ValueError("watch requires one to five targets")
    for name, url in targets.items():
        parsed = urlparse(url)
        if not name or parsed.scheme != "https" or parsed.path != "/health" or parsed.query or parsed.fragment or not parsed.hostname or parsed.username or parsed.password:
            raise ValueError("watch requires HTTPS /health URLs without credentials")
    args.state.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with args.state.with_suffix(".lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        previous = json.loads(args.state.read_text()) if args.state.exists() else {}
        env = {}
        for line in args.env_file.read_text().splitlines():
            if "=" in line and not line.startswith("#"):
                key, value = line.split("=", 1)
                if key in {"TOKENKEY_FEISHU_WEBHOOK_URL", "TOKENKEY_FEISHU_WEBHOOK_SECRET"}:
                    env[key] = value.strip().strip('"').strip("'")
        if not all(env.get(k) for k in ["TOKENKEY_FEISHU_WEBHOOK_URL", "TOKENKEY_FEISHU_WEBHOOK_SECRET"]):
            raise ValueError("host alert credentials are missing")
        actions = []

        def send(message):
            actions.append(message)
            if not args.dry_run:
                post_feishu(message, webhook_url=env["TOKENKEY_FEISHU_WEBHOOK_URL"],
                            signing_secret=env["TOKENKEY_FEISHU_WEBHOOK_SECRET"])

        result = tick(targets, previous, int(time.time()), probe_health, send)
        result = tick_jobs(jobs, result, int(time.time()), probe_job, send, config.get("node", os.uname().nodename))
        if not args.dry_run:
            _atomic_write(args.state, json.dumps(result, sort_keys=True) + "\n")
        print(json.dumps({"dry_run": args.dry_run, "targets": result, "notifications": len(actions)}))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        # Transport exception chains may contain the private webhook URL.
        print(f"HTTPS watch failed: {type(exc).__name__}", file=sys.stderr)
        raise SystemExit(1) from None
