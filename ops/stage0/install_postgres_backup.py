#!/usr/bin/env python3
"""Prepare/install/roll back the approved physical-backup host bundle.

Default is a plan. --apply requires the separately approved production window.
Image load and CloudFormation creation precede this entry point.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import time
from pathlib import Path

from postgres_backup import (
    ARCHIVE_TIMEOUT_SECONDS,
    INCREMENTAL_CALENDAR,
    BackupError,
    atomic_json,
    configuration,
    run,
)

ROOT = Path("/var/lib/tokenkey")
STATE = ROOT / "postgres-backup"
CONFIG = ROOT / "postgres-backup-config"
LIB = Path("/usr/local/lib/tokenkey/postgres-backup")
UNITS = Path("/etc/systemd/system")
KEYS = (
    "POSTGRES_IMAGE",
    "POSTGRES_PULL_POLICY",
    "POSTGRES_ARCHIVE_MODE",
    "POSTGRES_ARCHIVE_COMMAND",
    "POSTGRES_ARCHIVE_TIMEOUT",
)


def patch_env(content: str, values: dict) -> str:
    lines = [line for line in content.splitlines() if line.split("=", 1)[0] not in KEYS]
    return (
        "\n".join(lines + [f'{key}="{value}"' for key, value in values.items()]) + "\n"
    )


def units(script: Path) -> dict[str, str]:
    result = {}
    for name, command, timer, deadline in (
        (
            "backup",
            "backup",
            f"OnCalendar={INCREMENTAL_CALENDAR}\nPersistent=true",
            3670,
        ),
        ("archive-check", "check", "OnBootSec=2min\nOnUnitActiveSec=5min", 300),
    ):
        base = "tokenkey-postgres-" + name
        result[base + ".service"] = f"""[Unit]
Description=TokenKey PostgreSQL {command}
After=docker.service network-online.target
[Service]
Type=oneshot
ExecStart=/usr/bin/python3 {script} {command}
TimeoutStartSec={deadline}
UMask=0077
Nice=10
"""
        # CPU/IO limits on docker exec do not limit the child in PostgreSQL's
        # cgroup. Production pgBackRest process-max=2 is the honest first limit.
        result[base + ".timer"] = f"""[Unit]
