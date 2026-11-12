#!/usr/bin/env python3
"""Independent host-local QA expiry; loss after hour-end + 24h is approved."""
from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import json
import os
from pathlib import Path
import socket
import subprocess
import sys

import coordinator_host
import data_machine_coordinator as policy
import node1_archive as archive
import source_cleanup
import source_worker


def expired_directories(root, now, errors):
    candidates = []
    # Scan only the two payload trees, at fixed depth. Never follow symlinks.
    def walk(path, parts):
        try:
            with os.scandir(path) as entries:
                for entry in entries:
                    if entry.is_symlink():
                        errors.append({"path": entry.path, "error": "symlink in QA expiry tree"})
                        continue
                    if not entry.is_dir(follow_symlinks=False):
                        continue
                    values = parts + [entry.name]
                    if len(values) < 4:
                        if entry.name.isascii() and entry.name.isdigit() and len(entry.name) == (4 if not parts else 2):
                            walk(Path(entry.path), values)
                        continue
                    try:
                        hour = dt.datetime.strptime("/".join(values), archive.HOUR).replace(tzinfo=dt.timezone.utc)
                    except ValueError:
                        continue
                    if hour.strftime(archive.HOUR) == "/".join(values) and hour + dt.timedelta(hours=25) <= now:
                        candidates.append((hour, Path(entry.path).relative_to(root)))
        except FileNotFoundError:
            pass
        except OSError as exc:
            errors.append({"path": str(path), "error": str(exc)})
    for kind in ("qa_blobs", "qa_dlq"):
        path = root / kind
        if path.is_symlink():
            errors.append({"path": str(path), "error": "symlink in QA expiry tree"})
        else:
            walk(path, [])
    return sorted(candidates)


def expired_staging(root, now, errors):
    candidates = []
    try:
        with os.scandir(root) as entries:
            for entry in entries:
                if not archive.STAGING_PATTERN.fullmatch(entry.name):
                    continue
                if entry.is_symlink() or not entry.is_dir(follow_symlinks=False):
                    errors.append({"path": entry.path, "error": "unsafe QA staging directory"})
                    continue
                try:
                    hour = dt.datetime.strptime(entry.name[6:16], "%Y%m%d%H").replace(tzinfo=dt.timezone.utc)
                except ValueError:
                    continue
                if hour + dt.timedelta(hours=25) <= now:
                    candidates.append((hour, Path(entry.name)))
    except FileNotFoundError:
        pass
    except OSError as exc:
        errors.append({"path": str(root), "error": str(exc)})
    return sorted(candidates)


def run(config, role, *, apply=False, now=None):
    if role == "source":
        source_worker.validate_config(config)
    elif role == "data":
        coordinator_host.validate_config(config)
    else:
        raise policy.CoordinatorError("invalid retention role")
    if socket.gethostname() != config["hostname"]:
        raise policy.CoordinatorError("QA retention host identity mismatch")
    now = now or dt.datetime.now(dt.timezone.utc)
    if now.tzinfo is None or now.utcoffset() != dt.timedelta(0):
        raise policy.CoordinatorError("QA retention clock must use UTC")
    # A separate lock prevents an unavailable/slow archive owner blocking expiry.
    lock_path = Path(config["host_lock"] + ".retention")
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        result = {"ok": True, "role": role, "environment": config["environment"], "apply": apply,
                  "observed_at": now.isoformat(), "policy": "hour-end-plus-24h-accept-loss",
                  "archive_verified": False, "removed": [], "errors": []}
        if role == "data":
            result["database"] = coordinator_host.database(config, "expire-shared", dry_run=not apply)
            state = Path(config["state_root"])
        else:
            root = Path(config["source_root"]).resolve(strict=True)
            if not root.is_dir() or root == Path("/"):
                raise policy.CoordinatorError("invalid QA source root")
            state = root / "qa_retention_state"
            receipt_root = Path(config["receipt_root"]).resolve()
            targets = [(root, hour, relative) for hour, relative in expired_directories(root, now, result["errors"])]
            targets.extend((receipt_root, hour, relative) for hour, relative in expired_staging(receipt_root, now, result["errors"]))
            for target_root, hour, relative in targets:
                intent = {"environment": config["environment"], "source_id": config["source_id"],
                          "window_start": hour.isoformat(), "path": str(target_root / relative),
                          "reason": "retention_expired_archive_not_required", "archive_verified": False}
                if apply:
                    # Report accepted loss even if ENOSPC prevents persisting the audit.
                    print(json.dumps(dict(intent, event="qa_retention_delete_intent")), flush=True)
                    try:
                        archive.atomic_json(state / "last-intent.json", intent)
                    except OSError as exc:
                        result["errors"].append({"operation": "audit", "error": str(exc)})
                    try:
                        source_cleanup.remove_hour(target_root, relative)
                    except (OSError, ValueError) as exc:
                        result["errors"].append({"path": str(relative), "error": str(exc)})
                        continue
                result["removed"].append(intent)
        result["ok"] = not result["errors"]
        if apply:
            archive.atomic_json(state / "last-retention.json", result)
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--role", choices=("data", "source"), required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        result = run(policy.load_private_config(args.config), args.role, apply=args.apply)
        print(json.dumps(result, sort_keys=True))
        return 0 if result["ok"] else 1
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
