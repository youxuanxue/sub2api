#!/usr/bin/env python3
"""Install a content-addressed QA bundle without activating any timer."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile

import coordinator_host
import source_worker
import data_machine_coordinator as policy_config
from data_machine_coordinator import CoordinatorError


FILES = ("coordinator_host.py", "data_machine_coordinator.py", "source_cleanup.py",
         "source_worker.py", "node1_archive.py", "node1_remote.py")


def units(bundle: Path, environment_file: Path):
    return {"tokenkey-qa-coordinator.service": f"""[Unit]
Description=TokenKey data-machine QA coordinator
After=network-online.target docker.service
[Service]
Type=oneshot
EnvironmentFile={environment_file}
Environment=PYTHONDONTWRITEBYTECODE=1
ExecStart=/usr/bin/python3 {bundle}/coordinator_host.py --config {bundle}/config.json --apply
TimeoutStartSec=4h
UMask=0077
Nice=19
CPUQuota=20%
MemoryMax=512M
IOSchedulingClass=idle
""", "tokenkey-qa-coordinator.timer": """[Unit]
Description=TokenKey data-machine QA schedule
[Timer]
OnBootSec=5min
OnUnitInactiveSec=5min
AccuracySec=30s
[Install]
WantedBy=timers.target
"""}


def install(config, binary: Path, *, role: str, base: Path, environment_file: Path | None, apply=False):
    if role not in ("data", "source") or not policy_config.safe_path(str(base)):
        raise CoordinatorError("invalid installation role or base path")
    if socket.gethostname() != config["hostname"]:
        raise CoordinatorError("installation host identity mismatch")
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    if digest != config["binary_sha256"]:
        raise CoordinatorError("binary checksum mismatch")
    if role == "data":
        coordinator_host.validate_config(config)
        if not environment_file or not policy_config.safe_path(str(environment_file)) or environment_file.is_symlink():
            raise CoordinatorError("data coordinator requires a root-only PG environment file")
        info = environment_file.stat()
        if info.st_uid != 0 or info.st_mode & 0o077:
            raise CoordinatorError("PG environment file must be root-owned and private")
    else:
        source_worker.validate_config(config)
    here = Path(__file__).parent
    policy = here.parent / "backup" / "node1_storage_policy.py"
    material = [binary.read_bytes(), json.dumps(config, sort_keys=True).encode()]
    material.extend((here / name).read_bytes() for name in FILES)
    material.append(policy.read_bytes())
    bundle = base / hashlib.sha256(b"".join(material)).hexdigest()[:20]
    installed_config = dict(config, binary=str(bundle / "qa-local-maintenance"))
    plan = {"bundle": str(bundle), "hostname": config["hostname"], "role": role,
            "apply": apply, "timer_enabled": False,
            "worker": str(bundle / "source_worker.py"), "worker_config": str(bundle / "config.json")}
    if not apply:
        return plan
    if os.geteuid() != 0:
        raise CoordinatorError("installation requires root")
    base.mkdir(parents=True, exist_ok=True, mode=0o700)
    if bundle.exists():
        raise CoordinatorError("bundle already installed; use its existing installation receipt")
    with tempfile.TemporaryDirectory(prefix=".qa-install-", dir=base) as scratch:
        staged = Path(scratch) / "bundle"
        staged.mkdir(mode=0o700)
        for name in FILES:
            shutil.copyfile(here / name, staged / name)
        shutil.copyfile(binary, staged / "qa-local-maintenance")
        (staged / "qa-local-maintenance").chmod(0o700)
        (staged / "backup").mkdir()
        shutil.copyfile(policy, staged / "backup" / policy.name)
        (staged / "config.json").write_text(json.dumps(installed_config, sort_keys=True) + "\n")
        (staged / "config.json").chmod(0o600)
        files = units(bundle, environment_file) if role == "data" else {}
        for name, body in files.items():
            destination = Path("/etc/systemd/system") / name
            if destination.exists() or destination.is_symlink():
                raise CoordinatorError("refusing to replace an existing coordinator unit")
            (staged / name).write_text(body)
        staged.rename(bundle)
    if role == "data":
        subprocess.run(["systemd-analyze", "verify", str(bundle / "tokenkey-qa-coordinator.service"),
                        str(bundle / "tokenkey-qa-coordinator.timer")], check=True, capture_output=True)
        for name in files:
            shutil.copyfile(bundle / name, Path("/etc/systemd/system") / name)
        subprocess.run(["systemctl", "daemon-reload"], check=True)
    return plan


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--role", choices=("data", "source"), required=True)
    parser.add_argument("--base", type=Path, default=Path("/usr/local/lib/tokenkey/qa-coordinator"))
    parser.add_argument("--environment-file", type=Path)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        result = install(json.loads(args.config.read_text()), args.binary, role=args.role, base=args.base,
                         environment_file=args.environment_file, apply=args.apply)
        print(json.dumps(result, sort_keys=True))
        return 0
    except (ValueError, OSError, subprocess.SubprocessError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
