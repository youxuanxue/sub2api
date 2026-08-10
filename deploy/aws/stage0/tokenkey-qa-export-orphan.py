#!/usr/bin/env python3
"""Resolve and safely clean closed QA export crash-orphan files."""
from __future__ import annotations

import argparse
import calendar
import datetime as dt
import hashlib
import json
import os
import pathlib
import posixpath
import re
import stat
import sys
import tempfile
from typing import Any

CONFIRM_PREFIX = "tokenkey-prod-qa-export-orphan-apply-v1:"


class ExportOrphanError(RuntimeError):
    """A fail-closed export-orphan operation error."""


def _runtime_from_inspect(container: str, default_host: str, payload: Any) -> dict[str, str]:
    try:
        item = payload[0]
    except (KeyError, IndexError, TypeError) as exc:
        raise ExportOrphanError(f"active app inspect JSON is invalid: {exc}") from exc
    configured = ""
    for value in item.get("Config", {}).get("Env", []):
        if isinstance(value, str) and value.startswith("QA_EXPORT_TMP_DIR="):
            configured = value.split("=", 1)[1].strip()
    container_dir = configured or "/app/data/qa_exports_tmp"
    if not container_dir.startswith("/") or ".." in container_dir.split("/"):
        raise ExportOrphanError("QA_EXPORT_TMP_DIR must be an absolute normalized container path")
    container_dir = posixpath.normpath(container_dir)
    candidates: list[tuple[int, str, str]] = []
    for mount in item.get("Mounts", []):
        if not isinstance(mount, dict) or mount.get("Type") != "bind" or mount.get("RW") is not True:
            continue
        destination = posixpath.normpath(str(mount.get("Destination", "")))
        source = os.path.normpath(str(mount.get("Source", "")))
        if not destination.startswith("/") or not source.startswith("/"):
            continue
        if container_dir == destination or container_dir.startswith(destination.rstrip("/") + "/"):
            candidates.append((len(destination), destination, source))
    if not candidates:
        raise ExportOrphanError("effective QA export temp directory is not backed by a writable bind mount")
    _, destination, source = max(candidates)
    relative = posixpath.relpath(container_dir, destination)
    host_dir = source if relative == "." else os.path.normpath(os.path.join(source, relative))
    if host_dir != source and not host_dir.startswith(source.rstrip(os.sep) + os.sep):
        raise ExportOrphanError("effective QA export temp host path escapes its bind source")
    if not configured and host_dir != os.path.normpath(default_host):
        raise ExportOrphanError("default QA export temp host path does not match the approved bind")
    image = item.get("Config", {}).get("Image")
    if not isinstance(image, str) or not image.strip():
        raise ExportOrphanError("active app image is missing")
    return {
        "active_container": container,
        "active_image": image.strip(),
        "container_dir": container_dir,
        "host_dir": host_dir,
    }


def _cutoff_ns(cutoff_text: str) -> int:
    try:
        cutoff = dt.datetime.strptime(cutoff_text, "%Y-%m-%dT%H:%M:%S.%fZ")
    except ValueError as exc:
        raise ExportOrphanError(f"export orphan cutoff is invalid: {exc}") from exc
    return calendar.timegm(cutoff.timetuple()) * 1_000_000_000 + cutoff.microsecond * 1_000


def _open_identities(proc_root: pathlib.Path) -> set[tuple[int, int]]:
    if not proc_root.is_dir():
        raise ExportOrphanError("process fd inventory is unavailable")
    opened: set[tuple[int, int]] = set()
    try:
        processes = list(proc_root.iterdir())
    except OSError as exc:
        raise ExportOrphanError(f"process fd inventory failed: {exc}") from exc
    for process in processes:
        if not process.name.isdigit():
            continue
        try:
            descriptors = list((process / "fd").iterdir())
        except (FileNotFoundError, NotADirectoryError):
            continue
        except PermissionError as exc:
            raise ExportOrphanError(f"cannot inspect open handles for pid {process.name}") from exc
        for descriptor in descriptors:
            try:
                value = descriptor.stat()
            except FileNotFoundError:
                continue
            except PermissionError as exc:
                raise ExportOrphanError(f"cannot inspect open handle {descriptor}") from exc
            opened.add((value.st_dev, value.st_ino))
    return opened


def _base_plan(runtime: dict[str, str], cutoff_text: str, proc_root: pathlib.Path) -> dict[str, Any]:
    cutoff_ns = _cutoff_ns(cutoff_text)
    host_dir = pathlib.Path(runtime["host_dir"])
    present = os.path.lexists(host_dir)
    if present and (host_dir.is_symlink() or not host_dir.is_dir()):
        raise ExportOrphanError("effective QA export temp directory is missing or unsafe")
    opened = _open_identities(proc_root)
    files: list[dict[str, Any]] = []
    if present:
        try:
            entries = list(os.scandir(host_dir))
        except OSError as exc:
            raise ExportOrphanError(f"QA export temp inventory failed: {exc}") from exc
        for entry in entries:
            try:
                value = entry.stat(follow_symlinks=False)
            except FileNotFoundError as exc:
                raise ExportOrphanError("QA export temp changed during inventory") from exc
            if not stat.S_ISREG(value.st_mode) or value.st_mtime_ns >= cutoff_ns:
                continue
            if (value.st_dev, value.st_ino) not in opened:
                files.append({"basename": entry.name, "size_bytes": value.st_size, "mtime": value.st_mtime_ns})
    files.sort(key=lambda item: item["basename"])
    return {
        "schema_version": "qa-export-orphan-plan-v1",
        "container_dir": runtime["container_dir"],
        "host_dir": str(host_dir),
        "cutoff": cutoff_text,
        "directory_present": present,
        "files": files,
        "count": len(files),
        "total_bytes": sum(item["size_bytes"] for item in files),
        "deletion_authorized": False,
    }


