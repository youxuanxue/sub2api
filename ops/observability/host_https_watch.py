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
from datetime import datetime
from urllib.parse import urlparse

from https_health import probe_health
from feishu_delivery import _atomic_write, post_feishu


def build_config(role: str, hostname: str, prod_matrix: dict, domestic: dict) -> dict:
    """One coverage plan for the mainland observer and independent prod watch.

    ``prod_matrix`` is the live prod matrix (deploy/tencent/candidate-usw-20261022.json);
    its ``host_watch`` block owns watch identity, domains and expected-paused jobs.
    """
    if role not in ("prod", "cn-prod"):
        raise ValueError("WATCH_ROLE must be prod or cn-prod")
    watch = prod_matrix["host_watch"]
    allowed = {domestic["instance_name"] if role == "cn-prod" else watch["hostname"]}
    if hostname not in allowed:
        raise ValueError("host identity mismatch")
    targets = {}
    if role == "prod":
        targets["cn-prod"] = domestic["public_origin"].rstrip("/") + "/health"
    elif role == "cn-prod":
        api_domain = prod_matrix["target"]["api_domain"]
        targets["prod"] = "https://" + api_domain + "/health"
        for domain in watch["domains"]:
            if domain != api_domain:
                targets["prod:" + domain] = "https://" + domain + "/health"
        # The fallback domain routes to overseas prod; its green result must
        # never stand in for the independent cn-prod public_origin endpoint.
        targets["prod:cn-domain"] = "https://" + domestic["fallback_domain"] + "/health"
        targets["cn-prod"] = domestic["public_origin"].rstrip("/") + "/health"
    if role == "cn-prod":
        # cn-prod runs the same local QA jobs as prod (provision/cleanup/
        # archive); it just has its own host, so its own config carries the
        # list rather than hardcoding one job that predates qa-archive.
        jobs = ["tokenkey-disk-metrics"] + list(domestic.get("local_qa_jobs", []))
    else:
        jobs = ["tokenkey-pgdump", "tokenkey-disk-metrics", "tokenkey-ghcr-prune-daily"]
        jobs += ["tokenkey-qa-maintenance", "tokenkey-qa-boundary"]
        jobs += watch.get("local_qa_jobs", [])
    paused = dict(watch.get("paused_jobs", {})) if role == "prod" else {}
    return {"node": role, "targets": targets, "jobs": jobs, "paused_jobs": paused}


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


def tick(targets: dict, previous: dict, now: int, probe, send, node="unknown") -> dict:
    state = dict(previous)
    with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
        facts = dict(zip(targets, pool.map(probe, targets.values())))
    for name, fact in facts.items():
        updated, action = next_state(previous.get(name, {}), fact["healthy"], now)
        updated.update(url=targets[name], http_code=fact["http_code"],
                       reason=fact.get("error", ""))
        if action:
            label = "🔴 P0 公网入口不可用" if action == "unhealthy" else "✅ 公网入口已恢复"
            send(f"{label} ({action})\n探测节点：{node}\n目标：{name}\n地址：{targets[name]}\n"
                 f"HTTP：{fact['http_code']}\n原因：{fact.get('error') or ('健康检查通过' if fact['healthy'] else '健康检查失败')}")
            updated.update(active=action == "unhealthy", sent_at=now)
        state[name] = updated
    return state


# Allowlisted local jobs share the HTTPS watch's delivery and dedup state.
# (max age between completions, maximum normal execution time), in seconds.
JOB_LIMITS = {
    "tokenkey-pgdump": (3 * 3600, 45 * 60),
    "tokenkey-postgres-backup": (3 * 3600, 3670),
    "tokenkey-postgres-archive-check": (15 * 60, 300),
    "tokenkey-postgres-recovery": (15 * 60, 300),
    # cn-prod's own units carry a cn- infix (tokenkey-cn-postgres-backup, not
    # tokenkey-postgres-backup) because both hosts' unit namespaces coexist on
    # anyone inspecting cn-prod directly. tokenkey-postgres-recovery has no
    # such unit on either host -- probe_job reads a local evidence file
    # instead -- so the same name is shared rather than duplicated.
    "tokenkey-cn-postgres-backup": (3 * 3600, 3670),
    "tokenkey-cn-postgres-archive-check": (15 * 60, 300),
    "tokenkey-disk-metrics": (15 * 60, 60),
    "tokenkey-ghcr-prune-daily": (30 * 3600, 15 * 60),
    "tokenkey-qa-maintenance": (2 * 3600, 40 * 60 + 60),
    "tokenkey-qa-boundary": (2 * 3600, 40 * 60 + 60),
    "tokenkey-qa-provision": (2 * 3600, 960),
    "tokenkey-qa-cleanup": (20 * 60, 960),
    # Archive runs every 4 hours (ops/qa/local_maintenance_host.py SCHEDULE);
    # max age allows one missed run before alerting. A full first push of a
    # multi-GiB hour took 18.6 minutes on prod 2026-11-03; the unit's own
    # TimeoutStartSec=1800 caps a stuck run, so normal execution tracks that.
    "tokenkey-qa-archive": (9 * 3600, 1800),
    # Runs on node1 itself (ops/backup/install_node1_db_mirror.py SCHEDULE is
    # the same 4-hour cadence), monitored by node1_alert.py rather than this
    # watcher -- node1 is not one of the two hosts this script runs on. Kept
    # in the same limits table so both alert paths agree on what "healthy"
    # means for a job name, instead of drifting into two definitions.
    "tokenkey-node1-db-mirror": (9 * 3600, 5400),
}


