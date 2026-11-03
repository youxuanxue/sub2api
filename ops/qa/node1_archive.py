#!/usr/bin/env python3
"""Copy sealed QA raw hours to node1 with an atomic, verifiable receipt.

The command deliberately has no PostgreSQL or cloud credentials.  ``node1_root``
is expected to be a separately mounted, access-controlled node1 directory.  A
failed copy leaves the source untouched and the next run retries the hour.
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile
import time
import sys

_SCRIPT_DIR = Path(__file__).resolve().parent
_POLICY_DIRS = (_SCRIPT_DIR / "backup", _SCRIPT_DIR.parent / "backup")
for _policy_dir in _POLICY_DIRS:
    if (_policy_dir / "node1_storage_policy.py").is_file():
        sys.path.insert(0, str(_policy_dir))
        break
else:
    raise ImportError("node1_storage_policy.py is not bundled")
from node1_storage_policy import (
    BLOCK_USED_PERCENT,
    EMERGENCY_USED_PERCENT,
    QA_CAP_BYTES,
    StoragePolicyError,
    WARN_USED_PERCENT,
    check_write,
    receipt_bytes,
)

sys.path.insert(0, str(_SCRIPT_DIR))
import node1_remote


HOUR = "%Y/%m/%d/%H"
SCHEMA = "qa-node1-hour-v1"


def atomic_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, name = tempfile.mkstemp(prefix="." + path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(name, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def sha256_file(path: Path) -> tuple[str, int]:
    digest = hashlib.sha256()
    size = 0
    with path.open("rb") as stream:
        while True:
            block = stream.read(1024 * 1024)
            if not block:
                break
            digest.update(block)
            size += len(block)
    return digest.hexdigest(), size


def hour_start(value: dt.datetime) -> dt.datetime:
    value = value.astimezone(dt.timezone.utc)
    return value.replace(minute=0, second=0, microsecond=0)


def hours_to_copy(source_root: Path, before: dt.datetime) -> list[dt.datetime]:
    found: set[dt.datetime] = set()
    for kind in ("qa_blobs", "qa_dlq"):
        root = source_root / kind
        if not root.exists():
            continue
        if not root.is_dir() or root.is_symlink():
            raise ValueError(f"source QA root is not a real directory: {root}")
        for year in root.iterdir():
            if year.is_symlink() or not year.is_dir() or not year.name.isdigit():
                continue
            for path in root.glob("*/*/*/*"):
                if not path.is_dir() or path.is_symlink():
                    continue
                try:
                    value = dt.datetime.strptime(str(path.relative_to(root)), HOUR).replace(tzinfo=dt.timezone.utc)
                except ValueError:
                    continue
                if value + dt.timedelta(hours=1) <= before:
                    found.add(value)
    return sorted(found)


def receipt_path(root: Path, environment: str, hour: dt.datetime) -> Path:
    return root / "receipts" / environment / (hour.strftime(HOUR) + ".json")


def _verified_manifest(target: Path, environment: str, hour: dt.datetime) -> tuple[dict, str, int]:
    manifest_path = target / "manifest.json"
    if target.is_symlink() or not target.is_dir() or manifest_path.is_symlink() or not manifest_path.is_file():
        raise ValueError(f"published QA hour is missing a safe manifest: {target}")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        raise ValueError(f"published QA manifest is invalid: {manifest_path}") from exc
    if (
        manifest.get("schema_version") != SCHEMA
        or manifest.get("environment") != environment
        or manifest.get("window_start") != hour.isoformat()
        or manifest.get("window_end") != (hour + dt.timedelta(hours=1)).isoformat()
        or not isinstance(manifest.get("files"), list)
        or manifest.get("file_count") != len(manifest["files"])
    ):
        raise ValueError(f"published QA manifest identity is invalid: {manifest_path}")
    seen: set[str] = set()
    total = 0
    for entry in manifest["files"]:
        relative = entry.get("path") if isinstance(entry, dict) else None
        if not isinstance(relative, str) or not relative or relative in seen:
            raise ValueError(f"published QA manifest path is invalid: {relative!r}")
        relative_path = Path(relative)
        if relative_path.is_absolute() or ".." in relative_path.parts:
            raise ValueError(f"published QA manifest path escapes target: {relative!r}")
        seen.add(relative)
        path = target / relative_path
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"published QA file is missing or unsafe: {path}")
        digest, size = sha256_file(path)
        if entry.get("bytes") != size or entry.get("sha256") != digest:
            raise ValueError(f"published QA checksum mismatch: {relative}")
        total += size
    if manifest.get("total_bytes") != total:
        raise ValueError(f"published QA manifest byte count is invalid: {manifest_path}")
    return manifest, *sha256_file(manifest_path)


def _receipt_from_manifest(target: Path, environment: str, hour: dt.datetime) -> dict:
    manifest, manifest_sha, manifest_size = _verified_manifest(target, environment, hour)
    return {
        "schema_version": SCHEMA,
        "ok": True,
        "environment": environment,
        "window_start": hour.isoformat(),
        "window_end": (hour + dt.timedelta(hours=1)).isoformat(),
        "file_count": manifest["file_count"],
        "total_bytes": manifest["total_bytes"],
        "manifest_sha256": manifest_sha,
        "manifest_bytes": manifest_size,
        "completed_at": dt.datetime.now(dt.timezone.utc).isoformat(),
    }


def push_hour(source_root: Path, receipt_root: Path, environment: str,
              hour: dt.datetime, remote: dict) -> dict:
    """Archive one hour to a remote node1 over SSH instead of a mounted path.

    Identical guarantees to copy_hour: a manifest with a per-file sha256, a
    staging directory, capacity checked before any bytes move, and a receipt
    written only after the archive is verifiably complete. Two things differ.

    First, capacity is measured ON NODE1. The local policy reads the local
    filesystem, which here is the production disk and says nothing about the
    archive volume.

    Second, completeness is confirmed by the remote side recomputing the
    manifest digest. A truncated or reordered stream yields a different digest,
    so a receipt is never written on the strength of an exit code alone.

    The receipt stays local on purpose: the production cleanup gate reads it to
    decide whether an hour may be deleted, and routing that read over the
    network would tie log retention to node1 being reachable.
    """
    source_files = _hour_sources(source_root, hour)
    receipt = receipt_path(receipt_root, environment, hour)
    remote_dir = f"{remote['remote_root'].rstrip('/')}/qa-raw/{environment}/{hour.strftime(HOUR)}"

    repush_reason = ""
    if receipt.exists():
        try:
            existing = json.loads(receipt.read_text(encoding="utf-8"))
            valid = (existing.get("schema_version") == SCHEMA
                     and existing.get("ok") is True)
        except (OSError, ValueError, json.JSONDecodeError):
            existing, valid = None, False
        if not valid:
            raise ValueError(f"existing node1 receipt is invalid: {receipt}")
        # A receipt is a local claim; node1 holding the manifest is the
        # evidence. An earlier local-copy run wrote valid-looking receipts for
        # hours that never reached node1, which made this path skip them as
        # done while the cleanup gate blocked them forever. Read the digest
        # back and re-push when it does not match.
        remote_digest = node1_remote.remote_manifest_digest(
            remote["key"], remote["jump"], remote["target"], remote_dir)
        if remote_digest and remote_digest == existing.get("manifest_sha256"):
            return existing | {"already_completed": True}
        reason = ("node1 holds no manifest for this hour" if not remote_digest
                  else "node1 manifest digest does not match the local receipt")
        # Re-push below. The receipt is rewritten only after the new push
        # verifies, so a failure here leaves the gate blocking exactly as it
        # did before.
        repush_reason = reason

    required_bytes = sum(path.stat().st_size for _, path in source_files)
    capacity = node1_remote.remote_capacity(
        remote["key"], remote["jump"], remote["target"], remote["remote_root"])
    try:
        check_remote_write(required_bytes=required_bytes,
                           total_bytes=capacity["total_bytes"],
                           free_bytes=capacity["free_bytes"],
                           dataset_used_bytes=capacity["qa_used_bytes"],
                           dataset_cap_bytes=QA_CAP_BYTES)
    except StoragePolicyError as exc:
        raise ValueError(str(exc)) from exc

    staging = Path(tempfile.mkdtemp(prefix=f".push-{hour.strftime('%Y%m%d%H')}-",
                                    dir=receipt_root))
    try:
        manifest, manifest_sha, manifest_size, total = _stage_hour(staging, source_files,
                                                                   environment, hour)
        summary = node1_remote.push_directory(
            staging, key=remote["key"], jump=remote["jump"], target=remote["target"],
            remote_dir=remote_dir, rate_kbps=remote["rate_kbps"],
            timeout=remote["timeout"])
        # The digest is the proof. Byte and file counts are compared too so a
        # stream that somehow matched the manifest but dropped payload files
        # cannot be mistaken for a complete hour.
        if summary.get("manifest_sha256") != manifest_sha:
            raise ValueError(
                f"node1 manifest digest mismatch: local={manifest_sha} "
                f"remote={summary.get('manifest_sha256')}")
        if int(summary.get("file_count", -1)) != manifest["file_count"]:
            raise ValueError(f"node1 file count mismatch: local={manifest['file_count']} "
                             f"remote={summary.get('file_count')}")
        if int(summary.get("total_bytes", -1)) != total:
            raise ValueError(f"node1 byte count mismatch: local={total} "
                             f"remote={summary.get('total_bytes')}")
        result = {"schema_version": SCHEMA, "ok": True, "environment": environment,
                  "window_start": hour.isoformat(),
                  "window_end": (hour + dt.timedelta(hours=1)).isoformat(),
                  "file_count": manifest["file_count"], "total_bytes": total,
                  "manifest_sha256": manifest_sha, "manifest_bytes": manifest_size,
                  "transport": "ssh-tar", "remote_dir": remote_dir,
                  "remote_already_present": bool(summary.get("already_present")),
                  "completed_at": dt.datetime.now(dt.timezone.utc).isoformat()}
        if repush_reason:
            result["repushed_because"] = repush_reason
        atomic_json(receipt, result)
        return result
    finally:
        shutil.rmtree(staging, ignore_errors=True)


def check_remote_write(*, required_bytes: int, total_bytes: int, free_bytes: int,
                       dataset_used_bytes: int, dataset_cap_bytes: int) -> dict:
    """Same thresholds as the local policy, against node1's reported space."""
    if min(required_bytes, total_bytes, free_bytes, dataset_used_bytes, dataset_cap_bytes) < 0:
        raise StoragePolicyError("storage sizes cannot be negative")
    if total_bytes <= 0:
        raise StoragePolicyError("node1 reports zero capacity")
    projected_dataset = dataset_used_bytes + required_bytes
    if projected_dataset > dataset_cap_bytes:
        raise StoragePolicyError(
            f"dataset cap exceeded: projected={projected_dataset} cap={dataset_cap_bytes}")
    projected_used_percent = (total_bytes - (free_bytes - required_bytes)) * 100.0 / total_bytes
    if projected_used_percent >= EMERGENCY_USED_PERCENT:
        raise StoragePolicyError(
            f"node1 emergency threshold reached at {projected_used_percent:.1f}% used")
    if projected_used_percent >= BLOCK_USED_PERCENT:
        raise StoragePolicyError(
            f"node1 write blocked at {projected_used_percent:.1f}% used")
    return {"projected_used_percent": projected_used_percent,
            "warn": projected_used_percent >= WARN_USED_PERCENT}