Description=TokenKey PostgreSQL {command} schedule
[Timer]
{timer}
AccuracySec=15s
[Install]
WantedBy=timers.target
"""
    return result


def compose(*args: str, env: Path | None = None) -> str:
    return run(
        [
            "docker",
            "compose",
            "--env-file",
            str(env or ROOT / ".env"),
            "-f",
            str(ROOT / "docker-compose.yml"),
            "-f",
            str(ROOT / "docker-compose.prod-pg.yml"),
            *args,
        ],
        timeout=180,
    )


def wait_database() -> None:
    for _ in range(60):
        result = subprocess.run(
            [
                "docker",
                "exec",
                "tokenkey-postgres",
                "pg_isready",
                "-U",
                "tokenkey",
                "-d",
                "tokenkey",
            ],
            capture_output=True,
            check=False,
            timeout=5,
        )
        if result.returncode == 0:
            return
        time.sleep(1)
    raise BackupError("PostgreSQL readiness deadline exceeded; execute rollback")


def install(
    bucket: str, region: str, image: str, expected_host: str, apply: bool
) -> dict:
    if socket.gethostname() != expected_host:
        raise BackupError("host identity mismatch")
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", image):
        raise BackupError("use the loaded immutable ARM64 image ID")
    config = configuration(bucket, region)
    current = json.loads(run(["docker", "inspect", "tokenkey-postgres"]))[0]
    data_mounts = [
        m for m in current["Mounts"] if m["Destination"] == "/var/lib/postgresql/data"
    ]
    if len(data_mounts) != 1 or data_mounts[0]["Source"] != str(ROOT / "postgres"):
        raise BackupError("unexpected PostgreSQL data mount")
    version = run(
        ["docker", "exec", "tokenkey-postgres", "postgres", "--version"]
    ).strip()
    candidate = json.loads(run(["docker", "image", "inspect", image]))[0]
    candidate_version = run(
        [
            "docker",
            "run",
            "--rm",
            "--network",
            "none",
            "--entrypoint",
            "postgres",
            image,
            "--version",
        ]
    ).strip()
    if candidate["Architecture"] != "arm64" or candidate_version != version:
        raise BackupError(
            "candidate PostgreSQL version or architecture differs from production"
        )
    before = (ROOT / ".env").read_text()
    values = dict(
        zip(
            KEYS,
            [
                image,
                "never",
                "on",
                "pgbackrest --stanza=tokenkey archive-push %p",
                str(ARCHIVE_TIMEOUT_SECONDS),
            ],
        )
    )
    result = {
        "status": "planned",
        "host": expected_host,
        "before_image": current["Image"],
        "after_image": image,
        "postgres": version,
        "bucket": bucket,
        "region": region,
        "restart_required": True,
        "pgdump_changed": False,
    }
    if not apply:
        return result
    if os.geteuid() != 0 or (STATE / "enabled.json").exists():
        raise BackupError(
            "requires root and no existing activation; never overwrite the rollback baseline"
        )
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    # This snapshot contains secrets; it remains root-only on the source host.
    rollback = STATE / "rollback"
    rollback.mkdir(mode=0o700)
    for filename in (".env", "docker-compose.prod-pg.yml"):
        shutil.copy2(ROOT / filename, rollback / filename)
        (rollback / filename).chmod(0o600)
    atomic_json(rollback / "identity.json", {"image": current["Image"]})
    config_dir = CONFIG
    config_dir.mkdir(mode=0o755, exist_ok=True)
    (config_dir / "pgbackrest.conf").write_text(config)
    (config_dir / "pgbackrest.conf").chmod(
        0o644
    )  # only temporary instance credentials, no secrets
    source = Path(__file__).resolve().parent
    script = source / "postgres_backup.py"
    digest = hashlib.sha256(script.read_bytes()).hexdigest()[:16]
    destination = LIB / digest
    destination.mkdir(parents=True, mode=0o755, exist_ok=True)
    shutil.copy2(script, destination / script.name)
    shutil.copy2(
        source / "docker-compose.prod-pg.yml", ROOT / "docker-compose.prod-pg.yml"
    )
    pending = STATE / "candidate.env"
    pending.write_text(patch_env(before, values))
    pending.chmod(0o600)
    compose("config", "--quiet", env=pending)
    rendered_units = units(destination / script.name)
    for name, content in rendered_units.items():
        (UNITS / name).write_text(content)
    run(["systemd-analyze", "verify", *[str(UNITS / name) for name in rendered_units]])
    pending.replace(ROOT / ".env")
    # Mark before restart: any later failure has a visible rollback baseline.
    atomic_json(
        STATE / "enabled.json",
        dict(result, status="activating", started_at=time.time()),
    )
    compose("up", "-d", "--no-deps", "--pull", "never", "postgres")
    wait_database()
    run(
        [
            "docker",
            "exec",
            "--user",
            "postgres",
            "tokenkey-postgres",
            "pgbackrest",
            "--stanza=tokenkey",
            "stanza-create",
        ]
    )
    run(
        [
            "docker",
            "exec",
            "--user",
            "postgres",
            "tokenkey-postgres",
            "pgbackrest",
            "--stanza=tokenkey",
            "check",
        ],
        timeout=150,
    )
    run(["systemctl", "daemon-reload"])
    # First backup is asynchronous; the existing watch reports failures/staleness.
    run(
        [
            "systemctl",
            "enable",
            "--now",
            "tokenkey-postgres-backup.timer",
            "tokenkey-postgres-archive-check.timer",
        ]
    )
    run(["systemctl", "start", "--no-block", "tokenkey-postgres-backup.service"])
    result.update(status="parallel", started_at=time.time())
    atomic_json(STATE / "enabled.json", result)
    return result


def rollback() -> dict:
    if os.geteuid() != 0:
        raise BackupError("rollback requires root")
    baseline = STATE / "rollback"
    identity = json.loads((baseline / "identity.json").read_text())
    for name in ("backup", "archive-check"):
        # Installation may have failed before units were written or reloaded.
        for suffix in ("timer", "service"):
            unit = f"tokenkey-postgres-{name}.{suffix}"
            if (UNITS / unit).exists():
                run(["systemctl", "daemon-reload"])
                run(
                    ["systemctl", "disable", "--now", unit]
                    if suffix == "timer"
                    else ["systemctl", "stop", unit]
                )
    for filename in (".env", "docker-compose.prod-pg.yml"):
        shutil.copy2(baseline / filename, ROOT / filename)
    # The former image may have had a floating tag. Pin the saved image ID.
    environment = ROOT / ".env"
    original = environment.read_text()
    prior = {
        key: value.strip("\"'")
        for key, value in (
            line.split("=", 1) for line in original.splitlines() if "=" in line
        )
        if key in KEYS
    }
    prior["POSTGRES_IMAGE"] = identity["image"]
    environment.write_text(patch_env(original, prior))
    environment.chmod(0o600)
    overlay = ROOT / "docker-compose.prod-pg.yml"
    content = overlay.read_text()
    if re.search(r"^    image:", content, re.MULTILINE):
        content = re.sub(
            r"^    image:.*$",
            "    image: " + identity["image"],
            content,
            flags=re.MULTILINE,
        )
    else:
        content = content.replace(
            "  postgres:\n", "  postgres:\n    image: " + identity["image"] + "\n", 1
        )
    if re.search(r"^    pull_policy:", content, re.MULTILINE):
        content = re.sub(
            r"^    pull_policy:.*$",
            "    pull_policy: never",
            content,
            flags=re.MULTILINE,
        )
    else:
        content = content.replace(
            "  postgres:\n", "  postgres:\n    pull_policy: never\n", 1
        )
    overlay.write_text(content)
    compose("up", "-d", "--no-deps", "--pull", "never", "postgres")
    wait_database()
    (STATE / "enabled.json").unlink(missing_ok=True)
    # Preserve the secret-bearing evidence while allowing a later clean retry.
    baseline.rename(STATE / f"rollback-completed-{time.time_ns()}")
    return {
        "status": "rolled_back",
        "protection": "existing pgdump and EBS; physical RPO no longer claimed",
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bucket")
    parser.add_argument("--region")
    parser.add_argument("--image")
    parser.add_argument("--expected-host")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--rollback", action="store_true")
    args = parser.parse_args()
    if args.rollback:
        if not args.apply:
            raise BackupError(
                "rollback requires --apply in the approved maintenance window"
            )
        result = rollback()
    else:
        if not all((args.bucket, args.region, args.image, args.expected_host)):
            parser.error("--bucket, --region, --image and --expected-host required")
        result = install(
            args.bucket, args.region, args.image, args.expected_host, args.apply
        )
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (BackupError, OSError, ValueError, subprocess.SubprocessError) as exc:
        print(f"physical-backup install failed: {exc}", file=sys.stderr)
        sys.exit(1)
