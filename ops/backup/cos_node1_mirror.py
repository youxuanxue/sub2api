#!/usr/bin/env python3
"""Copy an immutable COS backup snapshot to node1 with a verifiable receipt.

This controller is deliberately conservative.  It only adds files below the
selected node1 namespace; it never removes source or destination objects.  A
snapshot becomes visible only after every file has a manifest entry and the
manifest has been verified.  Remote COS sources are fetched with ``rclone``
so the operator can supply a Tencent COS S3-compatible remote in its own
credential store; no credentials are read from this repository.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import shutil
import subprocess
import sys
import tempfile
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable


SCHEMA_VERSION = 1
SNAPSHOT_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._=-]{0,127}$")
NAMESPACE_RE = re.compile(r"^(db|qa)$")
DEFAULT_BWLIMIT = "20M"
DEFAULT_MIN_FREE_PERCENT = 20.0
DEFAULT_RESERVE_MULTIPLIER = 1.25
METADATA = {"_manifest.json", "_receipt.json"}


class MirrorError(RuntimeError):
    pass


def utc_now() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def validate_namespace(value: str) -> str:
    if not NAMESPACE_RE.fullmatch(value):
        raise MirrorError("namespace must be exactly 'db' or 'qa'")
    return value


def validate_snapshot(value: str) -> str:
    if not SNAPSHOT_RE.fullmatch(value):
        raise MirrorError("snapshot id contains unsupported characters")
    return value


def safe_join(root: Path, relative: str) -> Path:
    path = (root / relative).resolve()
    if path != root.resolve() and root.resolve() not in path.parents:
        raise MirrorError(f"source contains path outside snapshot: {relative!r}")
    return path


def iter_files(root: Path) -> Iterable[tuple[str, Path]]:
    root = root.resolve()
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise MirrorError(f"symlinks are not allowed in a snapshot: {path}")
        if not path.is_file():
            continue
        relative = path.relative_to(root).as_posix()
        if relative in METADATA:
            continue
        safe_join(root, relative)
        yield relative, path


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def build_manifest(root: Path) -> dict[str, Any]:
    files: list[dict[str, Any]] = []
    total = 0
    for relative, path in iter_files(root):
        size = path.stat().st_size
        files.append({"path": relative, "bytes": size, "sha256": sha256_file(path)})
        total += size
    return {
        "schema_version": SCHEMA_VERSION,
        "created_at": utc_now(),
        "file_count": len(files),
        "total_bytes": total,
        "files": files,
    }


def write_json_atomic(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, sort_keys=True, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def check_capacity(destination: Path, required_bytes: int, *, min_free_percent: float,
                   reserve_multiplier: float) -> dict[str, Any]:
    if required_bytes < 0:
        raise MirrorError("required bytes cannot be negative")
    if not math.isfinite(min_free_percent) or not 0 <= min_free_percent <= 100:
        raise MirrorError("minimum free percentage must be between 0 and 100")
    if not math.isfinite(reserve_multiplier) or reserve_multiplier < 1:
        raise MirrorError("reserve multiplier must be finite and at least 1")
    # Read capacity from the nearest existing ancestor.  This keeps dry-run
    # mode side-effect free when the archive root has not been provisioned.
    probe = destination
    while not probe.exists() and probe != probe.parent:
        probe = probe.parent
    usage = shutil.disk_usage(probe)
    reserve = int(required_bytes * reserve_multiplier)
    free_percent = usage.free * 100 / usage.total if usage.total else 0.0
    if free_percent < min_free_percent:
        raise MirrorError(
            f"node1 free space {free_percent:.1f}% is below {min_free_percent:.1f}% floor"
        )
    if usage.free < reserve:
        raise MirrorError(
            f"node1 free bytes {usage.free} below required reserve {reserve}"
        )
    return {
        "path": str(probe),
        "total_bytes": usage.total,
        "free_bytes": usage.free,
        "free_percent": round(free_percent, 2),
        "required_bytes": required_bytes,
        "reserve_bytes": reserve,
        "min_free_percent": min_free_percent,
    }


def verify_tree(root: Path, manifest: dict[str, Any]) -> None:
    if manifest.get("schema_version") != SCHEMA_VERSION:
        raise MirrorError("unsupported manifest schema")
    expected = {entry.get("path"): entry for entry in manifest.get("files", [])}
    if any(not isinstance(name, str) for name in expected):
        raise MirrorError("manifest contains invalid path")
    actual = {relative: path for relative, path in iter_files(root)}
    if set(actual) != set(expected):
        missing = sorted(set(expected) - set(actual))
        extra = sorted(set(actual) - set(expected))
        raise MirrorError(f"manifest file set mismatch missing={missing[:3]} extra={extra[:3]}")
    total = 0
    for relative, entry in expected.items():
        if not isinstance(entry, dict) or not isinstance(entry.get("bytes"), int):
            raise MirrorError(f"invalid manifest entry: {relative}")
        path = actual[relative]
        size = path.stat().st_size
        digest = sha256_file(path)
        if size != entry["bytes"] or digest != entry.get("sha256"):
            raise MirrorError(f"checksum mismatch: {relative}")
        total += size
    if len(actual) != manifest.get("file_count") or total != manifest.get("total_bytes"):
        raise MirrorError("manifest aggregate counts do not match files")


def run_rclone(source: str, staging: Path, *, bwlimit: str, transfers: int, checkers: int) -> None:
    command = [
        "rclone", "copy", source, str(staging), "--immutable", "--checksum",
        "--bwlimit", bwlimit, "--transfers", str(transfers), "--checkers", str(checkers),
        "--no-traverse", "--stats=0", "--retries=3", "--low-level-retries=10",
    ]
    try:
        subprocess.run(command, check=True)
    except FileNotFoundError as exc:
        raise MirrorError("rclone is required for a remote COS source") from exc
    except subprocess.CalledProcessError as exc:
        raise MirrorError(f"rclone failed with exit status {exc.returncode}") from exc


def copy_snapshot(args: argparse.Namespace) -> dict[str, Any]:
    namespace = validate_namespace(args.namespace)
    snapshot = validate_snapshot(args.snapshot)
    destination_root = Path(args.destination).expanduser().resolve()
    namespace_root = destination_root / namespace
    final = namespace_root / snapshot
    if final.is_symlink():
        raise MirrorError(f"refusing symlink snapshot path: {final}")
    receipt = final / "_receipt.json"
    if final.exists():
        if not receipt.is_file():
            raise MirrorError(f"snapshot already exists without receipt: {final}")
        verify_receipt(final, receipt)
        return {"status": "already_verified", "snapshot": str(final)}

    source_path = Path(args.source).expanduser().resolve() if args.source else None
    if source_path is not None and not source_path.is_dir():
        raise MirrorError(f"local source is not a directory: {source_path}")
    source_bytes = (
        sum(path.stat().st_size for _, path in iter_files(source_path))
        if source_path
        else args.expected_bytes
    )
    if source_bytes is None:
        raise MirrorError("--expected-bytes is required for a remote COS source")
    check = check_capacity(namespace_root, source_bytes, min_free_percent=args.min_free_percent,
                           reserve_multiplier=args.reserve_multiplier)
    staging = namespace_root / ".staging" / f"{snapshot}.{uuid.uuid4().hex}"
    staging.mkdir(parents=True, exist_ok=False)
    try:
        if source_path is not None:
            for relative, source_file in iter_files(source_path):
                target = safe_join(staging, relative)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(source_file, target)
        else:
            run_rclone(args.source_uri, staging, bwlimit=args.bwlimit,
                       transfers=args.transfers, checkers=args.checkers)
        manifest = build_manifest(staging)
        verify_tree(staging, manifest)
        write_json_atomic(staging / "_manifest.json", manifest)
        receipt_payload = {
            "schema_version": SCHEMA_VERSION,
            "status": "verified",
            "namespace": namespace,
            "snapshot": snapshot,
            "source": args.source or args.source_uri,
            "created_at": utc_now(),
            "manifest": manifest,
            "capacity": check,
            "copy_policy": {
                "source_immutable": True,
                "destination_delete": False,
                "bandwidth_limit": args.bwlimit,
                "transfers": args.transfers,
                "checkers": args.checkers,
            },
        }
        write_json_atomic(staging / "_receipt.json", receipt_payload)
        # The directory rename is the publication point.  A consumer never sees
        # a partially copied snapshot at the final path.
        final.parent.mkdir(parents=True, exist_ok=True)
        os.replace(staging, final)
        return {"status": "copied", "snapshot": str(final), "receipt": str(receipt)}
    except Exception:
        shutil.rmtree(staging, ignore_errors=True)
        raise


def plan_snapshot(args: argparse.Namespace) -> dict[str, Any]:
    """Perform read-only validation used by the default CLI mode."""
    namespace = validate_namespace(args.namespace)
    snapshot = validate_snapshot(args.snapshot)
    destination_root = Path(args.destination).expanduser().resolve()
    source_path = Path(args.source).expanduser().resolve() if args.source else None
    if source_path is not None:
        if not source_path.is_dir():
            raise MirrorError(f"local source is not a directory: {source_path}")
        required = sum(path.stat().st_size for _, path in iter_files(source_path))
    else:
        required = args.expected_bytes
    if required is None:
        raise MirrorError("--expected-bytes is required for a remote COS source")
    capacity = check_capacity(destination_root / namespace, required,
                              min_free_percent=args.min_free_percent,
                              reserve_multiplier=args.reserve_multiplier)
    return {
        "status": "dry_run",
        "namespace": namespace,
        "snapshot": snapshot,
        "destination": str(destination_root / namespace / snapshot),
        "source": args.source or args.source_uri,
        "expected_bytes": required,
        "capacity": capacity,
        "would_delete": False,
        "would_start_timer": False,
    }


def verify_receipt(snapshot: Path, receipt_path: Path) -> dict[str, Any]:
    try:
        payload = json.loads(receipt_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise MirrorError(f"invalid receipt: {receipt_path}") from exc
    if payload.get("schema_version") != SCHEMA_VERSION or payload.get("status") != "verified":
        raise MirrorError("receipt is not a verified schema v1 receipt")
    if payload.get("namespace") not in ("db", "qa"):
        raise MirrorError("receipt namespace is invalid")
    if payload.get("snapshot") != snapshot.name:
        raise MirrorError("receipt snapshot does not match directory")
    verify_tree(snapshot, payload.get("manifest") or {})
    return payload


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    copy = sub.add_parser("copy", help="copy and publish one immutable snapshot")
    copy.add_argument("--namespace", required=True, choices=("db", "qa"))
    copy.add_argument("--snapshot", required=True)
    source = copy.add_mutually_exclusive_group(required=True)
    source.add_argument("--source", help="local immutable snapshot directory (test/rehearsal path)")
    source.add_argument("--source-uri", help="rclone remote URI for COS")
    copy.add_argument("--expected-bytes", type=int,
                      help="expected source bytes for remote COS capacity reservation")
    copy.add_argument("--destination", required=True, help="node1 archive root")
    copy.add_argument("--bwlimit", default=DEFAULT_BWLIMIT)
    copy.add_argument("--transfers", type=int, default=2)
    copy.add_argument("--checkers", type=int, default=4)
    copy.add_argument("--min-free-percent", type=float, default=DEFAULT_MIN_FREE_PERCENT)
    copy.add_argument("--reserve-multiplier", type=float, default=DEFAULT_RESERVE_MULTIPLIER)
    copy.add_argument("--apply", action="store_true",
                      help="perform the copy; without this flag the command is read-only")
    verify = sub.add_parser("verify", help="verify a published receipt and all file hashes")
    verify.add_argument("--snapshot", required=True)
    capacity = sub.add_parser("capacity", help="report capacity without copying")
    capacity.add_argument("--destination", required=True)
    capacity.add_argument("--required-bytes", type=int, required=True)
    capacity.add_argument("--min-free-percent", type=float, default=DEFAULT_MIN_FREE_PERCENT)
    capacity.add_argument("--reserve-multiplier", type=float, default=DEFAULT_RESERVE_MULTIPLIER)
    args = parser.parse_args(argv)
    if getattr(args, "transfers", 1) < 1 or getattr(args, "checkers", 1) < 1:
        parser.error("transfers/checkers must be positive")
    min_free_percent = getattr(args, "min_free_percent", 0.0)
    reserve_multiplier = getattr(args, "reserve_multiplier", 1.0)
    if not math.isfinite(min_free_percent) or not 0 <= min_free_percent <= 100:
        parser.error("min-free-percent must be between 0 and 100")
    if not math.isfinite(reserve_multiplier) or reserve_multiplier < 1:
        parser.error("reserve multiplier must be finite and at least 1")
    if args.command == "copy" and args.source_uri and (args.expected_bytes is None or args.expected_bytes < 0):
        parser.error("remote COS copy requires non-negative --expected-bytes")
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv or sys.argv[1:])
    try:
        if args.command == "copy":
            result = copy_snapshot(args) if args.apply else plan_snapshot(args)
        elif args.command == "verify":
            snapshot = Path(args.snapshot).expanduser().resolve()
            result = verify_receipt(snapshot, snapshot / "_receipt.json")
            result = {"status": "verified", "snapshot": str(snapshot), "file_count": result["manifest"]["file_count"]}
        else:
            result = check_capacity(Path(args.destination).expanduser().resolve(), args.required_bytes,
                                    min_free_percent=args.min_free_percent,
                                    reserve_multiplier=args.reserve_multiplier)
        print(json.dumps(result, sort_keys=True, indent=2))
        return 0
    except MirrorError as exc:
        print(f"cos-node1-mirror: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