def job_fact(service: dict, timer: dict, uptime: float, limits: tuple, *, expected_paused=False) -> dict:
    if expected_paused:
        # Empty UnitFileState = unit not installed on this host (cutover layouts).
        unit_state = timer.get("UnitFileState") or ""
        healthy = (timer.get("ActiveState") == "inactive"
                   and unit_state in ("disabled", "masked", "")
                   and service.get("ActiveState") in ("inactive", "failed"))
        return {"healthy": healthy, "reason": "intentionally paused" if healthy else "paused job unexpectedly enabled or running"}
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


def physical_backup_fact(path: Path, now: float) -> dict:
    # The producer owns freshness thresholds. Only expired *successful*
    # verification evidence is advisory; missing/failed evidence stays a fault.
    # Tencent cutover writes archive_check.json (not Stage0 health.json); accept both.
    evidence = {
        "repository_verification_missing_or_stale": "repository_verification",
        "restore_acceptance_missing_or_stale": "restore_acceptance",
        # A newly configured pgBackRest repository reports "no valid backups"
        # until its first full completes. The check runners only emit this
        # reason for a non-primary repository with an empty backup list, so
        # repo1 losing its chain still reports as a fault.
        "repository_bootstrapping": "repository_bootstrapping",
    }
    candidates = [path]
    if path.name == "health.json":
        candidates.append(path.with_name("archive_check.json"))
    last_error = "physical backup health missing/invalid"
    for candidate in candidates:
        try:
            receipt = json.loads(candidate.read_text())
            stamp = receipt.get("observed_at", receipt.get("completed_at", receipt.get("started_at", 0)))
            if not isinstance(stamp, (int, float)) or not 0 <= now - stamp <= 900:
                last_error = "physical backup health missing/stale"
                continue
            reasons = receipt.get("reasons", [])
            if reasons is None:
                reasons = []
            if not isinstance(reasons, list) or any(not isinstance(reason, str) for reason in reasons):
                raise ValueError("invalid health reasons")
            advisory = receipt.get("status") == "degraded" and bool(reasons)
            for reason in reasons:
                prior = receipt.get(evidence.get(reason, ""))
                completed = prior.get("completed_at") if isinstance(prior, dict) else None
                if (reason not in evidence or not isinstance(prior, dict)
                        or prior.get("status") != "ok"
                        or type(completed) not in (int, float) or not 0 < completed <= now):
                    advisory = False
            if advisory:
                # Bootstrap and stale-verification are both advisory but mean
                # opposite things to the operator: one resolves itself at the
                # next backup, the other needs a verification scheduled.
                if reasons == ["repository_bootstrapping"]:
                    repos = (receipt.get("repository_bootstrapping") or {}).get("repositories") or []
                    detail = ", ".join(f"repo{repo}" for repo in repos) or "新仓库"
                    note = f"备份与 WAL 检查正常；{detail} 正在初始化，首个全量完成后自动恢复"
                else:
                    note = "备份与 WAL 检查正常；恢复/仓库验证已过期，请安排按需验证：" + ", ".join(reasons)
                return {"healthy": True, "severity": "warning", "reason": note}
            healthy = receipt.get("status") == "ok" and not reasons
            return {"healthy": healthy, "severity": "info" if healthy else "error",
                    "reason": ", ".join(reasons) or receipt.get("status", "invalid receipt")}
        except (OSError, ValueError, TypeError, AttributeError):
            continue
    return {"healthy": False, "reason": last_error}


