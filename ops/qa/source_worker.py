#!/usr/bin/env python3
"""Fixed-identity application-side worker invoked by the data machine over SSH."""
from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import hashlib
import json
from pathlib import Path
import socket
import subprocess
import sys
import tempfile

import data_machine_coordinator as coordinator
import node1_archive as archive
import source_cleanup


def validate_config(config):
    if config.get("schema_version") != 1 or config.get("environment") not in ("prod", "cn-prod") or not config.get("hostname"):
        raise coordinator.CoordinatorError("invalid source config")
    coordinator._sources([config["source_id"]])
    if not coordinator.re.fullmatch(r"[0-9a-f]{64}", config.get("binary_sha256", "")):
        raise coordinator.CoordinatorError("invalid source binary digest")
    for key in ("source_root", "host_lock", "binary", "receipt_root"):
        if not coordinator.safe_path(config.get(key)):
            raise coordinator.CoordinatorError(f"invalid source path: {key}")
    remote = config["remote"]
    for key in ("jump", "target"):
        if not coordinator.re.fullmatch(r"[A-Za-z0-9_.@:-]+", remote[key]) or remote[key].startswith("-"):
            raise coordinator.CoordinatorError("invalid node1 SSH endpoint")
    for key in ("key", "remote_root"):
        if not coordinator.safe_path(remote.get(key)):
            raise coordinator.CoordinatorError("invalid node1 path")
    for key, maximum in (("rate_kbps", 20480), ("timeout", 1500)):
        if type(remote.get(key)) is not int or not 1 <= remote[key] <= maximum:
            raise coordinator.CoordinatorError(f"invalid node1 {key}")


def seal(config, hour):
    binary = Path(config["binary"])
    if hashlib.sha256(binary.read_bytes()).hexdigest() != config["binary_sha256"]:
        raise coordinator.CoordinatorError("QA binary digest mismatch")
    proc = subprocess.run([str(binary), "--phase", "seal", "--hour", hour.isoformat(),
                           "--ledger-root", str(Path(config["source_root"]) / "qa_capture_ledger"),
                           "--confirm", "tokenkey-qa-local-seal-v1"], check=True,
                          text=True, capture_output=True, timeout=30)
    value = json.loads(proc.stdout)
    if (coordinator._hour(value["source_hour"]) != hour or not value.get("runtime_ids")
            or not coordinator.re.fullmatch(r"[0-9a-f]{64}", value.get("state_digest", ""))):
        raise coordinator.CoordinatorError("capture seal identity invalid")
    return value


def run(config, request, *, clock=None):
    validate_config(config)
    if socket.gethostname() != config["hostname"]:
        raise coordinator.CoordinatorError("QA source host identity mismatch")
    source_id, environment = config["source_id"], config["environment"]
    if request.get("source_id") != source_id or request.get("environment") != environment:
        raise coordinator.CoordinatorError("QA source request identity mismatch")
    coordinator._sources([source_id])
    if environment not in ("prod", "cn-prod"):
        raise coordinator.CoordinatorError("invalid environment")
    # Source worker activation is separate from installation. A second owner
    # cannot run concurrently with the legacy provision/cleanup/archive timers.
    for phase in ("provision", "cleanup", "archive"):
        proc = subprocess.run(["systemctl", "show", f"tokenkey-qa-{phase}.timer",
                               "-p", "LoadState", "-p", "ActiveState", "-p", "UnitFileState"],
                              capture_output=True, text=True, check=False, timeout=10)
        facts = dict(line.split("=", 1) for line in proc.stdout.splitlines() if "=" in line)
        missing = facts.get("LoadState") == "not-found"
        if proc.returncode and not missing:
            raise coordinator.CoordinatorError("cannot inspect legacy QA timer owner")
        if not missing and (facts.get("ActiveState") not in ("inactive", "failed") or facts.get("UnitFileState") in ("enabled", "enabled-runtime")):
            raise coordinator.CoordinatorError("legacy QA timer owner is not disabled")
        service = subprocess.run(["systemctl", "show", f"tokenkey-qa-{phase}.service", "-p", "LoadState", "-p", "ActiveState"],
                                 capture_output=True, text=True, check=False, timeout=10)
        service_facts = dict(line.split("=", 1) for line in service.stdout.splitlines() if "=" in line)
        if service_facts.get("LoadState") != "not-found":
            if service.returncode or service_facts.get("ActiveState") not in ("inactive", "failed"):
                raise coordinator.CoordinatorError("legacy QA service is still busy")
    hour = coordinator._hour(request.get("hour", ""))
    clock = clock or (lambda: dt.datetime.now(dt.timezone.utc))
    def require_retained_hour():
        if hour + dt.timedelta(hours=25) <= clock():
            raise coordinator.CoordinatorError("QA hour expired; local retention accepts loss, not archive success")
    if request.get("command") == "archive":
        require_retained_hour()
    source_root = Path(config["source_root"]).resolve(strict=True)
    lock_path = Path(config["host_lock"])
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        before = seal(config, hour)
        if request.get("command") == "archive":
            receipt_root = Path(config["receipt_root"])
            receipt_root.mkdir(parents=True, exist_ok=True)
            remote = dict(config["remote"], key=Path(config["remote"]["key"]))
            completed = source_root / "qa_cleanup_state" / environment / source_id / (hour.strftime(archive.HOUR) + ".json")
            if completed.exists():
                local = json.loads(completed.read_text())
                result = json.loads(archive.receipt_path(receipt_root, environment, hour, source_id).read_text())
                if (local["seal_digest"] != before["state_digest"]
                        or hashlib.sha256(archive.manifest_bytes(local["manifest"])).hexdigest() != result["manifest_sha256"]
                        or (local.get("complete") and archive._hour_sources(source_root, hour, allow_empty=True))):
                    raise coordinator.CoordinatorError("source cleanup is incomplete or has late files")
                digest = archive.node1_remote.remote_manifest_digest(remote["key"], remote["jump"], remote["target"], result["remote_dir"])
                if digest != result["manifest_sha256"]:
                    raise coordinator.CoordinatorError("node1 archive changed after source cleanup")
            else:
                result = archive.push_hour(source_root, receipt_root, environment, hour, remote, source_id)
            after = seal(config, hour)
            if before["state_digest"] != after["state_digest"]:
                raise coordinator.CoordinatorError("capture changed while archiving")
            require_retained_hour()
            return dict(result, seal_digest=after["state_digest"])
        if request.get("command") == "cleanup":
            with tempfile.TemporaryDirectory(prefix="qa-source-") as scratch:
                path = Path(scratch) / "plan.json"
                path.write_text(json.dumps(request["plan"]))
                return source_cleanup.apply_source_cleanup(path, source_root, source_id,
                    apply=request.get("apply") is True, environment=environment,
                    seal_check=lambda hour: seal(config, hour))
        raise coordinator.CoordinatorError("unknown QA source command")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = run(coordinator.load_private_config(args.config), json.load(sys.stdin))
        print(json.dumps(result, sort_keys=True))
        return 0
    except (ValueError, OSError, KeyError, TypeError, archive.node1_remote.RemoteError, subprocess.SubprocessError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
