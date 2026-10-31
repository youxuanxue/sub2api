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


def copy_hour(source_root: Path, node1_root: Path, environment: str, hour: dt.datetime) -> dict:
    source_files: list[tuple[str, Path]] = []
    for kind in ("qa_blobs", "qa_dlq"):
        base = source_root / kind / hour.strftime(HOUR)
        if not base.exists():
            continue
        if base.is_symlink() or not base.is_dir():
            raise ValueError(f"hour directory is not a real directory: {base}")
        for path in sorted(base.rglob("*")):
            if path.is_symlink() or (path.exists() and not path.is_file()):
                raise ValueError(f"unsafe QA source entry: {path}")
            if path.is_file():
                source_files.append((str(path.relative_to(source_root)), path))
    if not source_files:
        raise ValueError(f"hour has no QA files: {hour.isoformat()}")

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
    result = {"schema_version": SCHEMA, "ok": True, "environment": args.environment,
              "selected_hours": [h.isoformat() for h in selected], "completed": [], "failed": []}
    for hour in selected:
        try:
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
    return run(parser.parse_args())


if __name__ == "__main__":
    raise SystemExit(main())