# Cleanup reclaims every hour whose node1 receipt verifies and holds back only
# the rest, so a healthy unit can hide an archive that stopped keeping up. The
# oldest held-back hour is the signal: node1 was down 2026-11-04..07 and no
# receipt-age alert existed. 24h tolerates one slow push across the 4-hourly
# archive cadence plus the 24h retention boundary.
QA_CLEANUP_RECEIPT = Path("/var/lib/tokenkey/qa-local/cleanup.json")
QA_CLEANUP_MAX_BLOCKED_AGE = 24 * 3600


def qa_cleanup_backlog_fact(path: Path, now: float) -> dict | None:
    """Return an unhealthy fact when the oldest receipt-blocked hour is too old."""
    try:
        receipt = json.loads(path.read_text())
    except (OSError, ValueError):
        return None
    cleanup = ((receipt.get("result") or {}).get("cleanup") or {}) if isinstance(receipt, dict) else {}
    oldest = cleanup.get("oldest_receipt_blocked_hour_utc") if isinstance(cleanup, dict) else None
    if not isinstance(oldest, str) or not oldest:
        return None
    try:
        stamp = datetime.fromisoformat(oldest.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return {"healthy": False, "reason": "invalid oldest_receipt_blocked_hour_utc"}
    age = now - stamp
    if age <= QA_CLEANUP_MAX_BLOCKED_AGE:
        return None
    blocked = cleanup.get("receipt_blocked_hours", 0)
    return {"healthy": False,
            "reason": f"node1 archive behind: oldest unarchived hour {oldest} is {int(age // 3600)}h old ({blocked} hours held back)"}


def probe_job(name: str, *, expected_paused=False) -> dict:
    if name == "tokenkey-postgres-recovery":
        return physical_backup_fact(Path("/var/lib/tokenkey/postgres-backup/health.json"), time.time())
    def properties(unit):
        result = subprocess.run(["systemctl", "show", unit, "--property=ActiveState,UnitFileState,Result,ExecMainStartTimestampMonotonic,ExecMainExitTimestampMonotonic"],
                                capture_output=True, text=True, timeout=5, check=True)
        return dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)
    service = properties(name + ".service")
    timer = properties(name + ".timer")
    # Read after systemd: the job may start while properties are collected.
    # Linux monotonic() shares systemd's CLOCK_MONOTONIC; /proc/uptime is
    # truncated to centiseconds and includes suspend time (CLOCK_BOOTTIME).
    fact = job_fact(service, timer, time.monotonic(), JOB_LIMITS[name], expected_paused=expected_paused)
    if name == "tokenkey-qa-cleanup" and fact["healthy"] and not expected_paused:
        backlog = qa_cleanup_backlog_fact(QA_CLEANUP_RECEIPT, time.time())
        if backlog is not None:
            return backlog
    return fact


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
        warning = fact["healthy"] and fact.get("severity") == "warning"
        if warning and (not updated.get("warning_sent_at")
                        or now - updated.get("warning_sent_at", 0) >= 86400):
            send(f"TokenKey 备份验证提醒: {node}/{name}; {fact['reason']}.")
            updated["warning_sent_at"] = now
        updated["warning_active"] = warning
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
    jobs = list(config.get("jobs", []))
    # Stage0 installer writes enabled.json; Tencent cutover activates via
    # archive_check.json. This used to gate on node == "prod" only, so
    # cn-prod's own pgBackRest backup/archive-check/recovery ran unmonitored
    # even though archive_check.json existed there too -- found 2026-11-03.
    backup_state = Path("/var/lib/tokenkey/postgres-backup")
    if (backup_state / "enabled.json").exists() or (backup_state / "archive_check.json").exists():
        node_role = config.get("node")
        if node_role == "prod":
            jobs += ["tokenkey-postgres-backup", "tokenkey-postgres-archive-check",
                     "tokenkey-postgres-recovery"]
        elif node_role == "cn-prod":
            jobs += ["tokenkey-cn-postgres-backup", "tokenkey-cn-postgres-archive-check",
                     "tokenkey-postgres-recovery"]
    paused = config.get("paused_jobs", {})
    if not isinstance(jobs, list) or any(name not in JOB_LIMITS for name in jobs):
        raise ValueError("unknown host job")
    if not isinstance(paused, dict) or any(name not in jobs or not isinstance(reason, str) or not reason.strip() for name, reason in paused.items()):
        raise ValueError("paused jobs require a monitored job and an explicit reason")
    if not isinstance(targets, dict) or len(targets) > 16 or not (targets or jobs):
        raise ValueError("watch requires at most sixteen targets and at least one target or job")
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

        result = tick(targets, previous, int(time.time()), probe_health, send, config.get("node", os.uname().nodename))
        result = tick_jobs(jobs, result, int(time.time()),
                           lambda name: probe_job(name, expected_paused=name in paused),
                           send, config.get("node", os.uname().nodename))
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
