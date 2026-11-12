#!/usr/bin/env python3
"""Remove one application's QA hot files after coordinator authorization."""
from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys

from data_machine_coordinator import CoordinatorError, SOURCE_ID, _hour
import node1_archive as archive


def remove_hour(root: Path, relative: Path):
    if not shutil.rmtree.avoids_symlink_attacks:
        raise CoordinatorError("platform lacks symlink-safe recursive deletion")
    descriptors = []
    try:
        current = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        descriptors.append(current)
        for part in relative.parts[:-1]:
            try:
                current = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=current)
            except FileNotFoundError:
                return
            descriptors.append(current)
        try:
            info = os.stat(relative.name, dir_fd=current, follow_symlinks=False)
        except FileNotFoundError:
            return
        if not stat.S_ISDIR(info.st_mode):
            raise CoordinatorError("unsafe final QA hour path")
        shutil.rmtree(relative.name, dir_fd=current)
    finally:
        for fd in reversed(descriptors):
            os.close(fd)


def apply_source_cleanup(plan_path: Path, source_root: Path, source_id: str, *, apply: bool,
                         environment: str = "prod", seal_check=None, now=None) -> dict:
    try:
        plan = json.loads(plan_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise CoordinatorError("cleanup plan is unreadable") from exc
    if plan.get("schema_version") != "qa-data-machine-cleanup-v1":
        raise CoordinatorError("unsupported cleanup plan")
    supplied_hash = plan.get("plan_sha256")
    unsigned = {key: value for key, value in plan.items() if key != "plan_sha256"}
    expected_hash = hashlib.sha256(json.dumps(unsigned, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    if supplied_hash != expected_hash:
        raise CoordinatorError("cleanup plan hash mismatch")
    if not SOURCE_ID.fullmatch(source_id):
        raise CoordinatorError("unsafe source identity")
    source_root = source_root.expanduser()
    if source_root.is_symlink() or not source_root.is_dir():
        raise CoordinatorError("source root must be a real directory")
    source_root = source_root.resolve()
    if source_id not in plan.get("expected_sources", []):
        raise CoordinatorError("source is not part of the cleanup plan")
    if plan.get("environment") != environment or environment not in ("prod", "cn-prod"):
        raise CoordinatorError("cleanup plan environment mismatch")
    hour = _hour(plan.get("window_start", ""))
    now = now or dt.datetime.now(dt.timezone.utc)
    if hour + dt.timedelta(hours=25) > now.replace(minute=0, second=0, microsecond=0):
        raise CoordinatorError("QA hour has not passed the 24h retention boundary")
    state = (plan.get("sources", {}).get(source_id) or {}).get("state")
    if state != "complete" or source_id not in plan.get("source_cleanup_allowed", []):
        return {"ok": True, "source_id": source_id, "action": "hold", "reason": "source receipt incomplete"}
    evidence = plan["sources"][source_id]
    if seal_check is None:
        raise CoordinatorError("source cleanup requires a live capture ledger seal check")
    seal = seal_check(hour)
    if seal.get("state_digest") != evidence.get("seal_digest"):
        raise CoordinatorError("capture seal changed since archive")
    targets = []
    for kind in ("qa_blobs", "qa_dlq"):
        target = source_root / kind / hour.strftime("%Y/%m/%d/%H")
        for parent in (target, *target.parents):
            if parent == source_root:
                break
            if parent.is_symlink():
                raise CoordinatorError("symlink in QA cleanup path")
        if target.exists() and not target.is_dir():
            raise CoordinatorError("QA hour path is not a directory")
        targets.append(target)
    # Prove the files about to be deleted are exactly the archived snapshot.
    completion = source_root / "qa_cleanup_state" / environment / source_id / (hour.strftime(archive.HOUR) + ".json")
    if completion.exists():
        saved = json.loads(completion.read_text())
        manifest = saved["manifest"]
    else:
        files = archive._hour_sources(source_root, hour, allow_empty=True)
        manifest = archive._source_manifest(files, environment, hour, source_id)
        saved = {"manifest": manifest, "seal_digest": evidence["seal_digest"], "complete": False}
    if (hashlib.sha256(archive.manifest_bytes(manifest)).hexdigest() != evidence["manifest_sha256"]
            or saved["seal_digest"] != evidence["seal_digest"]):
        raise CoordinatorError("QA source differs from archived snapshot")
    # An interrupted cleanup may have removed one directory. Remaining files
    # must still be a subset of the originally verified immutable snapshot.
    expected_files = {entry["path"]: entry for entry in manifest["files"]}
    remaining = []
    for target in targets:
        if not target.exists():
            continue
        for path in target.rglob("*"):
            if path.is_symlink() or (not path.is_file() and not path.is_dir()):
                raise CoordinatorError("unsafe entry in QA hour")
            if path.is_file():
                relative = str(path.relative_to(source_root))
                entry = expected_files.get(relative)
                if not entry or archive.sha256_file(path) != (entry["sha256"], entry["bytes"]):
                    raise CoordinatorError("QA source changed after cleanup was authorized")
                remaining.append(relative)
    if saved.get("complete") and remaining:
        raise CoordinatorError("late QA files appeared after cleanup")
    if seal_check(hour).get("state_digest") != evidence["seal_digest"]:
        raise CoordinatorError("capture seal changed during cleanup validation")
    removed = []
    if apply and not completion.exists():
        archive.atomic_json(completion, saved)
    for target in targets:
        if target.exists():
            if target.is_symlink() or not target.is_dir():
                raise CoordinatorError(f"unsafe QA hour path: {target}")
            if apply:
                remove_hour(source_root, target.relative_to(source_root))
            removed.append(str(target))
    if apply:
        archive.atomic_json(completion, dict(saved, complete=True))
    return {"ok": True, "source_id": source_id, "window_start": hour.isoformat(),
            "action": "delete" if apply else "plan", "removed": removed,
            "shared_partition_action": plan.get("shared_partition_action")}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--source-id", required=True)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--environment", choices=("prod", "cn-prod"), required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--ledger-root", type=Path, required=True)
    parser.add_argument("--lock", type=Path, required=True)
    args = parser.parse_args()
    try:
        def check(hour):
            proc = subprocess.run([str(args.binary), "--phase", "seal", "--hour", hour.isoformat(),
                                   "--ledger-root", str(args.ledger_root),
                                   "--confirm", "tokenkey-qa-local-seal-v1"], check=True,
                                  capture_output=True, text=True, timeout=30)
            return json.loads(proc.stdout)
        with args.lock.open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            result = apply_source_cleanup(args.plan, args.source_root, args.source_id, apply=args.apply,
                                          environment=args.environment, seal_check=check)
    except (OSError, CoordinatorError) as exc:
        print(f"qa source cleanup: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