def _hour_sources(source_root: Path, hour: dt.datetime) -> list[tuple[str, Path]]:
    """Every regular file under one sealed hour, with its path relative to root.

    A sealed hour is not flat: capture shards it into 256 hex subdirectories
    (00..ff) with the blobs one level below, so one cn-prod hour holds 4,967
    files across 256 shards. Directories are therefore expected structure, not
    a hazard; only symlinks and non-regular entries (devices, sockets, fifos)
    are refused, which is what the original check was protecting against.
    """
    source_files: list[tuple[str, Path]] = []
    for kind in ("qa_blobs", "qa_dlq"):
        base = source_root / kind / hour.strftime(HOUR)
        if not base.exists():
            continue
        if base.is_symlink() or not base.is_dir():
            raise ValueError(f"hour directory is not a real directory: {base}")
        for path in sorted(base.rglob("*")):
            if path.is_symlink():
                raise ValueError(f"unsafe QA source entry (symlink): {path}")
            if path.is_dir():
                continue
            if not path.is_file():
                raise ValueError(f"unsafe QA source entry (not a regular file): {path}")
            source_files.append((str(path.relative_to(source_root)), path))
    if not source_files:
        raise ValueError(f"hour has no QA files: {hour.isoformat()}")
    return source_files


def _stage_hour(staging: Path, source_files: list[tuple[str, Path]],
                environment: str, hour: dt.datetime) -> tuple[dict, str, int, int]:
    """Copy sources into staging with checksums and write the manifest."""
    entries = []
    total = 0
    for relative, source in source_files:
        destination = staging / relative
        destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        digest, size = sha256_file(source)
        shutil.copyfile(source, destination)
        copied_digest, copied_size = sha256_file(destination)
        if (digest, size) != (copied_digest, copied_size):
            raise IOError(f"checksum mismatch after copy: {relative}")
        os.chmod(destination, 0o600)
        entries.append({"path": relative, "bytes": size, "sha256": digest})
        total += size
    manifest = {"schema_version": SCHEMA, "environment": environment,
                "window_start": hour.isoformat(),
                "window_end": (hour + dt.timedelta(hours=1)).isoformat(),
                "file_count": len(entries), "total_bytes": total, "files": entries}
    manifest_path = staging / "manifest.json"
    manifest_path.write_bytes(
        json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode() + b"\n")
    os.chmod(manifest_path, 0o600)
    manifest_sha, manifest_size = sha256_file(manifest_path)
    return manifest, manifest_sha, manifest_size, total