def _with_hash(value: dict[str, Any]) -> dict[str, Any]:
    digest = hashlib.sha256(json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return {**value, "plan_hash": digest, "required_confirmation": CONFIRM_PREFIX + digest}


def _write_activation(path: pathlib.Path, plan_hash: str) -> str:
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = {
        "schema_version": "qa-export-orphan-activation-v1",
        "activated_plan_hash": plan_hash,
        "activated_at": dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z"),
    }
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, ensure_ascii=True, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _revalidate_candidate(
    directory_fd: int, item: dict[str, Any], cutoff_ns: int, opened: set[tuple[int, int]]
) -> None:
    name = item["basename"]
    if name in {"", ".", ".."} or "/" in name:
        raise ExportOrphanError("QA export orphan basename is unsafe")
    value = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
    if (
        not stat.S_ISREG(value.st_mode)
        or value.st_size != item["size_bytes"]
        or value.st_mtime_ns != item["mtime"]
        or value.st_mtime_ns >= cutoff_ns
        or (value.st_dev, value.st_ino) in opened
    ):
        raise ExportOrphanError(f"QA export orphan changed before removal: {name}")


def _action(args: argparse.Namespace) -> dict[str, Any]:
    try:
        runtime = json.loads(args.runtime_json)
    except json.JSONDecodeError as exc:
        raise ExportOrphanError("QA export runtime is invalid") from exc
    if not isinstance(runtime, dict) or not all(isinstance(runtime.get(key), str) for key in ("container_dir", "host_dir")):
        raise ExportOrphanError("QA export runtime is invalid")
    cutoff_ns = _cutoff_ns(args.cutoff)
    proc_root = pathlib.Path(args.proc_root)
    plan = _with_hash(_base_plan(runtime, args.cutoff, proc_root))
    if args.mode == "plan":
        return plan
    if re.fullmatch(r"[0-9a-f]{64}", args.expected_hash or "") is None or plan["plan_hash"] != args.expected_hash:
        raise ExportOrphanError("QA export orphan plan drifted")
    if _with_hash(_base_plan(runtime, args.cutoff, proc_root))["plan_hash"] != args.expected_hash:
        raise ExportOrphanError("QA export orphan plan drifted before removal")
    host_dir = pathlib.Path(runtime["host_dir"])
    if plan["files"]:
        directory_fd = os.open(host_dir, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            opened = _open_identities(proc_root)
            for item in plan["files"]:
                _revalidate_candidate(directory_fd, item, cutoff_ns, opened)
            for item in plan["files"]:
                _revalidate_candidate(
                    directory_fd, item, cutoff_ns, _open_identities(proc_root)
                )
                os.unlink(item["basename"], dir_fd=directory_fd)
        finally:
            os.close(directory_fd)
    marker_sha = _write_activation(pathlib.Path(args.activation_marker), args.expected_hash) if args.mode == "apply-activate" else None
    return {
        "mode": "prod_qa_export_orphan_apply",
        "cutoff": args.cutoff,
        "container_dir": runtime["container_dir"],
        "host_dir": runtime["host_dir"],
        "files": plan["files"],
        "planned_count": plan["count"],
        "planned_bytes": plan["total_bytes"],
        "plan_hash": args.expected_hash,
        "deleted_count": plan["count"],
        "deleted_bytes": plan["total_bytes"],
        "deletion_authorized": True,
        "activation_marker_sha256": marker_sha,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    resolve = commands.add_parser("resolve-runtime")
    resolve.add_argument("--container", required=True)
    resolve.add_argument("--default-host", required=True)
    action = commands.add_parser("action")
    action.add_argument("--mode", choices=("plan", "apply", "apply-activate"), required=True)
    action.add_argument("--cutoff", required=True)
    action.add_argument("--runtime-json", required=True)
    action.add_argument("--proc-root", default="/proc")
    action.add_argument("--expected-hash", default="")
    action.add_argument("--activation-marker", required=True)
    args = parser.parse_args()
    try:
        if args.command == "resolve-runtime":
            print(json.dumps(_runtime_from_inspect(args.container, args.default_host, json.load(sys.stdin)), sort_keys=True, separators=(",", ":")))
        else:
            print(json.dumps(_action(args), ensure_ascii=True, sort_keys=True, separators=(",", ":")))
    except (ExportOrphanError, OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"tokenkey-qa-export-orphan: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
