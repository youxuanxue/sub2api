#!/usr/bin/env python3
"""Guarded host-only pgdump cutover; default plan, prepare, then verified activate.

Upload secrets over verified SSH into a root-only bundle, never SSM --with.
Prepare installs an inactive private release and uploads the latest existing dump.
Run the existing restore canary with that release's config; activate only after
its verified receipt. Gateway, database, Redis and timers are never restarted.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile

from pgdump_storage import Storage, StorageError, private_file, OBJECT_RE
from edge_routing_matrix import materialize_hetzner_target

ROOT = Path("/var/lib/tokenkey")
CONFIG = Path("/etc/tokenkey/pgdump-storage.json")
RELEASES = Path("/etc/tokenkey/pgdump")
INSTALLED = Path("/usr/local/bin/tokenkey-pgdump.sh")
HELPER = Path("/usr/local/lib/tokenkey/pgdump_storage.py")


def command(args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise StorageError("host readiness command failed")
    return result.stdout.strip()


def identity(target, bundle):
    if target == "prod":
        matrix = json.loads((bundle / "prod-target-hetzner.json").read_text())
        assert matrix["platform"] == "hetzner" and matrix["target"]["deployable"] is True
        expected = matrix["target"]["instance_name"]
        if command(["findmnt", "-n", "-o", "TARGET", "--mountpoint", str(ROOT)]) != str(ROOT):
            raise StorageError("prod data volume not mounted")
    else:
        edge = target.split(":", 1)[1]
        matrix = json.loads((bundle / "edge-targets-hetzner.json").read_text())
        assert matrix["platform"] == "hetzner"
        row = materialize_hetzner_target(edge, matrix, matrix["targets"][edge])
        assert row["deployable"] is True
        expected = row["instance_name"]
    if socket.gethostname() != expected:
        raise StorageError("host identity mismatch")
    if command(["systemctl", "is-active", "tokenkey-pgdump.timer"]) != "active":
        raise StorageError("backup timer not active")
    if command(["systemctl", "show", "tokenkey-pgdump.service", "-p", "ActiveState", "--value"]) != "inactive":
        raise StorageError("backup currently running; wait for completion")
    if target == "prod":
        devices = list(Path("/dev/disk/by-id").glob("scsi-0HC_Volume_*"))
        mounted = Path(command(["findmnt", "-n", "-o", "SOURCE", "--mountpoint", str(ROOT)])).resolve()
        if mounted not in [device.resolve() for device in devices]:
            raise StorageError("prod mount is not a Hetzner data volume")


def atomic_copy(source, destination, mode):
    destination.parent.mkdir(parents=True, exist_ok=True)
    descriptor, name = tempfile.mkstemp(prefix=".pgdump-", dir=destination.parent)
    os.close(descriptor)
    stage = Path(name)
    try:
        shutil.copyfile(source, stage)
        stage.chmod(mode)
        os.replace(stage, destination)
    finally:
        stage.unlink(missing_ok=True)


def restore_previous(backup, release, live_env):
    manifest = json.loads((backup / "manifest.json").read_text())
    for destination, name, existed in manifest:
        if destination == str(live_env):
            previous = [line for line in (backup / name).read_text().splitlines() if line.startswith("TOKENKEY_PGDUMP_S3_URI=")]
            current = [line for line in live_env.read_text().splitlines() if not line.startswith("TOKENKEY_PGDUMP_S3_URI=")]
            env_stage = release / "rollback.env"
            env_stage.write_text("\n".join(current + previous) + "\n")
            env_stage.chmod(0o600)
            atomic_copy(env_stage, live_env, 0o600)
        elif existed:
            atomic_copy(backup / name, Path(destination), 0o755 if destination == str(INSTALLED) else 0o600)
        else:
            Path(destination).unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--release", required=True)
    parser.add_argument("--operation", choices=["plan", "prepare", "activate", "rollback"], default="plan")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-z0-9-]{1,64}", args.release) or not re.fullmatch(r"prod|edge:[a-z][a-z0-9]{1,15}", args.target):
        raise StorageError("invalid release/target")
    if os.geteuid() != 0:
        raise StorageError("host installation requires root")
    bundle = args.bundle.resolve()
    identity(args.target, bundle)
    # systemctl is-active returns nonzero for a completed oneshot; test the
    # service state without interpreting inactivity as a readiness failure.
    private_file(bundle / "pgdump-storage.json")
    storage = Storage(target=args.target, config_path=bundle / "pgdump-storage.json")
    release = RELEASES / args.release
    backup = release / "previous"
    live_env = ROOT / ".env"
    if args.operation == "plan":
        print(json.dumps({"target": args.target, "operation": "plan", "bucket": storage.bucket,
                          "prefix": storage.prefix, "gateway_restart": False}))
        return 0
    if args.operation == "prepare":
        if release.exists():
            raise StorageError("release already prepared; do not overwrite recovery evidence")
        dumps = [p for p in (ROOT / "pgdump").glob("tokenkey-*.sql.gz") if OBJECT_RE.fullmatch(p.name) and p.is_file()]
        if not dumps:
            raise StorageError("no existing local dump")
        dump = max(dumps, key=lambda path: path.stat().st_mtime)
        age = dt.datetime.now(dt.timezone.utc).timestamp() - dump.stat().st_mtime
        if age < -120 or age > 4 * 3600:
            raise StorageError("local dump stale or future-dated")
        release.mkdir(mode=0o700, parents=True)
        for name in ["credentials", "sse-c.key"]:
            source = storage.credentials if name == "credentials" else storage.sse_key
            (release / name).write_bytes(private_file(source))
            (release / name).chmod(0o600)
        config = json.loads(private_file(bundle / "pgdump-storage.json"))
        config.update(credentials_file=str(release / "credentials"), sse_c_key_file=str(release / "sse-c.key"))
        config_path = release / "storage.json"
        config_path.write_text(json.dumps(config))
        config_path.chmod(0o600)
        storage = Storage(target=args.target, config_path=config_path)
        uri = storage.upload(dump)
        # Hash comparison happens in the restore canary using this exact source.
        with dump.open("rb") as handle:
            digest = hashlib.file_digest(handle, "sha256").hexdigest()
        artifact = {"source": str(dump), "s3_uri": uri, "size": dump.stat().st_size, "sha256": digest}
        (release / "artifact.json").write_text(json.dumps(artifact))
        print(json.dumps({"target": args.target, "operation": "prepare", "config": str(config_path),
                          "source": str(dump), "s3_uri": uri}))
        return 0
    if args.operation == "rollback":
        restore_previous(backup, release, live_env)
        print(json.dumps({"operation": "rollback", "target": args.target, "gateway_restart": False}))
        return 0
    storage = Storage(target=args.target, config_path=release / "storage.json")
    receipt_path = ROOT / "pgdump-canary" / args.release / "latest.json"
    receipt = json.loads(receipt_path.read_text())
    artifact = json.loads((release / "artifact.json").read_text())
    completed = dt.datetime.fromisoformat(receipt["completed_at"].replace("Z", "+00:00"))
    if (receipt.get("target") != args.target or receipt.get("source_s3_uri") != artifact["s3_uri"]
            or receipt.get("source_endpoint") != storage.endpoint or receipt.get("artifact_sha256") != artifact["sha256"]
            or receipt.get("cleanup_verified") is not True or receipt.get("source_mutated") is not False
            or receipt.get("s3_round_trip_verified") is not True
            or not 0 <= (dt.datetime.now(dt.timezone.utc) - completed).total_seconds() <= 3600):
        raise StorageError("fresh verified Hetzner restore receipt required before activation")
    if backup.exists():
        raise StorageError("activation already attempted; use rollback before another release")
    next_fire = command(["systemctl", "show", "tokenkey-pgdump.timer", "-p", "NextElapseUSecRealtime", "--value"])
    epoch = int(command(["date", "-d", next_fire, "+%s"]))
    if epoch - dt.datetime.now(dt.timezone.utc).timestamp() < 300:
        raise StorageError("backup timer fires soon; wait until after completion")
    command(["bash", "-n", str(bundle / "tokenkey-pgdump.sh")])
    backup.mkdir(mode=0o700)
    manifest = []
    for destination, name in [(CONFIG, "storage.json"), (INSTALLED, "pgdump.sh"), (HELPER, "storage.py"),
                              (live_env, "host.env"), (ROOT / "pgdump-canary/latest.json", "canary.json")]:
        existed = destination.exists()
        manifest.append((str(destination), name, existed))
        if existed:
            atomic_copy(destination, backup / name, 0o600)
    (backup / "manifest.json").write_text(json.dumps(manifest))
    uri = f"s3://{storage.bucket}/{storage.prefix}"
    lines = [line for line in live_env.read_text().splitlines() if not line.startswith("TOKENKEY_PGDUMP_S3_URI=")]
    lines.append("TOKENKEY_PGDUMP_S3_URI=" + uri)
    env_stage = release / "next.env"
    env_stage.write_text("\n".join(lines) + "\n")
    env_stage.chmod(0o600)
    try:
        # Install code while AWS URI remains usable, then switch the private
        # identity before updating its descriptive .env coordinate and receipt.
        atomic_copy(bundle / "pgdump_storage.py", HELPER, 0o600)
        atomic_copy(bundle / "tokenkey-pgdump.sh", INSTALLED, 0o755)
        atomic_copy(release / "storage.json", CONFIG, 0o600)
        atomic_copy(env_stage, live_env, 0o600)
        atomic_copy(receipt_path, ROOT / "pgdump-canary/latest.json", 0o600)
    except Exception:
        restore_previous(backup, release, live_env)
        raise
    print(json.dumps({"operation": "activate", "target": args.target, "s3_uri": uri,
                      "rollback_directory": str(backup), "gateway_restart": False}))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        # Avoid serializing config values, provider responses or credential text.
        print(json.dumps({"ok": False, "error_type": type(exc).__name__,
                          "reason": str(exc) if isinstance(exc, StorageError) else "host installation failed"}))
        raise SystemExit(2)