def copy_hour(source_root: Path, node1_root: Path, environment: str, hour: dt.datetime) -> dict:
    source_files = _hour_sources(source_root, hour)

    target = node1_root / "qa-raw" / environment / hour.strftime(HOUR)
    receipt = receipt_path(node1_root, environment, hour)
    if receipt.exists():
        try:
            existing = json.loads(receipt.read_text(encoding="utf-8"))
            if existing.get("schema_version") == SCHEMA and existing.get("ok") is True:
                _verified_manifest(target, environment, hour)
                return existing | {"already_completed": True}
        except (OSError, ValueError, json.JSONDecodeError):
            pass
        raise ValueError(f"existing node1 receipt is invalid: {receipt}")

    # A power loss or full filesystem can occur after the immutable hour
    # directory is published but before its external receipt is atomically
    # written. Verify that directory and complete the receipt on retry instead
    # of copying over it or leaving cleanup blocked forever.
    if target.exists():
        recovered = _receipt_from_manifest(target, environment, hour)
        atomic_json(receipt, recovered)
        return recovered | {"recovered_receipt": True}

    required_bytes = sum(path.stat().st_size for _, path in source_files)
    try:
        check_write(
            filesystem_root=node1_root,
            dataset_used_bytes=receipt_bytes(node1_root / "receipts", kind="qa"),
            required_bytes=required_bytes,
            dataset_cap_bytes=QA_CAP_BYTES,
        )
    except StoragePolicyError as exc:
        raise ValueError(str(exc)) from exc

    node1_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    staging_parent = target.parent
    staging_parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    staging = Path(tempfile.mkdtemp(prefix=f".{hour.strftime('%Y%m%d%H')}-", dir=staging_parent))
    entries = []
    try:
        total = 0
        for relative, source in source_files:
            destination = staging / relative
            destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            digest, size = sha256_file(source)
            shutil.copyfile(source, destination)
            copied_digest, copied_size = sha256_file(destination)
            if (digest, size) != (copied_digest, copied_size):
                raise IOError(f"checksum mismatch after copy: {relative}")
            os.chmod(destination, 0o600)
            entries.append({"path": relative, "bytes": size, "sha256": digest})
            total += size
        manifest = {"schema_version": SCHEMA, "environment": environment,
                    "window_start": hour.isoformat(),
                    "window_end": (hour + dt.timedelta(hours=1)).isoformat(),
                    "file_count": len(entries), "total_bytes": total, "files": entries}
        manifest_bytes = json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()
        manifest_path = staging / "manifest.json"
        manifest_path.write_bytes(manifest_bytes + b"\n")
        os.chmod(manifest_path, 0o600)
        manifest_sha, manifest_size = sha256_file(manifest_path)
        os.replace(staging, target)
        result = {"schema_version": SCHEMA, "ok": True, "environment": environment,
                  "window_start": hour.isoformat(),
                  "window_end": (hour + dt.timedelta(hours=1)).isoformat(),
                  "file_count": len(entries), "total_bytes": total,
                  "manifest_sha256": manifest_sha, "manifest_bytes": manifest_size,
                  "completed_at": dt.datetime.now(dt.timezone.utc).isoformat()}
        atomic_json(receipt, result)
        return result
    except Exception:
        shutil.rmtree(staging, ignore_errors=True)
        raise


def run(args: argparse.Namespace) -> int:
    source = Path(args.source_root).resolve()
    node1 = Path(args.node1_root).resolve()
    if source == node1 or source in node1.parents or node1 in source.parents:
        raise ValueError("source and node1 roots must be separate")
    before = dt.datetime.now(dt.timezone.utc) - dt.timedelta(hours=1)
    if args.before:
        before = dt.datetime.fromisoformat(args.before.replace("Z", "+00:00"))
    selected = hours_to_copy(source, hour_start(before))

    # node1_root is the receipt root in both modes. In remote mode the payload
    # goes to node1 over SSH while receipts stay local, because the cleanup
    # gate reads them and must not depend on node1 being reachable.
    remote = None
    if args.remote_target:
        remote = {"key": Path(args.remote_key), "jump": args.remote_jump,
                  "target": args.remote_target, "remote_root": args.remote_root,
                  "rate_kbps": args.rate_kbps, "timeout": args.push_timeout}
        if not remote["key"].is_file():
            raise ValueError(f"node1 ssh key is missing: {remote['key']}")

    result = {"schema_version": SCHEMA, "ok": True, "environment": args.environment,
              "transport": "ssh-tar" if remote else "local-copy",
              "selected_hours": [h.isoformat() for h in selected], "completed": [], "failed": []}
    for hour in selected:
        try:
            if remote:
                result["completed"].append(
                    push_hour(source, node1, args.environment, hour, remote))
            else:
                result["completed"].append(copy_hour(source, node1, args.environment, hour))
        except Exception as exc:
            result["failed"].append({"window_start": hour.isoformat(), "error": str(exc)})
    result["ok"] = not result["failed"]
    atomic_json(node1 / "receipts" / args.environment / "last-run.json", result)
    print(json.dumps(result, sort_keys=True))
    return 0 if result["ok"] else 2


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run", choices=("run",))
    parser.add_argument("--source-root", required=True)
    parser.add_argument("--node1-root", required=True)
    parser.add_argument("--environment", choices=("prod", "cn-prod"), required=True)
    parser.add_argument("--before")
    parser.add_argument("--remote-target", default="",
                        help="node1 ssh destination, e.g. root@10.10.2.11. Without it "
                             "the archive is a local copy into --node1-root.")
    parser.add_argument("--remote-jump", default=node1_remote.DEFAULT_JUMP,
                        help="jump host; the cloud security group only allows 22/443/8080")
    parser.add_argument("--remote-key", default=str(node1_remote.DEFAULT_KEY))
    parser.add_argument("--remote-root", default=node1_remote.DEFAULT_REMOTE_ROOT)
    parser.add_argument("--rate-kbps", type=int, default=node1_remote.DEFAULT_RATE_KBPS,
                        help="stream rate cap in KiB/s; 0 disables (needs pv installed)")
    parser.add_argument("--push-timeout", type=int, default=1800)
    return run(parser.parse_args())


if __name__ == "__main__":
    raise SystemExit(main())
